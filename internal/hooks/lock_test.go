package hooks

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

func TestFileLock_LockUnlock(t *testing.T) {
	dir := t.TempDir()

	lock := NewFileLock(dir)
	if err := lock.Lock(); err != nil {
		t.Fatalf("Lock() failed: %v", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("Unlock() failed: %v", err)
	}

	// Lock file should have been created
	if _, err := os.Stat(lock.path); err != nil {
		t.Fatalf("lock file not created: %v", err)
	}
}

func TestFileLock_UnlockWithoutLock(t *testing.T) {
	dir := t.TempDir()
	lock := NewFileLock(dir)

	// Unlock without Lock should be a no-op
	if err := lock.Unlock(); err != nil {
		t.Fatalf("Unlock() without Lock() failed: %v", err)
	}
}

func TestFileLock_ConcurrentAccess(t *testing.T) {
	dir := t.TempDir()

	// Use a shared counter to verify mutual exclusion
	var counter int64
	var maxConcurrent int64
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			lock := NewFileLock(dir)
			if err := lock.Lock(); err != nil {
				t.Errorf("Lock() failed: %v", err)
				return
			}

			// Increment and check - should always be exactly 1 while holding lock
			cur := atomic.AddInt64(&counter, 1)
			if cur > 1 {
				atomic.StoreInt64(&maxConcurrent, cur)
			}

			// Simulate some work
			atomic.AddInt64(&counter, -1)

			if err := lock.Unlock(); err != nil {
				t.Errorf("Unlock() failed: %v", err)
			}
		}()
	}

	wg.Wait()

	if max := atomic.LoadInt64(&maxConcurrent); max > 1 {
		t.Errorf("lock did not provide mutual exclusion: max concurrent = %d", max)
	}
}
