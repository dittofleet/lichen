package events

import (
	"errors"

	"golang.org/x/sys/windows"
)

// absent reports whether connecting failed for want of a daemon. Windows
// refuses a socket that is not there or that nothing listens on, and
// calls the network down when the socket's folder is not there either.
func absent(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED) || errors.Is(err, windows.WSAENETDOWN)
}
