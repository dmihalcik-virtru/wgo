// Package hooks provides git hook management and event processing for wgo.
package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// FileLock provides file-based exclusive locking using syscall.Flock.
type FileLock struct {
	path string
	file *os.File
}

// NewFileLock creates a new FileLock targeting ~/.wgo/.lock.
func NewFileLock(wgoDir string) *FileLock {
	return &FileLock{
		path: filepath.Join(wgoDir, ".lock"),
	}
}

// Lock acquires an exclusive lock. Blocks until the lock is available.
func (l *FileLock) Lock() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open lock file: %w", err)
	}
	l.file = f

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		l.file = nil
		return fmt.Errorf("failed to acquire lock: %w", err)
	}

	return nil
}

// Unlock releases the lock and closes the file.
func (l *FileLock) Unlock() error {
	if l.file == nil {
		return nil
	}

	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil

	if err != nil {
		return fmt.Errorf("failed to release lock: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close lock file: %w", closeErr)
	}

	return nil
}
