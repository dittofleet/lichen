package files_test

import (
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"lichen/internal/core"
	"lichen/internal/files"
	"lichen/internal/module"
)

// device is one simulated machine: its own home, sync folder and state,
// all sharing one bare remote. Environment variables are process-wide,
// so every step switches to the device it runs on.
type device struct {
	t    *testing.T
	name string
	home string
}

func newRemote(t *testing.T) string {
	remote := filepath.Join(t.TempDir(), "sync.git")
	if out, err := exec.Command("git", "init", "--quiet", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return remote
}

func newDevice(t *testing.T, remote, name string) *device {
	t.Helper()
	d := freshDevice(t, name)
	d.setup(remote)
	return d
}

// freshDevice is a machine lichen is not set up on yet.
func freshDevice(t *testing.T, name string) *device {
	d := &device{t: t, name: name, home: t.TempDir()}
	d.use()
	// No crosstalk in tests: the device knows its name from its cache.
	data := filepath.Join(d.home, ".local", "share", "lichen")
	os.MkdirAll(data, 0o755)
	os.WriteFile(filepath.Join(data, "device"), []byte(name+"\n"), 0o644)
	return d
}

func (d *device) setup(remote string) {
	d.t.Helper()
	d.use()
	if _, err := core.Setup(remote); err != nil {
		d.t.Fatalf("%s: setup: %v", d.name, err)
	}
	d.sync()
}

func (d *device) use() {
	d.t.Setenv("HOME", d.home)
	d.t.Setenv("LICHEN_DIR", "")
	d.t.Setenv("XDG_DATA_HOME", "")
	d.t.Setenv("XDG_CONFIG_HOME", "")
	d.t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
}

func (d *device) env() *core.Env {
	d.t.Helper()
	d.use()
	env, err := core.Load(log.New(io.Discard, "", 0))
	if err != nil {
		d.t.Fatalf("%s: %v", d.name, err)
	}
	return env
}

// sync runs a full pass, as the daemon does on a nudge.
func (d *device) sync() {
	d.t.Helper()
	if err := module.Pass(d.env(), true); err != nil {
		d.t.Fatalf("%s: pass: %v", d.name, err)
	}
}

// change runs a CLI-style edit: pull, edit, commit, pass.
func (d *device) change(edit func(env *core.Env) error) {
	d.t.Helper()
	env := d.env()
	if err := module.Change(env, "test", func() error { return edit(env) }); err != nil {
		d.t.Fatalf("%s: %v", d.name, err)
	}
}

func (d *device) path(rel string) string { return filepath.Join(d.home, rel) }

func (d *device) write(rel, content string) {
	d.t.Helper()
	p := d.path(rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		d.t.Fatal(err)
	}
}

func (d *device) read(rel string) string {
	d.t.Helper()
	b, err := os.ReadFile(d.path(rel))
	if err != nil {
		d.t.Fatalf("%s: %v", d.name, err)
	}
	return string(b)
}

func (d *device) has(rel string) bool {
	_, err := os.Lstat(d.path(rel))
	return err == nil
}

func (d *device) add(rel string, only ...string) {
	d.t.Helper()
	d.change(func(env *core.Env) error {
		_, err := files.Add(env, []string{"~/" + rel}, "", only)
		return err
	})
}

func expect(t *testing.T, d *device, rel, want string) {
	t.Helper()
	if got := d.read(rel); got != want {
		t.Fatalf("%s: ~/%s = %q, want %q", d.name, rel, got, want)
	}
}

func TestEditsSyncBothWays(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "one\n")
	a.add(".zshrc")
	if _, err := os.Stat(filepath.Join(a.home, "lichen", "files", "zshrc")); err != nil {
		t.Fatalf("source not created as files/zshrc: %v", err)
	}

	b := newDevice(t, remote, "banana")
	expect(t, b, ".zshrc", "one\n")

	b.write(".zshrc", "two\n")
	b.sync()
	a.sync()
	expect(t, a, ".zshrc", "two\n")

	// An edit to the source in the sync folder syncs too.
	os.WriteFile(filepath.Join(a.home, "lichen", "files", "zshrc"), []byte("three\n"), 0o644)
	a.sync()
	expect(t, a, ".zshrc", "three\n")
	b.sync()
	expect(t, b, ".zshrc", "three\n")
}

func TestJoiningDeviceGetsSyncedVersion(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".gitconfig", "synced\n")
	a.add(".gitconfig")

	b := freshDevice(t, "banana")
	b.write(".gitconfig", "local\n")
	b.setup(remote)
	expect(t, b, ".gitconfig", "synced\n")
}

