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

// WriteJSON writes v as pretty JSON via a temp file and rename, so a
// crash never leaves a half-written file behind.
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
