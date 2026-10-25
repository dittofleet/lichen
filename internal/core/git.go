package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"lichen/internal/config"
	"lichen/internal/events"
	"lichen/internal/gitutil"
)

// Git runs git in the sync folder.
func (e *Env) Git(args ...string) (string, error) {
	return gitutil.Run(e.Dir, args...)
}

// Origin returns the sync repo's remote URL ("" when none is set).
func (e *Env) Origin() string {
	url, err := e.Git("remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return url
}

// hasUpstream reports whether the branch tracks one on origin. A clone
// of an empty repo has none until its first push, or until another
// device's push creates the branch, which is picked up here.
func (e *Env) hasUpstream() bool {
	if _, err := e.Git("rev-parse", "--abbrev-ref", "@{u}"); err == nil {
		return true
	}
	branch, err := e.Git("branch", "--show-current")
	if err != nil || branch == "" {
		return false
	}
	if _, err := e.Git("fetch", "--quiet", "origin"); err != nil {
		return false
	}
	_, err = e.Git("branch", "--quiet", "--set-upstream-to=origin/"+branch)
	return err == nil
}

// abortStaleRebase recovers a sync folder left mid-rebase by a conflicted
// or interrupted pull. A wedged repo would otherwise block every commit
// and pull until a human runs `git rebase --abort`.
func (e *Env) abortStaleRebase() {
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(e.Path(".git", d)); err == nil {
			e.Log.Printf("aborting a stale rebase in the sync folder")
			e.Git("rebase", "--abort")
			return
		}
	}
}

// Pull brings in what the other devices pushed, under lichen's
// local-wins policy: rebase (not ff-only) so a committed-but-unpushed
// change doesn't wedge the pull forever, with -X theirs preferring OUR
// replayed commits on conflict (rebase swaps sides). A failed pull never
// leaves the repo mid-rebase. No-op without an origin, or while the
// branch exists nowhere but here.
func (e *Env) Pull() error {
	e.abortStaleRebase()
	if e.Origin() == "" || !e.hasUpstream() {
		return nil
	}
	if _, err := e.Git("pull", "--rebase", "-X", "theirs", "--quiet"); err != nil {
		e.abortStaleRebase()
		return err
	}
	return nil
}

// Commit stages everything in the sync folder and commits it under
// subject, reporting whether there was anything to commit. The sync
// folder belongs to lichen: whatever is in it is meant to sync.
func (e *Env) Commit(subject, body string) (bool, error) {
	if _, err := e.Git("add", "-A"); err != nil {
		return false, err
	}
	if _, err := e.Git("diff", "--cached", "--quiet"); err == nil {
		return false, nil
	}
	args := []string{"commit", "--quiet", "-m", fmt.Sprintf("lichen(%s): %s", e.Device.Name, subject)}
	if body != "" {
		args = append(args, "-m", body)
	}
	// A server may have no git identity of its own: commits there are
	// lichen's anyway.
	if email, _ := e.Git("config", "user.email"); email == "" {
		args = append([]string{"-c", "user.name=lichen", "-c", "user.email=lichen@" + e.Device.Name}, args...)
	}
	if _, err := e.Git(args...); err != nil {
		return false, err
	}
	return true, nil
}

// Changes lists the paths in the sync folder that differ from the last
// commit, for describing edits made there directly.
func (e *Env) Changes() []string {
	out, err := e.Git("status", "--porcelain", "--untracked-files=all")
	if err != nil || out == "" {
		return nil
	}
	var paths []string
	for _, l := range strings.Split(out, "\n") {
		if len(l) > 3 {
			p := l[3:]
			if _, to, ok := strings.Cut(p, " -> "); ok {
				p = to
			}
			paths = append(paths, strings.Trim(p, `"`))
		}
	}
	return paths
}

// Push publishes local commits and nudges the other devices. A racing
// push from another device, or being offline, is routine: one retry
// behind a pull, else the next pass tries again. Only an outdated build,
// discovered by that pull, is an error: it must not push.
func (e *Env) Push() error {
	if e.Origin() == "" {
		return nil
	}
	if e.hasUpstream() {
		if out, err := e.Git("rev-list", "--count", "@{u}..HEAD"); err != nil || out == "0" {
			return nil
		}
	}
	if _, err := e.Git("push", "--quiet", "-u", "origin", "HEAD"); err != nil {
		if e.Pull() != nil {
			e.Log.Printf("push failed (will retry on the next pass): %v", err)
			return nil
		}
		// That pull may have delivered a version bump.
		e.repairMarker()
		if gerr := e.Gate(); gerr != nil {
			return gerr
		}
		if _, err := e.Git("push", "--quiet", "-u", "origin", "HEAD"); err != nil {
			e.Log.Printf("push failed (will retry on the next pass): %v", err)
			return nil
		}
	}
	if err := events.Announce(); err != nil && !errors.Is(err, events.ErrNotRunning) {
		e.Log.Printf("other devices not nudged, they catch up on their next pass: %v", err)
	}
	return nil
}

// Setup creates this machine's sync folder: a clone of url, or with no
// url a folder of its own, not synced anywhere until it gets an origin.
// An empty repo is a first device, and gets lichen.json to mark it. A
// repo with content but no lichen.json is not a sync repo (most likely
// lichen's own source repo, passed by mistake) and is refused.
func Setup(url string) (string, error) {
	dir, err := config.SyncDir()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return dir, fmt.Errorf("%s is already set up", config.ContractHome(dir))
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return dir, fmt.Errorf("%s already exists and is not a sync folder: move it away first", config.ContractHome(dir))
	}
	if url == "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return dir, err
		}
		if _, err := gitutil.Run(dir, "init", "--quiet"); err != nil {
			return dir, err
		}
	} else if _, err := gitutil.Run(filepath.Dir(dir), "clone", "--quiet", url, dir); err != nil {
		return dir, err
	}
	settings := filepath.Join(dir, SettingsFile)
	if _, err := os.Stat(settings); err == nil {
		return dir, nil
	}
	if _, err := gitutil.Run(dir, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		os.RemoveAll(dir)
		return dir, fmt.Errorf("%s has files but no %s, so it is not a lichen sync repo (the sync repo is a separate, initially empty, private repo)", url, SettingsFile)
	}
	if err := config.WriteJSON(settings, map[string]any{}); err != nil {
		return dir, err
	}
	return dir, nil
}
