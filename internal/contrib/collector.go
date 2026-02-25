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

// ResolveGitHubURL returns the canonical GitHub HTTPS URL for a repo.
// It prefers the "upstream" remote (fork parent) over "origin".
func ResolveGitHubURL(path string) string {
	return resolveGitHubURL(path)
}

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

// CommitDetail represents a single commit with its changed files.
type CommitDetail struct {
	SHA     string
	Message string
	Files   []string
}

// RepoCommits holds commits for a single repo.
type RepoCommits struct {
	Name      string
	GitHubURL string
	Path      string
	Branch    string
	Commits   []CommitDetail
}

// CollectCommitsWithFiles returns detailed commit info (messages + files changed)
// for each repo with activity since the given time.
func CollectCommitsWithFiles(repoPaths []string, since time.Time, author string) ([]RepoCommits, error) {
	sinceStr := since.Format(time.RFC3339)

	// canonical key → merged commits
	merged := map[string]*RepoCommits{}
	var order []string

	for _, path := range repoPaths {
		rc, err := collectRepoCommits(path, sinceStr, author)
		if err != nil || len(rc.Commits) == 0 {
			continue
		}

		key := rc.canonicalKey()
		if existing, ok := merged[key]; ok {
			// Deduplicate by SHA
			seen := map[string]bool{}
			for _, c := range existing.Commits {
				seen[c.SHA] = true
			}
			for _, c := range rc.Commits {
				if !seen[c.SHA] {
					existing.Commits = append(existing.Commits, c)
				}
			}
		} else {
			merged[key] = &rc
			order = append(order, key)
		}
	}

	result := make([]RepoCommits, 0, len(order))
	for _, key := range order {
		result = append(result, *merged[key])
	}
	return result, nil
}

func (r *RepoCommits) canonicalKey() string {
	if r.GitHubURL != "" {
		slug := strings.TrimPrefix(r.GitHubURL, "https://github.com/")
		slug = strings.TrimSuffix(slug, ".git")
		return slug
	}
	return r.Name
}

func collectRepoCommits(path, since, author string) (RepoCommits, error) {
	// Get current branch
	branchOut, _ := exec.Command("git", "-C", path, "branch", "--show-current").Output()
	branch := strings.TrimSpace(string(branchOut))

	// Get commits with files: format is SHA<tab>message, followed by file names
	// Use %x09 for a literal tab character (git --format doesn't interpret \t)
	args := []string{"-C", path, "log",
		"--format=%h%x09%s",
		"--name-only",
		"--since=" + since,
	}
	if author != "" {
		args = append(args, "--author="+author)
	}
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		return RepoCommits{}, fmt.Errorf("git log failed: %w", err)
	}

	ghURL := resolveGitHubURL(path)
	name := repoNameFromURL(ghURL, path)

	rc := RepoCommits{
		Name:      name,
		GitHubURL: ghURL,
		Path:      path,
		Branch:    branch,
	}

	// Parse the output: commit lines have a tab, file lines don't
	var current *CommitDetail
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, "\t") {
			// This is a commit line: SHA<tab>message
			parts := strings.SplitN(line, "\t", 2)
			commit := CommitDetail{SHA: parts[0]}
			if len(parts) > 1 {
				commit.Message = parts[1]
			}
			rc.Commits = append(rc.Commits, commit)
			current = &rc.Commits[len(rc.Commits)-1]
		} else if current != nil {
			// This is a filename
			current.Files = append(current.Files, line)
		}
	}

	return rc, nil
}
