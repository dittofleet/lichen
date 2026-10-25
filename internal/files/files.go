package files

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"lichen/internal/backup"
	"lichen/internal/config"
	"lichen/internal/core"
)

// Module is the files module.
type Module struct{}

func (Module) Name() string   { return "files" }
func (Module) Config() string { return ConfigFile }

// pass is one reconcile of the files module on this device.
type pass struct {
	env     *core.Env
	cfg     *fileConfig
	st      *state
	home    string
	changes []string // captured into the sync folder, for the commit
}

// Reconcile brings this device's outputs and the sources together:
// first local changes are captured into the sources (edits, deletions,
// and new files in a synced directory), then every output is brought up
// to date, with edits on both sides merged. Whatever it got done is
// recorded and committed, even when a step fails.
func (Module) Reconcile(env *core.Env) error {
	p, err := newPass(env)
	if err != nil {
		return err
	}
	err = p.reconcile()
	return errors.Join(err, p.st.save(), p.commit())
}

func (p *pass) reconcile() error {
	if err := p.revive(); err != nil {
		return err
	}
	if err := p.buryMissing(); err != nil {
		return err
	}
	ws, dirs := wants(p.env, p.cfg, p.env.Log)
	if err := p.captureNew(ws, dirs); err != nil {
		return err
	}
	if err := p.syncModes(ws); err != nil {
		return err
	}
	if err := p.capture(ws); err != nil {
		return err
	}
	if len(p.changes) > 0 {
		// A capture can delete a source or add files to one.
		ws, dirs = wants(p.env, p.cfg, nil)
	}
	if err := p.apply(ws); err != nil {
		return err
	}
	return p.release(ws, dirs)
}

func newPass(env *core.Env) (*pass, error) {
	cfg, err := loadConfig(env)
	if err != nil {
		return nil, err
	}
	st, err := loadState()
	if err != nil {
		return nil, err
	}
	home, err := config.Home()
	if err != nil {
		return nil, err
	}
	return &pass{env: env, cfg: cfg, st: st, home: home}, nil
}

func (p *pass) note(change string) { p.changes = append(p.changes, change) }

func (p *pass) commit() error {
	if len(p.changes) == 0 {
		return nil
	}
	subject := p.changes[0]
	body := ""
	if len(p.changes) > 1 {
		subject = fmt.Sprintf("%d file changes", len(p.changes))
		body = strings.Join(p.changes, "\n")
	}
	return p.env.Commit(subject, body)
}

// revive settles a deleted source that was changed elsewhere at the
// same time, which the sync repo's merge keeps alongside the deletion:
// a source whose files came back returns, and a live entry beats its
// tombstone.
func (p *pass) revive() error {
	dead, err := loadDeleted(p.env)
	if err != nil {
		return err
	}
	settled := false
	for _, name := range slices.Sorted(maps.Keys(dead)) {
		if _, ok := p.cfg.Sources[name]; !ok {
			if _, err := os.Stat(sourcePath(p.env, name)); err != nil {
				continue
			}
			p.cfg.Sources[name] = dead[name]
		}
		delete(dead, name)
		settled = true
		p.note("keep " + name + ", changed while it was deleted")
	}
	if !settled {
		return nil
	}
	if err := p.cfg.save(p.env); err != nil {
		return err
	}
	return saveDeleted(p.env, dead)
}

// buryMissing handles sources deleted in the sync folder directly: one
// this device synced is deleted everywhere, like deleting its output.
func (p *pass) buryMissing() error {
	synced := map[string]bool{}
	for _, r := range p.st.Outputs {
		synced[r.Source] = true
	}
	for _, name := range slices.Sorted(maps.Keys(p.cfg.Sources)) {
		if _, err := os.Stat(sourcePath(p.env, name)); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if !synced[name] {
			p.env.Log.Printf("files: %s lists %s, but %s/%s does not exist", ConfigFile, name, SourcesDir, name)
			continue
		}
		if err := bury(p.env, p.cfg, name); err != nil {
			return err
		}
		p.note("delete " + name)
	}
	return nil
}

