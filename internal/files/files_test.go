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
	a.change(func(env *core.Env) error { return env.EditGroup(true, "fruit", []string{"banana"}) })
	b.sync()
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

	b.change(func(env *core.Env) error { return files.Resolve(env, b.path(".zshrc"), "here") })
	a.sync()
	expect(t, a, ".zshrc", "banana\n")
	if c := files.Conflicts(); len(c) != 0 {
		t.Fatalf("conflicts = %v after resolving", c)
	}

	// And the other way round: the synced version wins.
	a.write(".zshrc", "apple again\n")
	a.sync()
	b.write(".zshrc", "banana again\n")
	b.sync()
	b.change(func(env *core.Env) error { return files.Resolve(env, b.path(".zshrc"), "synced") })
	expect(t, b, ".zshrc", "apple again\n")
}

// A group edit lands on top of the other devices' edits, never over
// them: the edit runs on freshly pulled settings.
func TestGroupEditsDoNotRevertEachOther(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	b := newDevice(t, remote, "banana")
	a.change(func(env *core.Env) error { return env.EditGroup(true, "fruit", []string{"apple", "banana"}) })
	b.change(func(env *core.Env) error { return env.EditGroup(true, "servers", []string{"banana"}) })
	a.sync()
	if got := strings.Join(a.env().Device.Groups, " "); got != "apple mac fruit" && got != "apple linux fruit" {
		t.Fatalf("apple's groups = %q", got)
	}
	if got := b.env().Settings.GroupsOf("banana"); strings.Join(got, " ") != "fruit servers" {
		t.Fatalf("banana's groups = %v, want fruit servers", got)
	}
	b.change(func(env *core.Env) error { return env.EditGroup(false, "fruit", []string{"banana"}) })
	if got := b.env().Settings.GroupsOf("banana"); strings.Join(got, " ") != "servers" {
		t.Fatalf("banana's groups = %v, want servers", got)
	}
	for _, bad := range []string{"mac", "Bad Name"} {
		if err := b.env().EditGroup(true, bad, []string{"banana"}); err == nil {
			t.Fatalf("EditGroup(%q) went through", bad)
		}
	}
}

// Swap files, Finder's .DS_Store and the like never sync: not from the
// sync folder, and not from a synced directory.
func TestEditorDroppingsDoNotSync(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".config/app/settings", "x\n")
	a.add(".config/app")
	a.write(".config/app/.settings.swp", "swap")
	a.write(".config/app/.DS_Store", "finder")
	a.write("lichen/files/config/app/.settings.swp", "swap")
	a.write("lichen/.DS_Store", "finder")
	a.sync()
	b := newDevice(t, remote, "banana")
	expect(t, b, ".config/app/settings", "x\n")
	for _, rel := range []string{".config/app/.settings.swp", ".config/app/.DS_Store", "lichen/files/config/app/.settings.swp", "lichen/.DS_Store"} {
		if b.has(rel) {
			t.Errorf("banana got ~/%s", rel)
		}
	}
}

// An output written into files.json by hand is held to what lichen
// files add allows: under the home directory, outside lichen's folders.
func TestOutputsOutsideHomeAreIgnored(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "z\n")
	a.add(".zshrc")
	outside := filepath.Join(filepath.Dir(a.home), "outside-"+filepath.Base(a.home))
	t.Cleanup(func() { os.Remove(outside) })
	a.write("lichen/files.json", `{"sources": {"zshrc": {"to": [
		{"path": "~/.zshrc"},
		{"path": "~/../`+filepath.Base(outside)+`"},
		{"path": "~/lichen/zshrc"},
		{"path": "relative/zshrc"}
	]}}}`)
	a.sync()
	expect(t, a, ".zshrc", "z\n")
	for _, p := range []string{outside, a.path("lichen/zshrc"), a.path("relative/zshrc")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("wrote %s", p)
		}
	}
}

