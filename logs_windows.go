package main

import (
	"os"
	"os/exec"
)

// followLog prints the end of the log, and what is added to it from then
// on.
func followLog(path string) error {
	cmd := exec.Command("powershell", "-NoProfile", "-Command", "Get-Content -LiteralPath $env:LICHEN_LOG -Encoding UTF8 -Tail 50 -Wait")
	cmd.Env = append(os.Environ(), "LICHEN_LOG="+path)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
