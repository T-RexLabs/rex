//go:build unix

package eventlog

import (
	"fmt"
	"os"
	"syscall"
)

// acquireExclusiveLock blocks until this process holds an exclusive
// flock on f. POSIX flock is advisory and per-open-file-description,
// which is what storage.EVENTS.4 needs: every Rex writer cooperates;
// every Rex reader skips locking entirely.
func acquireExclusiveLock(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("eventlog: acquire flock: %w", err)
	}
	return nil
}

func releaseLock(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("eventlog: release flock: %w", err)
	}
	return nil
}
