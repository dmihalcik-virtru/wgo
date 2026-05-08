// Package cache provides a simple in-process TTL cache for expensive remote calls.
package cache

import (
	"sync"
	"time"
)

// TTL is a single-value cache that expires after a configurable duration.
// Safe for concurrent use.
type TTL[T any] struct {
	mu        sync.Mutex
	ttl       time.Duration
	value     T
	expiresAt time.Time
	populated bool
}

// NewTTL creates a new TTL cache with the given expiry duration.
func NewTTL[T any](ttl time.Duration) *TTL[T] {
	return &TTL[T]{ttl: ttl}
}

// Get returns the cached value if still valid, otherwise calls fetch, caches
// the result, and returns it. On fetch error the stale value (if any) is
// returned together with the error.
func (c *TTL[T]) Get(fetch func() (T, error)) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.populated && time.Now().Before(c.expiresAt) {
		return c.value, nil
	}
	val, err := fetch()
	if err != nil {
		return c.value, err
	}
	c.value = val
	c.expiresAt = time.Now().Add(c.ttl)
	c.populated = true
	return c.value, nil
}

// Invalidate clears the cached value so the next Get call triggers a fresh fetch.
func (c *TTL[T]) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.populated = false
}
