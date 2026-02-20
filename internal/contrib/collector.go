// Package contrib provides git activity aggregation for the contributions heatmap.
package contrib

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DayCount maps a date string (YYYY-MM-DD) to a commit count.
type DayCount map[string]int

// RepoActivity holds commit counts per day for a single repo.
type RepoActivity struct {
	Name   string
	Counts DayCount
	Total  int
}

// Collect aggregates git log commit counts for repos in the given paths,
// from `since` to now.
func Collect(repoPaths []string, since time.Time) ([]RepoActivity, DayCount, error) {
	total := make(DayCount)
	var activities []RepoActivity

	sinceStr := since.Format("2006-01-02")

	for _, path := range repoPaths {
		activity, err := collectRepo(path, sinceStr)
		if err != nil {
			continue // skip repos that fail
		}
		activities = append(activities, activity)
		for day, n := range activity.Counts {
			total[day] += n
		}
	}

	return activities, total, nil
}

func collectRepo(path, since string) (RepoActivity, error) {
	cmd := exec.Command("git", "-C", path, "log",
		"--oneline",
		"--format=%cd",
		"--date=format:%Y-%m-%d",
		"--since="+since,
	)
	out, err := cmd.Output()
	if err != nil {
		return RepoActivity{}, fmt.Errorf("git log failed: %w", err)
	}

	counts := make(DayCount)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		counts[line]++
	}

	name := repoName(path)
	total := 0
	for _, n := range counts {
		total += n
	}

	return RepoActivity{
		Name:   name,
		Counts: counts,
		Total:  total,
	}, nil
}

func repoName(path string) string {
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}
