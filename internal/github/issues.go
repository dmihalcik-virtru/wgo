package github

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// IssueInfo is the subset of a GitHub issue the dashboard caches for `gh-N`
// tickets.
type IssueInfo struct {
	Number int
	Title  string
	// State is "open" or "closed".
	State string
	// StateReason is GitHub's reason for the state (completed, not_planned,
	// reopened), or "".
	StateReason string
	URL         string
	UpdatedAt   time.Time
	// IsPR is true when the number names a pull request; GitHub serves PRs
	// from the issues endpoint too.
	IsPR bool
}

type apiIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	StateReason string    `json:"state_reason"`
	HTMLURL     string    `json:"html_url"`
	UpdatedAt   time.Time `json:"updated_at"`
	PullRequest *struct{} `json:"pull_request"`
}

// IssueDetails includes the body used to scaffold a spec.
type IssueDetails struct {
	IssueInfo
	Body string
}

// GetIssueDetails fetches a work item, rejecting PRs served by this endpoint.
func (c *CLIClient) GetIssueDetails(owner, repo string, number int) (IssueDetails, error) {
	if !c.Available() {
		return IssueDetails{}, ErrNoAuth
	}
	var is apiIssue
	if err := c.getJSON(fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number), &is); err != nil {
		return IssueDetails{}, err
	}
	if is.PullRequest != nil {
		return IssueDetails{}, fmt.Errorf("%s/%s#%d is a pull request, not an issue", owner, repo, number)
	}
	return IssueDetails{IssueInfo: issueInfo(is), Body: is.Body}, nil
}

// CreateIssue opens an issue and returns its metadata and canonical URL.
func (c *CLIClient) CreateIssue(owner, repo, title string, labels []string, milestone string) (IssueDetails, error) {
	if !c.Available() {
		return IssueDetails{}, ErrNoAuth
	}
	if strings.TrimSpace(title) == "" {
		return IssueDetails{}, fmt.Errorf("github issue title cannot be empty")
	}
	payload := map[string]any{"title": title, "body": "Spec and local workspace initialized by wgo"}
	if len(labels) > 0 {
		payload["labels"] = labels
	}
	if milestone != "" {
		n, err := c.resolveMilestone(owner, repo, milestone)
		if err != nil {
			return IssueDetails{}, err
		}
		payload["milestone"] = n
	}
	var is apiIssue
	if err := c.bodyJSON(http.MethodPost, fmt.Sprintf("/repos/%s/%s/issues", owner, repo), payload, &is); err != nil {
		return IssueDetails{}, fmt.Errorf("create GitHub issue: %w", err)
	}
	return IssueDetails{IssueInfo: issueInfo(is), Body: is.Body}, nil
}

func (c *CLIClient) resolveMilestone(owner, repo, milestone string) (int, error) {
	if n, err := strconv.Atoi(milestone); err == nil && n > 0 {
		return n, nil
	}
	for page := 1; page <= 10; page++ {
		var items []struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
		}
		endpoint := fmt.Sprintf("/repos/%s/%s/milestones?state=all&per_page=100&page=%d", owner, repo, page)
		if err := c.getJSON(endpoint, &items); err != nil {
			return 0, fmt.Errorf("find GitHub milestone %q: %w", milestone, err)
		}
		for _, item := range items {
			if item.Title == milestone {
				return item.Number, nil
			}
		}
		if len(items) < 100 {
			break
		}
	}
	return 0, fmt.Errorf("GitHub milestone %q not found in %s/%s", milestone, owner, repo)
}

func issueInfo(is apiIssue) IssueInfo {
	return IssueInfo{
		Number: is.Number, Title: is.Title, State: is.State,
		StateReason: is.StateReason, URL: is.HTMLURL, UpdatedAt: is.UpdatedAt,
		IsPR: is.PullRequest != nil,
	}
}

// GetIssue fetches one issue by owner, repo and number. It returns ErrNoAuth
// without credentials so a caller caching the result never records a missing
// token as a real answer.
func (c *CLIClient) GetIssue(owner, repo string, number int) (IssueInfo, error) {
	return c.GetIssueContext(context.Background(), owner, repo, number)
}

// GetIssueContext is GetIssue, abandoned when ctx is done.
func (c *CLIClient) GetIssueContext(ctx context.Context, owner, repo string, number int) (IssueInfo, error) {
	if !c.Available() {
		return IssueInfo{}, ErrNoAuth
	}
	var is apiIssue
	if err := c.getJSONContext(ctx, fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number), &is); err != nil {
		return IssueInfo{}, err
	}
	return IssueInfo{
		Number:      is.Number,
		Title:       is.Title,
		State:       is.State,
		StateReason: is.StateReason,
		URL:         is.HTMLURL,
		UpdatedAt:   is.UpdatedAt,
		IsPR:        is.PullRequest != nil,
	}, nil
}
