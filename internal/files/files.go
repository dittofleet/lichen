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

func (Module) Name() string { return "files" }

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
// to date, with edits on both sides merged.
func (Module) Reconcile(env *core.Env) error {
	p, err := newPass(env)
	if err != nil {
		return err
	}
	if err := p.buryMissing(); err != nil {
		return err
	}
	ws, roots := p.wants()
	if err := p.captureNew(ws, roots); err != nil {
		return err
	}
	if err := p.capture(ws); err != nil {
		return err
	}
	if len(p.changes) > 0 {
		// A capture can delete a source or add files to one.
		if p.cfg, err = loadConfig(env); err != nil {
			return err
		}
		ws, roots = p.wants()
	}
	if err := p.apply(ws); err != nil {
		return err
	}
	if err := p.release(ws, roots); err != nil {
		return err
	}
	if err := p.st.save(); err != nil {
		return err
	}
	return p.commit()
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
	_, err := p.env.Commit(subject, body)
	return err
}

// rootInfo describes one output this device has.
type rootInfo struct {
	source string
	dir    bool
}

func (p *pass) wants() (map[string]want, map[string]rootInfo) {
	ws := wants(p.env, p.cfg)
	roots := map[string]rootInfo{}
	for _, w := range ws {
		roots[w.root] = rootInfo{source: w.source, dir: w.dir}
	}
	// A synced directory with no files yet is still an output here.
	for name, s := range p.cfg.Sources {
		if fi, err := os.Stat(sourcePath(p.env, name)); err == nil && fi.IsDir() {
			for _, o := range s.To {
				if root, err := config.ExpandHome(o.Path); err == nil && p.env.Device.In(o.Only) {
					roots[filepath.Clean(root)] = rootInfo{source: name, dir: true}
				}
			}
		}
	}
	return ws, roots
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
func (p *pass) captureNew(ws map[string]want, roots map[string]rootInfo) error {
	for _, root := range slices.Sorted(maps.Keys(roots)) {
		info := roots[root]
		if !info.dir {
			continue
		}
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
			file := info.source + "/" + rel
			if err := writeFile(sourcePath(p.env, file), content, isExec(out)); err != nil {
				return err
			}
			if err := p.st.remember(out, info.source, file, root, content); err != nil {
				return err
			}
			ws[out] = want{source: info.source, file: file, path: out, root: root, dir: true}
			p.note("add " + config.ContractHome(out))
		}
	}
	return nil
}

