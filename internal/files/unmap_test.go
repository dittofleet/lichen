package files

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
)

// The output of zshrc, as every case starts from it, is
//
//	top
//	c1
//	c2
//	end
var zsh = map[string]string{
	"zshrc":      "top\n§ zsh/common\nend\n",
	"zsh/common": "c1\nc2\n",
}

func unmapped(t *testing.T, srcs map[string]string, live string, into string) (map[string]string, []question, error) {
	t.Helper()
	base, err := compile("zshrc", reader(srcs), []string{"mac"})
	if err != nil {
		t.Fatal(err)
	}
	out, open, err := unmap(base, split([]byte(live)), chooser([]string{into}))
	got := map[string]string{}
	for f, b := range out {
		got[f] = string(b)
	}
	return got, open, err
}

func TestUnmap(t *testing.T) {
	for name, tc := range map[string]struct {
		live, into string
		want       map[string]string
	}{
		"edit an inserted line": {
			live: "top\nC1\nc2\nend\n",
			want: map[string]string{"zsh/common": "C1\nc2\n"},
		},
		"edit the file's own line": {
			live: "TOP\nc1\nc2\nend\n",
			want: map[string]string{"zshrc": "TOP\n§ zsh/common\nend\n"},
		},
		"delete an inserted line": {
			live: "top\nc2\nend\n",
			want: map[string]string{"zsh/common": "c2\n"},
		},
		"add between inserted lines": {
			live: "top\nc1\nnew\nc2\nend\n",
			want: map[string]string{"zsh/common": "c1\nnew\nc2\n"},
		},
		"add at the very end": {
			live: "top\nc1\nc2\nend\nnew\n",
			want: map[string]string{"zshrc": "top\n§ zsh/common\nend\nnew\n"},
		},
		"add where either could take it, answered": {
			live: "top\nc1\nc2\nnew\nend\n",
			into: "zshrc",
			want: map[string]string{"zshrc": "top\n§ zsh/common\nnew\nend\n"},
		},
		"add where either could take it, answered the other way": {
			live: "top\nc1\nc2\nnew\nend\n",
			into: "zsh/common",
			want: map[string]string{"zsh/common": "c1\nc2\nnew\n"},
		},
		"typed § lines stay literal": {
			live: "top\nc1\n§ zsh/common\nc2\nend\n",
			want: map[string]string{"zsh/common": "c1\n§§ zsh/common\nc2\n"},
		},
		"lines of one source replaced by more": {
			live: "top\nx\ny\nz\nend\n",
			want: map[string]string{"zsh/common": "x\ny\nz\n"},
		},
		"final newline dropped": {
			live: "top\nc1\nc2\nend",
			want: map[string]string{"zshrc": "top\n§ zsh/common\nend"},
		},
		"emptied": {
			live: "",
			want: map[string]string{"zshrc": "§ zsh/common", "zsh/common": ""},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, open, err := unmapped(t, zsh, tc.live, tc.into)
			if err != nil || len(open) > 0 {
				t.Fatalf("err = %v, open = %v", err, open)
			}
			if !maps.Equal(got, tc.want) {
				t.Fatalf("sources = %q, want %q", got, tc.want)
			}
		})
	}
}

// An insert's last newline shows nowhere, so it always has one, which
// keeps a last line that is blank.
func TestUnmapKeepsBlankLastLines(t *testing.T) {
	for _, tc := range []struct{ part, live, want string }{
		{"\nx", "top\n\nend\n", "\n"},
		{"x", "top\nx\n\nend\n", "x\n\n"},
	} {
		srcs := map[string]string{"zshrc": "top\n§ part\nend\n", "part": tc.part}
		got, open, err := unmapped(t, srcs, tc.live, "part")
		if err != nil || len(open) > 0 || got["part"] != tc.want {
			t.Fatalf("part %q, output %q: sources = %q, %v, %v, want part %q", tc.part, tc.live, got, open, err, tc.want)
		}
	}
}

func TestUnmapAsks(t *testing.T) {
	for name, live := range map[string]string{
		"added after an insert":           "top\nc1\nc2\nnew\nend\n",
		"added before one":                "top\nnew\nc1\nc2\nend\n",
		"lines of two sources replaced":   "top\nx\ny\nz\nw\n",
		"added to a file of inserts only": "",
	} {
		t.Run(name, func(t *testing.T) {
			srcs := zsh
			if live == "" {
				srcs = map[string]string{"zshrc": "§ zsh/common\n", "zsh/common": "c1\n"}
				live = "c1\nnew\n"
			}
			_, open, err := unmapped(t, srcs, live, "")
			if err != nil || len(open) != 1 {
				t.Fatalf("err = %v, open = %v: want one question", err, open)
			}
			files := sourcesOf(open[0])
			slices.Sort(files)
			if !slices.Equal(files, []string{"zsh/common", "zshrc"}) {
				t.Fatalf("question offers %v", files)
			}
		})
	}
}

