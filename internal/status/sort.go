package status

import (
	"slices"
	"strings"

	"github.com/virtru/wgo/pkg/models"
)

// SortActivities sorts repo activities by the given criteria.
func SortActivities(activities []models.RepoActivity, sortBy string) {
	switch strings.ToLower(sortBy) {
	case "name":
		slices.SortFunc(activities, func(a, b models.RepoActivity) int {
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		})
	case "status":
		slices.SortFunc(activities, func(a, b models.RepoActivity) int {
			pa := statePriority(a.State)
			pb := statePriority(b.State)
			if pa != pb {
				return pa - pb
			}
			return compareActivity(a, b)
		})
	case "changes":
		slices.SortFunc(activities, func(a, b models.RepoActivity) int {
			ac := a.Status.Modified + a.Status.Added + a.Status.Deleted + a.Status.Untracked
			bc := b.Status.Modified + b.Status.Added + b.Status.Deleted + b.Status.Untracked
			if bc != ac {
				return bc - ac // descending
			}
			return compareActivity(a, b)
		})
	case "commits":
		slices.SortFunc(activities, func(a, b models.RepoActivity) int {
			if b.RecentCommits != a.RecentCommits {
				return b.RecentCommits - a.RecentCommits // descending
			}
			return compareActivity(a, b)
		})
	case "lines":
		slices.SortFunc(activities, func(a, b models.RepoActivity) int {
			al := a.DiffStat.Insertions + a.DiffStat.Deletions
			bl := b.DiffStat.Insertions + b.DiffStat.Deletions
			if bl != al {
				return bl - al // descending
			}
			return compareActivity(a, b)
		})
	default: // "activity" — most recent first
		slices.SortFunc(activities, compareActivity)
	}
}

// compareActivity sorts by most recent activity first (descending).
func compareActivity(a, b models.RepoActivity) int {
	if a.LastActivity.After(b.LastActivity) {
		return -1
	}
	if b.LastActivity.After(a.LastActivity) {
		return 1
	}
	return strings.Compare(a.Name, b.Name)
}

// GroupWorktrees reorders activities so worktrees appear immediately after
// their main repo. The relative order of main repos is preserved from the
// prior sort. Orphan worktrees (main repo not in list) are appended at the end.
func GroupWorktrees(activities []models.RepoActivity) []models.RepoActivity {
	// Index worktrees by their main repo path.
	worktreesByMain := make(map[string][]models.RepoActivity)
	var mains []models.RepoActivity

	for _, a := range activities {
		if a.IsWorktree && a.MainRepoPath != "" {
			worktreesByMain[a.MainRepoPath] = append(worktreesByMain[a.MainRepoPath], a)
		} else {
			mains = append(mains, a)
		}
	}

	// Interleave: for each main repo, append its worktrees.
	result := make([]models.RepoActivity, 0, len(activities))
	claimed := make(map[string]bool)

	for _, m := range mains {
		result = append(result, m)
		if wts, ok := worktreesByMain[m.Path]; ok {
			result = append(result, wts...)
			claimed[m.Path] = true
		}
	}

	// Append orphan worktrees whose main repo was not in the list.
	for path, wts := range worktreesByMain {
		if !claimed[path] {
			result = append(result, wts...)
		}
	}

	return result
}

// statePriority returns sort priority for states (lower = more important).
func statePriority(s models.RepoState) int {
	switch s {
	case models.StateConflict:
		return 0
	case models.StateStaged:
		return 1
	case models.StateModified:
		return 2
	case models.StateClean:
		return 3
	case models.StateStale:
		return 4
	default:
		return 5
	}
}
