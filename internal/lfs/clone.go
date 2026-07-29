package lfs

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// errCloneUnsupported is returned by the platform clone() when a copy-on-write
// reflink isn't possible (unsupported OS, filesystem, or cross-device). Callers
// fall back to a plain byte copy.
var errCloneUnsupported = errors.New("reflink clone not supported")

// cloneOrCopy materializes src at dst, preferring a copy-on-write reflink
// (near-zero time and disk cost on APFS/Btrfs/XFS) and falling back to a byte
// copy when the platform or filesystem can't clone. dst must not already exist;
// the result takes src's permission bits.
func cloneOrCopy(src, dst string) error {
	err := clone(src, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, errCloneUnsupported) {
		return err
	}
	// A failed clone may have left a partial dst; clear it before copying.
	_ = os.Remove(dst)
	return copyFile(src, dst)
}

// copyFile streams src to a freshly-created dst, preserving src's permission
// bits. dst must not already exist.
func copyFile(src, dst string) error {
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
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}
