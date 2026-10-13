//go:build !unix

package urlhandler

// TryLock is a no-op where flock is unavailable; the URL handler itself
// is macOS-only.
func TryLock(string) (func(), error) { return func() {}, nil }
