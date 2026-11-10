package files_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// junction makes link a junction to target, the kind of link Windows
// makes without asking for Developer Mode or an administrator.
func junction(t *testing.T, target, link string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v\n%s", err, out)
	}
}

// A junction is a link like any other: nothing syncs through one.
func TestNothingThroughJunctions(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".config/tool/one", "1")
	a.write(".config/tool/two", "2")
	a.add(".config/tool")
	b := newDevice(t, remote, "banana")

	// apple's .config becomes a junction to an empty directory.
	os.Rename(a.path(".config"), a.path("config-aside"))
	os.MkdirAll(a.path("empty"), 0o755)
	junction(t, a.path("empty"), a.path(".config"))
	a.sync()
	b.sync()
	expect(t, b, ".config/tool/one", "1")

	// banana's .config moves out of home, linked back, and apple deletes
	// one.
	outside := t.TempDir()
	os.Rename(b.path(".config"), filepath.Join(outside, "config"))
	junction(t, filepath.Join(outside, "config"), b.path(".config"))
	os.Remove(a.path(".config"))
	os.Rename(a.path("config-aside"), a.path(".config"))
	os.Remove(a.path(".config/tool/one"))
	a.sync()
	b.sync()
	if _, err := os.Stat(filepath.Join(outside, "config/tool/one")); err != nil {
		t.Fatalf("deleted through banana's junction: %v", err)
	}
}

// An output that is a junction itself, in the way of a synced directory,
// is left alone, and so is what it points at.
func TestJunctionsAreLeftAlone(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".config/tool/one", "synced")
	a.add(".config/tool")

	b := freshDevice(t, "banana")
	b.write("elsewhere/one", "mine")
	b.write("elsewhere/extra", "mine too")
	os.MkdirAll(b.path(".config"), 0o755)
	junction(t, b.path("elsewhere"), b.path(".config/tool"))
	b.setup(remote)
	b.sync()
	expect(t, b, "elsewhere/one", "mine")
	expect(t, b, "elsewhere/extra", "mine too")
	a.sync()
	expect(t, a, ".config/tool/one", "synced")
	if a.has(".config/tool/extra") {
		t.Fatal("a file reached apple through banana's junction")
	}
}
