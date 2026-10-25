package files

import (
	"crypto/sha256"
	"encoding/hex"
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
	// Conflict is set while the output and its source both changed in
	// ways that don't merge: neither is written until it is resolved.
	Conflict bool `json:"conflict,omitempty"`
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

// remember records content as what lichen wrote (or found in sync) at an
// output, keeping the content for later comparisons.
func (s *state) remember(out, source, file, root string, content []byte) error {
	d, err := stateDir()
	if err != nil {
		return err
	}
	h := hashOf(content)
	obj := filepath.Join(d, "objects", h)
	if _, err := os.Stat(obj); err != nil {
		if err := os.MkdirAll(filepath.Dir(obj), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(obj, content, 0o600); err != nil {
			return err
		}
	}
	s.Outputs[out] = &record{Source: source, File: file, Root: root, Hash: h}
	return nil
}

// base returns what lichen last wrote at an output.
func base(r *record) ([]byte, error) {
	d, err := stateDir()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(d, "objects", r.Hash))
}
