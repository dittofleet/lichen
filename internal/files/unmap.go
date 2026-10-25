package files

import (
	"bytes"
	"errors"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// question is lines added to an output that could belong to more than
// one of its sources, at any of spots (one per source).
type question struct {
	lines []string
	spots []pos
}

// errClash is one source line edited two ways at once: an output that
// has a source inserted twice, edited differently in each place.
var errClash = errors.New("a source inserted more than once was edited differently in each place")

// unmap works out what an edit to an output does to its sources, from
// base, what lichen compiled into it, and live, what it holds now. Each
// changed line goes back to the line it came from, and added lines go
// to the source the lines around them came from. Where that could be
// more than one, choose picks, or the lines are returned unplaced. The
// result is the new content of every source the edit changes.
func unmap(base *compiled, live text, choose func(question) (pos, bool)) (map[string][]byte, []question, error) {
	hunks, err := diff(base.out.lines, live.lines)
	if err != nil {
		return nil, nil, err
	}
	eds := map[string]*edit{}
	ed := func(file string) *edit {
		if eds[file] == nil {
			eds[file] = &edit{repl: map[int][]string{}, ins: map[int][]string{}}
		}
		return eds[file]
	}
	var open []question
	for _, h := range hunks {
		old := base.lines[h.a : h.a+h.b]
		added := live.lines[h.c : h.c+h.d]
		lines := escapeLines(added)
		if h.b == h.d {
			for i, o := range old {
				if base.out.lines[h.a+i] == added[i] {
					continue
				}
				if err := set(ed(o.file).repl, o.line, lines[i:i+1]); err != nil {
					return nil, nil, err
				}
			}
			continue
		}
		for _, o := range old {
			if err := set(ed(o.file).repl, o.line, nil); err != nil {
				return nil, nil, err
			}
		}
		if len(lines) == 0 {
			continue
		}
		spots := base.gaps[h.a]
		if h.b > 0 {
			spots = old
		}
		spots = firstPerFile(spots)
		at := spots[0]
		if len(spots) > 1 {
			q := question{lines: added, spots: spots}
			var ok bool
			if at, ok = choose(q); !ok {
				open = append(open, q)
				continue
			}
		}
		if err := set(ed(at.file).ins, at.line, lines); err != nil {
			return nil, nil, err
		}
	}
	if len(open) > 0 {
		return nil, open, nil
	}
	if live.eol != base.out.eol {
		ed(base.file)
	}
	out := map[string][]byte{}
	for f, e := range eds {
		// The file's own last newline is the output's, and an insert's
		// shows nowhere: it has one, for a last line that is blank.
		t := e.apply(split(base.deps[f]))
		t.eol = f != base.file || live.eol
		if b := t.bytes(); !bytes.Equal(b, base.deps[f]) {
			out[f] = b
		}
	}
	return out, nil, nil
}

// edit is what an edit to an output does to one of its sources: lines
// replaced (by none, when deleted), and lines inserted before a line.
type edit struct {
	repl map[int][]string
	ins  map[int][]string
}

// set records what an edit does at a line, unless it does something else
// there already.
func set(m map[int][]string, line int, lines []string) error {
	if prev, ok := m[line]; ok && !slices.Equal(prev, lines) {
		return errClash
	}
	m[line] = lines
	return nil
}

func (e *edit) apply(t text) text {
	var out []string
	for i, l := range t.lines {
		out = append(out, e.ins[i]...)
		if r, ok := e.repl[i]; ok {
			out = append(out, r...)
		} else {
			out = append(out, l)
		}
	}
	return text{lines: append(out, e.ins[len(t.lines)]...), eol: t.eol}
}

// chooser answers questions with into, in turn: the source named for
// each, the last one for any after it.
func chooser(into []string) func(question) (pos, bool) {
	i := 0
	return func(q question) (pos, bool) {
		if len(into) == 0 {
			return pos{}, false
		}
		name := into[min(i, len(into)-1)]
		i++
		for _, s := range q.spots {
			if s.file == name {
				return s, true
			}
		}
		return pos{}, false
	}
}

// firstPerFile keeps the first of spots in each file.
func firstPerFile(spots []pos) []pos {
	var out []pos
	for _, s := range spots {
		if !slices.ContainsFunc(out, func(o pos) bool { return o.file == s.file }) {
			out = append(out, s)
		}
	}
	return out
}

// hunk is one change from one version of an output to another: old
// lines [a, a+b) became new lines [c, c+d). With no old lines, the new
// ones went before old line a.
type hunk struct{ a, b, c, d int }

// diff lists the changes from old to new, the way git finds them.
func diff(old, new []string) ([]hunk, error) {
	out, err := gitDiff(map[string][]byte{"old": text{lines: old, eol: true}.bytes(), "new": text{lines: new, eol: true}.bytes()},
		"--no-color", "--no-ext-diff", "--text", "-U0", "--inter-hunk-context=0", "old", "new")
	if err != nil {
		return nil, err
	}
	var hunks []hunk
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 || f[0] != "@@" {
			continue
		}
		var h hunk
		h.a, h.b = span(strings.TrimPrefix(f[1], "-"))
		h.c, h.d = span(strings.TrimPrefix(f[2], "+"))
		hunks = append(hunks, h)
	}
	return hunks, nil
}

// gitDiff runs git diff --no-index on two of files.
func gitDiff(files map[string][]byte, args ...string) ([]byte, error) {
	out, err := gitOn(files, append([]string{"--no-pager", "diff", "--no-index"}, args...)...)
	// 1 is a difference, unless nothing says what it is.
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && len(out) > 0 {
		err = nil
	}
	return out, err
}

// span reads a hunk header's start,count: 1-based, and naming the line
// before when the count is 0.
func span(s string) (start, n int) {
	from, count, ok := strings.Cut(s, ",")
	start, _ = strconv.Atoi(from)
	n = 1
	if ok {
		n, _ = strconv.Atoi(count)
	}
	if n > 0 {
		start--
	}
	return start, n
}