func TestDirectories(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".claude/commands/one.md", "1")
	a.write(".claude/commands/sub/two.md", "2")
	a.add(".claude/commands")

	b := newDevice(t, remote, "banana")
	expect(t, b, ".claude/commands/sub/two.md", "2")

	// A new file in a synced directory joins it everywhere.
	b.write(".claude/commands/three.md", "3")
	b.write(".claude/commands/.DS_Store", "junk")
	b.sync()
	a.sync()
	expect(t, a, ".claude/commands/three.md", "3")
	if a.has(".claude/commands/.DS_Store") {
		t.Fatal(".DS_Store synced")
	}

	// A file deleted from it goes everywhere, empty directories with it.
	os.Remove(a.path(".claude/commands/sub/two.md"))
	a.sync()
	b.sync()
	if b.has(".claude/commands/sub/two.md") || b.has(".claude/commands/sub") {
		t.Fatal("deleted file (or its empty directory) still on banana")
	}
	expect(t, b, ".claude/commands/one.md", "1")
}

func TestDeleteEverywhereAndRecover(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "keep me\n")
	a.add(".zshrc")
	b := newDevice(t, remote, "banana")

	os.Remove(a.path(".zshrc"))
	a.sync()
	b.sync()
	if b.has(".zshrc") {
		t.Fatal("deleted file still on banana")
	}

	b.change(func(env *core.Env) error {
		_, err := files.Recover(env, []string{"~/.zshrc"})
		return err
	})
	expect(t, b, ".zshrc", "keep me\n")
	a.sync()
	expect(t, a, ".zshrc", "keep me\n")
}

func TestRemoveLeavesFilesInPlace(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "mine\n")
	a.add(".zshrc")
	b := newDevice(t, remote, "banana")

	a.change(func(env *core.Env) error {
		_, err := files.Remove(env, []string{"zshrc"})
		return err
	})
	b.sync()
	expect(t, a, ".zshrc", "mine\n")
	expect(t, b, ".zshrc", "mine\n")

	b.write(".zshrc", "only here\n")
	b.sync()
	a.sync()
	expect(t, a, ".zshrc", "mine\n")
}

func TestScopes(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "apple only\n")
	a.add(".zshrc", "apple")
	b := newDevice(t, remote, "banana")
	if b.has(".zshrc") {
		t.Fatal("banana got a file scoped to apple")
	}

	// A custom group brings banana in.
	a.change(func(env *core.Env) error {
		return files.Only(env, "~/.zshrc", []string{"apple", "fruit"})
	})
	b.change(func(env *core.Env) error {
		return os.WriteFile(env.Path(core.SettingsFile), []byte(`{"groups": {"fruit": ["banana"]}}`), 0o644)
	})
	expect(t, b, ".zshrc", "apple only\n")

	// Scoping banana out again leaves its copy, no longer synced.
	a.sync()
	a.change(func(env *core.Env) error { return files.Only(env, "zshrc", []string{"apple"}) })
	b.sync()
	expect(t, b, ".zshrc", "apple only\n")
}

func TestSeveralOutputs(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".claude/CLAUDE.md", "be brief\n")
	a.add(".claude/CLAUDE.md")
	a.change(func(env *core.Env) error {
		return files.AddOutput(env, "claude/CLAUDE.md", "~/.codex/AGENTS.md", nil)
	})
	expect(t, a, ".codex/AGENTS.md", "be brief\n")

	a.write(".codex/AGENTS.md", "be briefer\n")
	a.sync()
	expect(t, a, ".claude/CLAUDE.md", "be briefer\n")
}

func TestConcurrentEditsMerge(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "1\n2\n3\n")
	a.add(".zshrc")
	b := newDevice(t, remote, "banana")

	a.write(".zshrc", "one\n2\n3\n")
	a.sync()
	b.write(".zshrc", "1\n2\nthree\n")
	b.sync()
	a.sync()
	expect(t, b, ".zshrc", "one\n2\nthree\n")
	expect(t, a, ".zshrc", "one\n2\nthree\n")
}

func TestConflictWaitsForResolve(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "base\n")
	a.add(".zshrc")
	b := newDevice(t, remote, "banana")

	a.write(".zshrc", "apple\n")
	a.sync()
	b.write(".zshrc", "banana\n")
	b.sync()
	expect(t, b, ".zshrc", "banana\n")
	b.use()
	if c := files.Conflicts(); len(c) != 1 || !strings.HasSuffix(c[0], ".zshrc") {
		t.Fatalf("conflicts = %v, want ~/.zshrc", c)
	}

	env := b.env()
	if err := files.Resolve(env, b.path(".zshrc"), "here"); err != nil {
		t.Fatal(err)
	}
	b.sync()
	a.sync()
	expect(t, a, ".zshrc", "banana\n")
}

func TestDeviceGroups(t *testing.T) {
	d := core.Device{Name: "lychee", Groups: []string{"lychee", "mac", "servers"}}
	for _, c := range []struct {
		only []string
		want bool
	}{
		{nil, true},
		{[]string{"mac"}, true},
		{[]string{"linux"}, false},
		{[]string{"linux", "servers"}, true},
		{[]string{"lychee"}, true},
	} {
		if got := d.In(c.only); got != c.want {
			t.Errorf("In(%v) = %v, want %v", c.only, got, c.want)
		}
	}
}

