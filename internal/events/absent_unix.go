//go:build !windows

package events

import (
	"errors"
	"os"
	"syscall"
)

// absent reports whether connecting failed for want of a daemon: its
// socket is not there, or nothing listens on it.
func absent(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
}
