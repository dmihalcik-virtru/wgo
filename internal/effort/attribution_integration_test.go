package effort

import (
	"testing"

	"github.com/virtru/wgo/internal/jjtest"
	"github.com/virtru/wgo/internal/store"
)

// TestCrossRepoNoMatch_JJ uses jjtest to create two real repos with the same
// bookmark name and verifies they are not matched across repos.
func TestCrossRepoNoMatch_JJ(t *testing.T) {
	repo1Path, jjc := jjtest.NewRepo(t)
	repo2Path, _ := jjtest.NewRepo(t)

	// Create bookmark "feature" in both repos
	jjtest.Bookmark(t, repo1Path, "feature", "@")
	jjtest.Bookmark(t, repo2Path, "feature", "@")

	// Get current bookmarks
	ws1, err := jjc.ListWorkspaces(repo1Path)
	if err != nil {
		t.Fatalf("list workspaces repo1: %v", err)
	}
	ws2, err := jjc.ListWorkspaces(repo2Path)
	if err != nil {
		t.Fatalf("list workspaces repo2: %v", err)
	}

	// Build workspaces (using first workspace from each repo)
	workspaces := []WorkspaceInfo{
		{Path: ws1[0].Path, MainRepoPath: repo1Path, Bookmark: "feature"},
		{Path: ws2[0].Path, MainRepoPath: repo2Path, Bookmark: "feature"},
	}

	mainClones := []MainCloneInfo{
		{Path: repo1Path, Name: "repo1"},
		{Path: repo2Path, Name: "repo2"},
	}

	// Effort only claims repo1:feature
	efforts := map[string]MergedEffort{
		"effort-1": {
			Name:     "Effort 1",
			Branches: []string{repo1Path + ":feature"},
		},
	}

	result := AttributeWorkspaces(workspaces, mainClones, efforts, nil)

	// Only repo1 workspace should be grouped
	if len(result.Grouped["effort-1"]) != 1 {
		t.Errorf("expected 1 workspace in effort-1, got %d", len(result.Grouped["effort-1"]))
	}

	// repo2 workspace should be ungrouped (not matched across repos)
	if len(result.Ungrouped) != 1 {
		t.Errorf("expected 1 ungrouped workspace (repo2), got %d", len(result.Ungrouped))
	}

	// Verify the ungrouped workspace is repo2
	if len(result.Ungrouped) > 0 && result.Ungrouped[0].MainRepoPath != repo2Path {
		t.Errorf("expected ungrouped workspace to be repo2, got %s", result.Ungrouped[0].MainRepoPath)
	}
}

// TestLegacyStateEffort_JJ uses jjtest to verify that a legacy state-only
// effort with "repo:bookmark" format groups a real workspace.
func TestLegacyStateEffort_JJ(t *testing.T) {
	repoPath, jjc := jjtest.NewRepo(t)

	// Create a bookmark
	jjtest.Bookmark(t, repoPath, "fix-bug", "@")

	// Get workspace info
	workspaces, err := jjc.ListWorkspaces(repoPath)
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}

	wsInfo := []WorkspaceInfo{
		{Path: workspaces[0].Path, MainRepoPath: repoPath, Bookmark: "fix-bug"},
	}

	mainClones := []MainCloneInfo{
		{Path: repoPath, Name: "myrepo", Owner: "owner", Repo: "myrepo"},
	}

	// Create a legacy state-only effort with "repo:bookmark" format
	stateEfforts := map[string]store.Effort{
		"legacy-effort": {
			Name:        "Legacy Effort",
			Description: "State-only effort",
			Branches:    []string{"myrepo:fix-bug"}, // Legacy format
		},
	}

	// Merge with empty plan efforts
	merged, _ := MergeEfforts(stateEfforts, nil)

	result := AttributeWorkspaces(wsInfo, mainClones, merged, nil)

	// Workspace should be grouped under the legacy effort
	if len(result.Grouped["legacy-effort"]) != 1 {
		t.Errorf("expected 1 workspace in legacy-effort, got %d", len(result.Grouped["legacy-effort"]))
	}

	if len(result.Ungrouped) != 0 {
		t.Errorf("expected 0 ungrouped workspaces, got %d", len(result.Ungrouped))
	}
}
