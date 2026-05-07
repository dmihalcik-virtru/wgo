package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseBytes(t *testing.T) {
	tests := []struct {
		name string
		data string
		want struct {
			ticket string
			status Status
		}
	}{
		{
			name: "valid frontmatter",
			data: `---
ticket: WGO-101
title: Test Spec
status: draft
authors: [alice]
branches: []
prs: []
created: 2026-05-06
updated: 2026-05-06
---
# Body`,
			want: struct {
				ticket string
				status Status
			}{
				ticket: "WGO-101",
				status: StatusDraft,
			},
		},
		{
			name: "no frontmatter",
			data: `# Just body
Some content`,
			want: struct {
				ticket string
				status Status
			}{
				ticket: "",
				status: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sf, err := ParseBytes([]byte(tt.data))
			if err != nil {
				t.Fatalf("ParseBytes failed: %v", err)
			}
			if sf.Frontmatter.Ticket != tt.want.ticket {
				t.Errorf("ticket: got %q, want %q", sf.Frontmatter.Ticket, tt.want.ticket)
			}
			if sf.Frontmatter.Status != tt.want.status {
				t.Errorf("status: got %q, want %q", sf.Frontmatter.Status, tt.want.status)
			}
		})
	}
}

func TestUpdateFrontmatter(t *testing.T) {
	tmpDir := t.TempDir()
	specPath := filepath.Join(tmpDir, "test.md")

	original := `---
ticket: WGO-101
status: draft
authors: [alice]
branches: []
prs: []
created: 2026-05-06
updated: 2026-05-06
---
# Original Body

This is the body.`

	if err := os.WriteFile(specPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := UpdateFrontmatter(specPath, func(fm *Frontmatter) error {
		fm.Status = StatusInProgress
		fm.Updated = time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
		return nil
	}); err != nil {
		t.Fatalf("UpdateFrontmatter: %v", err)
	}

	updated, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}

	sf, err := ParseBytes(updated)
	if err != nil {
		t.Fatalf("parse updated file: %v", err)
	}

	if sf.Frontmatter.Status != StatusInProgress {
		t.Errorf("status not updated: got %q", sf.Frontmatter.Status)
	}

	if !strings.Contains(sf.Body, "Original Body") {
		t.Errorf("body changed unexpectedly")
	}
}

func TestParseTicketFromBranch(t *testing.T) {
	tests := []struct {
		branch string
		want   string
	}{
		{"WGO-101", "WGO-101"},
		{"WGO-101-foo", "WGO-101"},
		{"feature-WGO-101", ""},
		{"not-a-ticket", ""},
		{"WGO-101-long-branch-name", "WGO-101"},
	}

	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			got := ParseTicketFromBranch(tt.branch)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindByTicket(t *testing.T) {
	tmpDir := t.TempDir()
	specDir := filepath.Join(tmpDir, "spec")
	if err := os.Mkdir(specDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	specFile := filepath.Join(specDir, "WGO-101.md")
	if err := os.WriteFile(specFile, []byte("test"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := FindByTicket(tmpDir, "WGO-101")
	if err != nil {
		t.Fatalf("FindByTicket: %v", err)
	}

	if got != specFile {
		t.Errorf("path mismatch: got %q, want %q", got, specFile)
	}

	_, err = FindByTicket(tmpDir, "WGO-999")
	if err == nil {
		t.Errorf("FindByTicket should return error for non-existent ticket")
	}
}
