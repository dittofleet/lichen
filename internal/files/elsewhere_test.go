package files_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lichen/internal/core"
	"lichen/internal/module"
)

// push commits straight into the remote, as would a device whose file
// system allows names this one's does not: files to add or replace, then
// paths to remove.
func push(t *testing.T, remote string, add map[string]string, remove ...string) {
	t.Helper()
	index := filepath.Join(t.TempDir(), "index")
	work := t.TempDir()
	git := func(stdin string, args ...string) string {
		t.Helper()
		// Not checked against what Windows and macOS allow, as on a device
		// that allows it all.
		cmd := exec.Command("git", append([]string{"-c", "core.protectNTFS=false", "-c", "core.protectHFS=false", "--git-dir", remote, "--work-tree", work}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index,
			"GIT_AUTHOR_NAME=elsewhere", "GIT_AUTHOR_EMAIL=e@e", "GIT_COMMITTER_NAME=elsewhere", "GIT_COMMITTER_EMAIL=e@e")
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("", "read-tree", "main")
	for path, content := range add {
		blob := git(content, "hash-object", "-w", "--stdin")
		git("", "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path)
	}
	for _, path := range remove {
		git("", "update-index", "--force-remove", path)
	}
	commit := git("", "commit-tree", git("", "write-tree"), "-p", "main", "-m", "lichen(elsewhere): test")
	git("", "update-ref", "refs/heads/main", commit)
}

// expectPushed checks what the remote holds: path to content.
func expectPushed(t *testing.T, remote string, want map[string]string) {
	t.Helper()
	for path, content := range want {
		got, err := exec.Command("git", "--git-dir", remote, "show", "main:"+path).Output()
		if err != nil || string(got) != content {
			t.Fatalf("the remote's %s = %q (%v), want %q", path, got, err, content)
		}
	}
}

// pass runs a pass that may fail, as the daemon's may, and carries on.
func (d *device) pass() {
	if err := module.Pass(d.env(), true); err != nil {
		d.t.Logf("%s: pass: %v", d.name, err)
	}
}

// Two files whose names differ only by case are one file to Windows, and
// to macOS as it comes. A device that cannot hold both takes in nothing
// until one is renamed, and loses neither.
func TestNamesThatDifferOnlyByCase(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".config/tool/a.txt", "lower\n")
	a.add(".config/tool")
	push(t, remote, map[string]string{"files/config/tool/A.TXT": "upper\n"})
	both := map[string]string{"files/config/tool/a.txt": "lower\n", "files/config/tool/A.TXT": "upper\n"}

	// A device that was syncing already, with a change of its own to send.
	a.write(".config/tool/b.txt", "from apple\n")
	for range 3 {
		a.pass()
	}
	expectPushed(t, remote, both)
	expect(t, a, ".config/tool/a.txt", "lower\n")

	// A device joining.
	b := freshDevice(t, "banana")
	if _, err := core.Setup(remote); err != nil {
		if b.has("lichen") {
			t.Fatal("a setup that failed left a sync folder behind")
		}
	} else {
		b.pass()
		b.pass()
	}
	expectPushed(t, remote, both)

	// Renamed on a device that has both, they sync again.
	push(t, remote, map[string]string{"files/config/tool/upper.txt": "upper\n"}, "files/config/tool/A.TXT")
	a.sync()
	expect(t, a, ".config/tool/a.txt", "lower\n")
	expect(t, a, ".config/tool/upper.txt", "upper\n")
	expectPushed(t, remote, map[string]string{"files/config/tool/b.txt": "from apple\n"})
}

// A git killed while it wrote the index (a power cut, say) leaves its
// lock in the sync folder, which git then refuses to work in.
func TestALockLeftBehindIsCleared(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "one\n")
	a.add(".zshrc")
	b := newDevice(t, remote, "banana")

	// Locks this fresh may be a git still at work.
	locks := []string{a.path("lichen/.git/index.lock"), a.path("lichen/.git/HEAD.lock"), a.path("lichen/.git/refs/heads/main.lock")}
	for _, lock := range locks {
		os.WriteFile(lock, nil, 0o644)
	}
	a.write(".zshrc", "two\n")
	a.pass()
	for _, lock := range locks {
		if _, err := os.Stat(lock); err != nil {
			t.Fatal("a fresh lock was removed")
		}
		old := time.Now().Add(-time.Hour)
		os.Chtimes(lock, old, old)
	}
	a.sync()
	b.sync()
	expect(t, b, ".zshrc", "two\n")
}

// Something of the user's sitting where the sync folder goes survives a
// setup that fails.
func TestSetupLeavesWhatIsInItsWay(t *testing.T) {
	remote := newRemote(t)
	newDevice(t, remote, "apple")
	b := freshDevice(t, "banana")
	b.write("lichen", "mine")
	if _, err := core.Setup(remote); err == nil {
		t.Fatal("setup over a file: expected a refusal")
	}
	expect(t, b, "lichen", "mine")
}
