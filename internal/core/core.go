// Package core is what every module shares: the sync folder (~/lichen, a
// git checkout of the user's sync repo, holding each module's config and
// the synced sources), lichen.toml (device groups and settings), and
// which device this is. Modules never talk to each other: each reads its
// own config from the sync folder after the core pulls it.
package core

import (
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"lichen/internal/config"
	"lichen/internal/events"
)

// SettingsFile is the core's own config file in the sync folder.
const SettingsFile = "lichen.toml"

type Settings struct {
	// Groups names sets of devices, for scoping what goes where. Every
	// device is also in the group named after its OS ("mac" or "linux").
	Groups map[string][]string `toml:"groups,omitempty"`
	// Backups keeps files lichen overwrites or deletes in
	// ~/lichen-backups instead of removing them (see internal/backup).
	Backups bool `toml:"backups,omitempty"`
}

// Env is one run's view of this machine: the sync folder, this device,
// and the settings, re-read whenever they may have changed.
type Env struct {
	Dir      string
	Device   Device
	Settings Settings
	Log      *log.Logger
	// Notify tells the user something needs them, when that can't wait
	// for them to look (the daemon sets it).
	Notify func(msg string)
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
	name, err := DeviceName()
	if err != nil {
		return nil, err
	}
	e := &Env{Dir: dir, Device: Device{Name: name}, Log: lg}
	return e, e.Reload()
}

// Reload re-reads the settings, as after a pull or an edit to them.
func (e *Env) Reload() error {
	var s Settings
	if _, err := config.ReadTOML(e.Path(SettingsFile), &s); err != nil {
		return err
	}
	e.Settings = s
	e.Device = s.Device(e.Device.Name, OS())
	return nil
}

// GroupsOf lists the custom groups a device is in, sorted.
func (s Settings) GroupsOf(device string) []string {
	var in []string
	for _, g := range slices.Sorted(maps.Keys(s.Groups)) {
		if slices.Contains(s.Groups[g], device) {
			in = append(in, g)
		}
	}
	return in
}

// EditGroup adds devices to a custom group, or removes them from it: all
// of them, and the group, when none are named.
func (e *Env) EditGroup(add bool, group string, devices []string) error {
	for _, n := range append([]string{group}, devices...) {
		if err := ValidGroup(n); err != nil {
			return err
		}
	}
	if group == "mac" || group == "linux" {
		return fmt.Errorf("%s is a group of its own already", group)
	}
	groups := e.Settings.Groups
	if groups == nil {
		groups = map[string][]string{}
	}
	if _, ok := groups[group]; !ok && !add {
		return fmt.Errorf("%s is not a group (see: lichen groups)", group)
	}
	switch {
	case add:
		for _, d := range devices {
			if !slices.Contains(groups[group], d) {
				groups[group] = append(groups[group], d)
			}
		}
	case len(devices) == 0:
		delete(groups, group)
	default:
		groups[group] = slices.DeleteFunc(groups[group], func(d string) bool { return slices.Contains(devices, d) })
		if len(groups[group]) == 0 {
			delete(groups, group)
		}
	}
	if len(groups) == 0 {
		groups = nil
	}
	e.Settings.Groups = groups
	return config.WriteTOML(e.Path(SettingsFile), e.Settings)
}

// Path resolves a path inside the sync folder.
func (e *Env) Path(parts ...string) string {
	return filepath.Join(append([]string{e.Dir}, parts...)...)
}

// Device is this machine, as lichen scopes things to it.
type Device struct {
	Name   string
	Groups []string // its OS, and every custom group it is in
}

// Scope picks the devices something goes to, by device and group names:
// the ones Include names, or all but the ones Exclude names. Never both,
// and neither is every device.
type Scope struct {
	Include []string `toml:"include,omitempty" json:"include,omitempty"`
	Exclude []string `toml:"exclude,omitempty" json:"exclude,omitempty"`
}

// Check refuses a scope that both includes and excludes.
func (s Scope) Check() error {
	if len(s.Include) > 0 && len(s.Exclude) > 0 {
		return errors.New("use include or exclude, not both")
	}
	return nil
}

// Everywhere reports whether the scope is every device.
func (s Scope) Everywhere() bool { return len(s.Include) == 0 && len(s.Exclude) == 0 }

// In reports whether the device is in scope.
func (d Device) In(s Scope) bool {
	if len(s.Include) > 0 {
		return d.named(s.Include)
	}
	return !d.named(s.Exclude)
}

// Names lists what the device answers to in a scope: its name, then
// its groups.
func (d Device) Names() []string { return append([]string{d.Name}, d.Groups...) }

// named reports whether any of names is the device or one of its groups.
func (d Device) named(names []string) bool {
	return slices.ContainsFunc(names, func(n string) bool { return n == d.Name || slices.Contains(d.Groups, n) })
}

// Device is the device named name, running os ("mac" or "linux"), in
// the groups s puts it in.
func (s Settings) Device(name, os string) Device {
	return Device{Name: name, Groups: append([]string{os}, s.GroupsOf(name)...)}
}

// OS is this device's OS group: "mac" or "linux".
func OS() string {
	if runtime.GOOS == "darwin" {
		return "mac"
	}
	return runtime.GOOS
}

// DeviceName is the name crosstalk knows this machine by, so a device is
// called the same thing everywhere. It is remembered for when crosstalk
// can't say, as while offline.
func DeviceName() (string, error) {
	d, err := config.DataDir()
	if err != nil {
		return "", err
	}
	cache := filepath.Join(d, "device")
	if name, err := events.Self(); err == nil && name != "" {
		if old, _ := os.ReadFile(cache); strings.TrimSpace(string(old)) != name {
			os.MkdirAll(d, 0o755)
			os.WriteFile(cache, []byte(name+"\n"), 0o644)
		}
		return name, nil
	} else if b, _ := os.ReadFile(cache); strings.TrimSpace(string(b)) != "" {
		return strings.TrimSpace(string(b)), nil
	} else if err == nil {
		err = errors.New("crosstalk named no device")
	}
	return "", fmt.Errorf("lichen needs crosstalk, installed and joined to your hub (https://github.com/dittofleet/crosstalk): %w", err)
}

// Describe names a scope for people: "every device", the devices and
// groups it includes, or "every device but" the ones it excludes.
func Describe(s Scope) string {
	switch {
	case len(s.Include) > 0:
		return strings.Join(s.Include, ", ")
	case len(s.Exclude) > 0:
		return "every device but " + strings.Join(s.Exclude, ", ")
	}
	return "every device"
}

var groupName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidGroup reports whether a name can be a device group (or device).
func ValidGroup(name string) error {
	if !groupName.MatchString(name) {
		return fmt.Errorf("%q is not a device or group name (lowercase letters, digits and dashes)", name)
	}
	return nil
}