func TestExecutableBitSyncs(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write("bin/hello", "#!/bin/sh\necho hi\n")
	os.Chmod(a.path("bin/hello"), 0o755)
	a.add("bin/hello")
	b := newDevice(t, remote, "banana")
	if fi, err := os.Stat(b.path("bin/hello")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("bin/hello on banana is not executable: %v %v", fi.Mode(), err)
	}
}

func TestSymlinksAreLeftAlone(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "synced\n")
	a.add(".zshrc")

	b := freshDevice(t, "banana")
	b.write("elsewhere", "mine\n")
	os.Symlink(b.path("elsewhere"), b.path(".zshrc"))
	b.setup(remote)
	if fi, _ := os.Lstat(b.path(".zshrc")); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced")
	}
	expect(t, b, "elsewhere", "mine\n")
}

func TestDirectoryInTheWayIsMovedAside(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".config/tool", "a file\n")
	a.add(".config/tool")

	b := freshDevice(t, "banana")
	b.write(".config/tool/precious", "not synced anywhere\n")
	b.setup(remote)
	expect(t, b, ".config/tool", "a file\n")
	backups, _ := filepath.Glob(b.path("lichen-backups/*/.config/tool/precious"))
	if len(backups) != 1 {
		t.Fatalf("the directory in the way was not moved to lichen-backups: %v", backups)
	}
}

func TestDeletingOneOutputDeletesTheSource(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".claude/CLAUDE.md", "x\n")
	a.add(".claude/CLAUDE.md")
	a.change(func(env *core.Env) error {
		return files.AddOutput(env, "claude/CLAUDE.md", "~/.codex/AGENTS.md", nil)
	})
	b := newDevice(t, remote, "banana")

	os.Remove(a.path(".codex/AGENTS.md"))
	a.sync()
	if a.has(".claude/CLAUDE.md") {
		t.Fatal("the source's other output survived on apple")
	}
	b.sync()
	if b.has(".claude/CLAUDE.md") || b.has(".codex/AGENTS.md") {
		t.Fatal("outputs survived on banana")
	}
}

func TestEditedCopyOfADeletedFileIsKept(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "v1\n")
	a.add(".zshrc")
	b := newDevice(t, remote, "banana")

	os.Remove(a.path(".zshrc"))
	a.sync()
	b.write(".zshrc", "edited on banana\n")
	b.sync()
	expect(t, b, ".zshrc", "edited on banana\n")
}

func TestRecoverOneFileOfADirectory(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".claude/commands/one.md", "1")
	a.write(".claude/commands/two.md", "2")
	a.add(".claude/commands")
	b := newDevice(t, remote, "banana")

	os.Remove(a.path(".claude/commands/two.md"))
	a.sync()
	b.sync()
	b.change(func(env *core.Env) error {
		_, err := files.Recover(env, []string{b.path(".claude/commands/two.md")})
		return err
	})
	expect(t, b, ".claude/commands/two.md", "2")
	a.sync()
	expect(t, a, ".claude/commands/two.md", "2")
}

func TestOverlappingAddsAreRefused(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".claude/commands/one.md", "1")
	a.add(".claude/commands")
	env := a.env()
	for _, p := range []string{"~/.claude/commands/one.md", "~/.claude", "~/lichen/files.json"} {
		if _, err := files.Add(env, []string{p}, "", nil); err == nil {
			t.Errorf("adding %s: expected a refusal", p)
		}
	}
}

func TestTwoDevicesJoinAnEmptyRepo(t *testing.T) {
	remote := newRemote(t)
	a := freshDevice(t, "apple")
	a.use()
	if _, err := core.Setup(remote); err != nil {
		t.Fatal(err)
	}
	b := newDevice(t, remote, "banana")
	b.write(".zshrc", "from banana\n")
	b.add(".zshrc")
	// apple cloned before anything was pushed, so it has no upstream yet.
	a.sync()
	a.sync()
	expect(t, a, ".zshrc", "from banana\n")
	a.write(".zshrc", "from apple\n")
	a.sync()
	b.sync()
	expect(t, b, ".zshrc", "from apple\n")
}

func TestOfflineEditsDoNotWedge(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "base\n")
	a.add(".zshrc")
	b := newDevice(t, remote, "banana")

	a.write(".zshrc", "apple\n")
	a.sync()
	// banana is offline: its edit is captured and committed, not pushed.
	os.Rename(remote, remote+".away")
	b.write(".zshrc", "banana\n")
	b.sync()
	os.Rename(remote+".away", remote)
	b.sync()
	a.sync()
	if got, want := a.read(".zshrc"), b.read(".zshrc"); got != want {
		t.Fatalf("devices disagree after reconnecting: apple %q, banana %q", got, want)
	}
	b.write(".zshrc", "later\n")
	b.sync()
	a.sync()
	expect(t, a, ".zshrc", "later\n")
}
