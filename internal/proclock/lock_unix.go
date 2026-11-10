//go:build !windows

package proclock

import (
	"os"
	"syscall"
)

// errLocked is what tryLock fails with while another process holds the
// lock.
const errLocked = syscall.EWOULDBLOCK

// tryLock takes the lock on f for this process alone, without waiting.
func tryLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
