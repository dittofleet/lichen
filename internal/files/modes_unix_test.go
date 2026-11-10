//go:build !windows

package files_test

import (
	"os"
	"testing"
)

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

// An edit beats the deletion of a whole directory, including the files of
// it the edited one inserts.
func TestEditBeatsDeletionOfWhatItInserts(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".config/zsh/main", "")
	a.write(".config/zsh/part", "p1\n")
	os.Chmod(a.path(".config/zsh/part"), 0o755)
	a.add(".config/zsh")
	a.write("lichen/files/config/zsh/main", "m1\n§ config/zsh/part\n")
	a.sync()
	b := newDevice(t, remote, "banana")
	expect(t, b, ".config/zsh/main", "m1\np1\n")

	os.RemoveAll(a.path(".config/zsh"))
	a.sync()
	b.write(".config/zsh/main", "m1\nP1\n")
	b.sync()
	expect(t, b, "lichen/files/config/zsh/main", "m1\n§ config/zsh/part\n")
	expect(t, b, "lichen/files/config/zsh/part", "P1\n")
	b.sync()
	if fi, _ := os.Stat(b.path(".config/zsh/part")); fi.Mode().Perm()&0o100 == 0 {
		t.Fatal("~/.config/zsh/part lost its executable bit")
	}
	a.sync()
	expect(t, a, ".config/zsh/main", "m1\nP1\n")
}

// Bringing back a file edited here while it was deleted elsewhere keeps
// a chmod made with the edit.
func TestEditBeatsDeletionKeepsTheMode(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write("bin/x", "v1\n")
	a.add("bin/x")
	b := newDevice(t, remote, "banana")
	os.Remove(a.path("bin/x"))
	a.sync()
	b.write("bin/x", "v2\n")
	os.Chmod(b.path("bin/x"), 0o755)
	b.sync()
	b.sync()
	if fi, err := os.Stat(b.path("bin/x")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("~/bin/x lost its executable bit: %v, %v", fi.Mode(), err)
	}
	a.sync()
	expect(t, a, "bin/x", "v2\n")
	if fi, _ := os.Stat(a.path("bin/x")); fi.Mode().Perm()&0o100 == 0 {
		t.Fatal("~/bin/x is not executable on apple")
	}
}
