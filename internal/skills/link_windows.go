package skills

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// makeLink links to a skill's canonical copy with a junction, as the
// Vercel skills CLI does on Windows, where a symlink takes Developer Mode
// or an administrator. A junction's target is always absolute.
func makeLink(canonical, link string) error {
	cmd := exec.Command("cmd")
	// The paths go by the environment: cmd would take an & in one for the
	// end of the command.
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd /d /c mklink /J "%LICHEN_LINK%" "%LICHEN_TARGET%"`}
	cmd.Env = append(os.Environ(), "LICHEN_LINK="+link, "LICHEN_TARGET="+canonical)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("linking %s: %s", link, strings.TrimSpace(string(out)))
	}
	return nil
}
