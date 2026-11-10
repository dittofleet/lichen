//go:build !windows

package selfupdate

import (
	"os"
	"syscall"
)

// ExecSelf replaces this process with the binary at its own path (after
// an Install, that is the new build), preserving arguments and
// environment (callers os.Setenv anything the new process must inherit,
// which keeps repeated execs from stacking duplicate entries). Returns
// only on failure.
func ExecSelf() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(self, os.Args, os.Environ())
}