// An edit made in the sync folder directly is committed under its path.
func TestDirectEditsAreDescribed(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "one\n")
	a.add(".zshrc")
	a.write("lichen/files/zshrc", "two\n")
	a.sync()
	expect(t, a, ".zshrc", "two\n")
	out, err := exec.Command("git", "-C", a.path("lichen"), "log", "-1", "--format=%s").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "lichen(apple): edit files/zshrc" {
		t.Fatalf("commit subject = %q", got)
	}
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
	executable := func(d *device, want bool) {
		t.Helper()
		if fi, err := os.Stat(d.path("bin/hello")); err != nil || (fi.Mode().Perm()&0o100 != 0) != want {
			t.Fatalf("bin/hello on %s: mode %v, want executable %v (%v)", d.name, fi.Mode(), want, err)
		}
	}
	executable(b, true)

	// The bit alone changing syncs too, from either side.
	os.Chmod(b.path("bin/hello"), 0o644)
	b.sync()
	a.sync()
	executable(a, false)
	os.Chmod(a.path("lichen/files/bin/hello"), 0o755)
	a.sync()
	b.sync()
	executable(a, true)
	executable(b, true)

	// A chmod on one device and an edit on the other both stick.
	a.write("bin/hello", "#!/bin/sh\necho hello\n")
	a.sync()
	os.Chmod(b.path("bin/hello"), 0o644)
	b.sync()
	a.sync()
	for _, d := range []*device{a, b} {
		expect(t, d, "bin/hello", "#!/bin/sh\necho hello\n")
		executable(d, false)
	}
	os.Chmod(a.path("bin/hello"), 0o755)
	a.sync()
	b.write("bin/hello", "#!/bin/sh\necho hey\n")
	b.sync()
	a.sync()
	for _, d := range []*device{a, b} {
		expect(t, d, "bin/hello", "#!/bin/sh\necho hey\n")
		executable(d, true)
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

// An edit beats a deletion made elsewhere at the same time: the file
// comes back everywhere, edited.
func TestEditBeatsDeletion(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "v1\n")
	a.add(".zshrc")
	a.write(".claude/commands/one.md", "1")
	a.write(".claude/commands/two.md", "2")
	a.add(".claude/commands")
	b := newDevice(t, remote, "banana")

	os.Remove(a.path(".zshrc"))
	os.Remove(a.path(".claude/commands/two.md"))
	a.sync()
	b.write(".zshrc", "edited on banana\n")
	b.write(".claude/commands/two.md", "2, edited on banana")
	b.sync()
	a.sync()
	for _, d := range []*device{a, b} {
		expect(t, d, ".zshrc", "edited on banana\n")
		expect(t, d, ".claude/commands/two.md", "2, edited on banana")
	}
	// And it is synced again, not a leftover.
	a.write(".zshrc", "v3\n")
	a.sync()
	b.sync()
	b.sync()
	expect(t, b, ".zshrc", "v3\n")
}

// The same race, with the edit already committed while offline: the
// sync repo's merge gives the edit the win too, either way round, and
// syncing goes on.
func TestEditBeatsDeletionAcrossAnOfflineCommit(t *testing.T) {
	for _, editorRebases := range []bool{true, false} {
		remote := newRemote(t)
		a := newDevice(t, remote, "apple")
		a.write(".zshrc", "v1\n")
		a.add(".zshrc")
		b := newDevice(t, remote, "banana")

		// The device that is offline rebases its commit when it is back.
		offline, online := b, a
		if !editorRebases {
			offline, online = a, b
		}
		os.Rename(remote, remote+".away")
		if editorRebases {
			b.write(".zshrc", "edited on banana\n")
		} else {
			os.Remove(a.path(".zshrc"))
		}
		offline.sync()
		os.Rename(remote+".away", remote)
		if editorRebases {
			os.Remove(a.path(".zshrc"))
		} else {
			b.write(".zshrc", "edited on banana\n")
		}
		online.sync()
		offline.sync()
		online.sync()
		for _, d := range []*device{a, b} {
			expect(t, d, ".zshrc", "edited on banana\n")
		}
		a.write(".zshrc", "later\n")
		a.sync()
		b.sync()
		expect(t, b, ".zshrc", "later\n")
	}
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

	// Nor may one source's name sit inside another's (claude/commands
	// here): ~/claude would be the source claude.
	a.write("claude/notes.md", "n")
	a.write(".claude/settings.json", "{}")
	for p, name := range map[string]string{"~/claude": "", "~/.claude/settings.json": "claude/commands/x"} {
		if _, err := files.Add(env, []string{p}, name, nil); err == nil {
			t.Errorf("adding %s as %q: expected a refusal", p, name)
		}
	}

	// A directory output may not bring files of its own along.
	a.write(".codex/prompts/mine.md", "m")
	if err := files.AddOutput(env, "claude/commands", "~/.codex/prompts", nil); err == nil {
		t.Error("adding an output over a directory with other files: expected a refusal")
	}
}

// Files only the owner may read arrive that way on every device, and
// the sync folder is no one else's either.
func TestPrivateFilesStayPrivate(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".netrc", "machine example.com password hunter2\n")
	os.Chmod(a.path(".netrc"), 0o600)
	a.add(".netrc")
	// One private file in an open directory, there from the start or
	// added later, keeps it out of sight too.
	a.write(".config/gh/hosts.yml", "token: x\n")
	os.Chmod(a.path(".config/gh/hosts.yml"), 0o600)
	a.add(".config/gh")
	a.write(".config/tea/config.yml", "x\n")
	a.add(".config/tea")
	a.write(".config/tea/token", "y\n")
	os.Chmod(a.path(".config/tea/token"), 0o600)
	a.sync()
	b := newDevice(t, remote, "banana")
	for path, want := range map[string]os.FileMode{
		b.path(".netrc"): 0o600, b.path(".config/gh/hosts.yml"): 0o600, b.path(".config/tea/token"): 0o600, b.path("lichen"): 0o700,
	} {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v, want %v (%v)", path, fi.Mode().Perm(), want, err)
		}
	}
}

