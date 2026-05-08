package status

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/models"
)

func TestSortActivities_ByActivity(t *testing.T) {
	now := time.Now()
	activities := []models.RepoActivity{
		{Name: "old", LastActivity: now.Add(-2 * time.Hour)},
		{Name: "newest", LastActivity: now.Add(-5 * time.Minute)},
		{Name: "middle", LastActivity: now.Add(-1 * time.Hour)},
	}

	SortActivities(activities, "activity")

	assert.Equal(t, "newest", activities[0].Name, "expected newest first")
	assert.Equal(t, "old", activities[2].Name, "expected old last")
}

func TestSortActivities_ByName(t *testing.T) {
	activities := []models.RepoActivity{
		{Name: "charlie"},
		{Name: "alpha"},
		{Name: "bravo"},
	}

	SortActivities(activities, "name")

	assert.Equal(t, "alpha", activities[0].Name, "expected alpha first")
	assert.Equal(t, "charlie", activities[2].Name, "expected charlie last")
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
		assert.Equal(t, name, activities[i].Name, "position %d", i)
	}
}

func TestSortActivities_ByChanges(t *testing.T) {
	activities := []models.RepoActivity{
		{Name: "few", Status: models.GitStatus{Modified: 1}},
		{Name: "many", Status: models.GitStatus{Modified: 5, Untracked: 3}},
		{Name: "some", Status: models.GitStatus{Modified: 3}},
	}

	SortActivities(activities, "changes")

	assert.Equal(t, "many", activities[0].Name, "expected 'many' first (most changes)")
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
	require.Len(t, result, len(expected))

	for i, name := range expected {
		assert.Equal(t, name, result[i].Name, "position %d", i)
	}
}

func TestGroupWorktrees_OrphanWorktrees(t *testing.T) {
	activities := []models.RepoActivity{
		{Name: "repo-a", Path: "/tmp/repo-a"},
		{Name: "orphan-wt", Path: "/tmp/orphan-wt", IsWorktree: true, MainRepoPath: "/tmp/missing-repo"},
	}

	result := GroupWorktrees(activities)

	require.Len(t, result, 2)
	assert.Equal(t, "repo-a", result[0].Name, "expected repo-a first")
	assert.Equal(t, "orphan-wt", result[1].Name, "expected orphan-wt second")
}

func TestGroupWorktrees_NoWorktrees(t *testing.T) {
	activities := []models.RepoActivity{
		{Name: "repo-a", Path: "/tmp/repo-a"},
		{Name: "repo-b", Path: "/tmp/repo-b"},
	}

	result := GroupWorktrees(activities)

	require.Len(t, result, 2)
	assert.Equal(t, "repo-a", result[0].Name)
	assert.Equal(t, "repo-b", result[1].Name)
}

func TestSortActivities_ByLines(t *testing.T) {
	activities := []models.RepoActivity{
		{Name: "small", DiffStat: models.DiffStat{Insertions: 5, Deletions: 2}},
		{Name: "big", DiffStat: models.DiffStat{Insertions: 100, Deletions: 50}},
		{Name: "medium", DiffStat: models.DiffStat{Insertions: 20, Deletions: 10}},
	}

	SortActivities(activities, "lines")

	assert.Equal(t, "big", activities[0].Name, "expected 'big' first (most lines)")
}
