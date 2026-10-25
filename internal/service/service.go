// Package service keeps the daemon running in the background: as a launchd
// agent on macOS and a systemd user service on Linux, started on its own
// and restarted if it dies.
package service

// The daemon runs git and each harness's CLI (claude, codex), and finds
// its folders through these, so a service started by launchd or systemd,
// which know nothing of the shell's environment, is handed the installing
// shell's values.
var passedOn = []string{"PATH", "LICHEN_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME"}
