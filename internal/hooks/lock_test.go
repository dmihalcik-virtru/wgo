package hooks

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileLock_LockUnlock(t *testing.T) {
	dir := t.TempDir()

	lock := NewFileLock(dir)
	require.NoError(t, lock.Lock(), "Lock() failed")
	require.NoError(t, lock.Unlock(), "Unlock() failed")

	// Lock file should have been created
	_, err := os.Stat(lock.path)
	require.NoError(t, err, "lock file not created")
}

func TestFileLock_UnlockWithoutLock(t *testing.T) {
	dir := t.TempDir()
	lock := NewFileLock(dir)

	// Unlock without Lock should be a no-op
	require.NoError(t, lock.Unlock(), "Unlock() without Lock() failed")
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

	max := atomic.LoadInt64(&maxConcurrent)
	assert.LessOrEqual(t, max, int64(1), "lock did not provide mutual exclusion: max concurrent = %d", max)
}
