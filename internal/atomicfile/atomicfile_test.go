package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
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
