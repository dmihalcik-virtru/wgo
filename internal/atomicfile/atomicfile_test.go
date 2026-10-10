package atomicfile

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "f.json")
	if err := Write(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "two" {
		t.Fatalf("got %q", got)
	}
	assertNoTemps(t, filepath.Dir(path))
}

// TestFailedWriteKeepsGoodFile: a writer that fails after a partial write
// never replaces the good file, and leaves no temp file behind.
func TestFailedWriteKeepsGoodFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	if err := Write(path, []byte(`{"good":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("disk full")
	err := WriteFunc(path, 0o600, func(w io.Writer) error {
		_, _ = w.Write([]byte(`{"partial":`))
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	got, _ := os.ReadFile(path)
	if string(got) != `{"good":true}` {
		t.Fatalf("good file replaced: %q", got)
	}
	assertNoTemps(t, filepath.Dir(path))
}

func assertNoTemps(t *testing.T, dir string) {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(m) != 0 {
		t.Fatalf("temp files left behind: %v", m)
	}
}

func TestWriteExactFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	for _, perm := range []os.FileMode{0o600, 0o644} {
		path := filepath.Join(t.TempDir(), "f")
		require.NoError(t, Write(path, []byte("x"), perm))
		fi, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, perm, fi.Mode().Perm())
	}
}

func TestWriteReplaceNarrowsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	path := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))
	require.NoError(t, os.Chmod(path, 0o644))
	require.NoError(t, Write(path, []byte("new"), 0o600))
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	got, _ := os.ReadFile(path)
	assert.Equal(t, "new", string(got))
}

func TestWriteNewDirIsPrivateFor0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	dir := filepath.Join(t.TempDir(), "priv")
	require.NoError(t, Write(filepath.Join(dir, "f"), []byte("x"), 0o600))
	fi, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Zero(t, fi.Mode().Perm()&0o077, "mode %v", fi.Mode().Perm())
}

func TestWriteOverDirectoryFails(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	require.NoError(t, os.Mkdir(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, "keep"), []byte("k"), 0o644))
	require.Error(t, Write(target, []byte("x"), 0o600))
	assertNoTemps(t, root)
	fi, err := os.Stat(target)
	require.NoError(t, err)
	assert.True(t, fi.IsDir())
	got, err := os.ReadFile(filepath.Join(target, "keep"))
	require.NoError(t, err)
	assert.Equal(t, "k", string(got))
}

func TestWriteConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	const n = 16
	payloads := make([][]byte, n)
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte(strconv.Itoa(i%10)+string(rune('a'+i))), 32*1024)
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = Write(path, payloads[i], 0o600)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Len(t, got, 64*1024)
	matches := 0
	for _, p := range payloads {
		if bytes.Equal(got, p) {
			matches++
		}
	}
	assert.Equal(t, 1, matches, "file must equal exactly one payload")
	assertNoTemps(t, dir)
}

func TestWriteCreatesNestedDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "c", "f")
	require.NoError(t, Write(path, []byte("x"), 0o644))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "x", string(got))
}

// TestWriteSyncsDirSmoke checks that the directory-sync path succeeds on a
// freshly created directory.
func TestWriteSyncsDirSmoke(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory sync is a no-op on windows")
	}
	dir := filepath.Join(t.TempDir(), "fresh")
	require.NoError(t, Write(filepath.Join(dir, "f"), []byte("x"), 0o600))
	require.NoError(t, syncDir(dir))
	assertNoTemps(t, dir)
}
