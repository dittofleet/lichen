//go:build !windows

package daemon

import "lichen/internal/selfupdate"

// handOver puts the daemon on a binary just installed over its own,
// which takes this process's place.
func handOver() error {
	return selfupdate.ExecSelf()
}
