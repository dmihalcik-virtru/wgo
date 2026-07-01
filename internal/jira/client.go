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

// IssueStatus captures the status name and its standardized category key.
// The category key ("new", "indeterminate", "done") is consistent across all
// Jira projects and instances; the status name is project-specific.
type IssueStatus struct {
	Name           string `json:"name"`
	StatusCategory struct {
		Key string `json:"key"`
	} `json:"statusCategory"`
}

// IssueFields holds the fields subset we care about.
type IssueFields struct {
	Summary     string          `json:"summary"`
	Description json.RawMessage `json:"description"`
	Status      IssueStatus     `json:"status"`
	Priority    struct {
		Name string `json:"name"`
	} `json:"priority"`
	Assignee *User `json:"assignee"`
}

// DescriptionText extracts plain text from the description (ADF or plain string).
func (f *IssueFields) DescriptionText() string {
	return extractText(f.Description)
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

// Sprint represents a Jira agile sprint returned by `acli jira board list-sprints`.
type Sprint struct {
	ID        int
	Name      string
	Goal      string
	State     string // "future", "active", or "closed"
	StartDate time.Time
	EndDate   time.Time
}

// CheckAuth runs `acli jira auth status` and returns an error if not authenticated.
func CheckAuth() error {
	out, err := run("auth", "status")
	if err != nil {
		return fmt.Errorf("acli jira not authenticated — run: acli jira auth login\n%s", out)
	}
	return nil
}

// SiteHost returns the Jira site host (e.g. "virtru.atlassian.net") parsed from
// `acli jira auth status`. It lets callers build browse/board URLs without any
// configuration. Returns an error if not authenticated or the site cannot be found.
func SiteHost() (string, error) {
	out, err := run("auth", "status")
	if err != nil {
		return "", fmt.Errorf("acli jira not authenticated — run: acli jira auth login\n%s", out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "Site:"); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	return "", fmt.Errorf("could not determine Jira site from acli auth status")
}

// ListSprints returns the sprints for the given board id in the requested states.
// state is a comma-separated list of "future", "active", and/or "closed"; pass ""
// for the acli default (active). When paginate is true all pages are fetched —
// necessary for closed sprints, which acli returns oldest-first across many pages.
func ListSprints(boardID int, state string, paginate bool) ([]Sprint, error) {
	args := []string{"board", "list-sprints", "--id", fmt.Sprintf("%d", boardID), "--json"}
	if state != "" {
		args = append(args, "--state", state)
	}
	if paginate {
		args = append(args, "--paginate")
	}
	out, err := run(args...)
	if err != nil {
		return nil, fmt.Errorf("list sprints for board %d: %w\n%s", boardID, err, out)
	}
	return parseSprints(out)
}

// ListActiveSprints returns the active sprints for the given board id.
func ListActiveSprints(boardID int) ([]Sprint, error) {
	return ListSprints(boardID, "active", false)
}

// SearchIssues runs a JQL search and returns the matching issues. fields is the
// list of issue fields to request; acli restricts these to a fixed allowed set
// (issuetype, key, assignee, priority, status, summary). Pass nil for the default.
func SearchIssues(jql string, fields []string) ([]Issue, error) {
	args := []string{"workitem", "search", "--jql", jql, "--json"}
	if len(fields) > 0 {
		args = append(args, "--fields", strings.Join(fields, ","))
	}
	out, err := run(args...)
	if err != nil {
		return nil, fmt.Errorf("search issues: %w\n%s", err, out)
	}
	var issues []Issue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		return nil, fmt.Errorf("parse search results: %w", err)
	}
	return issues, nil
}

// InFlightJQL builds a JQL query for a single sprint's in-flight work assigned to
// the given assignee ("" means the current acli user). statuses are the workflow
// states that count as in flight (e.g. In Progress, In Review, In QA).
func InFlightJQL(sprintID int, assignee string, statuses []string) string {
	return fmt.Sprintf("sprint = %d AND %s", sprintID, inFlightClause(assignee, statuses))
}

// AllInFlightJQL builds a JQL query for all of the assignee's in-flight work,
// regardless of sprint (or lack of one).
func AllInFlightJQL(assignee string, statuses []string) string {
	return inFlightClause(assignee, statuses)
}

// inFlightClause builds the shared "assignee = X AND status in (...)" fragment.
func inFlightClause(assignee string, statuses []string) string {
	who := "currentUser()"
	if assignee != "" {
		who = quoteJQL(assignee)
	}
	quoted := make([]string, len(statuses))
	for i, s := range statuses {
		quoted[i] = quoteJQL(s)
	}
	return fmt.Sprintf("assignee = %s AND status in (%s)", who, strings.Join(quoted, ","))
}

// quoteJQL wraps a value in single quotes, escaping any embedded single quotes.
func quoteJQL(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "\\'") + "'"
}

