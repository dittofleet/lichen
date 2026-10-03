// Package module wires lichen's sync units together. A module owns one
// kind of synced thing: files (chezmoi-managed paths), skills (agent
// skills from public repos) and mcp (MCP servers). Every reconcile pass
// runs them as a set. Order matters: files goes first because it pulls
// the sync repo, which can deliver new config files (say, a skills.json
// with a source added on another machine), and the other modules read
// their config from disk, so they see what files just applied.
package module

import (
	"errors"
	"fmt"
	"log"

	"lichen/internal/config"
	"lichen/internal/files"
	"lichen/internal/mcp"
	"lichen/internal/skills"
)

// ReconcileAll runs every module in order. Modules fail independently: a
// files error must not keep skills or MCP servers stale (or the
// reverse), so errors are joined rather than short-circuiting. Callers hold the cross-process
// lock and pass a freshly loaded config.
func ReconcileAll(cfg *config.Config, lg *log.Logger) error {
	var errs []error
	if err := files.Reconcile(cfg, lg); err != nil {
		errs = append(errs, fmt.Errorf("files: %w", err))
	}
	errs = append(errs, ReconcileConfigured(lg))
	return errors.Join(errs...)
}

// ReconcileConfigured runs the modules driven by lichen's own config
// files, for when one of those files changed without a files pass.
func ReconcileConfigured(lg *log.Logger) error {
	var errs []error
	if err := skills.Reconcile(lg); err != nil {
		errs = append(errs, fmt.Errorf("skills: %w", err))
	}
	if err := mcp.Reconcile(lg); err != nil {
		errs = append(errs, fmt.Errorf("mcp: %w", err))
	}
	return errors.Join(errs...)
}
