package status

import (
	"testing"
	"time"

	"github.com/virtru/wgo/pkg/models"
)

func TestFilterActivities(t *testing.T) {
	now := time.Now()
	activities := []models.RepoActivity{
		{Name: "clean-repo", State: models.StateClean, LastActivity: now.Add(-1 * time.Hour)},
		{Name: "dirty-repo", State: models.StateModified, LastActivity: now.Add(-30 * time.Minute)},
		{Name: "staged-repo", State: models.StateStaged, LastActivity: now.Add(-2 * time.Hour)},
		{Name: "stale-repo", State: models.StateStale, LastActivity: now.Add(-30 * 24 * time.Hour)},
		{Name: "conflict-repo", State: models.StateConflict, LastActivity: now.Add(-10 * time.Minute)},
	}

	tests := []struct {
		filter string
		want   int
		desc   string
	}{
		{"", 5, "no filter returns all"},
		{"modified", 1, "modified filter"},
		{"clean", 1, "clean filter"},
		{"stale", 1, "stale filter"},
		{"staged", 1, "staged filter"},
		{"conflict", 1, "conflict filter"},
		{"dirty", 3, "dirty = modified + staged + conflict"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			result := FilterActivities(activities, tt.filter, time.Time{})
			if len(result) != tt.want {
				t.Errorf("filter %q: got %d results, want %d", tt.filter, len(result), tt.want)
			}
		})
	}
}

func TestFilterActivities_Since(t *testing.T) {
	now := time.Now()

	activities := []models.RepoActivity{
		{
			Name:         "recent",
			State:        models.StateClean,
			LastActivity: now.Add(-30 * time.Minute),
			LastCommit:   models.CommitInfo{Date: now.Add(-30 * time.Minute)},
		},
		{
			Name:         "old",
			State:        models.StateClean,
			LastActivity: now.Add(-48 * time.Hour),
			LastCommit:   models.CommitInfo{Date: now.Add(-48 * time.Hour)},
		},
	}

	since := now.Add(-1 * time.Hour)
	result := FilterActivities(activities, "", since)

	if len(result) != 1 {
		t.Fatalf("expected 1 result with since filter, got %d", len(result))
	}
	if result[0].Name != "recent" {
		t.Errorf("expected 'recent' repo, got %q", result[0].Name)
	}
}
