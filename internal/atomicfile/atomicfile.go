// Package atomicfile replaces files atomically: content is written to a
// uniquely named temp file in the destination directory, synced, and renamed
// over the target. A reader therefore sees either the old file or the
// complete new one, never a truncated write, and a writer that fails or is
// killed part-way leaves the previous file in place.
package atomicfile

import (
	"io"
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

// WriteFunc atomically replaces path with whatever fill writes. If fill
// returns an error, the partial temp file is removed and path is untouched.
func WriteFunc(path string, perm os.FileMode, fill func(io.Writer) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// The unique suffix keeps two concurrent writers from clobbering each
	// other's temp file before the rename.
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
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
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
