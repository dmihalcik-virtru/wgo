package spec

import (
	"os"
	"path/filepath"
	"testing"
)

const testFrontmatter = `---
ticket: WGO-201
title: Test Spec
status: draft
authors: [alice]
branches: []
prs: []
created: 2026-05-01T00:00:00Z
updated: 2026-05-01T00:00:00Z
---
# Body
`

func writeSpec(t *testing.T, dir, ticket, content string) string {
	t.Helper()
	specDir := filepath.Join(dir, "spec")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		t.Fatalf("mkdir spec: %v", err)
	}
	p := filepath.Join(specDir, ticket+".md")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	return p
}

func TestDetectForBranch_Untracked(t *testing.T) {
	dir := t.TempDir()
	// No spec file; branch has a ticket prefix.
	reports, err := DetectForBranch(dir, "WGO-201-some-feature")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	if reports[0].Kind != DriftUntracked {
		t.Errorf("expected DriftUntracked, got %s", reports[0].Kind)
	}
	if reports[0].Branch != "WGO-201-some-feature" {
		t.Errorf("unexpected branch: %s", reports[0].Branch)
	}
}

func TestDetectForBranch_NoTicket(t *testing.T) {
	dir := t.TempDir()
	reports, err := DetectForBranch(dir, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reports) != 0 {
		t.Errorf("expected 0 reports for non-ticket branch, got %d", len(reports))
	}
}

func TestDetectForBranch_ValidSpec(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "WGO-201", testFrontmatter)
	// Not a real git repo, so commit counting will fail gracefully — no stale report.
	reports, err := DetectForBranch(dir, "WGO-201-some-feature")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No stale report (git command fails gracefully in tmpdir) and not untracked.
	for _, r := range reports {
		if r.Kind == DriftUntracked {
			t.Errorf("should not be untracked when spec exists")
		}
	}
}

func TestDetectOrphaned(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "WGO-201", testFrontmatter)

	// No live branches that reference WGO-201.
	reports, err := detectOrphaned(dir, []string{"main", "feature-branch"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 orphaned report, got %d", len(reports))
	}
	if reports[0].Kind != DriftOrphaned {
		t.Errorf("expected DriftOrphaned, got %s", reports[0].Kind)
	}
}

func TestDetectOrphaned_LiveBranch(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "WGO-201", testFrontmatter)

	// WGO-201-feature starts with the ticket prefix — should not be orphaned.
	reports, err := detectOrphaned(dir, []string{"main", "WGO-201-feature"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reports) != 0 {
		t.Errorf("expected 0 orphaned reports when live branch exists, got %d", len(reports))
	}
}

func TestDetectOrphaned_Terminal(t *testing.T) {
	dir := t.TempDir()
	shipped := `---
ticket: WGO-202
title: Shipped
status: shipped
authors: [alice]
branches: []
prs: []
created: 2026-05-01T00:00:00Z
updated: 2026-05-01T00:00:00Z
---
`
	writeSpec(t, dir, "WGO-202", shipped)
	// Shipped spec should not appear as orphaned.
	reports, err := detectOrphaned(dir, []string{"main"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reports) != 0 {
		t.Errorf("terminal spec should not be orphaned, got %d reports", len(reports))
	}
}

func TestIsHexStr(t *testing.T) {
	tests := []struct {
		s    string
		n    int
		want bool
	}{
		{"abc123def456abc123def456abc123def456abc1", 40, true},
		{"abc123def456abc123def456abc123def456abc1", 39, false},
		{"abc123def456abc123def456abc123def456abcg", 40, false},
		{"", 0, true},
	}
	for _, tt := range tests {
		got := isHexStr(tt.s, tt.n)
		if got != tt.want {
			t.Errorf("isHexStr(%q, %d) = %v, want %v", tt.s, tt.n, got, tt.want)
		}
	}
}
