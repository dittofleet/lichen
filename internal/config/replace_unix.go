//go:build !windows

package config

import "os"

// replace puts tmp in path's place.
func replace(tmp, path string) error {
	return os.Rename(tmp, path)
}
