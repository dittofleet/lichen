// Package files is lichen's files module. A source is a file or directory
// in the sync folder's files/ directory, and files.toml says where each
// one goes: its outputs, each a path plus the devices it is for. A source
// file can insert others (see compile.go). Every device writes the
// outputs meant for it, and an edit to an output is captured back into
// the sources its lines came from, so editing either side syncs. lichen
// remembers what it last wrote to every output (internal state, outside
// the sync folder), which is what tells a local edit from an incoming
// one, and is the base when both happen at once.
package files

import (
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"lichen/internal/config"
	"lichen/internal/core"
)

const (
	// ConfigFile is the module's config in the sync folder.
	ConfigFile = "files.toml"
	// SourcesDir holds the sources, inside the sync folder.
	SourcesDir = "files"
	// deletedFile records sources deleted (as opposed to no longer
	// synced), so every device deletes its copies too.
	deletedFile = ".lichen/deleted.json"
)

// Output is one place a source is written to.
type Output struct {
	Path string `json:"path"` // ~/-relative
	core.Scope
}

// Source is one source and its outputs, keyed by its path under files/.
type Source struct {
	To []Output `json:"to"`
	// Private sources hold a file no one but its owner may read
	// (~/.netrc, say), and new outputs are written that way.
	Private bool `json:"private,omitempty"`
}

type fileConfig struct {
	Sources map[string]Source
}

// entry is one output in files.toml, keyed by its path.
type entry struct {
	Source string `toml:"source"`
	core.Scope
	Private bool `toml:"private,omitempty"`
}

func loadConfig(env *core.Env) (*fileConfig, error) {
	entries := map[string]entry{}
	if _, err := config.ReadTOML(env.Path(ConfigFile), &entries); err != nil {
		return nil, err
	}
	c := &fileConfig{Sources: map[string]Source{}}
	for _, p := range slices.Sorted(maps.Keys(entries)) {
		e := entries[p]
		if err := e.Check(); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", ConfigFile, p, err)
		}
		n := e.Source
		if err := validName(n); err != nil {
			return nil, fmt.Errorf("%s: %s: source: %w", ConfigFile, p, err)
		}
		s := c.Sources[n]
		s.To = append(s.To, Output{Path: p, Scope: e.Scope})
		s.Private = s.Private || e.Private
		c.Sources[n] = s
	}
	return c, nil
}

func (c *fileConfig) save(env *core.Env) error {
	return config.WriteTOML(env.Path(ConfigFile), c.entries())
}

// entries lays the sources out as files.toml does: one entry per output.
func (c *fileConfig) entries() map[string]entry {
	entries := map[string]entry{}
	for n, s := range c.Sources {
		for _, o := range s.To {
			entries[o.Path] = entry{Source: n, Scope: o.Scope, Private: s.Private}
		}
	}
	return entries
}

// loadDeleted reads the deleted sources, each with the entry it had, for
// bringing it back.
func loadDeleted(env *core.Env) (map[string]Source, error) {
	m := map[string]Source{}
	if _, err := config.ReadJSON(env.Path(deletedFile), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func saveDeleted(env *core.Env, m map[string]Source) error {
	if len(m) == 0 {
		err := os.Remove(env.Path(deletedFile))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return config.WriteJSON(env.Path(deletedFile), m)
}

// bury removes a source and its entry, recording it as deleted so every
// other device deletes its copies.
func bury(env *core.Env, c *fileConfig, name string) error {
	dead, err := loadDeleted(env)
	if err != nil {
		return err
	}
	dead[name] = c.Sources[name]
	if err := dropSource(env, c, name); err != nil {
		return err
	}
	if err := c.save(env); err != nil {
		return err
	}
	return saveDeleted(env, dead)
}

// dropSource removes a source and its entry, leaving every output as it
// is.
func dropSource(env *core.Env, c *fileConfig, name string) error {
	delete(c.Sources, name)
	if err := os.RemoveAll(sourcePath(env, name)); err != nil {
		return err
	}
	removeEmptyParents(filepath.Dir(sourcePath(env, name)), env.Path(SourcesDir))
	return nil
}

func sourcePath(env *core.Env, name string) string {
	return env.Path(SourcesDir, filepath.FromSlash(name))
}

func validName(name string) error {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == "." || strings.HasPrefix(name, "../") || name == ".." {
		return fmt.Errorf("%q is not a usable source name (a relative path like zshrc or claude/commands)", name)
	}
	return nil
}

// root is where an output goes, absolute.
func (o Output) root() (string, error) {
	abs, err := config.ExpandHome(o.Path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(abs) {
		return "", errors.New("not a ~/ path")
	}
	return filepath.Clean(abs), nil
}

// checkOutput refuses what can't be an output: anything outside the home
// directory, and lichen's own folders.
func checkOutput(env *core.Env, abs string) error {
	home, err := config.Home()
	if err != nil {
		return err
	}
	if abs == home {
		return errors.New("your home directory can't sync as a whole, only what's in it")
	}
	if !config.Within(abs, home) {
		return fmt.Errorf("%s is not inside your home directory: only files under ~ can sync", abs)
	}
	data, _ := config.DataDir()
	for _, own := range []string{env.Dir, data} {
		if config.Within(abs, own) || config.Within(own, abs) {
			return fmt.Errorf("%s is lichen's own folder, it can't sync", config.ContractHome(abs))
		}
	}
	return nil
}

// want is one output file this device should have.
type want struct {
	source string // files.toml source
	file   string // the source file, relative to files/
	path   string // absolute output file
	root   string // absolute output: the file itself, or the output directory
	dir    bool
}

// wants lists every output file meant for this device, keyed by its
// path, and every directory output meant for it (including ones with no
// files yet), mapped to its source. Two sources claiming one path are a
// config mistake: the first (by name) wins, and the clash goes to lg,
// unless it is nil.
func wants(env *core.Env, c *fileConfig, lg *log.Logger) (ws map[string]want, dirs map[string]string) {
	ws, dirs = map[string]want{}, map[string]string{}
	logf := func(format string, args ...any) {
		if lg != nil {
			lg.Printf(format, args...)
		}
	}
	add := func(w want) {
		if prev, ok := ws[w.path]; ok {
			logf("files: %s and %s both go to %s, keeping %s", prev.source, w.source, config.ContractHome(w.path), prev.source)
			return
		}
		ws[w.path] = w
	}
	for _, name := range slices.Sorted(maps.Keys(c.Sources)) {
		src := sourcePath(env, name)
		fi, err := os.Stat(src)
		if err != nil {
			continue // a missing source is handled by Reconcile
		}
		for _, o := range c.Sources[name].To {
			if !env.Device.In(o.Scope) {
				continue
			}
			root, err := o.root()
			if err == nil {
				err = checkOutput(env, root)
			}
			if err != nil {
				logf("files: %s: ignoring output %s: %v", name, o.Path, err)
				continue
			}
			if !fi.IsDir() {
				add(want{source: name, file: name, path: root, root: root})
				continue
			}
			if _, ok := dirs[root]; !ok {
				dirs[root] = name
			}
			for _, rel := range listFiles(src) {
				add(want{source: name, file: path.Join(name, rel), path: filepath.Join(root, filepath.FromSlash(rel)), root: root, dir: true})
			}
		}
	}
	return ws, dirs
}

// listFiles returns the regular files under dir, relative and slashed,
// skipping ignored names and anything behind a symlink.
func listFiles(dir string) []string {
	var rels []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p != dir && config.Ignored(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(dir, p)
			rels = append(rels, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(rels)
	return rels
}
