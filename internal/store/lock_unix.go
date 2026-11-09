//go:build unix

package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// lockPollInterval is how often a bounded acquireStateLock retries a held
// lock. flock has no timeout of its own, so a bounded wait polls a
// non-blocking attempt instead.
const lockPollInterval = 10 * time.Millisecond

// acquireStateLock takes an exclusive advisory lock (flock) on path and
// returns a closure that releases it. A timeout of zero or less blocks until
// the lock is available; otherwise it gives up with ErrStateLocked once
// timeout has passed. The lock file is created if missing and is never
// removed: flock is advisory and tied to the open file description, so
// leaving the file in place is harmless and avoids a create/unlink race
// between processes.
func acquireStateLock(path string, timeout time.Duration) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open state lock: %w", err)
	}
	if err := flockWithin(f, timeout); err != nil {
		_ = f.Close()
		if errors.Is(err, ErrStateLocked) {
			return nil, fmt.Errorf("%w: %s still held after %s", ErrStateLocked, path, timeout)
		}
		return nil, fmt.Errorf("failed to lock state: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func flockWithin(f *os.File, timeout time.Duration) error {
	if timeout <= 0 {
		return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		if time.Now().After(deadline) {
			return ErrStateLocked
		}
		time.Sleep(lockPollInterval)
	}
}