// GetIssue fetches summary, description, status, priority, and assignee for the given ticket.
func GetIssue(ticket string) (*Issue, error) {
	out, err := run("workitem", "view", ticket, "--json",
		"--fields", "summary,description,status,priority,assignee")
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

// CreateIssue creates a new Jira work item and returns the new ticket key (e.g. "WGO-123").
func CreateIssue(project, summary, issueType, description string) (string, error) {
	args := []string{
		"workitem", "create",
		"--project", project,
		"--summary", summary,
		"--type", issueType,
		"--json",
	}
	if description != "" {
		args = append(args, "--description", description)
	}
	out, err := run(args...)
	if err != nil {
		return "", fmt.Errorf("create issue in %s: %w\n%s", project, err, out)
	}
	var result struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return "", fmt.Errorf("parse create response: %w", err)
	}
	if result.Key == "" {
		return "", fmt.Errorf("create returned no ticket key; response: %s", out)
	}
	return result.Key, nil
}

// UpdateIssue pushes a new summary and description to an existing Jira ticket.
func UpdateIssue(ticket, summary, description string) error {
	args := []string{
		"workitem", "edit",
		"--key", ticket,
		"--yes",
	}
	if summary != "" {
		args = append(args, "--summary", summary)
	}
	if description != "" {
		// Truncate at 32 KB to stay within Jira ADF limits.
		if len(description) > 32*1024 {
			description = description[:32*1024]
		}
		args = append(args, "--description", description)
	}
	out, err := run(args...)
	if err != nil {
		return fmt.Errorf("update issue %s: %w\n%s", ticket, err, out)
	}
	return nil
}

// TransitionIssue moves a Jira ticket to the given status name (e.g. "Done").
func TransitionIssue(ticket, jiraStatus string) error {
	out, err := run("workitem", "transition",
		"--key", ticket,
		"--status", jiraStatus,
		"--yes")
	if err != nil {
		return fmt.Errorf("transition %s to %q: %w\n%s", ticket, jiraStatus, err, out)
	}
	return nil
}

// MapSpecStatus maps a wgo spec status to the Jira status name used for transitions.
// Only terminal states are mapped; "abandoned" is intentionally omitted because
// there is no standardized "won't do" status in Jira — it varies by project.
// Returns "" when no transition should be attempted.
func MapSpecStatus(specStatus string) string {
	switch specStatus {
	case "shipped":
		return "Done"
	case "in_progress":
		return "In Progress"
	case "draft":
		return "To Do"
	default:
		return ""
	}
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

// rawSprintList matches `{"sprints":[...]}` from `acli jira board list-sprints`.
type rawSprintList struct {
	Sprints []rawSprint `json:"sprints"`
}

// rawSprint matches a single sprint entry with string dates.
type rawSprint struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Goal      string `json:"goal"`
	State     string `json:"state"`
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
}

// parseSprints decodes one or more `{"sprints":[...]}` documents. `acli --paginate`
// emits one JSON object per page (concatenated, not a single array), so we stream
// the input and accumulate across every document.
func parseSprints(raw string) ([]Sprint, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	var sprints []Sprint
	for dec.More() {
		var wrapper rawSprintList
		if err := dec.Decode(&wrapper); err != nil {
			return nil, fmt.Errorf("parse sprints: %w", err)
		}
		for _, r := range wrapper.Sprints {
			s := Sprint{ID: r.ID, Name: r.Name, Goal: r.Goal, State: r.State}
			if t, err := parseJiraTime(r.StartDate); err == nil {
				s.StartDate = t
			}
			if t, err := parseJiraTime(r.EndDate); err == nil {
				s.EndDate = t
			}
			sprints = append(sprints, s)
		}
	}
	return sprints, nil
}

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
	Type    string    `json:"type"`
	Text    string    `json:"text"`
	Content []adfNode `json:"content"`
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

// MapJiraStatus maps a Jira status to a wgo spec lifecycle state.
// It uses the statusCategory.key first — that value is standardized across all
// Jira instances ("new" / "indeterminate" / "done") — and falls back to
// substring-matching the status name only when the category key is absent.
// Returns "" if no mapping can be determined.
func MapJiraStatus(s IssueStatus) string {
	switch s.StatusCategory.Key {
	case "done":
		return "shipped"
	case "indeterminate":
		return "in_progress"
	case "new":
		return "draft"
	}
	// Category key missing: fall back to name-based heuristics.
	lower := strings.ToLower(s.Name)
	switch {
	case containsAny(lower, "done", "closed", "resolved", "released"):
		return "shipped"
	case containsAny(lower, "in progress", "in review"):
		return "in_progress"
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
