package files_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// hold opens a file as another program might, letting others do to it
// what share says, until the returned func is called.
func hold(t *testing.T, path string, share uint32) (release func()) {
	t.Helper()
	p, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(p, windows.GENERIC_READ, share, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("holding %s: %v", path, err)
	}
	return func() { windows.CloseHandle(h) }
}

// The ways a program holds a file open: letting others do nothing with
// it, only read it, or read and write it. None lets it be replaced or
// deleted, which Unix always does.
var holds = map[string]uint32{
	"alone":                    0,
	"with readers":             windows.FILE_SHARE_READ,
	"with readers and writers": windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE,
}

func attributes(t *testing.T, path string) uint32 {
	t.Helper()
	p, _ := windows.UTF16PtrFromString(path)
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		t.Fatal(err)
	}
	return attrs
}

// leftovers lists the half-written files lichen left under dir.
func leftovers(dir string) []string {
	var found []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".lichen-tmp") {
			found = append(found, p)
		}
		return nil
	})
	return found
}

// An output a program holds open can't be replaced until it lets go. The
// other outputs are written all the same, and that one once it can be.
func TestAnOutputHeldOpen(t *testing.T) {
	for how, share := range holds {
		t.Run(how, func(t *testing.T) {
			remote := newRemote(t)
			a := newDevice(t, remote, "apple")
			a.write(".config/tool/a", "a1\n")
			a.write(".config/tool/m", "m1\n")
			a.write(".config/tool/z", "z1\n")
			a.add(".config/tool")
			b := newDevice(t, remote, "banana")

			a.write(".config/tool/a", "a2\n")
			a.write(".config/tool/m", "m2\n")
			a.write(".config/tool/z", "z2\n")
			a.sync()
			release := hold(t, b.path(".config/tool/m"), share)
			b.pass()
			b.pass()
			expect(t, b, ".config/tool/a", "a2\n")
			expect(t, b, ".config/tool/z", "z2\n")
			if left := leftovers(b.home); len(left) > 0 {
				t.Fatalf("left behind: %v", left)
			}
			release()
			expect(t, b, ".config/tool/m", "m1\n")
			b.sync()
			expect(t, b, ".config/tool/m", "m2\n")
			a.sync()
			expect(t, a, ".config/tool/m", "m2\n")
		})
	}
}

// An edit made here to a file a program still holds open is taken once
// it can be read, and is not mistaken for anything else meanwhile.
func TestAnEditHeldOpen(t *testing.T) {
	for how, share := range holds {
		t.Run(how, func(t *testing.T) {
			remote := newRemote(t)
			a := newDevice(t, remote, "apple")
			a.write(".zshrc", "one\n")
			a.add(".zshrc")
			b := newDevice(t, remote, "banana")

			b.write(".zshrc", "one\nfrom banana\n")
			release := hold(t, b.path(".zshrc"), share)
			a.write(".zshrc", "from apple\none\n")
			a.sync()
			b.pass()
			b.pass()
			release()
			b.sync()
			a.sync()
			expect(t, a, ".zshrc", "from apple\none\nfrom banana\n")
			expect(t, b, ".zshrc", "from apple\none\nfrom banana\n")
		})
	}
}

// A file deleted on another device, which a program holds open here, is
// deleted once it lets go. It does not come back as a new file.
func TestADeletionHeldOpen(t *testing.T) {
	for how, share := range holds {
		t.Run(how, func(t *testing.T) {
			remote := newRemote(t)
			a := newDevice(t, remote, "apple")
			a.write(".config/tool/one", "1")
			a.write(".config/tool/two", "2")
			a.write(".zshrc", "rc\n")
			a.add(".config/tool")
			a.add(".zshrc")
			b := newDevice(t, remote, "banana")

			os.Remove(a.path(".config/tool/one"))
			os.Remove(a.path(".zshrc"))
			a.sync()
			releaseOne := hold(t, b.path(".config/tool/one"), share)
			releaseRC := hold(t, b.path(".zshrc"), share)
			b.pass()
			b.pass()
			a.sync()
			if a.has(".config/tool/one") || a.has(".zshrc") {
				t.Fatal("a deleted file came back while it was held open elsewhere")
			}
			releaseOne()
			releaseRC()
			b.sync()
			a.sync()
			for _, d := range []*device{a, b} {
				if d.has(".config/tool/one") || d.has(".zshrc") {
					t.Fatalf("%s still has a deleted file", d.name)
				}
				expect(t, d, ".config/tool/two", "2")
			}
		})
	}
}

