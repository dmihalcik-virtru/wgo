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
	Name      string
	GitHubURL string // https://github.com/owner/repo, empty if not a GitHub repo
	Counts    DayCount
	Total     int
}

// Collect aggregates git log commit counts for repos in the given paths,
// from `since` to now. If author is non-empty, only commits by that author
// (matched against email or name via git log --author) are counted.
//
// Forks are consolidated: if a repo has an "upstream" remote pointing to GitHub,
// its commits are merged into the upstream repo's activity entry.
func Collect(repoPaths []string, since time.Time, author string) ([]RepoActivity, DayCount, error) {
	total := make(DayCount)

	// canonical key → merged activity
	merged := map[string]*RepoActivity{}
	// preserve insertion order for stable output
	var order []string

	sinceStr := since.Format("2006-01-02")

	for _, path := range repoPaths {
		activity, err := collectRepo(path, sinceStr, author)
		if err != nil {
			continue // skip repos that fail
		}
		if activity.Total == 0 {
			continue // omit repos with no activity in range
		}

		key := activity.canonicalKey()
		if existing, ok := merged[key]; ok {
			// merge fork commits into the existing entry
			for day, n := range activity.Counts {
				existing.Counts[day] += n
				existing.Total += n
				total[day] += n
			}
		} else {
			merged[key] = &activity
			order = append(order, key)
			for day, n := range activity.Counts {
				total[day] += n
			}
		}
	}

	activities := make([]RepoActivity, 0, len(order))
	for _, key := range order {
		activities = append(activities, *merged[key])
	}

	return activities, total, nil
}

// canonicalKey returns a deduplication key: the GitHub "owner/repo" slug if
// available, otherwise the local repo name.
func (r *RepoActivity) canonicalKey() string {
	if r.GitHubURL != "" {
		// strip https://github.com/ prefix
		slug := strings.TrimPrefix(r.GitHubURL, "https://github.com/")
		slug = strings.TrimSuffix(slug, ".git")
		return slug
	}
	return r.Name
}

func collectRepo(path, since, author string) (RepoActivity, error) {
	args := []string{"-C", path, "log",
		"--oneline",
		"--format=%cd",
		"--date=format:%Y-%m-%d",
		"--since=" + since,
	}
	if author != "" {
		args = append(args, "--author="+author)
	}
	cmd := exec.Command("git", args...)
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

	total := 0
	for _, n := range counts {
		total += n
	}

	ghURL := resolveGitHubURL(path)
	name := repoNameFromURL(ghURL, path)

	return RepoActivity{
		Name:      name,
		GitHubURL: ghURL,
		Counts:    counts,
		Total:     total,
	}, nil
}

// resolveGitHubURL returns the canonical GitHub HTTPS URL for a repo.
// It prefers the "upstream" remote (fork parent) over "origin".
func resolveGitHubURL(path string) string {
	for _, remote := range []string{"upstream", "origin"} {
		out, err := exec.Command("git", "-C", path, "remote", "get-url", remote).Output()
		if err != nil {
			continue
		}
		if u := parseGitHubHTTPS(strings.TrimSpace(string(out))); u != "" {
			return u
		}
	}
	return ""
}

// parseGitHubHTTPS normalises a git remote URL (SSH or HTTPS) into a canonical
// https://github.com/owner/repo URL, or returns "" if it's not a GitHub remote.
func parseGitHubHTTPS(remote string) string {
	// SSH form: git@github.com:owner/repo.git
	if strings.HasPrefix(remote, "git@github.com:") {
		slug := strings.TrimPrefix(remote, "git@github.com:")
		slug = strings.TrimSuffix(slug, ".git")
		return "https://github.com/" + slug
	}
	// HTTPS form: https://github.com/owner/repo[.git]
	if strings.Contains(remote, "github.com/") {
		idx := strings.Index(remote, "github.com/")
		slug := remote[idx+len("github.com/"):]
		slug = strings.TrimSuffix(slug, ".git")
		// ensure at least owner/repo
		if strings.Contains(slug, "/") {
			return "https://github.com/" + slug
		}
	}
	return ""
}

// repoNameFromURL extracts a display name from the GitHub URL (owner/repo) or
// falls back to the local directory name.
func repoNameFromURL(ghURL, path string) string {
	if ghURL != "" {
		slug := strings.TrimPrefix(ghURL, "https://github.com/")
		return slug
	}
	return repoName(path)
}

func repoName(path string) string {
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}