// captureNew adds files that appeared in a synced directory here to its
// source, so they reach every device. Files lichen wrote here before and
// that are gone from the source were deleted elsewhere, not new.
func (p *pass) captureNew(ws map[string]want, dirs map[string]string) error {
	for _, root := range slices.Sorted(maps.Keys(dirs)) {
		source := dirs[root]
		if fi, err := os.Lstat(root); err != nil || !fi.IsDir() || underSymlink(p.home, root) {
			continue
		}
		for _, rel := range listFiles(root) {
			out := filepath.Join(root, filepath.FromSlash(rel))
			if _, ok := ws[out]; ok || p.st.Outputs[out] != nil {
				continue
			}
			content, err := os.ReadFile(out)
			if err != nil {
				continue
			}
			w := want{source: source, file: source + "/" + rel, path: out, root: root, dir: true}
			if err := track(p.env, p.st, w, content); err != nil {
				return err
			}
			if s := p.cfg.Sources[source]; !s.Private && private(out) {
				s.Private = true
				p.cfg.Sources[source] = s
				if err := p.cfg.save(p.env); err != nil {
					return err
				}
			}
			ws[out] = w
			p.note("add " + config.ContractHome(out))
		}
	}
	return nil
}

// capture takes local edits and deletions of outputs into their sources.
// An edit is merged with whatever changed in them elsewhere since. A
// deletion counts unless the output's sources changed meanwhile, which
// brings it back instead.
func (p *pass) capture(ws map[string]want) error {
	for _, out := range slices.Sorted(maps.Keys(ws)) {
		w := ws[out]
		rec := p.st.Outputs[out]
		// Behind a symlink, a missing file says nothing about deletion.
		if rec == nil || underSymlink(p.home, out) {
			continue
		}
		fi, err := os.Lstat(out)
		if errors.Is(err, fs.ErrNotExist) {
			if c, err := build(p.env, w.file); err == nil && hashOf(c.content()) == rec.Hash {
				if err := p.captureDeletion(w); err != nil {
					return err
				}
			}
			continue
		}
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		live, err := os.ReadFile(out)
		if err != nil || hashOf(live) == rec.Hash {
			continue
		}
		if err := p.captureEdit(w, rec, live); err != nil {
			return err
		}
	}
	return nil
}

// captureEdit takes an edit made to an output here into its sources, and
// writes the output again if they now compile to something else (with a
// change from elsewhere merged in, say). An edit lichen can't take on its
// own is held: the output stays as it is until `lichen resolve`.
func (p *pass) captureEdit(w want, rec *record, live []byte) error {
	sources, open, err := place(p.env, rec, live, nil, false)
	var c conflict
	switch {
	case errors.As(err, &c):
		p.hold(w.path, rec, c.Error())
		return nil
	case err != nil:
		return err
	case len(open) > 0:
		p.hold(w.path, rec, "which source do the new lines go in?")
		return nil
	}
	if err := take(p.env, p.st, w, sources, live); err != nil {
		p.env.Log.Printf("files: %s: %v", config.ContractHome(w.path), err)
	}
	if len(sources) > 0 {
		p.note("edit " + config.ContractHome(w.path))
	}
	return nil
}

// hold marks an output whose edit waits on `lichen resolve`, for why. It
// is said once.
func (p *pass) hold(out string, rec *record, why string) {
	if rec.Held == why {
		return
	}
	rec.Held = why
	msg := config.ContractHome(out) + ": " + why
	p.env.Log.Printf("files: %s (run `lichen resolve`)", msg)
	if p.env.Notify != nil {
		p.env.Notify(msg)
	}
}

// captureDeletion deletes the source of an output deleted here: one file
// of a synced directory, or a whole source (and with it, its other
// outputs everywhere). A directory left with no files goes too.
func (p *pass) captureDeletion(w want) error {
	delete(p.st.Outputs, w.path)
	p.note("delete " + config.ContractHome(w.path))
	if _, ok := p.cfg.Sources[w.source]; !ok {
		return nil // buried already, by another of its outputs
	}
	if !w.dir {
		return bury(p.env, p.cfg, w.source)
	}
	f := sourcePath(p.env, w.file)
	if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	removeEmptyParents(filepath.Dir(f), sourcePath(p.env, w.source))
	if len(listFiles(sourcePath(p.env, w.source))) == 0 {
		return bury(p.env, p.cfg, w.source)
	}
	return nil
}

