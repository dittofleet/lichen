//go:build !windows

package daemon

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// untilStopped returns a context that ends when the daemon is told to
// stop: by launchd or systemd, or from the keyboard.
func untilStopped() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
