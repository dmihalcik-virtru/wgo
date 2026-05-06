package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFindBranchSpec(t *testing.T) {
	repoRoot := t.TempDir()
	specDir := filepath.Join(repoRoot, "spec")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	content := `---
ticket: WGO-101
title: Spec scaffold and plan integration
status: draft
authors: [dmihalcik]
branches: [virtru/wgo:WGO-101-spec-scaffold]
prs: []
created: 2026-05-06
updated: 2026-05-06
---
`
	if err := os.WriteFile(filepath.Join(specDir, "WGO-101.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	info, err := findBranchSpec(repoRoot, "WGO-101-spec-scaffold")
	if err != nil {
		t.Fatalf("findBranchSpec failed: %v", err)
	}
	if info == nil {
		t.Fatalf("expected spec info")
	}
	if info.RelPath != "spec/WGO-101.md" {
		t.Fatalf("expected relative spec path, got %q", info.RelPath)
	}
	if info.Status != "draft" {
		t.Fatalf("expected draft status, got %q", info.Status)
	}
	if info.Updated.Format(time.DateOnly) != "2026-05-06" {
		t.Fatalf("expected updated date, got %s", info.Updated.Format(time.DateOnly))
	}
}

func TestFormatSpecInfo(t *testing.T) {
	missing := formatSpecInfo(&branchSpecInfo{
		Ticket:  "WGO-101",
		Missing: true,
	})
	if missing != "⚠ no spec (run: wgo spec new WGO-101)" {
		t.Fatalf("unexpected missing spec format: %q", missing)
	}

	present := formatSpecInfo(&branchSpecInfo{
		RelPath: "spec/WGO-101.md",
		Status:  "draft",
		Updated: time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC),
	})
	if present != "📄 spec/WGO-101.md (draft, updated 2026-05-06)" {
		t.Fatalf("unexpected spec format: %q", present)
	}
}
