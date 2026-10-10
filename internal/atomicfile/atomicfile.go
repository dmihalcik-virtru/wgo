// Package atomicfile replaces files atomically: content is written to a
// uniquely named temp file in the destination directory, synced, and renamed
// over the target. A reader therefore sees either the old file or the
// complete new one, never a truncated write, and a writer that fails or is
// killed part-way leaves the previous file in place.
package atomicfile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Write atomically replaces path with data, creating its directory if needed.
func Write(path string, data []byte, perm os.FileMode) error {
	return WriteFunc(path, perm, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// WriteFunc atomically replaces path with whatever fill writes, creating its
// directory if needed. The replace is atomic: if fill or any later step fails,
// the partial temp file is removed and path is untouched. After a successful
// rename the parent directory is fsynced, so a nil return means the new
// content is durable (where the platform supports directory sync). If the
// directory sync fails, the new file is already in place but the error is
// returned because durability is not guaranteed. If cleanup of the temp file
// also fails, that error is joined to the primary one.
func WriteFunc(path string, perm os.FileMode, fill func(io.Writer) error) error {
	dir := filepath.Dir(path)
	// Directories get the file's read bits promoted to execute, plus owner
	// rwx (0o600 -> 0o700, 0o644 -> 0o755). This only affects directories
	// created here; existing directories keep their mode.
	dirPerm := perm | (perm&0o444)>>2 | 0o700
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return err
	}
	// The unique suffix keeps two concurrent writers from clobbering each
	// other's temp file before the rename.
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// cleanup removes the temp file, keeping err primary and joining any
	// removal failure other than "already gone".
	cleanup := func(err error) error {
		if rmErr := os.Remove(name); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			return errors.Join(err, rmErr)
		}
		return err
	}
	fail := func(err error) error {
		if cerr := tmp.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return cleanup(err)
	}
	if err := fill(tmp); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return cleanup(err)
	}
	if err := os.Rename(name, path); err != nil {
		return cleanup(err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync dir %s: %w", dir, err)
	}
	return nil
}
