//go:build darwin

package lfs

import "golang.org/x/sys/unix"

// clone reflinks src to dst using APFS clonefile(2). dst must not exist. Any
// failure (non-APFS volume, cross-device, unsupported) is reported as
// errCloneUnsupported so cloneOrCopy falls back to a plain copy.
func clone(src, dst string) error {
	if err := unix.Clonefile(src, dst, 0); err != nil {
		return errCloneUnsupported
	}
	return nil
}
