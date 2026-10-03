package mcp

import (
	"io"
	"log"
	"maps"
	"os"
	"slices"
	"testing"

	"lichen/internal/config"
)

// fake is an in-memory harness that records the calls it gets.
type fake struct {
	id      string
	missing bool
	servers map[string]Server
	calls   []string
}

func (f *fake) name() string    { return f.id }
func (f *fake) available() bool { return !f.missing }
func (f *fake) remove(n string) error {
	f.calls = append(f.calls, "remove "+n)
	delete(f.servers, n)
	return nil
}

func (f *fake) add(n string, s Server) error {
	f.calls = append(f.calls, "add "+n)
	if _, ok := f.servers[n]; ok {
		return errTaken
	}
	f.servers[n] = s
	return nil
}

func (f *fake) takeCalls() []string {
	c := f.calls
	f.calls = nil
	return c
}

func TestValidate(t *testing.T) {
	for _, n := range []string{"playwright", "my_server-2"} {
		if err := validate(n, Server{Command: "x"}); err != nil {
			t.Errorf("validateName(%q): %v", n, err)
		}
	}
	for _, n := range []string{"", "a.b", "a b", "owner/repo", "x:y"} {
		if validate(n, Server{Command: "x"}) == nil {
			t.Errorf("validateName(%q): expected error", n)
		}
	}
	good := []Server{{Command: "bunx", Args: []string{"x"}}, {Command: "x"}, {URL: "https://example.com/mcp"}}
	for _, s := range good {
		if err := validate("x", s); err != nil {
			t.Errorf("validate(%v): %v", s, err)
		}
	}
	bad := []Server{{}, {URL: "ftp://x"}, {URL: "https://x", Command: "y"}, {Args: []string{"x"}}, {Command: "--url", Args: []string{"https://x"}}}
	for _, s := range bad {
		if validate("x", s) == nil {
			t.Errorf("validate(%+v): expected error", s)
		}
	}
}

func TestReconcile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if h, _ := config.Home(); h != home {
		t.Skip("home dir was resolved before this test, so it would be the real one")
	}
	claude := &fake{id: "claude", servers: map[string]Server{"mine": {Command: "own"}}}
	codex := &fake{id: "codex", servers: map[string]Server{}}
	harnesses = []harness{claude, codex}
	lg := log.New(io.Discard, "", 0)
	reconcile := func() {
		t.Helper()
		if err := Reconcile(lg); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(f *fake, want ...string) {
		t.Helper()
		if got := f.takeCalls(); !slices.Equal(got, want) {
			t.Errorf("%s calls = %q, want %q", f.id, got, want)
		}
	}

	pw := Server{Command: "bunx", Args: []string{"@playwright/mcp@latest"}}
	if err := AddServer("pw", pw); err != nil {
		t.Fatal(err)
	}
	if err := AddServer("mine", Server{URL: "https://example.com/mcp"}); err != nil {
		t.Fatal(err)
	}
	reconcile()
	expect(claude, "add mine", "add pw")
	expect(codex, "add mine", "add pw")
	// claude's own "mine" was taken: left alone, and not lichen's.
	if claude.servers["mine"].Command != "own" {
		t.Errorf("claude's own server was replaced: %+v", claude.servers["mine"])
	}
	entries, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || !slices.Equal(entries[0].Harnesses, []string{"codex"}) || !slices.Equal(entries[1].Harnesses, []string{"claude", "codex"}) {
		t.Errorf("List = %+v", entries)
	}

	// Steady state runs no harness commands at all.
	reconcile()
	expect(claude, "add mine") // the taken name is retried, and refused again
	expect(codex)

	// A changed server is replaced, everywhere lichen installed it.
	pw.Args = []string{"@playwright/mcp@1.0.0"}
	if err := AddServer("pw", pw); err != nil {
		t.Fatal(err)
	}
	reconcile()
	expect(claude, "add mine", "remove pw", "add pw")
	expect(codex, "remove pw", "add pw")
	if !codex.servers["pw"].equal(pw) {
		t.Errorf("codex pw = %+v, want %+v", codex.servers["pw"], pw)
	}

	// A harness that is missing on this machine is skipped, and what
	// lichen installed there earlier is left as is.
	codex.missing = true
	if err := RemoveServers([]string{"mine"}); err != nil {
		t.Fatal(err)
	}
	reconcile()
	expect(claude) // never lichen's "mine" there
	expect(codex)
	if _, ok := claude.servers["mine"]; !ok {
		t.Error("claude's own server was removed")
	}
	codex.missing = false
	reconcile()
	expect(codex, "remove mine")

	// A config entry this build rejects keeps its installs.
	p, _ := config.MCPPath()
	if err := os.WriteFile(p, []byte(`{"servers":{"pw":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reconcile()
	expect(claude)
	expect(codex)

	// Neither is an mcp.json holding a bare null.
	if err := os.WriteFile(p, []byte("null\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Reconcile(lg); err == nil {
		t.Error("null mcp.json: expected an error")
	}
	expect(claude)
	expect(codex)

	// No mcp.json at all is not "remove everything".
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	reconcile()
	expect(claude)
	expect(codex)

	// Removing the last server uninstalls it everywhere.
	if err := AddServer("pw", pw); err != nil {
		t.Fatal(err)
	}
	if err := RemoveServers([]string{"pw"}); err != nil {
		t.Fatal(err)
	}
	reconcile()
	expect(claude, "remove pw")
	expect(codex, "remove pw")
	if got := slices.Sorted(maps.Keys(claude.servers)); !slices.Equal(got, []string{"mine"}) {
		t.Errorf("claude servers = %v, want only its own", got)
	}
	if len(codex.servers) != 0 {
		t.Errorf("codex servers = %v, want none", codex.servers)
	}
	if err := RemoveServers([]string{"pw"}); err == nil {
		t.Error("removing an unknown server: expected error")
	}
}
