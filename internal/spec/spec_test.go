package spec

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRenderTemplateAndParse(t *testing.T) {
	now := time.Date(2026, 5, 6, 9, 30, 0, 0, time.UTC)

	data, err := RenderTemplate(TemplateData{
		Ticket:      "WGO-101",
		Title:       "Spec scaffold and plan integration",
		Description: "Add spec scaffolding to wgo add.",
		Authors:     []string{"dmihalcik", "sujan"},
		Branches:    []string{"virtru/wgo:WGO-101-spec-scaffold"},
		Now:         now,
	})
	if err != nil {
		t.Fatalf("RenderTemplate failed: %v", err)
	}

	specFile, err := ParseBytes(data)
	if err != nil {
		t.Fatalf("ParseBytes failed: %v", err)
	}

	if specFile.Frontmatter.Ticket != "WGO-101" {
		t.Fatalf("expected ticket WGO-101, got %q", specFile.Frontmatter.Ticket)
	}

	if specFile.Frontmatter.Title != "Spec scaffold and plan integration" {
		t.Fatalf("expected title preserved, got %q", specFile.Frontmatter.Title)
	}

	if specFile.Frontmatter.Status != StatusDraft {
		t.Fatalf("expected draft status, got %q", specFile.Frontmatter.Status)
	}

	if got := specFile.Frontmatter.Created.Format(time.DateOnly); got != "2026-05-06" {
		t.Fatalf("expected created date 2026-05-06, got %s", got)
	}

	if summary := specFile.Sections["Summary"]; summary != "Add spec scaffolding to wgo add." {
		t.Fatalf("expected summary section parsed, got %q", summary)
	}
}

func TestFindByTicketAndBranch(t *testing.T) {
	repoRoot := t.TempDir()
	specDir := filepath.Join(repoRoot, "spec")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	specPath := filepath.Join(specDir, "WGO-101.md")
	if err := os.WriteFile(specPath, []byte("---\nticket: WGO-101\nstatus: draft\nauthors: []\nbranches: []\nprs: []\ncreated: 2026-05-06\nupdated: 2026-05-06\n---\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	foundByTicket, err := FindByTicket(repoRoot, "WGO-101")
	if err != nil {
		t.Fatalf("FindByTicket failed: %v", err)
	}
	if foundByTicket != specPath {
		t.Fatalf("expected %q, got %q", specPath, foundByTicket)
	}

	foundByBranch, err := FindByBranch(repoRoot, "WGO-101-spec-scaffold")
	if err != nil {
		t.Fatalf("FindByBranch failed: %v", err)
	}
	if foundByBranch != specPath {
		t.Fatalf("expected %q, got %q", specPath, foundByBranch)
	}

	if ticket := ParseTicketFromBranch("WGO-101-spec-scaffold"); ticket != "WGO-101" {
		t.Fatalf("expected WGO-101, got %q", ticket)
	}

	if ticket := ParseTicketFromBranch("feature/spec-scaffold"); ticket != "" {
		t.Fatalf("expected empty ticket, got %q", ticket)
	}
}

func TestUpdateFrontmatterPreservesBody(t *testing.T) {
	repoRoot := t.TempDir()
	path := filepath.Join(repoRoot, "spec.md")

	original := []byte(`---
ticket: WGO-101
title: Old title
status: draft
authors: [dmihalcik]
branches: [virtru/wgo:WGO-101-spec-scaffold]
prs: []
created: 2026-05-06
updated: 2026-05-06
estimate: 1d
---

# Heading

## Summary
This body must stay byte-for-byte identical.
`)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	originalFrontmatter, originalBody, err := splitFrontmatter(original)
	if err != nil {
		t.Fatalf("splitFrontmatter failed: %v", err)
	}
	if len(originalFrontmatter) == 0 {
		t.Fatalf("expected original frontmatter bytes")
	}

	if err := UpdateFrontmatter(path, func(fm *Frontmatter) error {
		fm.Status = StatusInProgress
		fm.Title = "New title"
		fm.Updated = time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
		return nil
	}); err != nil {
		t.Fatalf("UpdateFrontmatter failed: %v", err)
	}

	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	_, updatedBody, err := splitFrontmatter(updated)
	if err != nil {
		t.Fatalf("splitFrontmatter(updated) failed: %v", err)
	}

	if !bytes.Equal(originalBody, updatedBody) {
		t.Fatalf("expected body to be preserved byte-for-byte")
	}

	if !bytes.Contains(updated, []byte("estimate: 1d")) {
		t.Fatalf("expected unknown frontmatter fields to survive update")
	}

	parsed, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if parsed.Frontmatter.Status != StatusInProgress {
		t.Fatalf("expected updated status, got %q", parsed.Frontmatter.Status)
	}

	if parsed.Frontmatter.Title != "New title" {
		t.Fatalf("expected updated title, got %q", parsed.Frontmatter.Title)
	}
}
