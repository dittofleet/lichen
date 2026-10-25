// Package core is what every module shares: the sync folder (~/lichen, a
// git checkout of the user's sync repo, holding each module's config and
// the synced sources), lichen.json (device groups and settings), and
// which device this is. Modules never talk to each other: each reads its
// own config from the sync folder after the core pulls it.
package core

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"

	"lichen/internal/config"
	"lichen/internal/events"
)

// SettingsFile is the core's own config file in the sync folder.
const SettingsFile = "lichen.json"

type Settings struct {
	// Groups names sets of devices, for scoping what goes where. Every
	// device is also in a group named after itself and one named after
	// its OS ("mac" or "linux").
	Groups map[string][]string `json:"groups,omitempty"`
	// Backups keeps files lichen overwrites or deletes in
	// ~/lichen-backups instead of removing them (see internal/backup).
	Backups bool `json:"backups,omitempty"`
}

// Env is one run's view of this machine: the sync folder, this device,
// and the settings, read fresh for every pass.
type Env struct {
	Dir      string
	Device   Device
	Settings Settings
	Log      *log.Logger
}

// ErrNotSetUp means this machine has no sync folder yet.
var ErrNotSetUp = errors.New("lichen is not set up on this machine (run: lichen setup <sync repo git url>)")

// Load reads the sync folder's settings and works out which device this
// is.
func Load(lg *log.Logger) (*Env, error) {
	dir, err := config.SyncDir()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return nil, ErrNotSetUp
	}
	var s Settings
	if _, err := config.ReadJSON(filepath.Join(dir, SettingsFile), &s); err != nil {
		return nil, err
	}
	return &Env{Dir: dir, Device: device(s), Settings: s, Log: lg}, nil
}

// Reload re-reads the settings, as after an edit to them.
func (e *Env) Reload() error {
	fresh, err := Load(e.Log)
	if err != nil {
		return err
	}
	*e = *fresh
	return nil
}

// Path resolves a path inside the sync folder.
func (e *Env) Path(parts ...string) string {
	return filepath.Join(append([]string{e.Dir}, parts...)...)
}

// Device is this machine, as lichen scopes things to it.
type Device struct {
	Name   string
	OS     string
	Groups []string // its own name, its OS, and every custom group it is in
}

// In reports whether the device belongs to any of groups. No groups at
// all means every device.
func (d Device) In(groups []string) bool {
	if len(groups) == 0 {
		return true
	}
	return slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(d.Groups, g) })
}

func osGroup() string {
	if runtime.GOOS == "darwin" {
		return "mac"
	}
	return runtime.GOOS
}

func device(s Settings) Device {
	name := deviceName()
	d := Device{Name: name, OS: osGroup(), Groups: []string{name, osGroup()}}
	var custom []string
	for g, members := range s.Groups {
		if slices.Contains(members, name) {
			custom = append(custom, g)
		}
	}
	sort.Strings(custom)
	d.Groups = append(d.Groups, custom...)
	return d
}

// deviceName is the name crosstalk knows this machine by, so a device is
// called the same thing everywhere. It is remembered for when crosstalk
// is not running, and before crosstalk ever answered it is the hostname,
// the way crosstalk derives a name from it.
func deviceName() string {
	cache := ""
	if d, err := config.DataDir(); err == nil {
		cache = filepath.Join(d, "device")
	}
	if name, err := events.Self(); err == nil && name != "" {
		if cache != "" {
			if old, _ := os.ReadFile(cache); strings.TrimSpace(string(old)) != name {
				os.MkdirAll(filepath.Dir(cache), 0o755)
				os.WriteFile(cache, []byte(name+"\n"), 0o644)
			}
		}
		return name
	}
	if cache != "" {
		if b, err := os.ReadFile(cache); err == nil && strings.TrimSpace(string(b)) != "" {
			return strings.TrimSpace(string(b))
		}
	}
	host, _ := os.Hostname()
	return nameFrom(host)
}

var notNameChar = regexp.MustCompile(`[^a-z0-9]+`)

func nameFrom(host string) string {
	host, _, _ = strings.Cut(strings.ToLower(host), ".")
	name := strings.Trim(notNameChar.ReplaceAllString(host, "-"), "-")
	if len(name) > 32 {
		name = strings.Trim(name[:32], "-")
	}
	if name == "" {
		return "device"
	}
	return name
}

// Describe names a scope for people: "every device", or its groups.
func Describe(groups []string) string {
	if len(groups) == 0 {
		return "every device"
	}
	return strings.Join(groups, ", ")
}

// ValidGroup reports whether a name can be a device group (or device).
func ValidGroup(name string) error {
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`).MatchString(name) {
		return fmt.Errorf("%q is not a device or group name (lowercase letters, digits and dashes)", name)
	}
	return nil
}
