//go:build !windows

package atomicfile

import (
	"errors"
	"os"
	"syscall"
)

// syncDir fsyncs a directory so a completed rename inside it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	// Some filesystems cannot fsync a directory; that is not a failure.
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
		err = nil
	}
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
