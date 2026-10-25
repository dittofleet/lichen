package service

import (
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dittofleet/go-cli-kit/xdg"

	"lichen/internal/config"
)

// label names the service to launchd.
const label = "dev.lichen"

func plistPath() string {
	return filepath.Join(xdg.Home(), "Library", "LaunchAgents", label+".plist")
}

func domain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

// target names the service itself to launchctl.
func target() string {
	return domain() + "/" + label
}

func launchctl(args ...string) error {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %s", args[0], strings.TrimSpace(string(out)))
	}
	return nil
}

// loaded reports whether launchd has the service.
func loaded() bool {
	return exec.Command("launchctl", "print", target()).Run() == nil
}

// Installed reports whether the service has been set up on this machine.
func Installed() bool {
	_, err := os.Stat(plistPath())
	return err == nil
}

// Install sets the service up to run binary, and starts it. Run again, it
// replaces what was there and restarts the daemon.
func Install(binary string) error {
	logPath, err := config.LogPath()
	if err != nil {
		return err
	}
	var env strings.Builder
	for _, name := range passedOn {
		if value := os.Getenv(name); value != "" {
			fmt.Fprintf(&env, "    <key>%s</key><string>%s</string>\n", name, html.EscapeString(value))
		}
	}
	// RunAtLoad starts the daemon at login, KeepAlive restarts it if it
	// dies, and ThrottleInterval keeps a daemon that cannot start from
	// spinning.
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>30</integer>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
  <key>EnvironmentVariables</key>
  <dict>
%s  </dict>
</dict>
</plist>
`, label, html.EscapeString(binary), html.EscapeString(logPath), html.EscapeString(logPath), env.String())

	if err := os.MkdirAll(filepath.Dir(plistPath()), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	// Out with the old one first. There being none is the usual case.
	launchctl("bootout", target())
	if err := os.WriteFile(plistPath(), []byte(plist), 0o644); err != nil {
		return err
	}
	// launchd refuses a service whose last run is still shutting down, so
	// right after a bootout this can take a moment to go through.
	for range 10 {
		if err = launchctl("bootstrap", domain(), plistPath()); err == nil {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return err
}

// Running reports whether the daemon is up, as launchd describes it.
func Running() (bool, string) {
	out, err := exec.Command("launchctl", "print", target()).CombinedOutput()
	if err != nil {
		return false, "not running"
	}
	state := "registered"
	if m := regexp.MustCompile(`state = (\w+)`).FindSubmatch(out); m != nil {
		state = string(m[1])
	}
	if m := regexp.MustCompile(`pid = (\d+)`).FindSubmatch(out); m != nil {
		state += " (pid " + string(m[1]) + ")"
	}
	return strings.HasPrefix(state, "running"), state
}

// Start starts an installed daemon that was stopped.
func Start() error {
	if loaded() {
		return nil
	}
	return launchctl("bootstrap", domain(), plistPath())
}

// Stop stops the daemon until the next Start or login.
func Stop() error {
	if !loaded() {
		return nil
	}
	return launchctl("bootout", target())
}

// Restart stops the daemon and starts it again, for a new binary.
func Restart() error {
	if !loaded() {
		return Start()
	}
	return launchctl("kickstart", "-k", target())
}

// Remove stops the daemon and takes the service away. With no service set
// up it does nothing.
func Remove() error {
	if !Installed() {
		return nil
	}
	// Stopping one that is not loaded fails, which is fine. One that is
	// still loaded afterwards is not: its daemon would go on syncing.
	if err := launchctl("bootout", target()); err != nil && loaded() {
		return err
	}
	return os.Remove(plistPath())
}
