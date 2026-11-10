package selfupdate

import (
	"errors"
	"os"
	"os/exec"
)

// ExecSelf runs the binary at this process's own path (after an Install,
// that is the new build) with the same arguments and environment
// (callers os.Setenv anything it must inherit), and exits as it does. A
// process cannot be replaced on Windows, so this one waits for it.
// Returns only on failure.
func ExecSelf() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	var exit *exec.ExitError
	if err := cmd.Run(); err != nil && !errors.As(err, &exit) {
		return err
	}
	os.Exit(cmd.ProcessState.ExitCode())
	return nil
}
