package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

func (e *Env) rebasing() bool {
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(e.Path(".git", d)); err == nil {
			return true
		}
	}
	return false
}

// abortStaleRebase recovers a sync folder left mid-rebase by a failed or
// interrupted pull. A wedged repo would otherwise block every commit and
// pull until a human runs `git rebase --abort`.
func (e *Env) abortStaleRebase() {
	if e.rebasing() {
		e.Log.Printf("aborting a stale rebase in the sync folder")
		e.Git("rebase", "--abort")
	}
}

// pull brings in what the other devices pushed, under lichen's
// local-wins policy: rebase (not ff-only) so a committed-but-unpushed
// change doesn't wedge the pull forever, with -X theirs preferring OUR
// replayed commits on conflict (rebase swaps sides). A failed pull never
// leaves the repo mid-rebase. No-op without an origin, or while the
// branch exists nowhere but here.
func (e *Env) pull() error {
	e.abortStaleRebase()
	if e.Origin() == "" || !e.hasUpstream() {
		return nil
	}
	_, err := e.Git("pull", "--rebase", "-X", "theirs", "--quiet")
	if err != nil && e.rebasing() {
		err = e.settleRebase()
	}
	if err != nil {
		e.abortStaleRebase()
		return err
	}
	return nil
}

// settleRebase finishes a rebase stopped on what -X theirs leaves
// unresolved: a file edited on one side and deleted on the other. The
// edit wins, whichever side made it (the files module then brings back
// the source it belongs to), so an edit is never lost to a deletion, and
// the pull never wedges on one.
func (e *Env) settleRebase() error {
	for range 100 {
		if !e.rebasing() {
			return nil
		}
		out, err := e.Git("diff", "--name-only", "-z", "--diff-filter=U")
		if err != nil {
			return err
		}
		unmerged := split0(out)
		if len(unmerged) == 0 {
			return errors.New("the pull stopped on something other than a conflict")
		}
		for _, p := range unmerged {
			// Mid-rebase, theirs is the local commit being replayed.
			if _, err := e.Git("checkout", "--theirs", "--", p); err != nil {
				if _, err := e.Git("checkout", "--ours", "--", p); err != nil {
					return err
				}
			}
			if _, err := e.Git("add", "--", p); err != nil {
				return err
			}
		}
		if _, err := e.Git("-c", "core.editor=true", "rebase", "--continue"); err != nil {
			// The local commit had nothing else in it.
			if _, staged := e.Git("diff", "--cached", "--quiet"); staged != nil {
				return err
			}
			if _, err := e.Git("rebase", "--skip"); err != nil && e.rebasing() {
				return err
			}
		}
	}
	return errors.New("the pull would not settle")
}

func split0(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == 0 })
}

// Update brings the sync folder up to date with what the other devices
// pushed, and this Env with it, refusing with *OutdatedError if that
// makes this build too old. A failed pull (offline, say) is logged, not
// an error: pulled reports it, and work goes on with the local state.
func (e *Env) Update() (pulled bool, err error) {
	pulled = true
	if err := e.pull(); err != nil {
		pulled = false
		e.Log.Printf("pull: %v (continuing with local state)", err)
	}
	e.repairMarker()
	if err := e.Reload(); err != nil {
		return pulled, err
	}
	return pulled, e.Gate()
}

// Commit stages everything in the sync folder and commits it under
// subject, if there is anything to commit. The sync folder belongs to
// lichen: whatever is in it is meant to sync.
func (e *Env) Commit(subject, body string) error {
	if err := e.stage(); err != nil {
		return err
	}
	if _, err := e.Git("diff", "--cached", "--quiet"); err == nil {
		return nil
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
	_, err := e.Git(args...)
	return err
}

// Changes stages every edit made in the sync folder since the last
// commit, and lists the paths, for describing edits made there directly.
func (e *Env) Changes() ([]string, error) {
	if err := e.stage(); err != nil {
		return nil, err
	}
	out, err := e.Git("diff", "--cached", "--name-only", "-z")
	return split0(out), err
}

// stage stages everything in the sync folder but what lichen never syncs
// (config.Ignored). A synced directory may bring a .gitignore of its
// own, and what it ignores syncs all the same.
func (e *Env) stage() error {
	if _, err := e.Git("add", "-A"); err != nil {
		return err
	}
	out, err := e.Git("ls-files", "-z", "--others", "--ignored", "--exclude-standard")
	if err != nil {
		return err
	}
	var add []string
	for _, p := range split0(out) {
		if !slices.ContainsFunc(strings.Split(p, "/"), config.Ignored) {
			add = append(add, p)
		}
	}
	if len(add) == 0 {
		return nil
	}
	_, err = e.Git(append([]string{"add", "-f", "--"}, add...)...)
	return err
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
		// That pull may deliver a version bump.
		pulled, uerr := e.Update()
		if uerr != nil {
			return uerr
		}
		if !pulled {
			e.Log.Printf("push failed (will retry on the next pass): %v", err)
			return nil
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
// An empty repo is a first device, and gets lichen.json to mark it (and
// a .gitignore, so editors' droppings in the folder never sync). A
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
	// Synced files may be private ones (~/.netrc, say): the folder is
	// this user's alone, whatever modes they arrive with.
	if err := os.Chmod(dir, 0o700); err != nil {
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
	return dir, config.WriteFile(filepath.Join(dir, ".gitignore"), []byte(config.GitIgnore), 0o644)
}
