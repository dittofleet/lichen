package files

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"lichen/internal/config"
	"lichen/internal/core"
)

// outputPath checks that p can be an output, and makes it absolute.
func outputPath(env *core.Env, p string) (string, error) {
	abs, err := config.Abs(p)
	if err != nil {
		return "", err
	}
	return abs, checkOutput(env, abs)
}

// owner finds the source with an output at or around abs, on any device.
func owner(c *fileConfig, abs string) (name string, out Output, ok bool) {
	for _, n := range slices.Sorted(maps.Keys(c.Sources)) {
		for _, o := range c.Sources[n].To {
			if root, err := o.root(); err == nil && (config.Within(abs, root) || config.Within(root, abs)) {
				return n, o, true
			}
		}
	}
	return "", Output{}, false
}

// claimed refuses a new output at abs that overlaps one already syncing.
func claimed(c *fileConfig, abs string) error {
	n, o, ok := owner(c, abs)
	if !ok {
		return nil
	}
	if root, _ := o.root(); root != abs && config.Within(root, abs) {
		return fmt.Errorf("%s holds %s, which already syncs", config.ContractHome(abs), config.ContractHome(root))
	}
	return fmt.Errorf("%s already syncs, through %s", config.ContractHome(abs), n)
}

// outputAt finds the output at p exactly.
func outputAt(c *fileConfig, p string) (name string, out Output, err error) {
	abs, err := config.Abs(p)
	if err != nil {
		return "", Output{}, err
	}
	n, o, ok := owner(c, abs)
	if root, _ := o.root(); !ok || root != abs {
		return "", Output{}, fmt.Errorf("%s is not a source or an output (see: lichen files)", p)
	}
	return n, o, nil
}

// Add starts syncing files or directories: each becomes a source, with
// an output where it is. Only scopes the new outputs to some devices.
func Add(env *core.Env, paths []string, name string, only []string) ([]string, error) {
	if name != "" && len(paths) > 1 {
		return nil, errors.New("--name takes one path at a time")
	}
	c, err := loadConfig(env)
	if err != nil {
		return nil, err
	}
	st, err := loadState()
	if err != nil {
		return nil, err
	}
	dead, err := loadDeleted(env)
	if err != nil {
		return nil, err
	}
	var added []string
	for _, p := range paths {
		abs, err := outputPath(env, p)
		if err != nil {
			return nil, err
		}
		fi, err := os.Lstat(abs)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s doesn't exist", config.ContractHome(abs))
		}
		if err != nil {
			return nil, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s is a symlink: sync what it points at instead", p)
		}
		if err := claimed(c, abs); err != nil {
			return nil, err
		}
		n := name
		if n == "" {
			n = sourceName(config.ContractHome(abs))
		}
		if err := validName(n); err != nil {
			return nil, err
		}
		// One source inside another would have its files synced twice.
		for e := range c.Sources {
			if config.Within(n, e) || config.Within(e, n) {
				return nil, fmt.Errorf("%s would overlap the source %s: pick another name with --name", n, e)
			}
		}
		if _, err := os.Lstat(sourcePath(env, n)); err == nil {
			return nil, fmt.Errorf("%s/%s already exists: pick another name with --name", SourcesDir, n)
		}
		rels, secret := []string{""}, private(abs)
		if fi.IsDir() {
			if rels = listFiles(abs); len(rels) == 0 {
				return nil, fmt.Errorf("%s has no files to sync", p)
			}
		} else if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a file or directory", p)
		}
		for _, rel := range rels {
			out, file := abs, n
			if rel != "" {
				out, file = filepath.Join(abs, filepath.FromSlash(rel)), path.Join(n, rel)
			}
			content, err := os.ReadFile(out)
			if err != nil {
				return nil, err
			}
			if err := track(env, st, want{source: n, file: file, path: out, root: abs, dir: rel != ""}, content); err != nil {
				return nil, err
			}
			secret = secret || private(out)
		}
		c.Sources[n] = Source{To: []Output{{Path: config.ContractHome(abs), Only: only}}, Private: secret}
		// A source deleted around this one is gone for good: it would
		// otherwise look like it came back.
		for d := range dead {
			if config.Within(n, d) || config.Within(d, n) {
				delete(dead, d)
			}
		}
		added = append(added, n)
	}
	if err := c.save(env); err != nil {
		return nil, err
	}
	if err := saveDeleted(env, dead); err != nil {
		return nil, err
	}
	return added, st.save()
}

// AddOutput gives an existing source another output. A file already
// there is replaced by the source on the next pass. A directory there
// may only hold files the source has: any other file in it would join
// the source, and go everywhere.
func AddOutput(env *core.Env, source, p string, only []string) error {
	c, err := loadConfig(env)
	if err != nil {
		return err
	}
	s, ok := c.Sources[source]
	if !ok {
		return fmt.Errorf("no source named %s (see: lichen files)", source)
	}
	abs, err := outputPath(env, p)
	if err != nil {
		return err
	}
	if err := claimed(c, abs); err != nil {
		return err
	}
	if fi, err := os.Stat(sourcePath(env, source)); err == nil && fi.IsDir() {
		has := listFiles(sourcePath(env, source))
		for _, rel := range listFiles(abs) {
			if !slices.Contains(has, rel) {
				return fmt.Errorf("%s holds files %s doesn't have (%s): move them away first", config.ContractHome(abs), source, rel)
			}
		}
	}
	s.To = append(s.To, Output{Path: config.ContractHome(abs), Only: only})
	c.Sources[source] = s
	return c.save(env)
}

