package files_test

import (
	"os"
	"slices"
	"testing"

	"lichen/internal/core"
	"lichen/internal/files"
)

// zshDevices sets up apple and banana syncing ~/.zshrc, which inserts
// zsh/common, as is ~/.bashrc on banana.
func zshDevices(t *testing.T) (a, b *device) {
	remote := newRemote(t)
	a = newDevice(t, remote, "apple")
	a.write(".zshrc", "top\n")
	a.add(".zshrc")
	a.write("lichen/files/zshrc", "top\n§ zsh/common\nend\n")
	a.write("lichen/files/zsh/common", "c1\nc2\n")
	a.sync()
	b = newDevice(t, remote, "banana")
	b.write(".bashrc", "")
	b.add(".bashrc", "banana")
	b.write("lichen/files/bashrc", "§ zsh/common\n")
	b.sync()
	a.sync()
	return a, b
}

func TestInsertsSyncBothWays(t *testing.T) {
	a, b := zshDevices(t)
	for _, d := range []*device{a, b} {
		expect(t, d, ".zshrc", "top\nc1\nc2\nend\n")
	}
	expect(t, b, ".bashrc", "c1\nc2\n")

	// An edit to inserted lines goes back to the source they came from,
	// and so to everything that inserts it.
	a.write(".zshrc", "top\nC1\nc2\nend\n")
	a.sync()
	expect(t, a, "lichen/files/zsh/common", "C1\nc2\n")
	expect(t, a, "lichen/files/zshrc", "top\n§ zsh/common\nend\n")
	b.sync()
	expect(t, b, ".zshrc", "top\nC1\nc2\nend\n")
	expect(t, b, ".bashrc", "C1\nc2\n")

	// Two outputs on one device: the other follows in the same pass.
	b.write(".bashrc", "C1\nc1.5\nc2\n")
	b.sync()
	expect(t, b, ".zshrc", "top\nC1\nc1.5\nc2\nend\n")
}

func TestInsertsByDevice(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write(".zshrc", "")
	a.add(".zshrc")
	a.write("lichen/files/zshrc", "§ zsh/apple if apple\nshared\n§ zsh/others if banana, cherry\n")
	a.write("lichen/files/zsh/apple", "apple only\n")
	a.write("lichen/files/zsh/others", "not apple\n")
	a.sync()
	b := newDevice(t, remote, "banana")
	expect(t, a, ".zshrc", "apple only\nshared\n")
	expect(t, b, ".zshrc", "shared\nnot apple\n")

	// Lines inserted on banana only are edited from banana, and what
	// apple doesn't insert stays as it is there.
	b.write(".zshrc", "SHARED\nNOT APPLE\n")
	b.sync()
	a.sync()
	expect(t, a, ".zshrc", "apple only\nSHARED\n")
	expect(t, a, "lichen/files/zsh/others", "NOT APPLE\n")
}

// New lines between an insert and the file around it could belong to
// either: the output holds them until resolve says which.
func TestAmbiguousLinesWaitForResolve(t *testing.T) {
	a, b := zshDevices(t)
	a.write(".zshrc", "top\nc1\nc2\nnew\nend\n")
	a.sync()
	expect(t, a, ".zshrc", "top\nc1\nc2\nnew\nend\n")
	expect(t, a, "lichen/files/zshrc", "top\n§ zsh/common\nend\n")
	expect(t, a, "lichen/files/zsh/common", "c1\nc2\n")
	a.use()
	if h := files.Holds(); len(h) != 1 || h[0].Conflict {
		t.Fatalf("holds = %v, want a question", h)
	}
	qs, conflicting, err := files.Inspect(a.env(), a.path(".zshrc"), nil)
	if err != nil || conflicting != "" || len(qs) != 1 || !slices.Equal(qs[0].Lines, []string{"new"}) || !slices.Equal(qs[0].Sources, []string{"zsh/common", "zshrc"}) {
		t.Fatalf("inspect = %v, %q, %v", qs, conflicting, err)
	}

	// Held: a change from elsewhere waits too.
	b.write(".zshrc", "TOP\nc1\nc2\nend\n")
	b.sync()
	a.sync()
	expect(t, a, ".zshrc", "top\nc1\nc2\nnew\nend\n")

	a.change(func(env *core.Env) error { return files.Resolve(env, a.path(".zshrc"), "", []string{"zsh/common"}) })
	expect(t, a, "lichen/files/zsh/common", "c1\nc2\nnew\n")
	expect(t, a, ".zshrc", "TOP\nc1\nc2\nnew\nend\n")
	if h := files.Holds(); len(h) != 0 {
		t.Fatalf("holds = %v after resolving", h)
	}
	b.sync()
	expect(t, b, ".bashrc", "c1\nc2\nnew\n")
}

