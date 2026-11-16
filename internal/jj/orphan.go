package jj

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// OrphanReason reports whether ws is listed by jj but no longer exists on
// disk, and if so why. A workspace is orphaned when jj reports no root, when
// its root directory is missing (jj keeps reporting the old root after the
// directory is deleted), when its root is not a directory, or when its root
// has no .jj directory. An unmounted volume also appears missing.
//
// err is non-nil when the root or .jj directory could not be checked (for
// example, permission denied); the workspace is then neither live nor
// orphaned as far as the caller can tell, and must not be forgotten.
func OrphanReason(ws Workspace) (reason string, orphaned bool, err error) {
	if ws.Path == "" {
		// jj reports no root once the workspace directory is gone.
		return "its directory is gone", true, nil
	}
	root, err := os.Stat(ws.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf("its directory %s is gone", ws.Path), true, nil
	}
	if err != nil {
		return "", false, err
	}
	if !root.IsDir() {
		return fmt.Sprintf("%s is not a directory", ws.Path), true, nil
	}
	fi, err := os.Stat(filepath.Join(ws.Path, ".jj"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}
	if err != nil || !fi.IsDir() {
		return fmt.Sprintf("%s has no .jj directory", ws.Path), true, nil
	}
	return "", false, nil
}