// A synced directory's own .gitignore keeps nothing out of the sync.
func TestGitignoreInASyncedDirectory(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".config/foo/.gitignore", "*.log\n")
	a.write(".config/foo/app.log", "log\n")
	a.add(".config/foo")
	b := newDevice(t, remote, "banana")
	expect(t, b, ".config/foo/app.log", "log\n")
	expect(t, b, ".config/foo/.gitignore", "*.log\n")
}

// A CLI edit runs on the freshest state even with an edit made in the
// sync folder directly still uncommitted (git won't pull over one).
func TestChangeOverAnUncommittedEdit(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "z\n")
	a.add(".zshrc")
	b := newDevice(t, remote, "banana")
	a.write(".vimrc", "v\n")
	a.add(".vimrc")
	b.write("lichen/files/zshrc", "edited in the sync folder\n")
	b.change(func(env *core.Env) error {
		entries, err := files.List(env)
		if err != nil || len(entries) != 2 {
			t.Errorf("the edit saw %d sources (%v), want 2", len(entries), err)
		}
		return err
	})
	expect(t, b, ".zshrc", "edited in the sync folder\n")
	expect(t, b, ".vimrc", "v\n")
}

// An edit made while the source is deleted elsewhere keeps the source,
// and with it whatever else changed about it: removing it later leaves
// its files in place, as removing any source does.
func TestSourceKeptOverADeletionStaysRemovable(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "v1\n")
	a.add(".zshrc")
	b := newDevice(t, remote, "banana")

	os.Rename(remote, remote+".away")
	b.write(".zshrc", "edited on banana\n")
	b.change(func(env *core.Env) error { return files.AddOutput(env, "zshrc", "~/.zshrc.local", nil) })
	os.Rename(remote+".away", remote)
	os.Remove(a.path(".zshrc"))
	a.sync()
	b.sync()
	a.sync()
	expect(t, a, ".zshrc", "edited on banana\n")
	expect(t, a, ".zshrc.local", "edited on banana\n")

	a.change(func(env *core.Env) error {
		_, err := files.Remove(env, []string{"zshrc"})
		return err
	})
	b.sync()
	for _, d := range []*device{a, b} {
		expect(t, d, ".zshrc", "edited on banana\n")
		expect(t, d, ".zshrc.local", "edited on banana\n")
	}
}

// Several offline commits, each racing a deletion, all settle.
func TestSeveralOfflineCommitsRacingDeletions(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "z\n")
	a.write(".vimrc", "v\n")
	a.add(".zshrc")
	a.add(".vimrc")
	b := newDevice(t, remote, "banana")

	os.Rename(remote, remote+".away")
	b.write(".zshrc", "z, banana\n")
	b.sync()
	b.write(".vimrc", "v, banana\n")
	b.sync()
	os.Rename(remote+".away", remote)
	os.Remove(a.path(".zshrc"))
	os.Remove(a.path(".vimrc"))
	a.sync()
	b.sync()
	a.sync()
	for _, d := range []*device{a, b} {
		expect(t, d, ".zshrc", "z, banana\n")
		expect(t, d, ".vimrc", "v, banana\n")
	}
}

// A new source where a deleted one was doesn't bring the old one back.
func TestAddingInsideADeletedSource(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".config/tool/one", "1")
	a.add(".config/tool")
	os.RemoveAll(a.path(".config/tool"))
	a.sync()
	a.write(".config/tool/new", "n")
	a.add(".config/tool/new")
	a.sync()
	entries, err := files.List(a.env())
	if err != nil || len(entries) != 1 || entries[0].Name != "config/tool/new" {
		t.Fatalf("sources = %v (%v), want just config/tool/new", entries, err)
	}
	if a.has(".config/tool/one") {
		t.Fatal("the deleted source came back")
	}
}

// What sits behind a symlink is not lichen's to judge: a file missing
// there is not a deletion, and a deletion elsewhere doesn't reach it.
func TestNothingThroughSymlinks(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".config/tool/one", "1")
	a.write(".config/tool/two", "2")
	a.add(".config/tool")
	b := newDevice(t, remote, "banana")

	// apple's .config becomes a link to an empty directory.
	os.Rename(a.path(".config"), a.path("config-aside"))
	os.MkdirAll(a.path("empty"), 0o755)
	os.Symlink(a.path("empty"), a.path(".config"))
	a.sync()
	b.sync()
	expect(t, b, ".config/tool/one", "1")

	// banana's .config moves out of home, linked back; apple deletes one.
	outside := t.TempDir()
	os.Rename(b.path(".config"), filepath.Join(outside, "config"))
	os.Symlink(filepath.Join(outside, "config"), b.path(".config"))
	os.Remove(a.path(".config"))
	os.Rename(a.path("config-aside"), a.path(".config"))
	os.Remove(a.path(".config/tool/one"))
	a.sync()
	b.sync()
	if _, err := os.Stat(filepath.Join(outside, "config/tool/one")); err != nil {
		t.Fatalf("deleted through banana's symlink: %v", err)
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
