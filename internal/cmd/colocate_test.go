package cmd

import (
	"os/exec"
	"testing"

	"github.com/virtru/wgo/internal/config"
	gh "github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jjtest"
)

// TestCreateWorktreeColocatesLegacyRepo verifies that createWorktree brings a
// pre-existing, non-colocated main checkout (as would exist from before
// colocation became the default) into colocation before adding a workspace,
// while the new workspace itself stays plain — colocation is a
// main-workspace-only property (see jj.Client.EnsureColocated).
func TestCreateWorktreeColocatesLegacyRepo(t *testing.T) {
	jjtest.RequireJJ(t)
	repo, jjc := jjtest.NewRepo(t)

	// Simulate a repo created before colocation became the default.
	cmd := exec.Command("jj", "git", "colocation", "disable")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git colocation disable: %v\n%s", err, out)
	}
	if jjc.IsColocated(repo) {
		t.Fatalf("expected repo to be non-colocated after simulating legacy state")
	}
	jjtest.Bookmark(t, repo, "feature", "@")

	cfg := &config.Config{Worktree: config.WorktreeConfig{WorktreesDir: t.TempDir()}}
	parsed := &gh.ParsedURL{Owner: "acme", Repo: "widget", Type: gh.URLTypeBranch}

	wtPath, err := createWorktree(jjc, repo, cfg, parsed, "feature")
	if err != nil {
		t.Fatalf("createWorktree: %v", err)
	}

	if !jjc.IsColocated(repo) {
		t.Fatalf("expected main checkout to be colocated after createWorktree")
	}
	if jjc.IsColocated(wtPath) {
		t.Fatalf("expected new workspace to remain non-colocated (only the main checkout colocates)")
	}
}
