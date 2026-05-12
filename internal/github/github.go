// Package github provides GitHub URL parsing and gh CLI integration.
package github

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// URLType represents the type of GitHub URL.
type URLType int

const (
	URLTypePR     URLType = iota // Pull request URL
	URLTypeBranch                // Branch/tree URL
	URLTypeIssue                 // Issue URL
)

// ParsedURL contains the parsed components of a GitHub URL.
type ParsedURL struct {
	Owner      string
	Repo       string
	Type       URLType
	Identifier string // PR number, branch name, or issue number
}

// ParseGitHubURL parses a GitHub URL into its components.
func ParseGitHubURL(rawURL string) (*ParsedURL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	if u.Host != "github.com" && u.Host != "www.github.com" {
		return nil, fmt.Errorf("not a GitHub URL: %s", u.Host)
	}

	// Split path: /owner/repo/type/identifier...
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return nil, fmt.Errorf("URL must include owner and repo: %s", rawURL)
	}

	owner := parts[0]
	repo := strings.TrimSuffix(parts[1], ".git")

	// If only owner/repo, treat as branch URL for default branch
	if len(parts) == 2 {
		return &ParsedURL{
			Owner:      owner,
			Repo:       repo,
			Type:       URLTypeBranch,
			Identifier: "", // empty means default branch
		}, nil
	}

	switch parts[2] {
	case "pull":
		if len(parts) < 4 {
			return nil, fmt.Errorf("PR URL missing number: %s", rawURL)
		}
		// Validate it's a number
		if _, err := strconv.Atoi(parts[3]); err != nil {
			return nil, fmt.Errorf("invalid PR number: %s", parts[3])
		}
		return &ParsedURL{
			Owner:      owner,
			Repo:       repo,
			Type:       URLTypePR,
			Identifier: parts[3],
		}, nil

	case "tree":
		if len(parts) < 4 {
			return nil, fmt.Errorf("branch URL missing branch name: %s", rawURL)
		}
		// Rejoin remaining segments to support branch names with /
		branch := strings.Join(parts[3:], "/")
		return &ParsedURL{
			Owner:      owner,
			Repo:       repo,
			Type:       URLTypeBranch,
			Identifier: branch,
		}, nil

	case "issues":
		if len(parts) < 4 {
			return nil, fmt.Errorf("issue URL missing number: %s", rawURL)
		}
		if _, err := strconv.Atoi(parts[3]); err != nil {
			return nil, fmt.Errorf("invalid issue number: %s", parts[3])
		}
		return &ParsedURL{
			Owner:      owner,
			Repo:       repo,
			Type:       URLTypeIssue,
			Identifier: parts[3],
		}, nil

	default:
		return nil, fmt.Errorf("unsupported GitHub URL type: %s", parts[2])
	}
}

// PRBranch returns the head branch name for a pull request.
func PRBranch(owner, repo string, number int) (string, error) {
	out, err := exec.Command("gh", "pr", "view",
		strconv.Itoa(number),
		"--repo", owner+"/"+repo,
		"--json", "headRefName",
		"-q", ".headRefName",
	).Output()
	if err != nil {
		return "", fmt.Errorf("gh pr view failed: %w (is gh installed and authenticated?)", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "", fmt.Errorf("gh returned empty branch for PR #%d", number)
	}
	return branch, nil
}

// IssueTitle returns the title of a GitHub issue.
func IssueTitle(owner, repo string, number int) (string, error) {
	out, err := exec.Command("gh", "issue", "view",
		strconv.Itoa(number),
		"--repo", owner+"/"+repo,
		"--json", "title",
		"-q", ".title",
	).Output()
	if err != nil {
		return "", fmt.Errorf("gh issue view failed: %w (is gh installed and authenticated?)", err)
	}
	title := strings.TrimSpace(string(out))
	if title == "" {
		return "", fmt.Errorf("gh returned empty title for issue #%d", number)
	}
	return title, nil
}

// IssueBranchName creates a branch name from an issue number and title.
func IssueBranchName(number int, title string) string {
	slug := slugify(title)
	return fmt.Sprintf("%d-%s", number, slug)
}

// SanitizeBranch converts a branch name into a filesystem-safe path component.
func SanitizeBranch(branch string) string {
	s := branch
	// Replace / with -
	s = strings.ReplaceAll(s, "/", "-")
	// Strip unsafe characters (keep alphanumeric, dash, underscore, dot)
	re := regexp.MustCompile(`[^a-zA-Z0-9\-_.]`)
	s = re.ReplaceAllString(s, "-")
	// Collapse multiple dashes
	re = regexp.MustCompile(`-{2,}`)
	s = re.ReplaceAllString(s, "-")
	// Trim leading/trailing dashes
	s = strings.Trim(s, "-")
	// Truncate to ~60 chars
	if len(s) > 60 {
		s = s[:60]
		s = strings.TrimRight(s, "-")
	}
	return s
}

// slugify converts a title string into a URL/branch-safe slug.
func slugify(s string) string {
	s = strings.ToLower(s)
	re := regexp.MustCompile(`[^a-z0-9]+`)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
		s = strings.TrimRight(s, "-")
	}
	return s
}

