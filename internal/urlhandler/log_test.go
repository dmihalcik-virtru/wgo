package urlhandler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogTimestampsAndSanitizesLines(t *testing.T) {
	path := LogPath(filepath.Join(t.TempDir(), ".wgo"))
	at := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)
	l, err := OpenLog(path, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(l, "first ")
	fmt.Fprint(l, "line\nsecond\x1b[2Jline‮\n")
	fmt.Fprint(l, "unfinished")
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "2026-10-12T09:30:00Z first line\n2026-10-12T09:30:00Z second?[2Jline?\n2026-10-12T09:30:00Z unfinished\n"
	if string(b) != want {
		t.Fatalf("log = %q, want %q", b, want)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", fi.Mode(), err)
	}
}

func TestLogRotates(t *testing.T) {
	path := LogPath(t.TempDir())
	if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxLogSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := OpenLog(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	l.Printf("fresh")
	l.Close()
	if fi, err := os.Stat(path + ".1"); err != nil || fi.Size() != MaxLogSize+1 {
		t.Fatalf("previous log not kept: %v", err)
	}
	if b, _ := os.ReadFile(path); !strings.HasSuffix(string(b), " fresh\n") || len(b) > 100 {
		t.Fatalf("new log = %q", b)
	}
}

func TestLogUnwritableDiscards(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := OpenLog(filepath.Join(blocker, "url-handler.log"), nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if n, werr := fmt.Fprintln(l, "dropped"); werr != nil || n == 0 {
		t.Fatalf("write = %d %v", n, werr)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}
