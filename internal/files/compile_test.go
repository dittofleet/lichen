package files

import (
	"io/fs"
	"maps"
	"slices"
	"strings"
	"testing"
)

func reader(files map[string]string) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		s, ok := files[name]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return []byte(s), nil
	}
}

func TestCompile(t *testing.T) {
	srcs := map[string]string{
		"zshrc":      "a\n§ zsh/common\n§ zsh/mac if mac, lychee\n§ zsh/linux if linux\n§ zsh/linux unless mac\n§§ not an insert\nb\n",
		"zsh/common": "c1\n§ zsh/nested\n",
		"zsh/nested": "n1",
		"zsh/mac":    "m1\n",
		"zsh/linux":  "l1\n",
	}
	c, err := compile("zshrc", reader(srcs), []string{"lychee", "mac"})
	if err != nil {
		t.Fatal(err)
	}
	want := "a\nc1\nn1\nm1\n§ not an insert\nb\n"
	if got := string(c.content()); got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if got := slices.Sorted(maps.Keys(c.deps)); !slices.Equal(got, []string{"zsh/common", "zsh/mac", "zsh/nested", "zshrc"}) {
		t.Fatalf("deps = %v (an insert not compiled in is not one)", got)
	}
	from := []pos{{"zshrc", 0}, {"zsh/common", 0}, {"zsh/nested", 0}, {"zsh/mac", 0}, {"zshrc", 5}, {"zshrc", 6}}
	if !slices.Equal(c.lines, from) {
		t.Fatalf("lines = %v, want %v", c.lines, from)
	}
	// After m1: the end of zsh/mac, or zshrc around the linux inserts
	// that aren't compiled in here.
	if got, want := c.gaps[4], []pos{{"zsh/mac", 1}, {"zshrc", 3}, {"zshrc", 4}, {"zshrc", 5}}; !slices.Equal(got, want) {
		t.Fatalf("gap before line 4 = %v, want %v", got, want)
	}
}

func TestCompileErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		srcs map[string]string
		err  string
	}{
		"missing":      {map[string]string{"a": "§ b\n"}, "no source b"},
		"cycle":        {map[string]string{"a": "§ b\n", "b": "§ c\n", "c": "§ a\n"}, "a > b > c > a"},
		"self":         {map[string]string{"a": "x\n§ a if mac\n"}, "a > a"},
		"no space":     {map[string]string{"a": "§b\n"}, "§§"},
		"empty if":     {map[string]string{"a": "§ b if \n", "b": ""}, "`if` takes"},
		"empty unless": {map[string]string{"a": "§ b unless \n", "b": ""}, "`unless` takes"},
		"bad group":    {map[string]string{"a": "§ b if Mac\n", "b": ""}, "not a device or group"},
		"bad name":     {map[string]string{"a": "§ ../b\n"}, "not a usable source name"},
		"binary in":    {map[string]string{"a": "§ b\n", "b": "\x00"}, "not text"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := compile("a", reader(tc.srcs), []string{"mac"})
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("err = %v, want one saying %q", err, tc.err)
			}
		})
	}
}

func TestCompileKeepsBinaryAndEndings(t *testing.T) {
	for _, s := range []string{"\x00§ x\n", "no newline", "", "\n", "crlf\r\n§§ x\r\n"} {
		c, err := compile("a", reader(map[string]string{"a": s}), nil)
		if err != nil {
			t.Fatal(err)
		}
		want := strings.ReplaceAll(s, "§§", "§")
		if binary([]byte(s)) {
			want = s
		}
		if got := string(c.content()); got != want {
			t.Fatalf("compile(%q) = %q, want %q", s, got, want)
		}
	}
}

func TestEscapeRoundTrips(t *testing.T) {
	for _, s := range []string{"§ x\n§§ y\nz\n", "§", "a\n§ b", "plain\n"} {
		c, err := compile("a", reader(map[string]string{"a": string(escape([]byte(s)))}), nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(c.content()); got != s {
			t.Fatalf("escaped %q compiles to %q", s, got)
		}
	}
}

func TestCompileHidesBlanksOfInsertsNotHere(t *testing.T) {
	for name, tc := range map[string]struct{ src, want string }{
		"at the start":            {"§ x if linux\n\na\n", "a\n"},
		"at the end":              {"a\n\n§ x if linux\n", "a\n"},
		"between paragraphs":      {"a\n\n§ x if linux\n\nb\n", "a\n\nb\n"},
		"two in a row":            {"a\n\n§ x if linux\n§ x if linux\n\nb\n", "a\n\nb\n"},
		"each between paragraphs": {"a\n\n§ x if linux\n\n§ x if linux\n", "a\n"},
		"only spacing above":      {"a\n\n§ x if linux\nb\n", "a\n\nb\n"},
		"no spacing":              {"a\n§ x if linux\nb\n", "a\nb\n"},
		"an insert that is here":  {"a\n\n§ x if mac\n\nb\n", "a\n\nx\n\nb\n"},
		"inside an insert":        {"a\n\n§ y\n\nb\n", "a\n\ny\n\nb\n"},
	} {
		t.Run(name, func(t *testing.T) {
			srcs := map[string]string{"a": tc.src, "x": "x\n", "y": "y\n§ x if linux\n"}
			c, err := compile("a", reader(srcs), []string{"mac"})
			if err != nil {
				t.Fatal(err)
			}
			if got := string(c.content()); got != tc.want {
				t.Fatalf("content = %q, want %q", got, tc.want)
			}
			if len(c.lines) != len(c.out.lines) || len(c.gaps) != len(c.out.lines)+1 {
				t.Fatalf("%d lines, %d origins, %d gaps", len(c.out.lines), len(c.lines), len(c.gaps))
			}
		})
	}
}
