package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseBytes(t *testing.T) {
	content := `---
ticket: WGO-101
title: Spec scaffold
status: draft
authors: [dmihalcik]
created: 2026-05-06
updated: 2026-05-06
---

# Spec scaffold

## Summary
Test summary.
`
	spec, err := ParseBytes([]byte(content))
	if err != nil {
		t.Fatalf("ParseBytes failed: %v", err)
	}

	if spec.Frontmatter.Ticket != "WGO-101" {
		t.Errorf("expected ticket WGO-101, got %s", spec.Frontmatter.Ticket)
	}
	if spec.Sections["Summary"] != "Test summary." {
		t.Errorf("expected summary 'Test summary.', got '%s'", spec.Sections["Summary"])
	}
}

func TestRenderTemplate(t *testing.T) {
	data := TemplateData{
		Ticket:      "WGO-101",
		Title:       "Test Spec",
		Description: "A test description",
		Authors:     []string{"alice", "bob"},
		Now:         time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC),
	}

	out, err := RenderTemplate(data)
	if err != nil {
		t.Fatalf("RenderTemplate failed: %v", err)
	}

	s := string(out)
	if !contains(s, "ticket: WGO-101") {
		t.Errorf("missing ticket")
	}
	if !contains(s, "authors: [alice, bob]") {
		t.Errorf("missing authors")
	}
	if !contains(s, "## Summary\nA test description") {
		t.Errorf("missing summary")
	}
}

func TestParseTicketFromBranch(t *testing.T) {
	tests := []struct {
		branch string
		want   string
	}{
		{"WGO-101-spec-foo", "WGO-101"},
		{"WGO-101", "WGO-101"},
		{"feature-WGO-101", ""}, // Spec says "WGO-101-spec-foo" → "WGO-101"
		{"not-a-ticket", ""},
	}

	for _, tt := range tests {
		got := ParseTicketFromBranch(tt.branch)
		if got != tt.want {
			t.Errorf("ParseTicketFromBranch(%q) = %q, want %q", tt.branch, got, tt.want)
		}
	}
}

func TestUpdateFrontmatter(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "test.md")
	content := `---
ticket: WGO-101
status: draft
---

# Body
Keep me.
`
	err := os.WriteFile(tmp, []byte(content), 0644)
	if err != nil {
		t.Fatal(err)
	}

	err = UpdateFrontmatter(tmp, func(fm *Frontmatter) error {
		fm.Status = StatusInProgress
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}

	s := string(updated)
	if !contains(s, "status: in_progress") {
		t.Errorf("status not updated")
	}
	if !contains(s, "Keep me.") {
		t.Errorf("body lost")
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
