package notify

import "os/exec"

// command is what puts the notification up, or nil with nothing to.
func command(msg string) *exec.Cmd {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return nil
	}
	return exec.Command("notify-send", "lichen", msg)
}