// Remove stops syncing: an output path stops being one of its source's
// outputs, and a source name, or the last output of one, removes the
// source. Every file stays where it is, as a plain file. (Deleting a
// file deletes it everywhere instead.)
func Remove(env *core.Env, args []string) ([]string, error) {
	c, err := loadConfig(env)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, a := range args {
		if _, ok := c.Sources[a]; ok {
			if err := dropSource(env, c, a); err != nil {
				return nil, err
			}
			removed = append(removed, a)
			continue
		}
		n, o, err := outputAt(c, a)
		if err != nil {
			return nil, err
		}
		s := c.Sources[n]
		s.To = slices.DeleteFunc(s.To, func(x Output) bool { return x.Path == o.Path })
		c.Sources[n] = s
		if len(s.To) == 0 {
			if err := dropSource(env, c, n); err != nil {
				return nil, err
			}
		}
		removed = append(removed, o.Path)
	}
	return removed, c.save(env)
}

// Only scopes outputs to devices and groups: one output by its path, or
// every output of a source by its name. No groups is every device.
func Only(env *core.Env, target string, groups []string) error {
	c, err := loadConfig(env)
	if err != nil {
		return err
	}
	n, only := target, ""
	if _, ok := c.Sources[target]; !ok {
		var o Output
		if n, o, err = outputAt(c, target); err != nil {
			return err
		}
		only = o.Path
	}
	s := c.Sources[n]
	for i := range s.To {
		if only == "" || s.To[i].Path == only {
			s.To[i].Only = groups
		}
	}
	c.Sources[n] = s
	return c.save(env)
}

// Recover brings back what was deleted: a whole source, by name or by
// one of its outputs, or one file of a synced directory, by its path.
// Both come from the sync folder's history, as of just before the
// deletion.
func Recover(env *core.Env, args []string) ([]string, error) {
	c, err := loadConfig(env)
	if err != nil {
		return nil, err
	}
	dead, err := loadDeleted(env)
	if err != nil {
		return nil, err
	}
	var recovered []string
	for _, a := range args {
		name, ok := buriedAs(dead, a)
		if ok {
			if _, err := restore(env, path.Join(SourcesDir, name)); err != nil {
				return nil, err
			}
			c.Sources[name] = dead[name]
			delete(dead, name)
			recovered = append(recovered, name)
			continue
		}
		abs, err := config.Abs(a)
		if err != nil {
			return nil, err
		}
		n, o, ok := owner(c, abs)
		root, _ := o.root()
		if !ok || abs == root {
			return nil, fmt.Errorf("nothing deleted at %s", a)
		}
		rel, _ := filepath.Rel(root, abs)
		file := path.Join(SourcesDir, n, filepath.ToSlash(rel))
		if _, err := restore(env, file); err != nil {
			return nil, fmt.Errorf("nothing deleted at %s", a)
		}
		recovered = append(recovered, config.ContractHome(abs))
	}
	if err := c.save(env); err != nil {
		return nil, err
	}
	return recovered, saveDeleted(env, dead)
}

func buriedAs(dead map[string]Source, a string) (string, bool) {
	if _, ok := dead[a]; ok {
		return a, true
	}
	abs, err := config.Abs(a)
	if err != nil {
		return "", false
	}
	for name, s := range dead {
		for _, o := range s.To {
			if root, err := o.root(); err == nil && root == abs {
				return name, true
			}
		}
	}
	return "", false
}

// restore brings file back from just before the commit that deleted
// it, which it returns.
func restore(env *core.Env, file string) (string, error) {
	commit, err := deletedIn(env, file)
	if err != nil {
		return "", err
	}
	_, err = env.Git("checkout", commit+"^", "--", file)
	return commit, err
}

// deletedIn finds the commit that deleted file.
func deletedIn(env *core.Env, file string) (string, error) {
	out, err := env.Git("log", "-1", "--format=%H", "--diff-filter=D", "--", file)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", fmt.Errorf("no deletion of %s in the sync folder's history", file)
	}
	return out, nil
}

