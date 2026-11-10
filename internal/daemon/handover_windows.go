package daemon

import (
	"os"
	"os/exec"
)

// handOver puts the daemon on a binary just installed over its own. A
// process cannot be replaced on Windows, so the new binary's `restart`
// does it: it stops this daemon and starts one of its own.
func handOver() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	restart := exec.Command(self, "restart")
	restart.Stdout, restart.Stderr = os.Stdout, os.Stderr
	return restart.Start()
}
