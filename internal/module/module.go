// Package module runs lichen's modules as one pass. A module owns one
// kind of synced thing: files (sources compiled to outputs), skills
// (agent skills from public repos) and mcp (MCP servers). Each reads its
// own config file from the sync folder and never looks at another
// module, so a pass is: pull, commit anything edited in the sync folder
// directly, let every module bring this device in line, push.
package module

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"lichen/internal/core"
	"lichen/internal/files"
	"lichen/internal/mcp"
	"lichen/internal/skills"
)

// Module is one kind of synced thing.
type Module interface {
	Name() string
	// Config is the module's config file in the sync folder.
	Config() string
	// Reconcile makes this device match the sync folder. A module that
	// captures local changes back into the sync folder commits them
	// itself, under its own subject: the pass pushes.
	Reconcile(env *core.Env) error
}

// All lists the modules in the order a pass runs them.
var All = []Module{files.Module{}, skills.Module{}, mcp.Module{}}

// Pass runs every module. With pull it first brings in what other devices
// pushed (a nudge, the hourly pass, a CLI command); without it, the sync
// folder is as current as it gets already. An outdated build stops the
// pass before anything is applied or pushed. Callers hold the
// cross-process lock.
func Pass(env *core.Env, pull bool) error {
	if pull {
		pulled, err := env.Update()
		if err != nil {
			return err
		}
		if pulled {
			if err := env.RecordVersion(); err != nil {
				return err
			}
		}
	} else if err := env.Gate(); err != nil {
		return err
	}
	if _, err := CommitDirect(env); err != nil {
		return err
	}
	return run(env, All)
}

// Local is the pass for a change on this device, in the sync folder or
// at one of the files module's outputs, which are all the daemon
// watches. The files module runs; the others only when their config was
// edited by hand, as they sync nothing local.
func Local(env *core.Env) error {
	if err := env.Gate(); err != nil {
		return err
	}
	edited, err := CommitDirect(env)
	if err != nil {
		return err
	}
	var ms []Module
	for _, m := range All {
		if _, ok := m.(files.Module); ok || slices.Contains(edited, m.Config()) {
			ms = append(ms, m)
		}
	}
	return run(env, ms)
}

// run reconciles modules, then pushes. Modules fail independently: a
// files error must not keep skills or MCP servers stale, so errors are
// joined rather than short-circuiting.
func run(env *core.Env, ms []Module) error {
	var errs []error
	for _, m := range ms {
		if err := m.Reconcile(env); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.Name(), err))
		}
	}
	if err := env.Push(); err != nil {
		return err
	}
	return errors.Join(errs...)
}

// CommitDirect commits edits made in the sync folder itself, a source
// edited in ~/lichen or a module config edited by hand, and lists them.
func CommitDirect(env *core.Env) ([]string, error) {
	changed, err := env.Changes()
	if err != nil || len(changed) == 0 {
		return nil, err
	}
	subject := "edit " + strings.Join(changed, ", ")
	if len(changed) > 3 {
		subject = fmt.Sprintf("edit %d files", len(changed))
	}
	return changed, env.Commit(subject, strings.Join(changed, "\n"))
}

// Change applies a CLI edit on top of the freshest shared state and
// publishes it: update first (an edit on a stale copy would push the
// staleness, and local-wins would revert another device's change), edit,
// commit under subject, then a pass makes every module match and
// pushes. An outdated build stops before it writes anything.
func Change(env *core.Env, subject string, edit func() error) error {
	if _, err := env.Update(); err != nil {
		return err
	}
	if _, err := CommitDirect(env); err != nil {
		return err
	}
	if err := edit(); err != nil {
		return err
	}
	if err := env.Commit(subject, ""); err != nil {
		return err
	}
	if err := env.Reload(); err != nil {
		return err
	}
	return Pass(env, false)
}
