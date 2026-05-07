package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleSpec = `---
ticket: WGO-101
title: Spec scaffold
status: draft
authors:
    - dave
branches:
    - virtru/wgo:WGO-101-spec
prs: []
created: 2026-05-06T00:00:00Z
updated: 2026-05-06T00:00:00Z
---

# Spec scaffold

## Summary
A summary.
`

func TestParseBytes_withFrontmatter(t *testing.T) {
	sf, err := ParseBytes([]byte(sampleSpec))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if sf.Frontmatter.Ticket != "WGO-101" {
		t.Errorf("ticket = %q, want WGO-101", sf.Frontmatter.Ticket)
	}
	if sf.Frontmatter.Status != StatusDraft {
		t.Errorf("status = %q, want draft", sf.Frontmatter.Status)
	}
	if len(sf.Frontmatter.Authors) != 1 || sf.Frontmatter.Authors[0] != "dave" {
		t.Errorf("authors = %v, want [dave]", sf.Frontmatter.Authors)
	}
	if !strings.Contains(sf.Body, "## Summary") {
		t.Errorf("body missing ## Summary, got: %q", sf.Body)
	}
}

func TestParseBytes_withoutFrontmatter(t *testing.T) {
	content := "# Just a doc\n\nSome body.\n"
	sf, err := ParseBytes([]byte(content))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if sf.Frontmatter.Ticket != "" {
		t.Errorf("expected empty Frontmatter, got ticket=%q", sf.Frontmatter.Ticket)
	}
	if sf.Body != content {
		t.Errorf("body = %q, want %q", sf.Body, content)
	}
}

func TestUpdateFrontmatter_roundtrip(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "WGO-101.md")
	if err := os.WriteFile(path, []byte(sampleSpec), 0o644); err != nil {
		t.Fatal(err)
	}

	// Parse original body bytes
	original, _ := ParseBytes([]byte(sampleSpec))
	originalBody := original.Body

	// Update status only
	if err := UpdateFrontmatter(path, func(fm *Frontmatter) error {
		fm.Status = StatusInProgress
		fm.Updated = time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
		return nil
	}); err != nil {
		t.Fatalf("UpdateFrontmatter: %v", err)
	}

	updated, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse after update: %v", err)
	}
	if updated.Frontmatter.Status != StatusInProgress {
		t.Errorf("status = %q, want in_progress", updated.Frontmatter.Status)
	}
	// Body bytes must be identical
	if updated.Body != originalBody {
		t.Errorf("body changed after frontmatter update:\ngot:  %q\nwant: %q", updated.Body, originalBody)
	}
}

func TestParseTicketFromBranch(t *testing.T) {
	cases := []struct {
		branch string
		want   string
	}{
		{"WGO-101", "WGO-101"},
		{"WGO-101-foo", "WGO-101"},
		{"WGO-101-spec-scaffold", "WGO-101"},
		{"feature-WGO-101", "WGO-101"},
		{"not-a-ticket", ""},
		{"lowercase-123", ""},
		{"", ""},
	}
	for _, c := range cases {
		got := ParseTicketFromBranch(c.branch)
		if got != c.want {
			t.Errorf("ParseTicketFromBranch(%q) = %q, want %q", c.branch, got, c.want)
		}
	}
}

func TestRenderTemplate(t *testing.T) {
	data, err := RenderTemplate(TemplateData{
		Ticket:  "WGO-999",
		Title:   "demo",
		Authors: []string{"dave"},
		Now:     time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "ticket: WGO-999") {
		t.Errorf("rendered output missing ticket, got:\n%s", out)
	}
	if !strings.Contains(out, "status: draft") {
		t.Errorf("rendered output missing status, got:\n%s", out)
	}
	if !strings.Contains(out, "authors: [dave]") {
		t.Errorf("rendered output missing author, got:\n%s", out)
	}
	// Should be parseable as a spec
	sf, err := ParseBytes(data)
	if err != nil {
		t.Fatalf("ParseBytes of rendered template: %v", err)
	}
	if sf.Frontmatter.Ticket != "WGO-999" {
		t.Errorf("parsed ticket = %q, want WGO-999", sf.Frontmatter.Ticket)
	}
}
