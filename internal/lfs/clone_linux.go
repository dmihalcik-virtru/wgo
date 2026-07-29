//go:build linux

package lfs

import (
	"os"

	"golang.org/x/sys/unix"
)

// clone reflinks src to dst using the FICLONE ioctl (Btrfs/XFS/etc.). dst must
// not exist. Any failure (unsupported filesystem, cross-device) is reported as
// errCloneUnsupported so cloneOrCopy falls back to a plain copy.
func clone(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err != nil {
		out.Close()
		os.Remove(dst)
		return errCloneUnsupported
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}
