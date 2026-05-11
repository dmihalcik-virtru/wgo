// Package jira wraps the acli jira CLI for reading Jira issue data.
// It does not call the Jira REST API directly; all operations shell out to
// `acli jira` which manages its own credentials via `acli jira auth login`.
package jira

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Issue represents a Jira work item returned by `acli jira workitem view`.
type Issue struct {
	Key    string      `json:"key"`
	Fields IssueFields `json:"fields"`
}

// IssueFields holds the fields subset we care about.
type IssueFields struct {
	Summary  string `json:"summary"`
	Status   struct {
		Name string `json:"name"`
	} `json:"status"`
	Priority struct {
		Name string `json:"name"`
	} `json:"priority"`
	Assignee *User `json:"assignee"`
}

// Comment is a single Jira comment.
type Comment struct {
	ID      string
	Author  User
	Body    string
	Created time.Time
}

// User is a Jira account entry (assignee or watcher).
type User struct {
	AccountID    string `json:"accountId"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
}

// CheckAuth runs `acli jira auth status` and returns an error if not authenticated.
func CheckAuth() error {
	out, err := run("auth", "status")
	if err != nil {
		return fmt.Errorf("acli jira not authenticated — run: acli jira auth login\n%s", out)
	}
	return nil
}

// GetIssue fetches summary, status, priority, and assignee for the given ticket.
func GetIssue(ticket string) (*Issue, error) {
	out, err := run("workitem", "view", ticket, "--json",
		"--fields", "summary,status,priority,assignee")
	if err != nil {
		return nil, fmt.Errorf("get issue %s: %w\n%s", ticket, err, out)
	}
	var issue Issue
	if err := json.Unmarshal([]byte(out), &issue); err != nil {
		return nil, fmt.Errorf("parse issue %s: %w", ticket, err)
	}
	return &issue, nil
}

// GetComments fetches up to limit comments for the given ticket, newest last.
func GetComments(ticket string, limit int) ([]Comment, error) {
	out, err := run("workitem", "comment", "list",
		"--key", ticket, "--json",
		"--limit", fmt.Sprintf("%d", limit),
		"--order", "+created")
	if err != nil {
		return nil, fmt.Errorf("list comments %s: %w\n%s", ticket, err, out)
	}
	return parseComments(out)
}

// GetWatchers fetches the watcher list for the given ticket.
func GetWatchers(ticket string) ([]User, error) {
	out, err := run("workitem", "watcher", "list", "--key", ticket, "--json")
	if err != nil {
		return nil, fmt.Errorf("list watchers %s: %w\n%s", ticket, err, out)
	}
	return parseWatchers(out)
}

// run executes `acli jira <args>` and returns stdout. Stderr is included in
// any returned error so auth failures are surfaced clearly.
func run(args ...string) (string, error) {
	fullArgs := append([]string{"jira"}, args...)
	cmd := exec.Command("acli", fullArgs...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		combined := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
		return combined, err
	}
	return stdout.String(), nil
}

// --- JSON parsing helpers ---

// rawComment matches the JSON shape returned by `acli jira workitem comment list`.
type rawComment struct {
	ID      string          `json:"id"`
	Author  User            `json:"author"`
	Body    json.RawMessage `json:"body"`
	Created string          `json:"created"`
}

// rawCommentList covers the `{"comments":[...]}` wrapper that some acli versions use.
type rawCommentList struct {
	Comments []rawComment `json:"comments"`
}

func parseComments(raw string) ([]Comment, error) {
	var raws []rawComment
	// Try flat array first, then wrapped object.
	if err := json.Unmarshal([]byte(raw), &raws); err != nil {
		var wrapper rawCommentList
		if err2 := json.Unmarshal([]byte(raw), &wrapper); err2 != nil {
			return nil, fmt.Errorf("parse comments: %w", err)
		}
		raws = wrapper.Comments
	}
	comments := make([]Comment, 0, len(raws))
	for _, r := range raws {
		c := Comment{
			ID:     r.ID,
			Author: r.Author,
			Body:   extractText(r.Body),
		}
		if t, err := parseJiraTime(r.Created); err == nil {
			c.Created = t
		}
		comments = append(comments, c)
	}
	return comments, nil
}

// rawWatcherList covers `{"watchCount":N,"watchers":[...]}`.
type rawWatcherList struct {
	Watchers []User `json:"watchers"`
}

func parseWatchers(raw string) ([]User, error) {
	var wrapper rawWatcherList
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		return nil, fmt.Errorf("parse watchers: %w", err)
	}
	return wrapper.Watchers, nil
}

// extractText extracts plain text from a Jira ADF body or plain string body.
// ADF: {"version":1,"type":"doc","content":[...]}
// Plain string: "some text"
func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try plain string.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try ADF object.
	var node adfNode
	if err := json.Unmarshal(raw, &node); err != nil {
		return ""
	}
	var sb strings.Builder
	collectText(&node, &sb)
	return strings.TrimSpace(sb.String())
}

type adfNode struct {
	Type    string     `json:"type"`
	Text    string     `json:"text"`
	Content []adfNode  `json:"content"`
}

func collectText(n *adfNode, sb *strings.Builder) {
	if n.Text != "" {
		sb.WriteString(n.Text)
	}
	for i := range n.Content {
		collectText(&n.Content[i], sb)
		if n.Content[i].Type == "paragraph" || n.Content[i].Type == "hardBreak" {
			sb.WriteString("\n")
		}
	}
}

// parseJiraTime parses Jira's ISO-8601 timestamp with optional milliseconds.
func parseJiraTime(s string) (time.Time, error) {
	formats := []string{
		"2006-01-02T15:04:05.000-0700",
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05-0700",
		"2006-01-02T15:04:05Z",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time format: %q", s)
}

// MapJiraStatus maps a Jira status name to a wgo spec status string.
// Returns "" if the Jira status does not map to a known lifecycle state.
func MapJiraStatus(jiraStatus string) string {
	lower := strings.ToLower(jiraStatus)
	switch {
	case containsAny(lower, "done", "closed", "resolved"):
		return "shipped"
	case containsAny(lower, "in progress", "in review"):
		return "in_progress"
	case containsAny(lower, "won't do", "wont do", "won't fix", "wont fix", "abandoned"):
		return "abandoned"
	case containsAny(lower, "to do", "open", "backlog"):
		return "draft"
	default:
		return ""
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
