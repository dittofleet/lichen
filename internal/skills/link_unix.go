//go:build !windows

package skills

import (
	"os"
	"path/filepath"
)

// makeLink links to a skill's canonical copy with a relative symlink,
// exactly what the Vercel skills CLI would create.
func makeLink(canonical, link string) error {
	rel, err := filepath.Rel(realpath(filepath.Dir(link)), realpath(canonical))
	if err != nil {
		rel = canonical
	}
	return os.Symlink(rel, link)
}
