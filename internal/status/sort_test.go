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