// A read-only file is replaced, as on Unix, and stays read-only. A
// hidden one stays hidden.
func TestOutputsWindowsMarks(t *testing.T) {
	for name, mark := range map[string]uint32{
		"read-only":         windows.FILE_ATTRIBUTE_READONLY,
		"hidden":            windows.FILE_ATTRIBUTE_HIDDEN,
		"hidden system":     windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM,
		"hidden, read-only": windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_READONLY,
	} {
		t.Run(name, func(t *testing.T) {
			remote := newRemote(t)
			a := newDevice(t, remote, "apple")
			a.write(".config/tool/m", "m1\n")
			a.write(".config/tool/z", "z1\n")
			a.add(".config/tool")
			b := newDevice(t, remote, "banana")
			p, _ := windows.UTF16PtrFromString(b.path(".config/tool/m"))
			if err := windows.SetFileAttributes(p, mark); err != nil {
				t.Fatal(err)
			}

			for _, content := range []string{"m2\n", "m3\n"} {
				a.write(".config/tool/m", content)
				a.write(".config/tool/z", content)
				a.sync()
				b.sync()
				expect(t, b, ".config/tool/m", content)
				expect(t, b, ".config/tool/z", content)
				if got := attributes(t, b.path(".config/tool/m")); got&mark != mark {
					t.Fatalf("attributes are %#x, without %#x", got, mark)
				}
			}
			if left := leftovers(b.home); len(left) > 0 {
				t.Fatalf("left behind: %v", left)
			}
		})
	}
}

// A source in the sync folder that a program holds open can't be
// replaced by what another device pushed. Nothing is lost over it, here
// or there, and everything syncs once the program lets go.
func TestASourceHeldOpen(t *testing.T) {
	for how, share := range holds {
		t.Run(how, func(t *testing.T) {
			remote := newRemote(t)
			a := newDevice(t, remote, "apple")
			a.write(".config/tool/a", "a1\n")
			a.write(".config/tool/d", "d1\n")
			a.write(".config/tool/m", "m1\n")
			a.write(".config/tool/y", "y1\n")
			a.write(".config/tool/z", "z1\n")
			a.add(".config/tool")
			b := newDevice(t, remote, "banana")

			a.write(".config/tool/a", "a2\n")
			a.write(".config/tool/m", "m2\n")
			a.write(".config/tool/n", "n1\n")
			a.write(".config/tool/z", "z2\n")
			os.Remove(a.path(".config/tool/d"))
			a.sync()
			release := hold(t, b.path("lichen/files/config/tool/m"), share)
			// Git stops part-way into each pull. What it did check out must not
			// pass for edits made here, which would undo these two.
			b.write(".config/tool/d", "d2, from banana\n")
			b.write(".config/tool/y", "y2, from banana\n")
			b.pass()
			b.pass()
			// Nor may a file that came new with the pull pass for one added
			// here, which would undo what is done to it elsewhere meanwhile.
			a.write(".config/tool/n", "n2\n")
			a.sync()
			b.pass()
			expectPushed(t, remote, map[string]string{"files/config/tool/a": "a2\n", "files/config/tool/m": "m2\n", "files/config/tool/z": "z2\n"})
			release()
			b.sync()
			b.sync()
			a.sync()
			for _, d := range []*device{a, b} {
				expect(t, d, ".config/tool/a", "a2\n")
				expect(t, d, ".config/tool/d", "d2, from banana\n")
				expect(t, d, ".config/tool/m", "m2\n")
				expect(t, d, ".config/tool/n", "n2\n")
				expect(t, d, ".config/tool/y", "y2, from banana\n")
				expect(t, d, ".config/tool/z", "z2\n")
			}
		})
	}
}

// A file that comes new with a pull that stops part-way must not pass
// for one added here, which would undo what another device does to it
// meanwhile.
func TestAFileNewWithABlockedPull(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".config/tool/a", "a1\n")
	a.write(".config/tool/m", "m1\n")
	a.add(".config/tool")
	b := newDevice(t, remote, "banana")

	a.write(".config/tool/m", "m2\n")
	a.write(".config/tool/n", "n1\n")
	a.sync()
	release := hold(t, b.path("lichen/files/config/tool/m"), windows.FILE_SHARE_READ)
	b.pass()
	b.pass()
	a.write(".config/tool/n", "n2\n")
	a.sync()
	b.pass()
	release()
	b.sync()
	b.sync()
	a.sync()
	for _, d := range []*device{a, b} {
		expect(t, d, ".config/tool/m", "m2\n")
		expect(t, d, ".config/tool/n", "n2\n")
	}
}
