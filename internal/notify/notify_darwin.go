package notify

import "os/exec"

// command is what puts the notification up.
func command(msg string) *exec.Cmd {
	return exec.Command("osascript", "-e", "on run argv", "-e", `display notification (item 1 of argv) with title "lichen"`, "-e", "end run", msg)
}