func TestUnmapInsertedTwice(t *testing.T) {
	srcs := map[string]string{"zshrc": "§ x\nmid\n§ x\n", "x": "a\nb\n"}
	got, open, err := unmapped(t, srcs, "a\nB\nmid\na\nb\n", "")
	if err != nil || len(open) > 0 || got["x"] != "a\nB\n" {
		t.Fatalf("one copy edited: sources = %q, %v, %v, want x edited", got, open, err)
	}
	got, _, err = unmapped(t, srcs, "a\nB\nmid\na\nB\n", "")
	if err != nil || got["x"] != "a\nB\n" {
		t.Fatalf("both copies edited alike: sources = %q, %v", got, err)
	}
	if _, _, err := unmapped(t, srcs, "a\nB\nmid\na\nBee\n", ""); !errors.Is(err, errClash) {
		t.Fatalf("copies edited differently: err = %v, want a clash", err)
	}
}

func TestDiff(t *testing.T) {
	for _, tc := range []struct {
		old, new string
		want     []hunk
	}{
		{"a b c", "a x c", []hunk{{1, 1, 1, 1}}},
		{"a b", "a b c", []hunk{{2, 0, 2, 1}}},
		{"a b", "x a b", []hunk{{0, 0, 0, 1}}},
		{"a b c", "a c", []hunk{{1, 1, 1, 0}}},
		{"", "a", []hunk{{0, 0, 0, 1}}},
	} {
		got, err := diff(strings.Fields(tc.old), strings.Fields(tc.new))
		if err != nil || !slices.Equal(got, tc.want) {
			t.Fatalf("diff(%q, %q) = %v, %v, want %v", tc.old, tc.new, got, err, tc.want)
		}
	}
}

// A blank line hidden with an insert not here is still in the source,
// and lines added beside it go above it.
func TestUnmapBesideAHiddenBlank(t *testing.T) {
	srcs := map[string]string{"zshrc": "§ zsh/linux if linux\n\na\n\n§ zsh/linux if linux\n", "zsh/linux": "l\n"}
	for live, want := range map[string]string{
		"a\nb\n": "§ zsh/linux if linux\n\na\nb\n\n§ zsh/linux if linux\n",
		"A\n":    "§ zsh/linux if linux\n\nA\n\n§ zsh/linux if linux\n",
		"b\na\n": "b\n§ zsh/linux if linux\n\na\n\n§ zsh/linux if linux\n",
	} {
		got, open, err := unmapped(t, srcs, live, "")
		if err != nil || len(open) > 0 {
			t.Fatalf("%q: %v, %v", live, err, open)
		}
		if got["zshrc"] != want {
			t.Fatalf("%q: zshrc = %q, want %q", live, got["zshrc"], want)
		}
	}
}

// Editing the blank line left beside a hidden one takes that one along,
// so it doesn't show up in its place.
func TestUnmapSpacingBesideAHiddenBlank(t *testing.T) {
	srcs := map[string]string{"zshrc": "a\n\n§ zsh/linux if linux\n\nb\n", "zsh/linux": "l\n"}
	for live, want := range map[string]string{
		"a\nb\n":      "a\n§ zsh/linux if linux\nb\n",
		"a\nx\nb\n":   "a\n§ zsh/linux if linux\nx\nb\n",
		"a\n\nx\nb\n": "a\n\n§ zsh/linux if linux\n\nx\nb\n",
		"a\nx\n\nb\n": "a\nx\n\n§ zsh/linux if linux\n\nb\n",
		"a\n\n\nb\n":  "a\n\n§ zsh/linux if linux\n\n\nb\n",
	} {
		got, open, err := unmapped(t, srcs, live, "")
		if err != nil || len(open) > 0 {
			t.Fatalf("%q: %v, %v", live, err, open)
		}
		if got["zshrc"] != want {
			t.Fatalf("%q: zshrc = %q, want %q", live, got["zshrc"], want)
		}
		// What the device shows after a recompile is what was typed.
		c, err := compile("zshrc", reader(map[string]string{"zshrc": got["zshrc"], "zsh/linux": "l\n"}), []string{"mac"})
		if err != nil {
			t.Fatal(err)
		}
		if string(c.content()) != live {
			t.Fatalf("%q recompiles to %q", live, c.content())
		}
	}
}
