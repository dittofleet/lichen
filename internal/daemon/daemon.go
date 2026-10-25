// Package daemon is lichen's long-running core: a pass over every module
// on start, then again whenever the sync repo moves (crosstalk nudges
// from the other devices) and whenever something it watches changes
// here (the sync folder, and every module's watched paths). An hourly
// pass is the backstop for nudges that never arrived, and doubles as the
// skills module's poll of its upstream repos.
package daemon

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"lichen/internal/core"
	"lichen/internal/events"
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

	// Passes run one at a time off two queues: pulls (a nudge, the hourly
	// pass) and local changes (the watcher). A burst adds at most one more
	// of each, and the listener never blocks, which crosstalk would take
	// as it being gone.
	pulls := make(chan struct{}, 1)
	locals := make(chan struct{}, 1)
	rewatch := make(chan struct{}, 1)
	go func() {
		for {
			pull := true
			select {
			case <-ctx.Done():
				return
			case <-pulls:
			case <-locals:
				pull = false
			case <-time.After(pollInterval):
			}
			pass(ctx, lg, pull)
			poke(rewatch)
		}
	}()

	// The startup pass waits for the listener's first word, which comes
	// within seconds: a catch-up, or an error when crosstalk is not there.
	// Starting sooner would queue a second pass right behind it. A hub
	// that stays away holds it back for a minute at most.
	var started atomic.Bool
	startup := func() {
		if !started.Swap(true) {
			poke(pulls)
		}
	}
	time.AfterFunc(time.Minute, startup)

	go watch(ctx, lg, locals, rewatch)

	var lastErr string
	events.Listen(ctx,
		func(err error) {
			startup()
			if err.Error() != lastErr {
				lastErr = err.Error()
				lg.Printf("events: %v (retrying with backoff, the hourly pass covers the gap)", err)
			}
		},
		func() {
			lastErr = ""
			lg.Printf("events: connected, catching up")
			started.Store(true)
			poke(pulls)
		},
		func(from string) {
			lg.Printf("events: the sync repo moved (pushed from %s)", from)
			poke(pulls)
		})
	lg.Printf("lichen stopped")
	return nil
}

// pass runs one module pass holding the cross-process lock, so none can
// race a CLI command. An outdated build self-updates afterwards, once
// the lock is released: the download needs neither lock, and holding it
// through a slow network would block every CLI command meanwhile.
func pass(ctx context.Context, lg *log.Logger, pull bool) {
	var repoV string
	func() {
		release, err := proclock.Acquire(ctx, nil)
		if err != nil {
			if ctx.Err() == nil {
				lg.Printf("lock: %v", err)
			}
			return
		}
		defer release()
		env, err := core.Load(lg)
		if err != nil {
			lg.Printf("%v", err)
			return
		}
		err = module.Pass(env, pull)
		var outdated *core.OutdatedError
		if errors.As(err, &outdated) {
			repoV = outdated.Repo
		}
		if err != nil {
			lg.Printf("pass: %v", err)
		}
	}()
	if repoV != "" {
		autoUpdate(lg, repoV)
	}
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

var lastUpdateAttempt time.Time

// autoUpdate installs the release the sync folder requires and execs it
// in place of this process. On failure the daemon stays up with syncing
// paused until a later pass gets through.
//
// The env guard survives the exec: if the installed release does not
// actually clear the requirement it was installed for (a mis-stamped
// asset), the fresh process must not download it again in a tight loop.
// A LATER requirement re-arms it.
func autoUpdate(lg *log.Logger, repoV string) {
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

// watched is what the watcher follows: single files (watched through
// their directory, which may hold much else) and whole trees.
type watched struct {
	files map[string]bool
	trees []string
}

func (w *watched) concerns(path string) bool {
	if w.files[path] {
		return true
	}
	base := filepath.Base(path)
	if base == ".DS_Store" || strings.HasSuffix(base, ".lichen-tmp") {
		return false
	}
	for _, t := range w.trees {
		if path == t || strings.HasPrefix(path, t+"/") {
			return true
		}
	}
	return false
}

// watch queues a local pass whenever something watched changes:
// fsnotify on directories (watching dirs, not files, survives the
// write-tmp-then-rename dance editors do), debounced. Its own writes
// trigger it too, but the pass that follows finds nothing to do. The
// watch list is refreshed after every pass, which is when it can change.
func watch(ctx context.Context, lg *log.Logger, locals, rewatch chan struct{}) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		lg.Printf("watcher: %v", err)
		return
	}
	defer w.Close()

	var cur watched
	refresh := func() {
		env, err := core.Load(lg)
		if err != nil {
			return
		}
		next := watched{files: map[string]bool{}, trees: []string{env.Dir}}
		dirs := map[string]bool{}
		for _, m := range module.All {
			if wm, ok := m.(module.Watcher); ok {
				files, trees := wm.Watched(env)
				for _, f := range files {
					next.files[f] = true
					dirs[filepath.Dir(f)] = true
				}
				next.trees = append(next.trees, trees...)
			}
		}
		for _, t := range next.trees {
			dirs[filepath.Dir(t)] = true // to see the tree itself go
			filepath.WalkDir(t, func(p string, d os.DirEntry, err error) error {
				if err != nil || !d.IsDir() {
					return nil
				}
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				dirs[p] = true
				return nil
			})
		}
		for _, d := range w.WatchList() {
			if !dirs[d] {
				w.Remove(d)
			}
		}
		for d := range dirs {
			w.Add(d) // a missing dir is retried on the next refresh
		}
		cur = next
	}
	refresh()

	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
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
			if strings.Contains(ev.Name, "/.git/") || strings.HasSuffix(ev.Name, "/.git") {
				continue
			}
			if cur.concerns(ev.Name) {
				debounce.Reset(1500 * time.Millisecond)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			lg.Printf("watcher: %v", err)
		case <-debounce.C:
			poke(locals)
		}
	}
}
