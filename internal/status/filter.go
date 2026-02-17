package status

import (
	"strings"
	"time"

	"github.com/virtru/wgo/models"
)

// FilterActivities filters repo activities by state and/or time window.
func FilterActivities(activities []models.RepoActivity, filter string, since time.Time) []models.RepoActivity {
	if filter == "" && since.IsZero() {
		return activities
	}

	var result []models.RepoActivity
	for _, a := range activities {
		if !matchesFilter(a, filter) {
			continue
		}
		if !since.IsZero() && !matchesSince(a, since) {
			continue
		}
		result = append(result, a)
	}
	return result
}

// matchesFilter checks if a repo activity matches the given state filter.
func matchesFilter(a models.RepoActivity, filter string) bool {
	if filter == "" {
		return true
	}

	switch strings.ToLower(filter) {
	case "modified":
		return a.State == models.StateModified
	case "clean":
		return a.State == models.StateClean
	case "stale":
		return a.State == models.StateStale
	case "staged":
		return a.State == models.StateStaged
	case "conflict":
		return a.State == models.StateConflict
	case "dirty":
		// dirty = anything not clean
		return a.State != models.StateClean && a.State != models.StateStale
	default:
		return true
	}
}

// matchesSince checks if a repo has any activity since the given time.
func matchesSince(a models.RepoActivity, since time.Time) bool {
	// Has recent commits
	if a.RecentCommits > 0 {
		return true
	}
	// Last activity is after since
	if !a.LastActivity.IsZero() && a.LastActivity.After(since) {
		return true
	}
	// Last commit is after since
	if !a.LastCommit.Date.IsZero() && a.LastCommit.Date.After(since) {
		return true
	}
	// Has uncommitted changes (always relevant)
	if a.State == models.StateModified || a.State == models.StateStaged || a.State == models.StateConflict {
		return true
	}
	return false
}