// apply writes every output whose sources moved on, unless it holds an
// edit made here that waits on `lichen resolve`.
func (p *pass) apply(ws map[string]want) error {
	written := 0
	for _, out := range slices.Sorted(maps.Keys(ws)) {
		w := ws[out]
		if underSymlink(p.home, out) {
			p.env.Log.Printf("files: NOT syncing %s (a symlink sits above it)", config.ContractHome(out))
			continue
		}
		c, err := build(p.env, w.file)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			p.env.Log.Printf("files: NOT syncing %s: %v", config.ContractHome(out), err)
			continue
		}
		rec := p.st.Outputs[out]
		fi, err := os.Lstat(out)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			p.env.Log.Printf("files: %s: %v", config.ContractHome(out), err)
			continue
		case fi.Mode()&os.ModeSymlink != 0:
			p.env.Log.Printf("files: NOT syncing %s (it is a symlink)", config.ContractHome(out))
			continue
		case !fi.Mode().IsRegular():
			// Something other than a file in the way (a directory, most
			// likely): moved aside, never deleted, as it may hold files
			// that exist nowhere else.
			to, err := backup.Discard(out, true)
			if err != nil {
				return fmt.Errorf("replacing %s: %w", out, err)
			}
			p.env.Log.Printf("files: moved %s out of the way to %s", config.ContractHome(out), to)
		default:
			live, err := os.ReadFile(out)
			if err != nil {
				continue
			}
			switch {
			case bytes.Equal(live, c.content()):
				if err := p.st.remember(w, c); err != nil {
					return err
				}
				continue
			case rec == nil:
				// A file this device had before it synced this one: the
				// synced version replaces it.
				to, err := backup.Discard(out, p.env.Settings.Backups)
				if err != nil {
					return fmt.Errorf("replacing %s: %w", out, err)
				}
				p.env.Log.Printf("files: replacing local %s with the synced version%s", config.ContractHome(out), keptAt(to))
			case hashOf(live) != rec.Hash:
				continue // edited here, and held
			}
		}
		if err := writeFile(out, c.content(), isExec(sourcePath(p.env, w.file)), p.cfg.Sources[w.source].Private); err != nil {
			return err
		}
		if err := p.st.remember(w, c); err != nil {
			return err
		}
		written++
	}
	if written > 0 {
		p.env.Log.Printf("files: wrote %d output(s)", written)
	}
	return nil
}

