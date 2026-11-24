package skills

import (
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lichen/internal/core"
)

func TestParseSpec(t *testing.T) {
	cases := []struct {
		spec, key string
		bad       bool
	}{
		{spec: "vercel-labs/agent-skills", key: "github.com/vercel-labs/agent-skills"},
		{spec: "github.com/owner/repo", key: "github.com/owner/repo"},
		{spec: "https://github.com/owner/repo", key: "github.com/owner/repo"},
		{spec: "https://github.com/owner/repo.git", key: "github.com/owner/repo"},
		{spec: "https://github.com/owner/repo/tree/main/skills/x", key: "github.com/owner/repo"},
		{spec: "git@github.com:owner/repo.git", key: "github.com/owner/repo"},
		{spec: "gitlab.com/org/repo", key: "gitlab.com/org/repo"},
		{spec: "GitHub.com/Owner/Repo", key: "github.com/Owner/Repo"},
		{spec: "just-a-name", bad: true},
		{spec: "a/b/c", bad: true},
		{spec: "", bad: true},
	}
	for _, c := range cases {
		key, url, err := parseSpec(c.spec)
		if c.bad {
			if err == nil {
				t.Errorf("parseSpec(%q): expected error, got %q", c.spec, key)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSpec(%q): %v", c.spec, err)
			continue
		}
		if key != c.key {
			t.Errorf("parseSpec(%q) key = %q, want %q", c.spec, key, c.key)
		}
		if want := "https://" + c.key + ".git"; url != want {
			t.Errorf("parseSpec(%q) url = %q, want %q", c.spec, url, want)
		}
	}
}

func TestFrontmatterName(t *testing.T) {
	cases := []struct{ md, want string }{
		{"---\nname: my-skill\ndescription: d\n---\nbody", "my-skill"},
		{"---\nname: \"quoted\"\n---\n", "quoted"},
		{"---\nname: s # the name\n---\n", "s"},
		{"---\ndescription: d\n---\nname: not-frontmatter", ""},
		// A nested name (under metadata) is not the skill's name.
		{"---\nmetadata:\n  name: inner\n---\n", ""},
		{"no frontmatter here", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := frontmatterName([]byte(c.md)); got != c.want {
			t.Errorf("frontmatterName(%q) = %q, want %q", c.md, got, c.want)
		}
	}
}

func TestLinkDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	claude := filepath.Join(home, ".claude", "skills")
	codex := filepath.Join(home, ".codex", "skills")
	gemini := filepath.Join(home, ".gemini", "config", "skills")
	cases := []struct {
		harnesses, want []string
	}{
		{nil, nil},
		{[]string{"claude"}, []string{claude}},
		{[]string{"claude", "codex", "antigravity"}, []string{claude, codex, gemini}},
		// Codex and Antigravity read the universal copy themselves.
		{[]string{"universal"}, nil},
		{[]string{"claude", "codex", "universal"}, []string{claude}},
	}
	for _, c := range cases {
		if got, err := linkDirs(c.harnesses); err != nil || !slices.Equal(got, c.want) {
			t.Errorf("linkDirs(%v) = %v, %v, want %v", c.harnesses, got, err, c.want)
		}
	}
}

func TestOrderHarnesses(t *testing.T) {
	got, err := orderHarnesses([]string{"universal", "codex", "claude", "codex"})
	if err != nil || !slices.Equal(got, []string{"claude", "codex", "universal"}) {
		t.Errorf("orderHarnesses = %v, %v", got, err)
	}
	if _, err := orderHarnesses([]string{"claude", "cursor"}); err == nil {
		t.Error("an unknown harness passed")
	}
}

func TestSanitizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"My Skill", "my-skill"},
		{"frontend-design", "frontend-design"},
		{"  weird/../path  ", "weird..path"},
		{"---", ""},
	}
	for _, c := range cases {
		if got := sanitizeName(c.in); got != c.want {
			t.Errorf("sanitizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("skills/alpha/SKILL.md", "---\nname: alpha\n---\n")
	// Frontmatter name wins over the directory name.
	write("skills/beta-dir/SKILL.md", "---\nname: beta\n---\n")
	// No frontmatter: the directory name is the skill name.
	write(".claude/skills/gamma/SKILL.md", "instructions only")
	// A nested SKILL.md inside a skill dir is shadowed by its parent.
	write("skills/alpha/nested/SKILL.md", "---\nname: shadowed\n---\n")
	// Too deep to be discovered.
	write("a/b/c/d/SKILL.md", "---\nname: too-deep\n---\n")

	found := discover(root, "repo")
	want := map[string]string{
		"alpha": filepath.Join("skills", "alpha"),
		"beta":  filepath.Join("skills", "beta-dir"),
		"gamma": filepath.Join(".claude", "skills", "gamma"),
	}
	if len(found) != len(want) {
		t.Fatalf("discover found %v, want %v", found, want)
	}
	for name, dir := range want {
		if found[name] != dir {
			t.Errorf("discover[%q] = %q, want %q", name, found[name], dir)
		}
	}
}

func TestDiscoverRootSkill(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\ndescription: d\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	found := discover(root, "single-skill-repo")
	if found["single-skill-repo"] != "." {
		t.Fatalf("discover = %v, want the repo name mapping to the root", found)
	}
}

// TestRun drives full passes against a scratch home: a repo's skills
// live in the private store and are linked into the harnesses it names,
// naming universal moves them to ~/.agents/skills, and a repo scoped
// away from a device, or naming no harness, leaves it.
func TestRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LICHEN_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	upstream := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	for _, name := range []string{"both", "only"} {
		p := filepath.Join(upstream, "skills", name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("---\nname: "+name+"\n---\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(upstream, "init", "-q", "-b", "main")
	git(upstream, "add", "-A")
	git(upstream, "commit", "-qm", "init")
	// A pre-seeded clone whose origin is local: polls work offline.
	clones, _ := clonesRoot()
	git(home, "clone", "-q", upstream, filepath.Join(clones, cloneDirName("github.com/me/skills")))
	writeConfig := func(cfg string) {
		t.Helper()
		p, _ := ConfigPath()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig("[\"me/skills\"]\nharnesses = [\"claude\", \"codex\"]\ninclude = [\"all\"]\n")
	quiet := log.New(io.Discard, "", 0)
	pass := func() {
		t.Helper()
		env := &core.Env{Log: quiet, Device: core.Device{Name: "lychee", Groups: []string{"lychee", "mac"}}}
		if err := Update(env); err != nil {
			t.Fatal(err)
		}
	}
	agents, _ := agentsSkillsDir()
	private, _ := PrivateDir()
	claude, codex := filepath.Join(home, ".claude", "skills"), filepath.Join(home, ".codex", "skills")
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }
	expect := func(p string, want bool) {
		t.Helper()
		if exists(p) != want {
			t.Errorf("%s exists = %v, want %v", p, !want, want)
		}
	}

	harness := func(names ...string) {
		t.Helper()
		if _, err := SetHarnesses("me/skills", names); err != nil {
			t.Fatal(err)
		}
		pass()
	}
	linked := func(dir, store string, want bool) {
		t.Helper()
		for _, name := range []string{"both", "only"} {
			if linksTo(filepath.Join(dir, name), filepath.Join(store, name)) != want {
				t.Errorf("%s linked from %s = %v, want %v", name, dir, !want, want)
			}
		}
	}

	pass()
	expect(filepath.Join(private, "both"), true)
	expect(filepath.Join(agents, "both"), false)
	linked(claude, private, true)
	linked(codex, private, true)
	expect(filepath.Join(home, ".gemini"), false)

	// Dropping a harness removes its links and keeps the rest.
	harness("claude")
	linked(claude, private, true)
	expect(filepath.Join(codex, "both"), false)

	// Universal moves the skills to ~/.agents/skills, which Codex reads
	// itself and Claude Code doesn't.
	harness("claude", "codex", "universal")
	expect(filepath.Join(private, "both"), false)
	linked(claude, agents, true)
	expect(filepath.Join(codex, "both"), false)

	// And without it they move back.
	harness("codex")
	expect(filepath.Join(agents, "both"), false)
	expect(filepath.Join(claude, "both"), false)
	linked(codex, private, true)

	// A harness this build doesn't know fails the pass, removing nothing.
	writeConfig("[\"me/skills\"]\nharnesses = [\"cursor\"]\ninclude = [\"all\"]\n")
	if err := Update(&core.Env{Log: quiet, Device: core.Device{Name: "lychee"}}); err == nil {
		t.Error("an unknown harness passed")
	}
	linked(codex, private, true)

	// A repo naming no harness goes nowhere.
	writeConfig("[\"me/skills\"]\ninclude = [\"all\"]\n")
	pass()
	expect(filepath.Join(private, "both"), false)
	expect(filepath.Join(codex, "both"), false)
	expect(filepath.Join(clones, cloneDirName("github.com/me/skills")), false)
	git(home, "clone", "-q", upstream, filepath.Join(clones, cloneDirName("github.com/me/skills")))

	// Narrowing to one skill removes the other's copy and link.
	writeConfig("[\"me/skills\"]\nskills = [\"both\"]\nharnesses = [\"claude\"]\ninclude = [\"mac\"]\n")
	pass()
	expect(filepath.Join(private, "only"), false)
	expect(filepath.Join(claude, "only"), false)
	expect(filepath.Join(private, "both"), true)

	// A repo for other devices is uninstalled here, clone and all, and
	// comes back when this device is in again.
	scope := func(edit func(core.Scope) core.Scope) {
		t.Helper()
		if err := SetScope("me/skills", edit); err != nil {
			t.Fatal(err)
		}
		pass()
	}
	scope(func(s core.Scope) core.Scope { return core.Settings{}.Without(s, "mac") })
	expect(filepath.Join(private, "both"), false)
	expect(filepath.Join(claude, "both"), false)
	expect(filepath.Join(clones, cloneDirName("github.com/me/skills")), false)
	git(home, "clone", "-q", upstream, filepath.Join(clones, cloneDirName("github.com/me/skills")))
	scope(func(s core.Scope) core.Scope { return s.With("linux") })
	expect(filepath.Join(private, "both"), false)
	scope(func(s core.Scope) core.Scope { return s.With("lychee") })
	expect(filepath.Join(private, "both"), true)
	if !linksTo(filepath.Join(claude, "both"), filepath.Join(private, "both")) {
		t.Error("claude link missing after scoping back in")
	}
	// Adding more of the repo keeps its scope and harnesses, and gains
	// the harnesses named.
	here := &core.Env{Log: quiet, Device: core.Device{Name: "lychee", Groups: []string{"mac"}}}
	if _, err := AddSource(here, "me/skills", []string{"only"}, []string{"antigravity"}, core.Scope{}); err != nil {
		t.Fatal(err)
	}
	got, _ := Configured()
	if !slices.Equal(got[0].Include, []string{"linux", "lychee"}) {
		t.Errorf("scope after re-adding = %v", got[0].Include)
	}
	if !slices.Equal(got[0].Harnesses, []string{"claude", "antigravity"}) {
		t.Errorf("harnesses after re-adding = %v", got[0].Harnesses)
	}
	// A skill the repo doesn't have is refused, and nothing is saved.
	cfg, _ := ConfigPath()
	before, _ := os.ReadFile(cfg)
	if _, err := AddSource(here, "me/skills", []string{"typo"}, nil, core.Scope{}); err == nil || !strings.Contains(err.Error(), "it has both, only") {
		t.Errorf("adding a missing skill: %v", err)
	}
	// So is a new repo that names no harness, or one lichen doesn't know.
	if _, err := AddSource(here, "me/other", nil, nil, core.Scope{}); err == nil || !strings.Contains(err.Error(), "--harness") {
		t.Errorf("adding a repo without harnesses: %v", err)
	}
	if _, err := AddSource(here, "me/skills", nil, []string{"cursor"}, core.Scope{}); err == nil || !strings.Contains(err.Error(), "unknown harness") {
		t.Errorf("adding an unknown harness: %v", err)
	}
	if after, _ := os.ReadFile(cfg); string(after) != string(before) {
		t.Errorf("refused adds changed the config:\n%s", after)
	}

	// An entry this build can't parse holds removals back, but not one
	// for other devices.
	writeConfig("[\"me/skills\"]\nharnesses = [\"claude\"]\ninclude = [\"linux\"]\n[\"not a spec\"]\nharnesses = [\"claude\"]\ninclude = [\"linux\"]\n")
	pass()
	expect(filepath.Join(private, "both"), false)
}
