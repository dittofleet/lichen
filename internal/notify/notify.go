// Package notify puts up a desktop notification, where there is a
// desktop for one: always on macOS, and on Linux when notify-send is
// there (a server has none, and its log has to do).
package notify

import (
	"os/exec"
	"runtime"
)

func Send(msg string) {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("osascript", "-e", "on run argv", "-e", `display notification (item 1 of argv) with title "lichen"`, "-e", "end run", msg)
	} else if _, err := exec.LookPath("notify-send"); err == nil {
		cmd = exec.Command("notify-send", "lichen", msg)
	} else {
		return
	}
	cmd.Run()
}
