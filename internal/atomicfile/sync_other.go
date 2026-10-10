//go:build windows

package atomicfile

// syncDir is a no-op on Windows, where directories cannot be fsynced.
func syncDir(string) error { return nil }
