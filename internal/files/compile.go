package files

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"unicode"

	"lichen/internal/core"
)

// A source line starting with § inserts another source there:
//
//	§ zsh/aliases
//	§ zsh/work if lychee, servers
//
// The first on every device, the second only on devices in one of those
// groups. A line meant to start with § in the output starts with §§ in
// the source. Outputs carry no trace of either: lichen remembers where
// each of their lines came from instead, which is how an edit to one
// finds its way back (see unmap).
const mark = "§"

// text is a file's content as lines, without their newlines, and
// whether the last one ends with a newline.
type text struct {
	lines []string
	eol   bool
}

func split(b []byte) text {
	if len(b) == 0 {
		return text{}
	}
	s := string(b)
	eol := strings.HasSuffix(s, "\n")
	return text{lines: strings.Split(strings.TrimSuffix(s, "\n"), "\n"), eol: eol}
}

func (t text) bytes() []byte {
	if len(t.lines) == 0 {
		return nil
	}
	s := strings.Join(t.lines, "\n")
	if t.eol {
		s += "\n"
	}
	return []byte(s)
}

func binary(b []byte) bool { return bytes.IndexByte(b, 0) >= 0 }

// directive parses a line starting with § (and not §§): the source it
// inserts, and the groups it is limited to.
func directive(line string) (name string, groups []string, err error) {
	rest := strings.TrimPrefix(line, mark)
	if strings.TrimLeftFunc(rest, unicode.IsSpace) == rest {
		return "", nil, fmt.Errorf("%q: an insert is § and a source name, and a line starting with a § of its own is §§", line)
	}
	name, cond, limited := strings.Cut(rest, " if ")
	name = strings.TrimSpace(name)
	if err := validName(name); err != nil {
		return "", nil, err
	}
	if limited {
		groups = strings.FieldsFunc(cond, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
		if len(groups) == 0 {
			return "", nil, fmt.Errorf("%q: `if` takes devices or groups", line)
		}
		for _, g := range groups {
			if err := core.ValidGroup(g); err != nil {
				return "", nil, err
			}
		}
	}
	return name, groups, nil
}

// pos is a place in a source file: one of its lines, or for a gap, the
// place before that line (len(lines) being the end).
type pos struct {
	file string
	line int
}

// compiled is one source file compiled for a device: the output, where
// each of its lines came from, what each gap between them stands for in
// the sources (gaps[i] being before line i, and holding every place a
// line inserted there could go), and every source file read. A binary
// file is copied as it is.
type compiled struct {
	file   string
	out    text
	raw    []byte
	lines  []pos
	gaps   [][]pos
	deps   map[string][]byte
	groups []string
}

func (c *compiled) content() []byte {
	if c.raw != nil {
		return c.raw
	}
	return c.out.bytes()
}

// compile compiles file for a device in groups, reading sources through
// read.
func compile(file string, read func(string) ([]byte, error), groups []string) (*compiled, error) {
	c := &compiled{file: file, deps: map[string][]byte{}, gaps: [][]pos{nil}, groups: groups}
	top, err := read(file)
	if err != nil {
		return nil, err
	}
	c.deps[file] = top
	if binary(top) {
		c.raw = top
		return c, nil
	}
	in := core.Device{Groups: groups}.In

	var walk func(name string, inside []string) error
	walk = func(name string, inside []string) error {
		b, ok := c.deps[name]
		if !ok {
			var err error
			if b, err = read(name); errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("there is no source %s to insert", name)
			} else if err != nil {
				return err
			}
			c.deps[name] = b
		}
		if binary(b) {
			return fmt.Errorf("%s is not text, so it can't be inserted", name)
		}
		t := split(b)
		chain := append(slices.Clone(inside), name)
		c.spot(name, 0)
		for i, l := range t.lines {
			switch {
			case strings.HasPrefix(l, mark+mark):
				c.emit(strings.TrimPrefix(l, mark), pos{name, i})
			case strings.HasPrefix(l, mark):
				ins, only, err := directive(l)
				if err != nil {
					return fmt.Errorf("%s line %d: %w", name, i+1, err)
				}
				if slices.Contains(chain, ins) {
					return fmt.Errorf("inserts go in a circle: %s", strings.Join(append(chain, ins), " > "))
				}
				if in(only) {
					if err := walk(ins, chain); err != nil {
						return err
					}
				}
			default:
				c.emit(l, pos{name, i})
			}
			c.spot(name, i+1)
		}
		if len(inside) == 0 {
			c.out.eol = t.eol
		}
		return nil
	}
	if err := walk(file, nil); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *compiled) emit(line string, from pos) {
	c.out.lines = append(c.out.lines, line)
	c.lines = append(c.lines, from)
	c.gaps = append(c.gaps, nil)
}

func (c *compiled) spot(file string, line int) {
	last := len(c.gaps) - 1
	c.gaps[last] = append(c.gaps[last], pos{file, line})
}

// escape turns what an output should hold into the source for it: a
// line starting with § gets another, so it isn't taken for an insert.
func escape(b []byte) []byte {
	if binary(b) || !bytes.Contains(b, []byte(mark)) {
		return b
	}
	t := split(b)
	t.lines = escapeLines(t.lines)
	return t.bytes()
}

func escapeLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l
		if strings.HasPrefix(l, mark) {
			out[i] = mark + l
		}
	}
	return out
}