// RepoCloneURL returns the HTTPS clone URL for a GitHub repo.
func RepoCloneURL(owner, repo string) string {
	return fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
}

// PRMergeCommit holds the OID of the commit that landed the PR on the target branch.
type PRMergeCommit struct {
	OID string `json:"oid"`
}

// PRInfo contains information about a pull request.
type PRInfo struct {
	Number      int            `json:"number"`
	State       string         `json:"state"`
	Branch      string         `json:"headRefName"`
	HeadSHA     string         `json:"headRefOid"`
	MergeCommit *PRMergeCommit `json:"mergeCommit"`
	MergedAt    *time.Time     `json:"mergedAt"`
	ClosedAt    *time.Time     `json:"closedAt"`
	URL         string         `json:"url"`
	Title       string         `json:"title"`
}

// IsMerged reports whether the PR was merged.
func (p *PRInfo) IsMerged() bool {
	return strings.EqualFold(p.State, "merged") || p.MergedAt != nil
}

// IsClosed reports whether the PR was closed without merging.
func (p *PRInfo) IsClosed() bool {
	return strings.EqualFold(p.State, "closed") && !p.IsMerged()
}

// Client is the interface for GitHub operations.
type Client interface {
	GetPRStatus(repoPath, branch string) (*PRInfo, error)
	ListPRsForBranch(repoPath, branch string) ([]PRInfo, error)
	ClosePR(repoPath string, prNumber int) error
	DeleteRemoteBranch(repoPath, branch string) error
	Available() bool
}

// CLIClient is a GitHub Client implementation using the gh CLI.
type CLIClient struct{}

// NewClient creates a new CLIClient.
func NewClient() *CLIClient {
	return &CLIClient{}
}

// Available returns true if gh is on PATH.
func (c *CLIClient) Available() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

// GetPRStatus fetches PR status for a branch. Returns nil PRInfo if gh unavailable
// or no PR exists.
func (c *CLIClient) GetPRStatus(repoPath, branch string) (*PRInfo, error) {
	if !c.Available() {
		return nil, nil
	}

	cmd := exec.Command("gh", "pr", "view", branch,
		"--json", "number,state,headRefName,headRefOid,mergeCommit,mergedAt,closedAt,url,title")
	cmd.Dir = repoPath

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errStr := stderr.String()
		if strings.Contains(errStr, "no pull requests found") ||
			strings.Contains(errStr, "Could not find") ||
			strings.Contains(errStr, "no open pull request") {
			return nil, nil
		}
		return nil, nil // graceful degradation
	}

	var pr PRInfo
	if err := json.Unmarshal([]byte(stdout.String()), &pr); err != nil {
		return nil, fmt.Errorf("failed to parse pr info: %w", err)
	}
	return &pr, nil
}

// ClosePR closes a pull request.
func (c *CLIClient) ClosePR(repoPath string, prNumber int) error {
	if !c.Available() {
		return fmt.Errorf("gh CLI not available")
	}
	cmd := exec.Command("gh", "pr", "close", fmt.Sprintf("%d", prNumber))
	cmd.Dir = repoPath
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh pr close: %s", stderr.String())
	}
	return nil
}

// DeleteRemoteBranch deletes a remote branch via gh API.
func (c *CLIClient) DeleteRemoteBranch(repoPath, branch string) error {
	if !c.Available() {
		return fmt.Errorf("gh CLI not available")
	}
	slug, err := c.repoSlug(repoPath)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("repos/%s/git/refs/heads/%s", slug, branch)
	cmd := exec.Command("gh", "api", "-X", "DELETE", endpoint)
	cmd.Dir = repoPath
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errMsg := stderr.String()
		if strings.Contains(errMsg, "Reference does not exist") {
			return nil // already deleted, treat as success
		}
		return fmt.Errorf("gh api DELETE branch: %s", errMsg)
	}
	return nil
}

// ListPRsForBranch returns all PRs (any state) whose head branch matches.
func (c *CLIClient) ListPRsForBranch(repoPath, branch string) ([]PRInfo, error) {
	if !c.Available() {
		return nil, nil
	}

	cmd := exec.Command("gh", "pr", "list",
		"--head", branch,
		"--state", "all",
		"--json", "number,state,headRefName,headRefOid,mergeCommit,mergedAt,closedAt,url,title",
		"--limit", "5")
	cmd.Dir = repoPath

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, nil // graceful degradation
	}

	var prs []PRInfo
	if err := json.Unmarshal([]byte(stdout.String()), &prs); err != nil {
		return nil, nil
	}
	return prs, nil
}

func (c *CLIClient) repoSlug(repoPath string) (string, error) {
	cmd := exec.Command("gh", "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh repo view: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
