// Package config resolves where lichen keeps things on this machine: the
// sync folder (~/lichen, the git checkout every module's config and every
// synced source lives in), machine-local state, and the daemon log. It
// also holds the JSON helpers the modules share for their config files.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dittofleet/go-cli-kit/xdg"
)

func Home() (string, error) { return os.UserHomeDir() }

// HomeJoin resolves a path under the home directory.
func HomeJoin(parts ...string) (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home}, parts...)...), nil
}

// SyncDir is the sync folder: ~/lichen, or LICHEN_DIR.
func SyncDir() (string, error) {
	if d := os.Getenv("LICHEN_DIR"); d != "" {
		return filepath.Abs(d)
	}
	return HomeJoin("lichen")
}

// SyncPath resolves a path inside the sync folder.
func SyncPath(parts ...string) (string, error) {
	dir, err := SyncDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{dir}, parts...)...), nil
}

// DataDir holds lichen's machine-local state: the cross-process lock,
// each module's record of what it installed here, and the skill repo
// clones.
func DataDir() (string, error) {
	if _, err := Home(); err != nil {
		return "", err
	}
	return xdg.DataDir("lichen"), nil
}

func LogPath() (string, error) {
	if runtime.GOOS == "darwin" {
		return HomeJoin("Library", "Logs", "lichen.log")
	}
	return HomeJoin(".local", "state", "lichen", "lichen.log")
}

func ExpandHome(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := Home()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
	}
	return p, nil
}

// Abs canonicalizes a user-supplied path: ~ expanded, made absolute.
func Abs(p string) (string, error) {
	abs, err := ExpandHome(p)
	if err != nil {
		return "", err
	}
	return filepath.Abs(abs)
}

// Within reports whether p is root or lies inside it. The separator
// keeps ~/.zshrc.bak from counting as inside ~/.zshrc.
func Within(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// Ignored names files that never sync, wherever they are: Finder's and
// editors' droppings, and lichen's own half-written files. GitIgnore
// says the same to git, for the sync folder.
func Ignored(name string) bool {
	switch {
	case name == ".DS_Store", name == ".git", strings.HasSuffix(name, tmpSuffix),
		strings.HasSuffix(name, ".swp"), strings.HasSuffix(name, ".swo"), strings.HasSuffix(name, ".swx"),
		strings.HasSuffix(name, "~"), strings.HasPrefix(name, ".#"),
		strings.HasPrefix(name, "#") && strings.HasSuffix(name, "#"):
		return true
	}
	return false
}

const GitIgnore = ".DS_Store\n*" + tmpSuffix + "\n*.swp\n*.swo\n*.swx\n*~\n.#*\n#*#\n"

// tmpSuffix marks a file WriteFile has not finished writing.
const tmpSuffix = ".lichen-tmp"

// ContractHome is ExpandHome's inverse: paths under the home directory
// become ~/-relative (portable across machines), others pass through.
func ContractHome(abs string) string {
	home, err := Home()
	if err != nil {
		return abs
	}
	rel, err := filepath.Rel(home, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return abs
	}
	if rel == "." {
		return "~"
	}
	return "~/" + filepath.ToSlash(rel)
}

// ReadJSON unmarshals the JSON object in the file at path into v. A
// missing file is not an error: exists reports it, and v is left
// untouched.
func ReadJSON(path string, v any) (exists bool, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// A bare null would decode as an empty file, and for a module config
	// that reads as "uninstall everything".
	if string(bytes.TrimSpace(data)) == "null" {
		return true, fmt.Errorf("parsing %s: not a JSON object", path)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return true, fmt.Errorf("parsing %s: %w", path, err)
	}
	return true, nil
}

// SetJSONField rewrites one top-level field of the JSON object at path,
// keeping every other field, including ones this build doesn't know
// about: lichen's config files are shared by machines that may run
// different lichen versions. A nil v deletes the field.
func SetJSONField(path, key string, v any) error {
	raw := map[string]json.RawMessage{}
	if _, err := ReadJSON(path, &raw); err != nil {
		return err
	}
	if v == nil {
		delete(raw, key)
	} else {
		enc, err := json.Marshal(v)
		if err != nil {
			return err
		}
		raw[key] = enc
	}
	return WriteJSON(path, raw)
}

// WriteJSON writes v as pretty JSON.
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(path, append(data, '\n'), 0o644)
}

// WriteFile writes data via a temp file and rename, so a crash never
// leaves a half-written file behind, creating parent directories.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + tmpSuffix
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