func TestInsertConflict(t *testing.T) {
	a, b := zshDevices(t)
	a.write(".zshrc", "top\napple\nc2\nend\n")
	a.sync()
	b.write(".bashrc", "banana\nc2\n")
	b.write(".zshrc", "top\nc1\nc2\nend, from banana\n")
	b.sync()
	expect(t, b, ".bashrc", "banana\nc2\n")
	b.use()
	if h := files.Holds(); len(h) != 1 || !h[0].Conflict {
		t.Fatalf("holds = %v, want a conflict at ~/.bashrc", h)
	}
	// The edit that merges went through, and with it apple's edit.
	expect(t, b, ".zshrc", "top\napple\nc2\nend, from banana\n")

	// Keeping here wins where they clash.
	b.change(func(env *core.Env) error { return files.Resolve(env, b.path(".bashrc"), "here", nil) })
	a.sync()
	expect(t, a, ".zshrc", "top\nbanana\nc2\nend, from banana\n")
}

// Deleting an output deletes its own source, not what it inserts.
func TestDeletingAnOutputKeepsItsInserts(t *testing.T) {
	a, b := zshDevices(t)
	os.Remove(a.path(".zshrc"))
	a.sync()
	b.sync()
	if b.has(".zshrc") {
		t.Fatal("~/.zshrc is still on banana")
	}
	expect(t, b, ".bashrc", "c1\nc2\n")
	expect(t, b, "lichen/files/zsh/common", "c1\nc2\n")
}

// A file whose lines start with § syncs as it is.
func TestLiteralMarks(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote, "apple")
	a.write("notes.md", "§ 1 Scope\n§§ odd\n")
	a.add("notes.md")
	b := newDevice(t, remote, "banana")
	expect(t, b, "notes.md", "§ 1 Scope\n§§ odd\n")
	b.write("notes.md", "§ 1 Scope\n§ 2 Terms\n§§ odd\n")
	b.sync()
	a.sync()
	expect(t, a, "notes.md", "§ 1 Scope\n§ 2 Terms\n§§ odd\n")
}

// A broken insert leaves the output alone until it is fixed.
func TestBrokenInsertHoldsTheOutput(t *testing.T) {
	a, _ := zshDevices(t)
	a.write("lichen/files/zshrc", "top\n§ zsh/missing\nend\n")
	a.sync()
	expect(t, a, ".zshrc", "top\nc1\nc2\nend\n")
	a.write("lichen/files/zshrc", "top\n§ zsh/common\n")
	a.sync()
	expect(t, a, ".zshrc", "top\nc1\nc2\n")
}

// An edit beats a deletion made elsewhere at the same time, inserts and
// all: the file comes back as it was, with the edit in its sources.
func TestEditBeatsDeletionWithInserts(t *testing.T) {
	a, b := zshDevices(t)
	os.Remove(a.path(".zshrc"))
	a.sync()
	b.write(".zshrc", "top\nC1\nc2\nend, edited\n")
	b.sync()
	expect(t, b, "lichen/files/zshrc", "top\n§ zsh/common\nend, edited\n")
	expect(t, b, "lichen/files/zsh/common", "C1\nc2\n")
	b.sync() // what else inserts it follows on the next pass
	expect(t, b, ".bashrc", "C1\nc2\n")
	a.sync()
	expect(t, a, ".zshrc", "top\nC1\nc2\nend, edited\n")
}
