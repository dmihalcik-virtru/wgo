package status

import (
	"testing"
	"time"

	"github.com/virtru/wgo/pkg/models"
)

func TestSortActivities_ByActivity(t *testing.T) {
	now := time.Now()
	activities := []models.RepoActivity{
		{Name: "old", LastActivity: now.Add(-2 * time.Hour)},
		{Name: "newest", LastActivity: now.Add(-5 * time.Minute)},
		{Name: "middle", LastActivity: now.Add(-1 * time.Hour)},
	}

	SortActivities(activities, "activity")

	if activities[0].Name != "newest" {
		t.Errorf("expected newest first, got %q", activities[0].Name)
	}
	if activities[2].Name != "old" {
		t.Errorf("expected old last, got %q", activities[2].Name)
	}
}

func TestSortActivities_ByName(t *testing.T) {
	activities := []models.RepoActivity{
		{Name: "charlie"},
		{Name: "alpha"},
		{Name: "bravo"},
	}

	SortActivities(activities, "name")

	if activities[0].Name != "alpha" {
		t.Errorf("expected alpha first, got %q", activities[0].Name)
	}
	if activities[2].Name != "charlie" {
		t.Errorf("expected charlie last, got %q", activities[2].Name)
	}
}

func TestSortActivities_ByStatus(t *testing.T) {
	activities := []models.RepoActivity{
		{Name: "clean", State: models.StateClean},
		{Name: "conflict", State: models.StateConflict},
		{Name: "modified", State: models.StateModified},
		{Name: "staged", State: models.StateStaged},
	}

	SortActivities(activities, "status")

	expected := []string{"conflict", "staged", "modified", "clean"}
	for i, name := range expected {
		if activities[i].Name != name {
			t.Errorf("position %d: expected %q, got %q", i, name, activities[i].Name)
		}
	}
}

func TestSortActivities_ByChanges(t *testing.T) {
	activities := []models.RepoActivity{
		{Name: "few", Status: models.GitStatus{Modified: 1}},
		{Name: "many", Status: models.GitStatus{Modified: 5, Untracked: 3}},
		{Name: "some", Status: models.GitStatus{Modified: 3}},
	}

	SortActivities(activities, "changes")

	if activities[0].Name != "many" {
		t.Errorf("expected 'many' first (most changes), got %q", activities[0].Name)
	}
}

func TestGroupWorktrees(t *testing.T) {
	activities := []models.RepoActivity{
		{Name: "repo-a", Path: "/tmp/repo-a"},
		{Name: "repo-b", Path: "/tmp/repo-b"},
		{Name: "wt-a1", Path: "/tmp/wt-a1", IsWorktree: true, MainRepoPath: "/tmp/repo-a"},
		{Name: "wt-b1", Path: "/tmp/wt-b1", IsWorktree: true, MainRepoPath: "/tmp/repo-b"},
		{Name: "wt-a2", Path: "/tmp/wt-a2", IsWorktree: true, MainRepoPath: "/tmp/repo-a"},
	}

	result := GroupWorktrees(activities)

	expected := []string{"repo-a", "wt-a1", "wt-a2", "repo-b", "wt-b1"}
	if len(result) != len(expected) {
		t.Fatalf("expected %d results, got %d", len(expected), len(result))
	}

	for i, name := range expected {
		if result[i].Name != name {
			t.Errorf("position %d: expected %q, got %q", i, name, result[i].Name)
		}
	}
}

func TestGroupWorktrees_OrphanWorktrees(t *testing.T) {
	activities := []models.RepoActivity{
		{Name: "repo-a", Path: "/tmp/repo-a"},
		{Name: "orphan-wt", Path: "/tmp/orphan-wt", IsWorktree: true, MainRepoPath: "/tmp/missing-repo"},
	}

	result := GroupWorktrees(activities)

	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}

	// Main repo first, then orphan at the end
	if result[0].Name != "repo-a" {
		t.Errorf("expected repo-a first, got %q", result[0].Name)
	}
	if result[1].Name != "orphan-wt" {
		t.Errorf("expected orphan-wt second, got %q", result[1].Name)
	}
}

func TestGroupWorktrees_NoWorktrees(t *testing.T) {
	activities := []models.RepoActivity{
		{Name: "repo-a", Path: "/tmp/repo-a"},
		{Name: "repo-b", Path: "/tmp/repo-b"},
	}

	result := GroupWorktrees(activities)

	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
	if result[0].Name != "repo-a" || result[1].Name != "repo-b" {
		t.Error("expected order preserved when no worktrees")
	}
}

func TestSortActivities_ByLines(t *testing.T) {
	activities := []models.RepoActivity{
		{Name: "small", DiffStat: models.DiffStat{Insertions: 5, Deletions: 2}},
		{Name: "big", DiffStat: models.DiffStat{Insertions: 100, Deletions: 50}},
		{Name: "medium", DiffStat: models.DiffStat{Insertions: 20, Deletions: 10}},
	}

	SortActivities(activities, "lines")

	if activities[0].Name != "big" {
		t.Errorf("expected 'big' first (most lines), got %q", activities[0].Name)
	}
}
