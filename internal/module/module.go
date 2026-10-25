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
	"strings"

	"lichen/internal/core"
	"lichen/internal/files"
	"lichen/internal/mcp"
	"lichen/internal/skills"
)

// Module is one kind of synced thing.
type Module interface {
	Name() string
	// Reconcile makes this device match the sync folder. A module that
	// captures local changes back into the sync folder commits them
	// itself, under its own subject: the pass pushes.
	Reconcile(env *core.Env) error
}

// Watcher is a module with local paths whose changes call for a pass:
// files to watch one by one (their directories may hold much else), and
// directory trees where any change counts.
type Watcher interface {
	Watched(env *core.Env) (files, trees []string)
}

// All lists the modules in the order a pass runs them.
var All = []Module{files.Module{}, skills.Module{}, mcp.Module{}}

// Pass runs every module. With pull it first brings in what other devices
// pushed (a nudge, the hourly pass, a CLI command); without it, only
// local changes are in play (the watcher). Modules fail independently: a
// files error must not keep skills or MCP servers stale, so errors are
// joined rather than short-circuiting. An outdated build stops the pass
// before anything is applied or pushed. Callers hold the cross-process
// lock.
func Pass(env *core.Env, pull bool) error {
	if pull {
		pulled := true
		if err := env.Pull(); err != nil {
			pulled = false
			env.Log.Printf("pull: %v (continuing with local state)", err)
		}
		if err := env.SyncVersion(pulled); err != nil {
			return err
		}
		// The pull may have brought new settings (a device added to a
		// group, say).
		if err := env.Reload(); err != nil {
			return err
		}
	} else if err := env.Gate(); err != nil {
		return err
	}
	if err := CommitDirect(env); err != nil {
		return err
	}
	var errs []error
	for _, m := range All {
		if err := m.Reconcile(env); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.Name(), err))
		}
	}
	if err := env.Push(); err != nil {
		return err
	}
	return errors.Join(errs...)
}

// CommitDirect commits edits made in the sync folder itself: a source
// edited in ~/lichen, or a module config edited by hand.
func CommitDirect(env *core.Env) error {
	changed := env.Changes()
	if len(changed) == 0 {
		return nil
	}
	subject := "edit " + strings.Join(changed, ", ")
	if len(changed) > 3 {
		subject = fmt.Sprintf("edit %d files", len(changed))
	}
	_, err := env.Commit(subject, strings.Join(changed, "\n"))
	return err
}

// Change applies a CLI edit to a module's config on top of the freshest
// shared state and publishes it: pull first (an edit on a stale copy
// would push the staleness, and local-wins would revert another device's
// change), edit, commit under subject, then a pass makes every module
// match and pushes. An outdated build stops before it writes anything.
func Change(env *core.Env, subject string, edit func() error) error {
	if err := env.Pull(); err != nil {
		env.Log.Printf("pull: %v (continuing with local state)", err)
	}
	if err := env.Gate(); err != nil {
		return err
	}
	if err := CommitDirect(env); err != nil {
		return err
	}
	if err := edit(); err != nil {
		return err
	}
	if _, err := env.Commit(subject, ""); err != nil {
		return err
	}
	if err := env.Reload(); err != nil {
		return err
	}
	return Pass(env, false)
}
