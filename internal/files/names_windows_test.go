package files_test

import (
	"strings"
	"testing"

	"lichen/internal/core"
	"lichen/internal/files"
)

// Another device can push a file under a name Windows does not allow. A
// Windows device takes in nothing until it is renamed, and deletes
// nothing for the others meanwhile.
func TestNamesWindowsDoesNotAllow(t *testing.T) {
	for _, name := range []string{"what?.txt", "a:b", "aux", "con.txt", "ends in a dot."} {
		t.Run(name, func(t *testing.T) {
			remote := newRemote(t)
			a := newDevice(t, remote, "apple")
			a.write(".config/tool/a.txt", "a\n")
			a.write(".zshrc", "rc\n")
			a.add(".config/tool")
			a.add(".zshrc")
			push(t, remote, map[string]string{"files/config/tool/" + name: "from elsewhere\n"})
			held := map[string]string{"files/config/tool/a.txt": "a\n", "files/config/tool/" + name: "from elsewhere\n", "files/zshrc": "rc\n"}

			// A device that was syncing already, with a change of its own
			// to send.
			a.write(".zshrc", "rc, edited\n")
			for range 3 {
				a.pass()
			}
			expectPushed(t, remote, held)

			// A device joining gets no sync folder, rather than one with
			// every file missing, which its first pass would delete for
			// every device.
			b := freshDevice(t, "banana")
			if _, err := core.Setup(remote); err == nil {
				t.Fatal("setup went through")
			}
			if b.has("lichen") {
				t.Fatal("a setup that failed left a sync folder behind")
			}
			expectPushed(t, remote, held)

			// Renamed where it came from, everything syncs again.
			push(t, remote, map[string]string{"files/config/tool/renamed.txt": "from elsewhere\n"}, "files/config/tool/"+name)
			a.sync()
			expect(t, a, ".config/tool/renamed.txt", "from elsewhere\n")
			expectPushed(t, remote, map[string]string{"files/zshrc": "rc, edited\n"})
			b.setup(remote)
			expect(t, b, ".config/tool/renamed.txt", "from elsewhere\n")
		})
	}
}

// A path longer than the 260 characters Windows once stopped at.
func TestLongPaths(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".config/tool/a.txt", "a\n")
	a.add(".config/tool")
	long := strings.Repeat("a-directory-with-a-long-name/", 12) + "file.txt"
	push(t, remote, map[string]string{"files/config/tool/" + long: "deep\n"})
	a.sync()
	expect(t, a, ".config/tool/"+long, "deep\n")

	a.write(".config/tool/"+long, "deep, edited\n")
	a.sync()
	expectPushed(t, remote, map[string]string{"files/config/tool/" + long: "deep, edited\n"})
	b := newDevice(t, remote, "banana")
	expect(t, b, ".config/tool/"+long, "deep, edited\n")
}

// A terminal keeps a folder as it was typed, so a path can reach lichen
// with the home directory spelled in another case.
func TestAHomeTypedInAnotherCase(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".config/tool/one", "1\n")
	typed := strings.ToUpper(a.home) + `\.config\tool`
	if typed == a.path(".config/tool") {
		t.Skip("the home directory has no letters to change the case of")
	}
	a.change(func(env *core.Env) error { return files.Add(env, typed, "tool", core.Scope{}) })
	b := newDevice(t, remote, "banana")
	expect(t, b, ".config/tool/one", "1\n")
	a.change(func(env *core.Env) error {
		_, err := files.Remove(env, []string{strings.ToLower(a.home) + `\.config\tool`})
		return err
	})
}
