package github

import (
	"fmt"
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
	State       string    `json:"state"`
	StateReason string    `json:"state_reason"`
	HTMLURL     string    `json:"html_url"`
	UpdatedAt   time.Time `json:"updated_at"`
	PullRequest *struct{} `json:"pull_request"`
}

// GetIssue fetches one issue by owner, repo and number. It returns ErrNoAuth
// without credentials so a caller caching the result never records a missing
// token as a real answer.
func (c *CLIClient) GetIssue(owner, repo string, number int) (IssueInfo, error) {
	if !c.Available() {
		return IssueInfo{}, ErrNoAuth
	}
	var is apiIssue
	if err := c.getJSON(fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number), &is); err != nil {
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