// capture takes local edits and deletions of outputs into their sources,
// wherever the source has not changed since lichen wrote the output. A
// source that changed too is merged by apply instead.
func (p *pass) capture(ws map[string]want) error {
	for _, out := range slices.Sorted(maps.Keys(ws)) {
		w := ws[out]
		rec := p.st.Outputs[out]
		if rec == nil || rec.Conflict {
			continue
		}
		src, err := os.ReadFile(sourcePath(p.env, w.file))
		if err != nil || hashOf(src) != rec.Hash {
			continue
		}
		fi, err := os.Lstat(out)
		if errors.Is(err, fs.ErrNotExist) {
			if err := p.captureDeletion(w); err != nil {
				return err
			}
			continue
		}
		if err != nil || !fi.Mode().IsRegular() || underSymlink(p.home, out) {
			continue
		}
		live, err := os.ReadFile(out)
		if err != nil || hashOf(live) == rec.Hash {
			continue
		}
		if err := writeFile(sourcePath(p.env, w.file), live, isExec(out)); err != nil {
			return err
		}
		if err := p.st.remember(out, w.source, w.file, w.root, live); err != nil {
			return err
		}
		p.note("edit " + config.ContractHome(out))
	}
	return nil
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

// apply writes every output whose source moved on, merging when the
// output was edited here as well.
func (p *pass) apply(ws map[string]want) error {
	written := 0
	for _, out := range slices.Sorted(maps.Keys(ws)) {
		w := ws[out]
		srcPath := sourcePath(p.env, w.file)
		src, err := os.ReadFile(srcPath)
		if err != nil {
			continue
		}
		if underSymlink(p.home, out) {
			p.env.Log.Printf("files: NOT syncing %s (a symlink sits above it)", config.ContractHome(out))
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
			done, err := p.reconcileExisting(w, rec, live, src)
			if err != nil {
				return err
			}
			if done {
				continue
			}
		}
		if err := writeFile(out, src, isExec(srcPath)); err != nil {
			return err
		}
		if err := p.st.remember(out, w.source, w.file, w.root, src); err != nil {
			return err
		}
		written++
	}
	if written > 0 {
		p.env.Log.Printf("files: wrote %d output(s)", written)
	}
	return nil
}

// reconcileExisting handles an output that exists, reporting whether it
// is settled (true) or should be overwritten with the source (false).
func (p *pass) reconcileExisting(w want, rec *record, live, src []byte) (bool, error) {
	out := w.path
	switch {
	case bytes.Equal(live, src):
		return true, p.st.remember(out, w.source, w.file, w.root, src)
	case rec == nil:
		// A file this device had before it synced this one: the synced
		// version replaces it.
		to, err := backup.Discard(out, p.env.Settings.Backups)
		if err != nil {
			return false, fmt.Errorf("replacing %s: %w", out, err)
		}
		p.env.Log.Printf("files: replacing local %s with the synced version%s", config.ContractHome(out), keptAt(to))
		return false, nil
	case hashOf(live) == rec.Hash:
		return false, nil // only the source moved on
	case hashOf(src) == rec.Hash:
		return true, nil // edited here, held by an unresolved conflict
	}
	b, err := base(rec)
	if err != nil {
		return true, err
	}
	merged, ok := merge3(b, live, src)
	if !ok {
		if !rec.Conflict {
			rec.Conflict = true
			p.env.Log.Printf("files: CONFLICT in %s: edited here and on another device in the same place. Run `lichen files resolve %s --keep here` or `--keep synced`", config.ContractHome(out), config.ContractHome(out))
		}
		return true, nil
	}
	if err := writeFile(out, merged, isExec(out)); err != nil {
		return true, err
	}
	if err := writeFile(sourcePath(p.env, w.file), merged, isExec(out)); err != nil {
		return true, err
	}
	p.note("merge " + config.ContractHome(out))
	return true, p.st.remember(out, w.source, w.file, w.root, merged)
}

// release handles outputs lichen wrote here that are no longer meant for
// this device. A file deleted on another device is deleted here too,
// unless it was edited here since. Anything else (a source that stopped
// syncing, an output moved or scoped away from this device) stays, as a
// plain file lichen no longer touches.
func (p *pass) release(ws map[string]want, roots map[string]rootInfo) error {
	dead, err := loadDeleted(p.env)
	if err != nil {
		return err
	}
	for _, out := range slices.Sorted(maps.Keys(p.st.Outputs)) {
		if _, ok := ws[out]; ok {
			continue
		}
		rec := p.st.Outputs[out]
		delete(p.st.Outputs, out)
		_, entryLives := p.cfg.Sources[rec.Source]
		_, buried := dead[rec.Source]
		info, rootLives := roots[rec.Root]
		rootLives = rootLives && info.source == rec.Source
		deleted := buried && !entryLives || entryLives && rootLives && rec.Root != out
		fi, err := os.Lstat(out)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if !deleted {
			p.env.Log.Printf("files: %s no longer syncs here, left as a plain file", config.ContractHome(out))
			continue
		}
		live, err := os.ReadFile(out)
		if err != nil {
			continue
		}
		if hashOf(live) != rec.Hash {
			p.env.Log.Printf("files: %s was deleted on another device, but it was edited here: kept, no longer synced", config.ContractHome(out))
			continue
		}
		to, err := backup.Discard(out, p.env.Settings.Backups)
		if err != nil {
			return err
		}
		p.env.Log.Printf("files: deleted %s (deleted on another device)%s", config.ContractHome(out), keptAt(to))
		if rec.Root != out {
			stop := filepath.Dir(rec.Root)
			if rootLives {
				stop = rec.Root
			}
			removeEmptyParents(filepath.Dir(out), stop)
		}
	}
	return nil
}

func keptAt(to string) string {
	if to == "" {
		return ""
	}
	return " (kept in " + config.ContractHome(to) + ")"
}

// merge3 merges two edits of base, as git would. ok is false when they
// overlap, or either side is binary.
func merge3(base, ours, theirs []byte) ([]byte, bool) {
	for _, b := range [][]byte{base, ours, theirs} {
		if bytes.IndexByte(b, 0) >= 0 {
			return nil, false
		}
	}
	dir, err := os.MkdirTemp("", "lichen-merge")
	if err != nil {
		return nil, false
	}
	defer os.RemoveAll(dir)
	names := []string{"ours", "base", "theirs"}
	for i, b := range [][]byte{ours, base, theirs} {
		if os.WriteFile(filepath.Join(dir, names[i]), b, 0o600) != nil {
			return nil, false
		}
	}
	cmd := exec.Command("git", "merge-file", "-p", "--quiet", "ours", "base", "theirs")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

// writeFile replaces path's content through a temp file and rename, so a
// crash never leaves half a file. An existing file keeps its permissions
// apart from the executable bit, which follows exec.
func writeFile(path string, content []byte, exec bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	if exec {
		perm |= (perm & 0o444) >> 2
	} else {
		perm &^= 0o111
	}
	tmp := path + ".lichen-tmp"
	if err := os.WriteFile(tmp, content, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
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
	for dir != stop && strings.HasPrefix(dir, stop+string(filepath.Separator)) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// Watched lists what concerns the files module on this device: every
// output file, and the tree of every synced directory.
func (Module) Watched(env *core.Env) (files, trees []string) {
	p, err := newPass(env)
	if err != nil {
		return nil, nil
	}
	ws, roots := p.wants()
	for out, w := range ws {
		if !w.dir {
			files = append(files, out)
		}
	}
	for root, info := range roots {
		if info.dir {
			trees = append(trees, root)
		}
	}
	slices.Sort(files)
	slices.Sort(trees)
	return files, trees
}