// Resolve takes or drops the edit a held output holds: keep "synced"
// drops it for what its sources compile to, "here" takes it, winning
// where it clashes with a change from elsewhere, and no keep takes it
// only if it doesn't. into names the source for each question about new
// lines in turn (see Inspect), the last one going for any after it.
func Resolve(env *core.Env, p, keep string, into []string) error {
	if keep != "" && keep != "here" && keep != "synced" {
		return errors.New("--keep takes here or synced")
	}
	abs, st, rec, live, err := held(p)
	if err != nil {
		return err
	}
	var sources map[string][]byte
	if keep != "synced" {
		var open []question
		sources, open, err = place(env, rec, live, into, keep == "here")
		var c conflict
		switch {
		case errors.As(err, &c) && c.keepable:
			return fmt.Errorf("%s: %v (--keep here or --keep synced)", p, c)
		case errors.As(err, &c):
			return fmt.Errorf("%s: %v, so it can't be kept as it is (--keep synced, or edit its sources)", p, c)
		case err != nil:
			return err
		case len(open) > 0:
			return fmt.Errorf("%s: the new lines could go in %s (--into one of them)", p, strings.Join(sourcesOf(open[0]), ", "))
		}
	}
	if err := take(env, st, rec.want(abs), sources, live); err != nil {
		return err
	}
	return st.save()
}

// Question is lines added to an output that could go in more than one
// of its sources.
type Question struct {
	Lines   []string
	Sources []string
}

// Pending is what holds the edit at a held output.
type Pending struct {
	Questions []Question
	Conflict  string // why it conflicts, if it does
	Keepable  bool   // whether keeping it here would settle that
}

// Inspect says what holds the edit at a held output: the questions left
// once into answers the first ones (as Resolve takes it), or with none
// left, the conflict, if it is one.
func Inspect(env *core.Env, p string, into []string) (Pending, error) {
	_, _, rec, live, err := held(p)
	if err != nil {
		return Pending{}, err
	}
	_, open, err := place(env, rec, live, into, false)
	var c conflict
	if errors.As(err, &c) {
		return Pending{Conflict: c.Error(), Keepable: c.keepable}, nil
	}
	var pd Pending
	for _, q := range open {
		pd.Questions = append(pd.Questions, Question{Lines: q.lines, Sources: sourcesOf(q)})
	}
	return pd, err
}

// Diff shows how a held output differs from what its sources compile to.
func Diff(env *core.Env, p string, color bool) ([]byte, error) {
	_, _, rec, live, err := held(p)
	if err != nil {
		return nil, err
	}
	c, err := build(env, rec.File)
	if err != nil {
		return nil, err
	}
	colors := "--no-color"
	if color {
		colors = "--color"
	}
	return gitDiff(map[string][]byte{"synced": c.content(), "here": live}, colors, "synced", "here")
}

// ErrNotHeld is an output that has nothing to resolve.
var ErrNotHeld = errors.New("nothing to resolve")

func held(p string) (abs string, st *state, rec *record, live []byte, err error) {
	if abs, err = config.Abs(p); err != nil {
		return
	}
	if st, err = loadState(); err != nil {
		return
	}
	if rec = st.Outputs[abs]; rec == nil || rec.Held == "" {
		err = fmt.Errorf("%s: %w", config.ContractHome(abs), ErrNotHeld)
		return
	}
	live, err = os.ReadFile(abs)
	return
}

func sourcesOf(q question) []string {
	var names []string
	for _, s := range q.spots {
		names = append(names, s.file)
	}
	return names
}

// Entry is one source, for listing.
type Entry struct {
	Name    string
	Path    string // in the sync folder, absolute
	Dir     bool
	Private bool
	Outputs []Output
}

// List returns every source and where it goes.
func List(env *core.Env) ([]Entry, error) {
	c, err := loadConfig(env)
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, n := range slices.Sorted(maps.Keys(c.Sources)) {
		p := sourcePath(env, n)
		fi, err := os.Stat(p)
		var outs []Output
		for _, o := range c.Sources[n].To {
			// Spelled the way issues are, however files.json has it.
			if root, err := o.root(); err == nil {
				o.Path = config.ContractHome(root)
			}
			outs = append(outs, o)
		}
		entries = append(entries, Entry{Name: n, Path: p, Dir: err == nil && fi.IsDir(), Private: c.Sources[n].Private, Outputs: outs})
	}
	return entries, nil
}

// Hold is an output file holding an edit made on this device that waits
// on `lichen resolve`, and why.
type Hold struct {
	Path string // ~/-relative
	Root string // its output, ~/-relative: Path, or its synced directory
	Why  string
}

// Holds lists this device's held outputs.
func Holds() []Hold {
	st, err := loadState()
	if err != nil {
		return nil
	}
	var out []Hold
	for _, p := range slices.Sorted(maps.Keys(st.Outputs)) {
		if r := st.Outputs[p]; r.Held != "" {
			out = append(out, Hold{Path: config.ContractHome(p), Root: config.ContractHome(r.Root), Why: r.Held})
		}
	}
	return out
}

// Broken lists this device's outputs whose sources don't compile (an
// insert of a source that is gone, say), which stay as they are until
// they do.
func Broken(env *core.Env) []Hold {
	c, err := loadConfig(env)
	if err != nil {
		return nil
	}
	ws, _ := wants(env, c, nil)
	var out []Hold
	for _, p := range slices.Sorted(maps.Keys(ws)) {
		if _, err := build(env, ws[p].file); err != nil {
			out = append(out, Hold{Path: config.ContractHome(p), Root: config.ContractHome(ws[p].root), Why: err.Error()})
		}
	}
	return out
}
