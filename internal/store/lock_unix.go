//go:build unix

package store

import (
	"fmt"
	"os"
	"syscall"
)

// acquireStateLock takes an exclusive advisory lock (flock) on path, blocking
// until it is available, and returns a closure that releases it. The lock file
// is created if missing and is never removed: flock is advisory and tied to the
// open file description, so leaving the file in place is harmless and avoids a
// create/unlink race between processes.
func acquireStateLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open state lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to lock state: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
