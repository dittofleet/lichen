package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// A Windows console prints color codes as they are until it is asked to
// read them.
func init() {
	if !useColor {
		return
	}
	out := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if windows.GetConsoleMode(out, &mode) != nil || windows.SetConsoleMode(out, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) != nil {
		useColor = false
	}
}
