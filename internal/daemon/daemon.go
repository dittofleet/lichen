// Package daemon is lichen's long-running core: reconcile every module
// on start, then react to the sync repo moving (via crosstalk nudges from
// the other machines) and to local edits of managed files (via fsnotify).
// An hourly pass is the backstop for events that never arrived, and
// doubles as the skills module's poll of its upstream repos. Each pass
// re-reads the config.
package daemon

import (
	"context"
	"errors"
	"log"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"lichen/internal/config"
	"lichen/internal/events"
	"lichen/internal/files"
	"lichen/internal/module"
	"lichen/internal/proclock"
	"lichen/internal/selfupdate"
	"lichen/internal/version"
)

// pollInterval catches whatever the nudges missed: a push made outside
// lichen, or one while crosstalk was not running here.
const pollInterval = time.Hour

func Run() error {
	lg := log.New(os.Stdout, "", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	lg.Printf("lichen %s starting", version.Current)

	// One mutex serializes every pass: a startup pass, the hourly pass,
	// watcher flushes, and event bursts must never run git or chezmoi
	// concurrently. rewatch asks the file watcher to refresh its list
	// whenever the managed set may have changed.
	var mu sync.Mutex
	rewatch := make(chan struct{}, 1)
	// runLocked is the single doorway for daemon work that touches the
	// sync repo: in-process mutex, then the cross-process lock (inside mu
	// so this process never double-acquires it), then a fresh config load.
	// Every pass goes through it, so none can race a CLI command.
	runLocked := func(fn func(c *config.Config)) {
		mu.Lock()
		defer mu.Unlock()
		release, err := proclock.Acquire(ctx, nil)
		if err != nil {
			if ctx.Err() == nil {
				lg.Printf("lock: %v", err)
			}
			return
		}
		defer release()
		c, err := files.LoadConfig(lg)
		if err != nil {
			// The config may be mid-rewrite by an apply: skip this cycle.
			lg.Printf("config: %v", err)
			return
		}
		fn(c)
	}
	reconcile := func() {
		var repoV string
		runLocked(func(c *config.Config) {
			err := module.ReconcileAll(c, lg)
			// An outdated refusal pauses file syncing and triggers the
			// self-update below. It rides the joined error, so the other
			// modules' failures still get logged alongside it.
			var outdated *files.OutdatedError
			if errors.As(err, &outdated) {
				repoV = outdated.Repo
			}
			if err != nil {
				lg.Printf("reconcile: %v", err)
			}
			poke(rewatch)
		})
		// The update runs AFTER the locks are released: the download needs
		// neither, and holding them through a slow network would block
		// every CLI command and queued pass for its duration.
		if repoV != "" {
			autoUpdate(lg, repoV)
		}
	}

	// Passes run one at a time off this queue: a burst of nudges arriving
	// while a pass runs adds at most one more, and the listener never
	// blocks, which crosstalk would take as it being gone. An hour without
	// a pass runs one anyway.
	queued := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-queued:
			case <-time.After(pollInterval):
			}
			reconcile()
		}
	}()

	// The startup pass runs in the background so the daemon is listening
	// and watching from the first seconds: on a cold machine the first
	// apply can take a while. The listener's first catch-up queues one
	// more, for pushes made while this one was starting.
	poke(queued)

	go watchFiles(ctx, lg, runLocked, rewatch)

	var lastErr string
	events.Listen(ctx,
		func(err error) {
			if err.Error() != lastErr {
				lastErr = err.Error()
				lg.Printf("events: %v (retrying with backoff, the hourly pass covers the gap)", err)
			}
		},
		func() {
			lastErr = ""
			lg.Printf("events: connected, catching up")
			poke(queued)
		},
		func(from string) {
			lg.Printf("events: sync repo moved (pushed from %s)", from)
			poke(queued)
		})
	lg.Printf("lichen stopped")
	return nil
}

