package files

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"

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
		if err != nil {
			return nil, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s is a symlink: sync what it points at instead", p)
		}
		if n, _, ok := owner(c, abs); ok {
			return nil, fmt.Errorf("%s already syncs, through %s", config.ContractHome(abs), n)
		}
		n := name
		if n == "" {
			n = sourceName(config.ContractHome(abs))
		}
		if err := validName(n); err != nil {
			return nil, err
		}
		if _, ok := c.Sources[n]; ok {
			return nil, fmt.Errorf("a source named %s already exists: pick another with --name", n)
		}
		if _, err := os.Lstat(sourcePath(env, n)); err == nil {
			return nil, fmt.Errorf("%s/%s already exists: pick another name with --name", SourcesDir, n)
		}
		rels := []string{""}
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
			if err := writeFile(sourcePath(env, file), content, isExec(out)); err != nil {
				return nil, err
			}
			if err := st.remember(want{source: n, file: file, path: out, root: abs, dir: rel != ""}, content); err != nil {
				return nil, err
			}
		}
		c.Sources[n] = Source{To: []Output{{Path: config.ContractHome(abs), Only: only}}}
		delete(dead, n)
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
// there is replaced by the source on the next pass.
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
	if n, _, ok := owner(c, abs); ok {
		return fmt.Errorf("%s already syncs, through %s", config.ContractHome(abs), n)
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
	for _, g := range groups {
		if err := core.ValidGroup(g); err != nil {
			return err
		}
	}
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
			if err := recoverSource(env, c, name); err != nil {
				return nil, err
			}
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

func buriedAs(dead map[string][]string, a string) (string, bool) {
	if _, ok := dead[a]; ok {
		return a, true
	}
	abs, err := config.Abs(a)
	if err != nil {
		return "", false
	}
	for name, outs := range dead {
		for _, o := range outs {
			if root, err := (Output{Path: o}).root(); err == nil && root == abs {
				return name, true
			}
		}
	}
	return "", false
}

// recoverSource restores a buried source and its files.json entry from
// the commit before the one that deleted it.
func recoverSource(env *core.Env, c *fileConfig, name string) error {
	commit, err := restore(env, path.Join(SourcesDir, name))
	if err != nil {
		return err
	}
	old, err := env.Git("show", commit+"^:"+ConfigFile)
	if err != nil {
		return err
	}
	var prev fileConfig
	if err := json.Unmarshal([]byte(old), &prev); err != nil {
		return err
	}
	s, ok := prev.Sources[name]
	if !ok {
		return fmt.Errorf("%s was not in %s before it was deleted", name, ConfigFile)
	}
	c.Sources[name] = s
	return nil
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

// Resolve settles a conflicted output: keep "here" takes this device's
// version into the source, keep "synced" overwrites it with the source.
func Resolve(env *core.Env, p, keep string) error {
	if keep != "here" && keep != "synced" {
		return errors.New("--keep takes here or synced")
	}
	abs, err := config.Abs(p)
	if err != nil {
		return err
	}
	st, err := loadState()
	if err != nil {
		return err
	}
	rec := st.Outputs[abs]
	if rec == nil || !rec.Conflict {
		return fmt.Errorf("%s has no conflict to resolve", p)
	}
	from, to := abs, sourcePath(env, rec.File)
	if keep == "synced" {
		from, to = to, from
	}
	content, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err := writeFile(to, content, isExec(from)); err != nil {
		return err
	}
	if err := st.remember(rec.want(abs), content); err != nil {
		return err
	}
	return st.save()
}

// Entry is one source, for listing.
type Entry struct {
	Name    string
	Dir     bool
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
		fi, err := os.Stat(sourcePath(env, n))
		entries = append(entries, Entry{Name: n, Dir: err == nil && fi.IsDir(), Outputs: c.Sources[n].To})
	}
	return entries, nil
}

// Conflicts lists this device's outputs waiting on `lichen files resolve`.
func Conflicts() []string {
	st, err := loadState()
	if err != nil {
		return nil
	}
	var out []string
	for p, r := range st.Outputs {
		if r.Conflict {
			out = append(out, config.ContractHome(p))
		}
	}
	slices.Sort(out)
	return out
}
