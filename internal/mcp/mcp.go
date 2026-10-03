// Package mcp is lichen's mcp module: it installs MCP servers into every
// supported harness (Claude Code and Codex) through each harness's own
// `mcp add` and `mcp remove` commands, so the harness stays the only
// writer of its config file. A server is just a name and either a
// command to launch or a URL to reach: no env vars, headers or auth, so
// nothing secret ever enters the synced config. WHICH servers to install
// lives in this module's own config file (~/.config/lichen/mcp.json),
// which the files module carries to every machine. What this machine
// installed where lives in a local manifest, the ownership boundary:
// lichen only ever replaces or removes servers it added itself, so
// servers added by other means always coexist with lichen's.
package mcp

import (
	"errors"
	"fmt"
	"log"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"lichen/internal/config"
)

// Server is one MCP server: a local one lichen launches through Command
// and Args, or a remote one reachable at URL.
type Server struct {
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	URL     string   `json:"url,omitempty"`
}

func (s Server) equal(o Server) bool {
	return s.Command == o.Command && s.URL == o.URL && slices.Equal(s.Args, o.Args)
}

// String is the server as it would be typed after the name in
// `lichen mcp add`.
func (s Server) String() string {
	if s.URL != "" {
		return s.URL
	}
	return strings.Join(append([]string{s.Command}, s.Args...), " ")
}

// IsURL reports whether arg names a remote server rather than a command.
func IsURL(arg string) bool {
	return strings.HasPrefix(arg, "https://") || strings.HasPrefix(arg, "http://")
}

// Names must work in every harness: Claude Code allows only letters,
// digits, - and _ (Codex allows a superset).
var nameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func validate(name string, s Server) error {
	switch {
	case !nameRe.MatchString(name):
		return fmt.Errorf("invalid server name %q (use letters, digits, - and _)", name)
	case s.URL != "" && (s.Command != "" || len(s.Args) > 0):
		return fmt.Errorf("%s has both a url and a command", name)
	case s.URL != "" && !IsURL(s.URL):
		return fmt.Errorf("%s url %q is not http(s)", name, s.URL)
	case s.URL == "" && s.Command == "":
		return fmt.Errorf("%s has neither a url nor a command", name)
	}
	return nil
}

// mcpConfig is the synced mcp config file.
type mcpConfig struct {
	Servers map[string]Server `json:"servers,omitempty"`
}

// loadConfig reads the synced mcp config. The exists flag lets the
// caller tell "no file yet" (a fresh machine before its first sync, or
// a file briefly moved aside) apart from a file that lists no servers.
func loadConfig() (cfg *mcpConfig, exists bool, err error) {
	p, err := config.MCPPath()
	if err != nil {
		return nil, false, err
	}
	cfg = &mcpConfig{}
	if exists, err = config.ReadJSON(p, cfg); err != nil {
		return nil, exists, err
	}
	if cfg.Servers == nil {
		cfg.Servers = map[string]Server{}
	}
	return cfg, exists, nil
}

// saveServers rewrites the servers map inside mcp.json.
func saveServers(servers map[string]Server) error {
	p, err := config.MCPPath()
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		// The file stays (deleting a managed file makes the files module
		// restore it), just with no servers.
		return config.SetJSONField(p, "servers", nil)
	}
	return config.SetJSONField(p, "servers", servers)
}

// The manifest records what THIS machine installed into which harness:
// server name → harness name → the server as lichen added it there. A
// server not listed for a harness was put there by something else and is
// never replaced or removed.
type manifest struct {
	Servers map[string]map[string]Server `json:"servers,omitempty"`
}

func manifestPath() (string, error) {
	d, err := config.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "mcp-manifest.json"), nil
}

func loadManifest() (*manifest, error) {
	m := &manifest{Servers: map[string]map[string]Server{}}
	p, err := manifestPath()
	if err != nil {
		return nil, err
	}
	if _, err := config.ReadJSON(p, m); err != nil {
		return nil, err
	}
	if m.Servers == nil {
		m.Servers = map[string]map[string]Server{}
	}
	return m, nil
}

func (m *manifest) save() error {
	p, err := manifestPath()
	if err != nil {
		return err
	}
	return config.WriteJSON(p, m)
}

func (m *manifest) record(name, harness string, s Server) {
	if m.Servers[name] == nil {
		m.Servers[name] = map[string]Server{}
	}
	m.Servers[name][harness] = s
}

func (m *manifest) forget(name, harness string) {
	delete(m.Servers[name], harness)
	if len(m.Servers[name]) == 0 {
		delete(m.Servers, name)
	}
}

