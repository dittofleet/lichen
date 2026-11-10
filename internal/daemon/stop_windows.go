package daemon

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// Windows has no signal to stop a process in the background with. The
// daemon holds an event by this name for as long as it runs, and `lichen
// stop` sets it.
const stopEvent = `Local\lichen-daemon-stop`

func openStopEvent() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(stopEvent)
	if err != nil {
		return 0, err
	}
	return windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
}

// untilStopped returns a context that ends when the daemon is told to
// stop: by `lichen stop`, from the keyboard, or by Windows as the user
// logs off, which Go hands over as SIGTERM.
func untilStopped() (context.Context, context.CancelFunc) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	name, _ := windows.UTF16PtrFromString(stopEvent)
	// A daemon already running holds the event, which is opened then.
	if event, _ := windows.CreateEvent(nil, 1, 0, name); event != 0 {
		go func() {
			windows.WaitForSingleObject(event, windows.INFINITE)
			cancel()
		}()
	}
	return ctx, cancel
}

// Running reports whether a daemon is up.
func Running() bool {
	event, err := openStopEvent()
	if err != nil {
		// A daemon started from an administrator's terminal is there, and
		// out of reach of any other.
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	windows.CloseHandle(event)
	return true
}

// Stop tells the running daemon to stop, and waits for its process to
// go, as Windows will not remove a file it still has open. With no daemon
// running it does nothing.
func Stop() error {
	event, err := openStopEvent()
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return errors.New("the daemon was started from an administrator's terminal, and only one can stop it")
	}
	if err != nil {
		return nil
	}
	err = windows.SetEvent(event)
	windows.CloseHandle(event)
	if err != nil {
		return err
	}
	for range 100 {
		if !Running() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("the daemon did not stop")
}
