//go:build !unix

package store

import "time"

// acquireStateLock is a no-op on platforms without flock support: the store
// still functions, it just lacks cross-process serialization of state writes.
func acquireStateLock(_ string, _ time.Duration) (func(), error) {
	return func() {}, nil
}
