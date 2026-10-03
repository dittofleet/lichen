package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
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
}

var errTaken = errors.New("name already taken")

// harnesses lists the supported harnesses. A var so tests can swap in
// fakes.
var harnesses = func() []harness { return []harness{claude{}, codex{}} }

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
	entry := map[string]any{"type": "stdio", "command": s.Command, "args": s.Args}
	if s.URL != "" {
		entry = map[string]any{"type": "http", "url": s.URL}
	} else if s.Args == nil {
		entry["args"] = []string{}
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
	if c.has(name) {
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
// confirmed with `codex mcp get`.
func (c codex) addURL(name, url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "mcp", "add", name, "--url", url)
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var out []string
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		out = append(out, sc.Text())
		if strings.HasPrefix(sc.Text(), "Added global MCP server") {
			cancel()
			break
		}
	}
	waitErr := cmd.Wait()
	if c.has(name) {
		return nil
	}
	return fmt.Errorf("codex mcp add: %v (%s)", waitErr, strings.TrimSpace(strings.Join(out, "\n")+"\n"+stderr.String()))
}

func (codex) has(name string) bool {
	_, err := run("codex", "mcp", "get", name, "--json")
	return err == nil
}

func (codex) remove(name string) error {
	_, err := run("codex", "mcp", "remove", name)
	return err
}
