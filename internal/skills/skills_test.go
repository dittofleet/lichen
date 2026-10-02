package skills

import (
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"lichen/internal/config"
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

func TestParseFrontmatterName(t *testing.T) {
	cases := []struct{ md, want string }{
		{"---\nname: my-skill\ndescription: d\n---\nbody", "my-skill"},
		{"---\nname: \"quoted\"\n---\n", "quoted"},
		{"---\ndescription: d\n---\nname: not-frontmatter", ""},
		{"no frontmatter here", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseFrontmatter([]byte(c.md)).name; got != c.want {
			t.Errorf("parseFrontmatter(%q).name = %q, want %q", c.md, got, c.want)
		}
	}
}

func TestParseFrontmatterOnlyFor(t *testing.T) {
	cases := []struct {
		md   string
		want []string
	}{
		{"---\nname: s\nmetadata:\n  version: \"1\"\n  lichen-harnesses: codex, Claude\n---\n", []string{"claude", "codex"}},
		{"---\nmetadata:\n  lichen-harnesses: \"claude\"\n---\n", []string{"claude"}},
		{"---\nmetadata:\n  lichen-harnesses: claude # not codex\n---\n", []string{"claude"}},
		// Only the metadata map is read, never a top-level key.
		{"---\nlichen-harnesses: claude\n---\n", nil},
		{"---\nname: s\n---\nmetadata:\n  lichen-harnesses: claude", nil},
		{"---\nname: s\n---\n", nil},
	}
	for _, c := range cases {
		if got := parseFrontmatter([]byte(c.md)).onlyFor; !slices.Equal(got, c.want) {
			t.Errorf("parseFrontmatter(%q).onlyFor = %q, want %q", c.md, got, c.want)
		}
	}
	// A nested name (under metadata) is not the skill's name.
	if got := parseFrontmatter([]byte("---\nmetadata:\n  name: inner\n---\n")).name; got != "" {
		t.Errorf("nested name leaked: %q", got)
	}
}

func TestHarnessName(t *testing.T) {
	cases := map[string]string{
		"/Users/u/.claude/skills":          "claude",
		"/Users/u/.codex/skills":           "codex",
		"/Users/u/.config/opencode/skills": "opencode",
		"/Users/u/.agents/skills":          "agents",
	}
	for dir, want := range cases {
		if got := harnessName(dir); got != want {
			t.Errorf("harnessName(%q) = %q, want %q", dir, got, want)
		}
	}
}

func TestLinkDirs(t *testing.T) {
	claude, codex := "/h/.claude/skills", "/h/.codex/skills"
	dirs := []string{claude, codex}
	if got := linkDirs(dirs, nil); !slices.Equal(got, dirs) {
		t.Errorf("unrestricted: %v", got)
	}
	if got := linkDirs(dirs, []string{"claude"}); !slices.Equal(got, []string{claude}) {
		t.Errorf("claude only: %v", got)
	}
	if got := linkDirs(dirs, []string{"cursor"}); len(got) != 0 {
		t.Errorf("unconfigured harness: %v", got)
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
		if found[name].dir != dir {
			t.Errorf("discover[%q] = %q, want %q", name, found[name].dir, dir)
		}
	}
}

func TestDiscoverRootSkill(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\ndescription: d\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	found := discover(root, "single-skill-repo")
	if found["single-skill-repo"].dir != "." {
		t.Fatalf("discover = %v, want the repo name mapping to the root", found)
	}
}

// TestRunOnlyFor drives full passes against a scratch home: a limited
// skill lives in the private store and is linked only into its harness,
// and changing lichen-harnesses moves it between stores.
func TestRunOnlyFor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if h, _ := config.Home(); h != home {
		t.Skip("home dir was resolved before this test, so it would be the real one")
	}
	upstream := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	skill := func(name, frontmatter string) {
		t.Helper()
		p := filepath.Join(upstream, "skills", name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("---\nname: "+name+"\n"+frontmatter+"---\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	skill("both", "")
	skill("only", "metadata:\n  lichen-harnesses: claude\n")
	git(upstream, "init", "-q", "-b", "main")
	git(upstream, "add", "-A")
	git(upstream, "commit", "-qm", "init")
	// A pre-seeded clone whose origin is local: polls work offline.
	clones, _ := clonesRoot()
	git(home, "clone", "-q", upstream, filepath.Join(clones, cloneDirName("github.com/me/skills")))
	writeConfig := func(cfg string) {
		t.Helper()
		p, _ := config.SkillsPath()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(`{"sources":[{"repo":"me/skills"}],"harnesses":["~/.claude/skills","~/.codex/skills"]}`)
	pass := func() {
		t.Helper()
		if err := Update(log.New(io.Discard, "", 0)); err != nil {
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

	pass()
	expect(filepath.Join(agents, "both"), true)
	expect(filepath.Join(agents, "only"), false)
	expect(filepath.Join(private, "only"), true)
	if !linksTo(filepath.Join(claude, "only"), filepath.Join(private, "only")) {
		t.Error("claude link to the private copy missing")
	}
	expect(filepath.Join(codex, "only"), false)
	expect(filepath.Join(codex, "both"), true)

	// Dropping the limit moves it to the shared store and links it everywhere.
	skill("only", "")
	git(upstream, "commit", "-qam", "unlimit")
	pass()
	expect(filepath.Join(private, "only"), false)
	expect(filepath.Join(agents, "only"), true)
	for _, d := range []string{claude, codex} {
		if !linksTo(filepath.Join(d, "only"), filepath.Join(agents, "only")) {
			t.Errorf("%s link to the shared copy missing", d)
		}
	}

	// And limiting it again moves it back.
	skill("only", "metadata:\n  lichen-harnesses: codex\n")
	git(upstream, "commit", "-qam", "limit")
	pass()
	expect(filepath.Join(agents, "only"), false)
	expect(filepath.Join(claude, "only"), false)
	if !linksTo(filepath.Join(codex, "only"), filepath.Join(private, "only")) {
		t.Error("codex link to the private copy missing")
	}

	// Removal cleans up the private copy and its link.
	writeConfig(`{"sources":[{"repo":"me/skills","except":["only"]}],"harnesses":["~/.claude/skills","~/.codex/skills"]}`)
	pass()
	expect(filepath.Join(private, "only"), false)
	expect(filepath.Join(codex, "only"), false)
	expect(filepath.Join(agents, "both"), true)
}