// poke signals ch without blocking: a full buffer already means the
// signal is pending.
func poke(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// updateBackoff spaces out failed auto-update attempts: the release
// download is the biggest I/O the daemon ever does, so an event burst
// must not repeat it back-to-back. The hourly pass is the natural retry.
const updateBackoff = 10 * time.Minute

// updateMu serializes update attempts (the reconcile and watcher paths
// both trigger them, outside runLocked) and guards lastUpdateAttempt. A
// successful attempt never records itself: the process is replaced.
var updateMu sync.Mutex
var lastUpdateAttempt time.Time

// autoUpdate installs the release the sync repo requires and execs it in
// place of this process: launchd sees the same PID, so KeepAlive's
// restart throttle never enters the picture. On failure the daemon stays
// up with syncing paused until a later pass gets through.
//
// The env guard survives the exec, unlike lastUpdateAttempt: if the
// installed release does not actually clear the requirement it was
// installed for (a mis-stamped asset), the fresh process must not
// download it again in a tight loop. A LATER requirement re-arms it.
func autoUpdate(lg *log.Logger, repoV string) {
	updateMu.Lock()
	defer updateMu.Unlock()
	if os.Getenv("LICHEN_AUTOUPDATED") == repoV {
		lg.Printf("update: already self-updated for %s yet still outdated (mis-stamped release?), not retrying", repoV)
		return
	}
	if since := time.Since(lastUpdateAttempt); since < updateBackoff {
		lg.Printf("update: last attempt failed %s ago, backing off", since.Round(time.Second))
		return
	}
	lastUpdateAttempt = time.Now()
	tag, err := selfupdate.Required(repoV)
	if err != nil {
		lg.Printf("update: %v (syncing stays paused, retrying on a later pass)", err)
		return
	}
	lg.Printf("update: installed %s, restarting the daemon on it", tag)
	os.Setenv("LICHEN_AUTOUPDATED", repoV)
	if err := selfupdate.ExecSelf(); err != nil {
		lg.Printf("update: exec: %v", err)
	}
}

// watchFiles pushes local edits out: fsnotify on the parent directories of
// every managed file (watching dirs, not files, survives the
// write-tmp-then-rename dance editors do), debounced, then re-add + commit
// + push. The loop is self-settling: our own `chezmoi apply` fires events
// too, but re-add of an unmodified file is a no-op and git has nothing to
// commit. The manifest is watched too: it changes whenever the managed set
// does, including when a CLI command starts syncing a path.
func watchFiles(ctx context.Context, lg *log.Logger, runLocked func(func(*config.Config)), rewatch chan struct{}) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		lg.Printf("files: watcher: %v", err)
		return
	}
	defer w.Close()

	manifest, _ := files.ManifestPath()
	manifestDir := filepath.Dir(manifest)
	managed := map[string]bool{}
	watchedDirs := map[string]bool{}
	refresh := func() {
		if !files.Active() {
			return
		}
		if manifest != "" {
			w.Add(manifestDir) // retried on the next refresh until it exists
		}
		paths, err := files.Managed()
		if err != nil {
			lg.Printf("files: watcher: %v", err)
			return
		}
		nm := map[string]bool{}
		dirs := map[string]bool{}
		for _, f := range paths {
			nm[f] = true
			dirs[filepath.Dir(f)] = true
		}
		for _, d := range w.WatchList() {
			if !dirs[d] && d != manifestDir {
				w.Remove(d)
			}
		}
		for d := range dirs {
			w.Add(d) // errors (e.g. dir not created yet) are retried on next refresh
		}
		managed = nm
		watchedDirs = dirs
	}
	refresh()

	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	pending := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-rewatch:
			refresh()
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if ev.Name == manifest {
				if ev.Op&(fsnotify.Write|fsnotify.Create) != 0 {
					poke(rewatch)
				}
				continue
			}
			// A watched dir DISAPPEARING matters too: deleting a whole
			// synced directory can surface as one Remove for the dir,
			// with no per-file events (kqueue).
			if (managed[ev.Name] && ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0) ||
				(watchedDirs[ev.Name] && ev.Op&(fsnotify.Rename|fsnotify.Remove) != 0) {
				pending[ev.Name] = true
				debounce.Reset(1500 * time.Millisecond)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			lg.Printf("files: watcher: %v", err)
		case <-debounce.C:
			if len(pending) == 0 {
				continue
			}
			paths := slices.Sorted(maps.Keys(pending))
			pending = map[string]bool{}
			// runLocked provides the same mutex and cross-process lock
			// sequence as every other mutating flow. An outdated refusal
			// triggers the self-update here too (post-lock), so a machine
			// whose user is actively editing does not wait for the hourly
			// pass. The backoff keeps edit bursts from hammering it.
			var repoV string
			runLocked(func(c *config.Config) {
				lg.Printf("files: local change: %v", paths)
				if err := files.LocalChange(c, lg, paths); err != nil {
					var outdated *files.OutdatedError
					if errors.As(err, &outdated) {
						repoV = outdated.Repo
					}
					lg.Printf("files: %v", err)
				}
				// A hand-edited module config (say, skills.json's
				// harnesses list) reaches the other machines via the
				// push above. Reconcile the modules HERE too, or the
				// machine the user typed on would be the last to converge.
				owned := config.OwnedPaths()
				if slices.ContainsFunc(paths, func(p string) bool { return slices.Contains(owned, p) }) {
					if err := module.ReconcileConfigured(lg); err != nil {
						lg.Printf("%v", err)
					}
				}
			})
			if repoV != "" {
				autoUpdate(lg, repoV)
			}
		}
	}
}
