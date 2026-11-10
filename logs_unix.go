//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// followLog prints the end of the log, and what is added to it from then
// on.
func followLog(path string) error {
	tail, err := exec.LookPath("tail")
	if err != nil {
		return err
	}
	return syscall.Exec(tail, []string{"tail", "-n", "50", "-F", path}, os.Environ())
}
