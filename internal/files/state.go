package files

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"lichen/internal/config"
)

// record is what lichen last wrote to one output file on this device.
type record struct {
	Source string `json:"source"` // files.json entry
	File   string `json:"file"`   // source file, relative to files/
	Root   string `json:"root"`   // the output: this file, or its synced directory
	Hash   string `json:"hash"`   // of the content lichen wrote
	// Deps is every source file that content was compiled from (the file
	// itself, and what it inserts), by the hash of its content then, and
	// Groups the device's groups then: together they say where each line
	// of it came from.
	Deps   map[string]string `json:"deps"`
	Groups []string          `json:"groups"`
	Exec   bool              `json:"exec,omitempty"`
	// Conflict and Question are set while the output holds an edit made
	// here that lichen can't take into its sources on its own: one that
	// doesn't merge with a change from elsewhere, or new lines that could
	// go to more than one source. Until `lichen resolve`, the output is
	// left as it is.
	Conflict bool `json:"conflict,omitempty"`
	Question bool `json:"question,omitempty"`
}

// state is this device's memory of every output it wrote, keyed by
// absolute path. The content itself is kept by hash (objects/), as the
// base for telling and merging edits.
type state struct {
	Outputs map[string]*record `json:"outputs"`
}

func stateDir() (string, error) {
	d, err := config.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "files"), nil
}

func loadState() (*state, error) {
	d, err := stateDir()
	if err != nil {
		return nil, err
	}
	s := &state{}
	if _, err := config.ReadJSON(filepath.Join(d, "state.json"), s); err != nil {
		return nil, err
	}
	if s.Outputs == nil {
		s.Outputs = map[string]*record{}
	}
	return s, nil
}

// save writes the state and drops stored content nothing refers to.
func (s *state) save() error {
	d, err := stateDir()
	if err != nil {
		return err
	}
	if err := config.WriteJSON(filepath.Join(d, "state.json"), s); err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, r := range s.Outputs {
		keep[r.Hash] = true
		for _, h := range r.Deps {
			keep[h] = true
		}
	}
	entries, _ := os.ReadDir(filepath.Join(d, "objects"))
	for _, e := range entries {
		if !keep[e.Name()] {
			os.Remove(filepath.Join(d, "objects", e.Name()))
		}
	}
	return nil
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// remember records c as what lichen wrote (or found in sync) at an
// output, which is there, keeping its content and that of its sources
// for later comparisons.
func (s *state) remember(w want, c *compiled) error {
	r := &record{Source: w.source, File: w.file, Root: w.root, Hash: hashOf(c.content()), Deps: map[string]string{}, Groups: c.groups, Exec: isExec(w.path)}
	if err := store(c.content()); err != nil {
		return err
	}
	for f, b := range c.deps {
		if err := store(b); err != nil {
			return err
		}
		r.Deps[f] = hashOf(b)
	}
	s.Outputs[w.path] = r
	return nil
}

func store(content []byte) error {
	d, err := stateDir()
	if err != nil {
		return err
	}
	obj := filepath.Join(d, "objects", hashOf(content))
	if _, err := os.Stat(obj); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(obj), 0o700); err != nil {
		return err
	}
	return os.WriteFile(obj, content, 0o600)
}

func object(hash string) ([]byte, error) {
	d, err := stateDir()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(d, "objects", hash))
}

// want is what a record was written for, at out.
func (r *record) want(out string) want {
	return want{source: r.Source, file: r.File, path: out, root: r.Root, dir: r.Root != out}
}

// base compiles again what lichen last wrote at an output, from the
// sources it was compiled from, for where each of its lines came from.
func base(r *record) (*compiled, error) {
	c, err := compile(r.File, func(name string) ([]byte, error) {
		h, ok := r.Deps[name]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return object(h)
	}, r.Groups)
	if err == nil && hashOf(c.content()) != r.Hash {
		err = fmt.Errorf("its sources don't compile to what lichen wrote")
	}
	return c, err
}