// Reconcile makes every available harness's lichen-installed servers
// match the mcp config: add what's missing, replace what changed, and
// remove what the config no longer lists. A server that fails in one
// harness is logged and retried on the next pass, never blocking the
// rest.
func Reconcile(lg *log.Logger) error {
	cfg, exists, err := loadConfig()
	if err != nil {
		return err
	}
	man, err := loadManifest()
	if err != nil {
		return err
	}
	if len(cfg.Servers) == 0 && len(man.Servers) == 0 {
		return nil
	}
	// An absent mcp.json is not "zero servers": a fresh machine's first
	// pass runs before the sync repo delivers the file, and an apply can
	// briefly move it aside. Treating absence as emptiness would turn
	// those windows into a mass uninstall.
	if !exists && len(man.Servers) > 0 {
		lg.Printf("mcp: no mcp.json yet, leaving installed servers alone")
		return nil
	}

	desired := map[string]Server{}
	for _, name := range slices.Sorted(maps.Keys(cfg.Servers)) {
		if err := validate(name, cfg.Servers[name]); err != nil {
			lg.Printf("mcp: %v", err)
			continue
		}
		desired[name] = cfg.Servers[name]
	}

	for _, h := range harnesses {
		if !h.available() {
			if len(desired) > 0 {
				lg.Printf("mcp: %s not found on PATH, skipping it", h.name())
			}
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(desired)) {
			want := desired[name]
			prev, owned := man.Servers[name][h.name()]
			if owned && prev.equal(want) {
				continue
			}
			if owned {
				if err := h.remove(name); err != nil {
					lg.Printf("mcp: updating %s in %s: %v", name, h.name(), err)
					continue
				}
				man.forget(name, h.name())
				if err := man.save(); err != nil {
					return err
				}
			}
			if err := h.add(name, want); err != nil {
				if errors.Is(err, errTaken) {
					lg.Printf("mcp: NOT installing %s in %s (it already has a server by that name; remove it there to let lichen manage it)", name, h.name())
				} else {
					lg.Printf("mcp: installing %s in %s: %v", name, h.name(), err)
				}
				continue
			}
			// Persist each install as it lands, so a pass killed midway
			// never leaves a server in place that lichen doesn't own.
			man.record(name, h.name(), want)
			if err := man.save(); err != nil {
				return err
			}
			verb := "installed"
			if owned {
				verb = "updated"
			}
			lg.Printf("mcp: %s %s in %s", verb, name, h.name())
		}
		for _, name := range slices.Sorted(maps.Keys(man.Servers)) {
			if _, owned := man.Servers[name][h.name()]; !owned {
				continue
			}
			// A hand-edited entry this build rejects keeps whatever lichen
			// installed under its name, rather than reading as a removal.
			if _, ok := cfg.Servers[name]; ok {
				continue
			}
			if err := h.remove(name); err != nil {
				lg.Printf("mcp: removing %s from %s: %v", name, h.name(), err)
				continue
			}
			man.forget(name, h.name())
			lg.Printf("mcp: removed %s from %s", name, h.name())
		}
	}
	return man.save()
}

// AddServer records a server in the mcp config, replacing any server of
// the same name. Install happens on the next reconcile, which the CLI
// runs immediately after.
func AddServer(name string, s Server) error {
	if err := validate(name, s); err != nil {
		return err
	}
	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}
	cfg.Servers[name] = s
	return saveServers(cfg.Servers)
}

// RemoveServers drops servers from the mcp config. The next reconcile
// uninstalls them from every harness.
func RemoveServers(names []string) error {
	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}
	for _, name := range names {
		if _, ok := cfg.Servers[name]; !ok {
			return fmt.Errorf("%q is not a synced MCP server (see: lichen mcp list)", name)
		}
		delete(cfg.Servers, name)
	}
	return saveServers(cfg.Servers)
}

// Entry is one configured server and the harnesses this machine
// installed it into.
type Entry struct {
	Name      string
	Server    Server
	Harnesses []string
}

// List returns the configured servers, sorted by name.
func List() ([]Entry, error) {
	cfg, _, err := loadConfig()
	if err != nil {
		return nil, err
	}
	man, err := loadManifest()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, name := range slices.Sorted(maps.Keys(cfg.Servers)) {
		out = append(out, Entry{Name: name, Server: cfg.Servers[name], Harnesses: slices.Sorted(maps.Keys(man.Servers[name]))})
	}
	return out, nil
}
