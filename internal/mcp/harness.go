package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lichen/internal/config"
)

// harness is one agent tool lichen installs MCP servers into, driven
// through its own CLI so lichen never edits a file the harness also
// rewrites.
type harness interface {
	name() string
	available() bool
	// add installs a server at user scope. It returns errTaken, without
	// changing anything, when the harness already has a server by that
	// name.
	add(name string, s Server) error
	// remove uninstalls a server. A server that is already gone is not
	// an error.
	remove(name string) error
	// installed lists the names of the servers the harness has at user
	// scope, whoever added them.
	installed() (map[string]bool, error)
}

var errTaken = errors.New("name already taken")

// harnesses lists the supported harnesses. A var so tests can swap in
// fakes.
var harnesses = []harness{claude{}, codex{}}

// cliTimeout bounds every harness command: the caller holds the
// cross-process lock, so a wedged CLI must not wedge the daemon.
const cliTimeout = 2 * time.Minute

func run(bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return text, fmt.Errorf("%s %s: timed out after %s", bin, strings.Join(args[:min(2, len(args))], " "), cliTimeout)
	}
	if err != nil {
		return text, fmt.Errorf("%s %s: %w (%s)", bin, strings.Join(args[:min(2, len(args))], " "), err, text)
	}
	return text, nil
}

// claude drives Claude Code. User-scope servers live in ~/.claude.json,
// which every running session rewrites, so only `claude mcp` may touch
// it. `claude mcp add-json` refuses a name that is already taken.
type claude struct{}

func (claude) name() string { return "claude" }

func (claude) available() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

func (claude) add(name string, s Server) error {
	entry := map[string]any{"type": "http", "url": s.URL}
	if s.URL == "" {
		entry = map[string]any{"type": "stdio", "command": s.Command, "args": append([]string{}, s.Args...)}
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	out, err := run("claude", "mcp", "add-json", "--scope", "user", name, string(data))
	if err != nil && strings.Contains(out, "already exists") {
		return errTaken
	}
	return err
}

// installed reads ~/.claude.json directly (read-only): `claude mcp list`
// would health-check every server, launching each local one.
func (claude) installed() (map[string]bool, error) {
	p := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".claude.json")
	if os.Getenv("CLAUDE_CONFIG_DIR") == "" {
		var err error
		if p, err = config.HomeJoin(".claude.json"); err != nil {
			return nil, err
		}
	}
	var cfg struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if _, err := config.ReadJSON(p, &cfg); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for name := range cfg.MCPServers {
		names[name] = true
	}
	return names, nil
}

func (claude) remove(name string) error {
	out, err := run("claude", "mcp", "remove", "--scope", "user", name)
	if err != nil && strings.Contains(out, "No MCP server named") {
		return nil
	}
	return err
}

// codex drives the Codex CLI, whose servers live in ~/.codex/config.toml.
// `codex mcp add` silently replaces a server of the same name, so add
// checks first.
type codex struct{}

func (codex) name() string { return "codex" }

func (codex) available() bool {
	_, err := exec.LookPath("codex")
	return err == nil
}

func (c codex) add(name string, s Server) error {
	taken, err := c.has(name)
	if err != nil {
		return err
	}
	if taken {
		return errTaken
	}
	if s.URL == "" {
		_, err := run("codex", append([]string{"mcp", "add", name, "--", s.Command}, s.Args...)...)
		return err
	}
	return c.addURL(name, s.URL)
}

// addURL works around `codex mcp add --url` going straight into a
// browser OAuth login whenever the server advertises OAuth, even when
// it works without one. Nobody would be there to finish that login, so
// the command is stopped as soon as it reports the server saved (the
// config is written before the login starts), and the result is
// confirmed with `codex mcp get`. The short deadline caps the wait in
// case that message ever changes.
func (c codex) addURL(name, url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "mcp", "add", name, "--url", url)
	// A writer rather than a pipe: exec copies the output itself, and
	// WaitDelay then bounds that copy even if a child of codex keeps the
	// pipe open after codex is killed.
	out := &stopWriter{marker: "Added global MCP server", stop: cancel}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 5 * time.Second
	runErr := cmd.Run()
	added, err := c.has(name)
	if err != nil {
		return err
	}
	if !added {
		return fmt.Errorf("codex mcp add: %v (%s)", runErr, out.String())
	}
	return nil
}

// stopWriter collects a command's output and calls stop once marker
// shows up in it.
type stopWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	marker string
	stop   func()
}

func (w *stopWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if strings.Contains(w.buf.String(), w.marker) {
		w.stop()
	}
	return len(p), nil
}

func (w *stopWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(w.buf.String())
}

// has reports whether Codex has a server by this name. Only Codex's own
// "not found" answer counts as absent: any other failure is an error,
// since `codex mcp add` would silently replace a server it missed.
func (codex) has(name string) (bool, error) {
	out, err := run("codex", "mcp", "get", name, "--json")
	if err == nil {
		return true, nil
	}
	if strings.Contains(out, "No MCP server named") {
		return false, nil
	}
	return false, err
}

func (codex) installed() (map[string]bool, error) {
	out, err := run("codex", "mcp", "list", "--json")
	if err != nil {
		return nil, err
	}
	var list []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("parsing codex mcp list: %w", err)
	}
	names := map[string]bool{}
	for _, s := range list {
		names[s.Name] = true
	}
	return names, nil
}

func (codex) remove(name string) error {
	_, err := run("codex", "mcp", "remove", name)
	return err
}
