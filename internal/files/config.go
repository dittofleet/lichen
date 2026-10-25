// Package files is lichen's files module. A source is a file or directory
// in the sync folder's files/ directory, and files.json says where each
// one goes: its outputs, each a path plus the devices it is for. Every
// device writes the outputs meant for it, and an edit to an output is
// captured back into its source, so editing either side syncs. lichen
// remembers what it last wrote to every output (internal state, outside
// the sync folder), which is what tells a local edit from an incoming
// one, and is the base when both happen at once.
package files

import (
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"lichen/internal/config"
	"lichen/internal/core"
)

const (
	// ConfigFile is the module's config in the sync folder.
	ConfigFile = "files.json"
	// SourcesDir holds the sources, inside the sync folder.
	SourcesDir = "files"
	// deletedFile records sources deleted (as opposed to no longer
	// synced), so every device deletes its copies too.
	deletedFile = ".lichen/deleted.json"
)

// Output is one place a source is written to.
type Output struct {
	Path string `json:"path"` // ~/-relative
	// Only limits the output to these devices and groups. Empty is every
	// device.
	Only []string `json:"only,omitempty"`
}

// Source is one entry of files.json, keyed by its path under files/.
type Source struct {
	To []Output `json:"to"`
}

type fileConfig struct {
	Sources map[string]Source `json:"sources,omitempty"`
}

func loadConfig(env *core.Env) (*fileConfig, error) {
	c := &fileConfig{}
	if _, err := config.ReadJSON(env.Path(ConfigFile), c); err != nil {
		return nil, err
	}
	if c.Sources == nil {
		c.Sources = map[string]Source{}
	}
	return c, nil
}

func (c *fileConfig) save(env *core.Env) error {
	if len(c.Sources) == 0 {
		return config.SetJSONField(env.Path(ConfigFile), "sources", nil)
	}
	return config.SetJSONField(env.Path(ConfigFile), "sources", c.Sources)
}

// tombstone records a deleted source: which outputs it had, for recover.
type tombstone struct {
	Outputs []string `json:"outputs"`
	Device  string   `json:"device"`
	At      string   `json:"at"`
}

func loadDeleted(env *core.Env) (map[string]tombstone, error) {
	m := map[string]tombstone{}
	if _, err := config.ReadJSON(env.Path(deletedFile), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func saveDeleted(env *core.Env, m map[string]tombstone) error {
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
	var outs []string
	for _, o := range c.Sources[name].To {
		outs = append(outs, o.Path)
	}
	dead[name] = tombstone{Outputs: outs, Device: env.Device.Name, At: time.Now().UTC().Format(time.RFC3339)}
	delete(c.Sources, name)
	if err := os.RemoveAll(sourcePath(env, name)); err != nil {
		return err
	}
	removeEmptyParents(filepath.Dir(sourcePath(env, name)), env.Path(SourcesDir))
	if err := c.save(env); err != nil {
		return err
	}
	return saveDeleted(env, dead)
}

func sourcePath(env *core.Env, name string) string {
	return env.Path(SourcesDir, filepath.FromSlash(name))
}

// sourceName turns a home-relative output path into a source name:
// leading dots dropped from every part, so ~/.claude/CLAUDE.md is
// claude/CLAUDE.md and the sync folder shows no hidden files.
func sourceName(contracted string) string {
	parts := strings.Split(strings.TrimPrefix(contracted, "~/"), "/")
	for i, p := range parts {
		if t := strings.TrimLeft(p, "."); t != "" {
			parts[i] = t
		}
	}
	return path.Join(parts...)
}

func validName(name string) error {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == "." || strings.HasPrefix(name, "../") || name == ".." {
		return fmt.Errorf("%q is not a usable source name (a relative path like zshrc or claude/commands)", name)
	}
	return nil
}

// want is one output file this device should have.
type want struct {
	source string // files.json entry
	file   string // the source file, relative to files/
	path   string // absolute output file
	root   string // absolute output: the file itself, or the output directory
	dir    bool
}

// wants lists every output file meant for this device, keyed by its
// path. Two sources claiming one path are a config mistake: the first
// (by name) wins and the clash is logged.
func wants(env *core.Env, c *fileConfig) map[string]want {
	out := map[string]want{}
	for _, name := range slices.Sorted(maps.Keys(c.Sources)) {
		src := sourcePath(env, name)
		fi, err := os.Stat(src)
		if err != nil {
			continue // a missing source is handled by Reconcile
		}
		for _, o := range c.Sources[name].To {
			if !env.Device.In(o.Only) {
				continue
			}
			root, err := config.ExpandHome(o.Path)
			if err != nil || !filepath.IsAbs(root) {
				env.Log.Printf("files: %s: ignoring output %q (not a ~/ path)", name, o.Path)
				continue
			}
			root = filepath.Clean(root)
			add := func(w want) {
				if prev, ok := out[w.path]; ok {
					env.Log.Printf("files: %s and %s both go to %s, keeping %s", prev.source, w.source, config.ContractHome(w.path), prev.source)
					return
				}
				out[w.path] = w
			}
			if !fi.IsDir() {
				add(want{source: name, file: name, path: root, root: root})
				continue
			}
			for _, rel := range listFiles(src) {
				add(want{source: name, file: path.Join(name, rel), path: filepath.Join(root, filepath.FromSlash(rel)), root: root, dir: true})
			}
		}
	}
	return out
}

// ignored names files that never sync, even inside a synced directory:
// Finder's and editors' droppings, and lichen's own half-written files.
func ignored(name string) bool {
	switch {
	case name == ".DS_Store", name == ".git", strings.HasSuffix(name, ".lichen-tmp"),
		strings.HasSuffix(name, ".swp"), strings.HasSuffix(name, ".swo"), strings.HasSuffix(name, ".swx"),
		strings.HasSuffix(name, "~"), strings.HasPrefix(name, ".#"),
		strings.HasPrefix(name, "#") && strings.HasSuffix(name, "#"):
		return true
	}
	return false
}

// listFiles returns the regular files under dir, relative and slashed,
// skipping ignored names and anything behind a symlink.
func listFiles(dir string) []string {
	var rels []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p != dir && ignored(d.Name()) {
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

// SourcePath is where a source lives, for showing people.
func SourcePath(name string) string {
	dir, _ := config.SyncDir()
	return filepath.Join(dir, SourcesDir, filepath.FromSlash(name))
}
