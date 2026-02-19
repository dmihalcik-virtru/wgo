// Package links provides URL construction and terminal hyperlink formatting
// for GitHub repositories.
package links

import (
	"fmt"
	"net/url"
	"strings"
)

// parseRemoteURL extracts owner and repo from a git remote URL.
// Supports SSH (git@github.com:owner/repo.git) and HTTPS (https://github.com/owner/repo.git).
// Returns ok=false for non-GitHub remotes.
func parseRemoteURL(raw string) (owner, repo string, ok bool) {
	raw = strings.TrimSpace(raw)

	// SSH format: git@github.com:owner/repo.git
	if path, ok := strings.CutPrefix(raw, "git@github.com:"); ok {
		path = strings.TrimSuffix(path, ".git")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1], true
		}
		return "", "", false
	}

	// HTTPS format
	u, err := url.Parse(raw)
	if err != nil || u.Host != "github.com" {
		return "", "", false
	}
	path := strings.TrimPrefix(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1], true
	}
	return "", "", false
}

// RepoURL returns the GitHub URL for a repository, or "" if the remote is not GitHub.
func RepoURL(remoteURL string) string {
	owner, repo, ok := parseRemoteURL(remoteURL)
	if !ok {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/%s", owner, repo)
}

// BranchURL returns the GitHub URL for a branch.
func BranchURL(remoteURL, branch string) string {
	base := RepoURL(remoteURL)
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s/tree/%s", base, branch)
}

// CommitURL returns the GitHub URL for a commit.
func CommitURL(remoteURL, hash string) string {
	base := RepoURL(remoteURL)
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s/commit/%s", base, hash)
}

// PRURL returns the GitHub URL for a pull request.
func PRURL(remoteURL string, number int) string {
	base := RepoURL(remoteURL)
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s/pull/%d", base, number)
}

// IssueURL returns the GitHub URL for an issue.
func IssueURL(remoteURL string, number int) string {
	base := RepoURL(remoteURL)
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s/issues/%d", base, number)
}
