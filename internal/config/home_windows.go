package config

import (
	"os"
	"strings"
)

// spelled gives a path inside the home directory the home directory's
// own spelling. To Windows, names that differ only by case are one name,
// and a terminal keeps a folder as it was typed (cd c:\users\me), where
// lichen compares paths letter for letter.
func spelled(abs, home string) string {
	if len(abs) >= len(home) && strings.EqualFold(abs[:len(home)], home) && (len(abs) == len(home) || os.IsPathSeparator(abs[len(home)])) {
		return home + abs[len(home):]
	}
	return abs
}
