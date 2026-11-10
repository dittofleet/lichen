package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	clikit "github.com/dittofleet/go-cli-kit"
	"github.com/dittofleet/go-cli-kit/xdg"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"lichen/internal/config"
	"lichen/internal/daemon"
)

// Windows has nothing like launchd for one user's background programs, so
// here the daemon is an ordinary process that lichen starts and stops
// itself. It is started at login from the Run key, which Settings lists
// under Startup apps. Nothing starts it again if it dies.
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// value names lichen's entry in the Run key.
const value = "lichen"

// start runs the daemon with no window, writing to its log.
func start(binary string) error {
	logPath, err := config.LogPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	log, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer log.Close()
	child := exec.Command(binary, "daemon")
	// Not the folder this command was run in: Windows will not let a
	// folder be renamed or removed while a process is working in it.
	child.Dir = xdg.Home()
	child.Stdout, child.Stderr = log, log
	// A console of its own, so it outlives this terminal and is not
	// interrupted along with it.
	child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if err := child.Start(); err != nil {
		return err
	}
	return child.Process.Release()
}

// Installed reports whether the service has been set up on this machine.
func Installed() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	_, _, err = key.GetStringValue(value)
	return err == nil
}

// Install sets the service up to run binary, and starts it. Run again, it
// replaces what was there and restarts the daemon.
func Install(binary string) error {
	// Windows gives a command-line program started outside a terminal, as
	// one is at login, a window of its own. So what it starts at login is
	// the windowless launcher the install script puts beside the binary,
	// which runs `lichen start`. A build from source has none.
	launcher := clikit.Windowless(binary)
	if _, err := os.Stat(launcher); err != nil {
		return fmt.Errorf("%s, which starts the daemon at login, is not there", launcher)
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.SetStringValue(value, fmt.Sprintf(`"%s" start`, launcher)); err != nil {
		return err
	}
	if err := daemon.Stop(); err != nil {
		return err
	}
	return start(binary)
}

// Running reports whether the daemon is up, and says so.
func Running() (bool, string) {
	if daemon.Running() {
		return true, "running"
	}
	return false, "not running"
}

// Start starts an installed daemon that was stopped.
func Start() error {
	if daemon.Running() {
		return nil
	}
	binary, err := clikit.Executable()
	if err != nil {
		return err
	}
	return start(binary)
}

// Stop stops the daemon until the next Start or login.
func Stop() error { return daemon.Stop() }

// Restart stops the daemon and starts it again, for a new binary. An
// update leaves the new binary where the running one was started from.
func Restart() error {
	if err := daemon.Stop(); err != nil {
		return err
	}
	return Start()
}

// Remove stops the daemon and takes the service away. With no service set
// up it does nothing.
func Remove() error {
	if !Installed() {
		return nil
	}
	// A daemon still running afterwards would go on syncing.
	if err := daemon.Stop(); err != nil {
		return err
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.DeleteValue(value)
}
