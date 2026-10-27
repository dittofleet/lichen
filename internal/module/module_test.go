package module

import (
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"lichen/internal/core"
)

type recorder struct {
	name, config string
	ran          *[]string
}

func (r recorder) Name() string   { return r.name }
func (r recorder) Config() string { return r.config }
func (r recorder) Reconcile(*core.Env) error {
	*r.ran = append(*r.ran, r.name)
	return nil
}

// A local pass runs a module whose config was edited by hand, and every
// module when lichen.toml was, since its groups scope them all.
func TestLocalRunsWhatAnEditConcerns(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var ran []string
	saved := All
	t.Cleanup(func() { All = saved })
	All = []Module{recorder{"skills", "skills.toml", &ran}, recorder{"mcp", "mcp.toml", &ran}}
	env := &core.Env{Dir: dir, Device: core.Device{Name: "lychee"}, Log: log.New(io.Discard, "", 0)}

	for _, c := range []struct {
		edit string
		want []string
	}{
		{"", nil},
		{"mcp.toml", []string{"mcp"}},
		{core.SettingsFile, []string{"skills", "mcp"}},
	} {
		ran = nil
		if c.edit != "" {
			write(c.edit, "edited = \""+c.edit+"\"\n")
		}
		if err := Local(env); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(ran, c.want) {
			t.Errorf("editing %q ran %v, want %v", c.edit, ran, c.want)
		}
	}
}
