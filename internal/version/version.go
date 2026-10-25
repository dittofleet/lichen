// Package version carries the build's stamped release version and orders
// release tags, backing lichen's version gate: the sync folder records the
// newest version that has synced with it, and older builds refuse to
// touch it until they update.
package version

import (
	"regexp"
	"strconv"
	"strings"
)

// Current is stamped by the release workflow via
// -ldflags "-X lichen/internal/version.Current=<tag>". Source builds
// stay "dev".
var Current = "dev"

// IsRelease reports whether this binary carries a stamped release tag.
// Dev builds do not, and sit outside version gating entirely.
func IsRelease() bool { return Valid(Current) }

var tagRE = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// Valid reports whether s has the exact vX.Y.Z shape the release
// workflow enforces on tags.
func Valid(s string) bool { return tagRE.MatchString(s) }

// Compare orders two Valid release tags numerically, returning -1, 0 or
// +1 as a is older than, equal to, or newer than b.
func Compare(a, b string) int {
	as := strings.Split(strings.TrimPrefix(a, "v"), ".")
	bs := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := range as {
		ai, _ := strconv.Atoi(as[i])
		bi, _ := strconv.Atoi(bs[i])
		switch {
		case ai < bi:
			return -1
		case ai > bi:
			return 1
		}
	}
	return 0
}
