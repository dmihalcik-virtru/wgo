// Package contrib provides commit activity aggregation for the
// contributions heatmap. Reads commit history via internal/jj.
package contrib

import (
	"fmt"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/jj"
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

// Collect aggregates commit counts for repos in the given paths, from
// `since` to now. If author is non-empty, only commits whose author email
// matches that string (as a jj `author(exact:...)` filter) are counted.
//
// Forks are consolidated: if a repo has an "upstream" remote pointing to
// GitHub, its commits are merged into the upstream repo's activity entry.
func Collect(repoPaths []string, since time.Time, author string) ([]RepoActivity, DayCount, error) {
	jjc := jj.NewCLI()
	total := make(DayCount)

	// canonical key → merged activity
	merged := map[string]*RepoActivity{}
	// preserve insertion order for stable output
	var order []string

	for _, path := range repoPaths {
		activity, err := collectRepo(jjc, path, since, author)
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
		slug := strings.TrimPrefix(r.GitHubURL, "https://github.com/")
		slug = strings.TrimSuffix(slug, ".git")
		return slug
	}
	return r.Name
}

// buildRevset composes a jj revset for commits authored after `since`,
// optionally filtered by author email. The revset only includes commits
// reachable from the current workspace's @ (the same scope `git log` uses
// for a normal checkout).
func buildRevset(since time.Time, author string) string {
	revset := fmt.Sprintf(
		`author_date(after:%q) & ::@`,
		since.UTC().Format(time.RFC3339),
	)
	if author != "" {
		revset = fmt.Sprintf("%s & author(exact:%q)", revset, author)
	}
	return revset
}

func collectRepo(jjc jj.Client, path string, since time.Time, author string) (RepoActivity, error) {
	entries, err := jjc.Log(path, buildRevset(since, author))
	if err != nil {
		return RepoActivity{}, fmt.Errorf("jj log failed: %w", err)
	}

	counts := make(DayCount)
	for _, e := range entries {
		if e.AuthorTimestamp.IsZero() {
			continue
		}
		day := e.AuthorTimestamp.Format("2006-01-02")
		counts[day]++
	}

	total := 0
	for _, n := range counts {
		total += n
	}

	ghURL := resolveGitHubURL(jjc, path)
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
	return resolveGitHubURL(jj.NewCLI(), path)
}

func resolveGitHubURL(jjc jj.Client, path string) string {
	remotes, err := jjc.RemoteURLs(path)
	if err != nil {
		return ""
	}
	for _, name := range []string{"upstream", "origin"} {
		if u, ok := remotes[name]; ok {
			if normalized := parseGitHubHTTPS(u); normalized != "" {
				return normalized
			}
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

// CollectCommitsWithFiles returns detailed commit info (messages + files
// changed) for each repo with activity since the given time. Author filter
// matches the git form: substring of the author email.
func CollectCommitsWithFiles(repoPaths []string, since time.Time, author string) ([]RepoCommits, error) {
	jjc := jj.NewCLI()

	// canonical key → merged commits
	merged := map[string]*RepoCommits{}
	var order []string

	for _, path := range repoPaths {
		rc, err := collectRepoCommits(jjc, path, since, author)
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

func collectRepoCommits(jjc jj.Client, path string, since time.Time, author string) (RepoCommits, error) {
	// Current bookmark on this workspace (the jj-side "branch").
	branch := ""
	if ch, err := jjc.CurrentChange(path); err == nil && len(ch.Bookmarks) > 0 {
		branch = ch.Bookmarks[0]
	}

	entries, err := jjc.Log(path, buildRevset(since, author))
	if err != nil {
		return RepoCommits{}, fmt.Errorf("jj log failed: %w", err)
	}

	ghURL := resolveGitHubURL(jjc, path)
	name := repoNameFromURL(ghURL, path)

	rc := RepoCommits{
		Name:      name,
		GitHubURL: ghURL,
		Path:      path,
		Branch:    branch,
	}

	for _, e := range entries {
		short := e.CommitID
		if len(short) > 7 {
			short = short[:7]
		}
		commit := CommitDetail{
			SHA:     short,
			Message: firstLine(e.Description),
		}
		// File list per commit. Errors are non-fatal; commits with no
		// resolvable diff (e.g. merges) just get an empty Files list.
		if files, err := jjc.ChangedFiles(path, e.CommitID); err == nil {
			commit.Files = files
		}
		rc.Commits = append(rc.Commits, commit)
	}

	return rc, nil
}

// firstLine returns the first newline-delimited line of s, used to extract
// a short commit subject from a multi-line description.
func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return strings.TrimSpace(s)
}