// syncModes carries a change of the executable bit, whichever side made
// it, ahead of the content, so neither side's content change undoes the
// other's chmod.
func (p *pass) syncModes(ws map[string]want) error {
	for _, out := range slices.Sorted(maps.Keys(ws)) {
		w, rec := ws[out], p.st.Outputs[out]
		src := sourcePath(p.env, w.file)
		if rec == nil || !regular(out) || !regular(src) || underSymlink(p.home, out) {
			continue
		}
		here := isExec(out)
		if here == isExec(src) {
			continue
		}
		var err error
		if here != rec.Exec {
			p.note("chmod " + config.ContractHome(out))
			err = setExec(src, here)
		} else {
			err = setExec(out, !here)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// release handles outputs lichen wrote here that are no longer meant for
// this device. A file deleted on another device is deleted here too,
// unless it was edited here since: then the edit wins, and the file
// comes back everywhere. Anything else (a source that stopped syncing,
// an output moved or scoped away from this device) stays, as a plain
// file lichen no longer touches.
func (p *pass) release(ws map[string]want, dirs map[string]string) error {
	dead, err := loadDeleted(p.env)
	if err != nil {
		return err
	}
	deleted := func(out string, rec *record) bool {
		_, entryLives := p.cfg.Sources[rec.Source]
		_, buried := dead[rec.Source]
		return buried && !entryLives || entryLives && dirs[rec.Root] == rec.Source && rec.Root != out
	}
	var released []string
	for _, out := range slices.Sorted(maps.Keys(p.st.Outputs)) {
		if _, ok := ws[out]; !ok {
			released = append(released, out)
		}
	}

	revived := map[string]bool{}
	for _, out := range released {
		rec := p.st.Outputs[out]
		if !deleted(out, rec) || !regular(out) || underSymlink(p.home, out) {
			continue
		}
		live, err := os.ReadFile(out)
		if err != nil || hashOf(live) == rec.Hash {
			continue
		}
		if s, ok := dead[rec.Source]; ok {
			p.cfg.Sources[rec.Source] = s
			delete(dead, rec.Source)
		}
		// What it was compiled from comes back as it was when last written
		// here, to take the edit like any other.
		for f, h := range rec.Deps {
			if _, err := os.Stat(sourcePath(p.env, f)); err == nil {
				continue
			}
			was, err := object(h)
			if err != nil {
				return err
			}
			if err := writeFile(sourcePath(p.env, f), was, f == rec.File && rec.Exec, false); err != nil {
				return err
			}
		}
		if err := p.captureEdit(rec.want(out), rec, live); err != nil {
			return err
		}
		revived[rec.Source] = true
		p.note("bring back " + config.ContractHome(out) + ", edited here while it was deleted elsewhere")
	}
	if len(revived) > 0 {
		if err := p.cfg.save(p.env); err != nil {
			return err
		}
		if err := saveDeleted(p.env, dead); err != nil {
			return err
		}
	}

	for _, out := range released {
		rec := p.st.Outputs[out]
		if revived[rec.Source] {
			continue // the next pass syncs the rest of it again
		}
		delete(p.st.Outputs, out)
		if !regular(out) {
			continue
		}
		if !deleted(out, rec) {
			p.env.Log.Printf("files: %s no longer syncs here, left as a plain file", config.ContractHome(out))
			continue
		}
		if underSymlink(p.home, out) {
			p.env.Log.Printf("files: NOT deleting %s (a symlink sits above it), no longer synced", config.ContractHome(out))
			continue
		}
		if live, err := os.ReadFile(out); err != nil || hashOf(live) != rec.Hash {
			continue
		}
		to, err := backup.Discard(out, p.env.Settings.Backups)
		if err != nil {
			return err
		}
		p.env.Log.Printf("files: deleted %s (deleted on another device)%s", config.ContractHome(out), keptAt(to))
		if rec.Root != out {
			stop := filepath.Dir(rec.Root)
			if dirs[rec.Root] == rec.Source {
				stop = rec.Root
			}
			removeEmptyParents(filepath.Dir(out), stop)
		}
	}
	return nil
}

// private reports whether no one but the owner may read path.
func private(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().Perm()&0o077 == 0
}

func regular(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular()
}

func keptAt(to string) string {
	if to == "" {
		return ""
	}
	return " (kept in " + config.ContractHome(to) + ")"
}

// conflict is why an edit made here can't be taken into its sources as
// it is.
type conflict struct{ error }

// place works out what an edit to an output here makes of its sources:
// the new content of each it changes, merged with whatever changed in
// them elsewhere since. Lines that could go to more than one source come
// back unplaced, unless into places them (see chooser). An edit that
// clashes with a change from elsewhere is a conflict, unless ours: then
// the edit wins where they clash.
func place(env *core.Env, rec *record, live []byte, into []string, ours bool) (map[string][]byte, []question, error) {
	b, err := base(rec)
	if err != nil {
		return nil, nil, conflict{fmt.Errorf("lichen can't tell what changed in it: %w", err)}
	}
	// A file that inserts nothing, here or elsewhere, is its output.
	plain := b.raw != nil || len(b.deps) == 1 && len(b.lines) == len(split(b.deps[rec.File]).lines)
	var edited map[string][]byte
	switch {
	case plain:
		edited = map[string][]byte{rec.File: escape(live)}
	case binary(live):
		return nil, nil, conflict{errors.New("it isn't text now, and it inserts other sources")}
	default:
		var open []question
		edited, open, err = unmap(b, split(live), chooser(into))
		if errors.Is(err, errClash) {
			return nil, nil, conflict{err}
		}
		if err != nil || len(open) > 0 {
			return nil, open, err
		}
	}
	sources := map[string][]byte{}
	for f, e := range edited {
		cur, err := os.ReadFile(sourcePath(env, f))
		switch {
		case errors.Is(err, fs.ErrNotExist) && ours:
			sources[f] = e
			continue
		case errors.Is(err, fs.ErrNotExist):
			return nil, nil, conflict{fmt.Errorf("edited here, and its source %s was deleted elsewhere", f)}
		case err != nil:
			return nil, nil, err
		}
		merged, ok := merge3(b.deps[f], e, cur, ours)
		switch {
		case ok:
		case ours:
			merged = e
		default:
			return nil, nil, conflict{errors.New("edited here and on another device in the same place")}
		}
		if !bytes.Equal(merged, cur) {
			sources[f] = merged
		}
	}
	return sources, nil, nil
}

// take writes the sources an edit made here changed, then the output
// compiled from them, unless it holds that already (live), and remembers
// it.
func take(env *core.Env, st *state, w want, sources map[string][]byte, live []byte) error {
	for _, f := range slices.Sorted(maps.Keys(sources)) {
		path := sourcePath(env, f)
		if err := writeFile(path, sources[f], isExec(path), false); err != nil {
			return err
		}
	}
	c, err := build(env, w.file)
	if err != nil {
		return err
	}
	if !bytes.Equal(c.content(), live) {
		if err := writeFile(w.path, c.content(), isExec(w.path), false); err != nil {
			return err
		}
	}
	return st.remember(w, c)
}

// track starts syncing a file found at an output, as a new source file.
func track(env *core.Env, st *state, w want, content []byte) error {
	if err := writeFile(sourcePath(env, w.file), escape(content), isExec(w.path), false); err != nil {
		return err
	}
	c, err := build(env, w.file)
	if err != nil {
		return err
	}
	return st.remember(w, c)
}

// build compiles a source file for this device.
func build(env *core.Env, file string) (*compiled, error) {
	return compile(file, func(name string) ([]byte, error) {
		return os.ReadFile(sourcePath(env, name))
	}, env.Device.Groups)
}

// merge3 merges two edits of base, as git would. ok is false when they
// overlap (unless favorOurs: then ours is taken where they do), or any
// is binary.
func merge3(base, ours, theirs []byte, favorOurs bool) ([]byte, bool) {
	switch {
	case bytes.Equal(base, theirs):
		return ours, true
	case bytes.Equal(base, ours):
		return theirs, true
	}
	if binary(base) || binary(ours) || binary(theirs) {
		return nil, false
	}
	args := []string{"merge-file", "-p", "--quiet"}
	if favorOurs {
		args = append(args, "--ours")
	}
	out, err := gitOn(map[string][]byte{"ours": ours, "base": base, "theirs": theirs}, append(args, "ours", "base", "theirs")...)
	return out, err == nil
}

// gitOn runs git on content, each in a file of its name in a directory of
// their own, and returns what it prints.
func gitOn(files map[string][]byte, args ...string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "lichen")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return nil, err
		}
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Output()
}

// writeFile replaces path's content, keeping an existing file's
// permissions apart from the executable bit, which follows exec. A new
// file is readable by anyone, unless private: then it and any directory
// made for it are the owner's alone.
func writeFile(path string, content []byte, exec, private bool) error {
	perm := os.FileMode(0o644)
	if private {
		perm = 0o600
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
	}
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	return config.WriteFile(path, content, execPerm(perm, exec))
}

func execPerm(perm os.FileMode, exec bool) os.FileMode {
	if exec {
		return perm | (perm&0o444)>>2
	}
	return perm &^ 0o111
}

func setExec(path string, exec bool) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.Chmod(path, execPerm(fi.Mode().Perm(), exec))
}

func isExec(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().Perm()&0o100 != 0
}

// underSymlink reports whether any directory between home (exclusive)
// and abs is a symlink: syncing through one would read or write whatever
// the link points at.
func underSymlink(home, abs string) bool {
	for d := filepath.Dir(abs); len(d) > len(home); d = filepath.Dir(d) {
		if fi, err := os.Lstat(d); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

// removeEmptyParents removes dir and its parents while they are empty,
// stopping before stop.
func removeEmptyParents(dir, stop string) {
	for dir != stop && config.Within(dir, stop) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// Watched lists what concerns the files module on this device: every
// output file, and the tree of every synced directory.
func Watched(env *core.Env) (files, trees []string) {
	c, err := loadConfig(env)
	if err != nil {
		return nil, nil
	}
	ws, dirs := wants(env, c, nil)
	for out, w := range ws {
		if !w.dir {
			files = append(files, out)
		}
	}
	return files, slices.Collect(maps.Keys(dirs))
}
