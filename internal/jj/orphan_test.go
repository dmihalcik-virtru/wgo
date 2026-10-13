package jj

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrphanReason(t *testing.T) {
	live := t.TempDir()
	if err := os.Mkdir(filepath.Join(live, ".jj"), 0o755); err != nil {
		t.Fatal(err)
	}
	noJJ := t.TempDir()
	jjFile := t.TempDir()
	if err := os.WriteFile(filepath.Join(jjFile, ".jj"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		path     string
		orphaned bool
		reason   string
	}{
		{"gone", "", true, "its directory is gone"},
		{"missing dir", filepath.Join(t.TempDir(), "deleted"), true, "deleted is gone"},
		{"no .jj", noJJ, true, "has no .jj directory"},
		{".jj is a file", jjFile, true, "has no .jj directory"},
		{"live", live, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, orphaned, err := OrphanReason(Workspace{Name: "ws", Path: tt.path})
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if orphaned != tt.orphaned || !strings.Contains(reason, tt.reason) {
				t.Fatalf("OrphanReason(%q) = %q, %v; want orphaned=%v containing %q", tt.path, reason, orphaned, tt.orphaned, tt.reason)
			}
		})
	}
}

func TestOrphanReasonUncheckable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	parent := t.TempDir()
	ws := filepath.Join(parent, "ws")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ws, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ws, 0o755) })

	_, orphaned, err := OrphanReason(Workspace{Name: "ws", Path: ws})
	if err == nil || orphaned {
		t.Fatalf("unreadable workspace: orphaned=%v err=%v; want an error and not orphaned", orphaned, err)
	}
}
