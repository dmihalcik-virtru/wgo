package lfs

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCloneOrCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	content := bytes.Repeat([]byte("copy-on-write me\n"), 4096)
	if err := os.WriteFile(src, content, 0o640); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")

	if err := cloneOrCopy(src, dst); err != nil {
		t.Fatalf("cloneOrCopy: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch: got %d bytes want %d", len(got), len(content))
	}
	// The result is an independent regular file, never a symlink.
	fi, err := os.Lstat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("dst should be a regular file, got a symlink")
	}
	// Mutating src must not change dst (reflinks are copy-on-write, not shared).
	if err := os.WriteFile(src, []byte("changed"), 0o640); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, content) {
		t.Fatalf("dst changed after src was rewritten; reflink is not copy-on-write")
	}
}

func TestCopyFileRejectsExistingDst(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(dst, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err == nil {
		t.Fatalf("copyFile should refuse to overwrite an existing dst")
	}
}
