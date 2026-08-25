package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/jjtest"
)

// findingsText flattens findings for substring assertions; the exact wording is
// not the contract, the named path and suggested command are.
func findingsText(findings []doctorFinding) string {
	var sb strings.Builder
	for _, f := range findings {
		sb.WriteString(f.Repo)
		sb.WriteString(" ")
		sb.WriteString(f.Issue)
		sb.WriteString("\n")
	}
	return sb.String()
}

func TestCheckMainWorkspaceCleanClone(t *testing.T) {
	f := newMainsFixture(t)

	if got := checkMainWorkspace(f.JJ, f.Cfg, f.RepoPath); len(got) != 0 {
		t.Errorf("checkMainWorkspace on a clean clone = %v, want no findings", got)
	}
}

func TestCheckMainWorkspaceReportsStrandedWork(t *testing.T) {
	f := newMainsFixture(t)
	mustJJ(t, f.RepoPath, "describe", "-m", "wip: stranded")
	if err := os.WriteFile(filepath.Join(f.RepoPath, "s.txt"), []byte("s\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := findingsText(checkMainWorkspace(f.JJ, f.Cfg, f.RepoPath))
	if !strings.Contains(got, "wgo park") {
		t.Errorf("finding should suggest `wgo park`, got:\n%s", got)
	}
	wantDest := filepath.Join(f.WorktreesDir, "wip-stranded", testRepo)
	if !strings.Contains(got, wantDest) {
		t.Errorf("finding should name the destination %s, got:\n%s", wantDest, got)
	}
}

func TestCheckMainWorkspaceReportsRedundantTrunkWorkspace(t *testing.T) {
	f := newMainsFixture(t)
	junk := filepath.Join(f.WorktreesDir, "main", testRepo)
	if err := os.MkdirAll(filepath.Dir(junk), 0o755); err != nil {
		t.Fatalf("mkdir junk parent: %v", err)
	}
	mustJJ(t, f.RepoPath, "workspace", "add", "--name", "main", junk)

	got := findingsText(checkMainWorkspace(f.JJ, f.Cfg, f.RepoPath))
	if !strings.Contains(got, junk) {
		t.Errorf("finding should name the redundant workspace %s, got:\n%s", junk, got)
	}
}

// TestCheckMainWorkspaceSkipsSecondaryWorkspaces keeps doctor from reporting a
// feature workspace's own in-progress work as "stranded" — that work is exactly
// where it belongs.
func TestCheckMainWorkspaceSkipsSecondaryWorkspaces(t *testing.T) {
	f := newMainsFixture(t)
	ws := jjtest.NewWorkspace(t, f.RepoPath, "feature")
	mustJJ(t, ws, "describe", "-m", "wip: legitimate feature work")

	if got := checkMainWorkspace(f.JJ, f.Cfg, ws); len(got) != 0 {
		t.Errorf("checkMainWorkspace on a secondary workspace = %v, want no findings", got)
	}
}
