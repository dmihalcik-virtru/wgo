// Package jira wraps the twg CLI for Jira work items.
package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/config"
)

// Issue represents a Jira work item. Fields preserves the existing standup
// and spec command interface while twg returns the fields at top level.
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

// Sprint represents a Jira agile sprint.
type Sprint struct {
	ID        int
	Name      string
	Goal      string
	State     string // "future", "active", or "closed"
	StartDate time.Time
	EndDate   time.Time
}

// CheckAuth verifies the configured twg identity.
func CheckAuth() error {
	return CheckAuthContext(context.Background())
}

// CheckAuthContext is CheckAuth, stopped when ctx is done.
func CheckAuthContext(ctx context.Context) error {
	return LoginStatus(ctx)
}

// ErrNotLoggedIn says twg's Jira login is absent or expired.
var ErrNotLoggedIn = errors.New("twg is not authenticated: run `twg login` or `twg doctor`")

// IsAuthError recognizes structured twg error codes and authentication
// messages without treating an ordinary missing work item as an auth error.
func IsAuthError(msg string) bool {
	s := strings.ToLower(msg)
	return strings.Contains(s, "unauthorized") || strings.Contains(s, "unauthenticated") ||
		strings.Contains(s, "not authenticated") || strings.Contains(s, "authentication required") ||
		strings.Contains(s, "auth_required") || strings.Contains(s, "auth_expired") ||
		strings.Contains(s, "not logged in") || strings.Contains(s, "login required")
}

// LoginStatus runs twg whoami, stopped when ctx is done.
func LoginStatus(ctx context.Context) error {
	data, err := runContext(ctx, "whoami")
	if err != nil {
		return err
	}
	var user struct {
		AccountID string `json:"accountId"`
		Email     string `json:"email"`
	}
	if err := json.Unmarshal(data, &user); err != nil {
		return fmt.Errorf("parse twg whoami: %w", err)
	}
	if user.AccountID == "" && user.Email == "" {
		return ErrNotLoggedIn
	}
	return nil
}

// SiteHost returns the configured site or twg's locally saved default site.
func SiteHost() (string, error) {
	return SiteHostContext(context.Background())
}

// SiteHostContext is SiteHost, stopped when ctx is done.
func SiteHostContext(ctx context.Context) (string, error) {
	if cfg := config.Get(); cfg != nil && cfg.Jira.Site != "" {
		return cfg.Jira.Site, nil
	}
	data, err := runContext(ctx, "auth", "profile", "list")
	if err != nil {
		return "", err
	}
	var profiles struct {
		ActiveEnvironment string            `json:"activeEnvironment"`
		Defaults          map[string]string `json:"defaults"`
		Profiles          []struct {
			ID   string `json:"id"`
			Site string `json:"site"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(data, &profiles); err != nil {
		return "", fmt.Errorf("parse twg profiles: %w", err)
	}
	id := profiles.Defaults[profiles.ActiveEnvironment]
	for _, profile := range profiles.Profiles {
		if profile.ID == id && profile.Site != "" {
			return profile.Site, nil
		}
	}
	return "", fmt.Errorf("no default Jira site in twg profiles; run `twg doctor`")
}

// ListSprints returns the sprints for the given board id in the requested states.
// state is a comma-separated list of "future", "active", and/or "closed"; pass ""
// for active. When paginate is true all pages are fetched.
func ListSprints(boardID int, state string, paginate bool) ([]Sprint, error) {
	return ListSprintsContext(context.Background(), boardID, state, paginate)
}

// ListSprintsContext is ListSprints, stopped when ctx is done.
func ListSprintsContext(ctx context.Context, boardID int, state string, paginate bool) ([]Sprint, error) {
	if state == "" {
		state = "active"
	}
	var all []Sprint
	for _, oneState := range strings.Split(state, ",") {
		oneState = strings.TrimSpace(oneState)
		if oneState != "active" && oneState != "future" && oneState != "closed" {
			return nil, fmt.Errorf("invalid sprint state %q", oneState)
		}
		for offset := 0; ; {
			args := []string{"jira", "board", "sprints", "query", "--id", fmt.Sprintf("%d", boardID), "--state", oneState, "--start-at", fmt.Sprintf("%d", offset), "--max-results", "50"}
			data, err := runContext(ctx, args...)
			if err != nil {
				return nil, fmt.Errorf("list sprints for board %d: %w", boardID, err)
			}
			var page struct {
				Sprints []rawSprint `json:"sprints"`
				Total   int         `json:"total"`
				IsLast  bool        `json:"isLast"`
			}
			if err := json.Unmarshal(data, &page); err != nil {
				return nil, fmt.Errorf("parse sprints: %w", err)
			}
			parsed, err := parseSprints(data)
			if err != nil {
				return nil, err
			}
			all = append(all, parsed...)
			offset += len(page.Sprints)
			if !paginate || page.IsLast || len(page.Sprints) == 0 || (page.Total > 0 && offset >= page.Total) {
				break
			}
		}
	}
	return all, nil
}

// ListActiveSprints returns the active sprints for the given board id.
func ListActiveSprints(boardID int) ([]Sprint, error) {
	return ListSprints(boardID, "active", false)
}

// SearchIssues runs a JQL search and returns the matching issues. fields is the
// list of issue fields to request. Pass nil for the default.
func SearchIssues(jql string, fields []string) ([]Issue, error) {
	return SearchIssuesContext(context.Background(), jql, fields)
}

// SearchIssuesContext is SearchIssues, stopped when ctx is done.
func SearchIssuesContext(ctx context.Context, jql string, fields []string) ([]Issue, error) {
	raws, err := SearchIssuesJSONContext(ctx, jql, fields, int(^uint(0)>>1))
	if err != nil {
		return nil, err
	}
	issues := make([]Issue, 0, len(raws))
	for _, raw := range raws {
		issue, err := decodeIssue(raw)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

// InFlightJQL builds a JQL query for a single sprint's in-flight work assigned to
// the given assignee ("" means the current Jira user). statuses are the workflow
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
// A backslash is escaped first, so a value ending in one cannot escape the
// closing quote.
func quoteJQL(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}

// GetIssue fetches summary, description, status, priority, and assignee for the given ticket.
func GetIssue(ticket string) (*Issue, error) {
	return GetIssueContext(context.Background(), ticket)
}

// GetIssueContext is GetIssue, stopped when ctx is done.
func GetIssueContext(ctx context.Context, ticket string) (*Issue, error) {
	raw, err := GetIssueJSONContext(ctx, ticket, "summary,description,status,priority,assignee")
	if err != nil {
		return nil, err
	}
	issue, err := decodeIssue(raw)
	if err != nil {
		return nil, fmt.Errorf("parse issue %s: %w", ticket, err)
	}
	return &issue, nil
}

// GetIssueJSON returns twg's flattened JSON for one work item, with fields (a
// comma-separated --fields value, such as FactFields) requested. Parse it
// with ParseFacts.
func GetIssueJSON(ticket, fields string) ([]byte, error) {
	return GetIssueJSONContext(context.Background(), ticket, fields)
}

// GetIssueJSONContext is GetIssueJSON, stopped when ctx is done.
func GetIssueJSONContext(ctx context.Context, ticket, fields string) ([]byte, error) {
	args := []string{"jira", "workitem", "get", ticket}
	if fields == "*all" {
		args = append(args, "--full")
	} else if fields != "" {
		args = append(args, "--fields", fields)
	}
	data, err := runContext(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("get issue %s: %w", ticket, err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("parse issue %s: %w", ticket, err)
	}
	if len(items) != 1 || len(items[0]) == 0 || string(items[0]) == "null" {
		return nil, fmt.Errorf("get issue %s: twg returned %d issues", ticket, len(items))
	}
	return items[0], nil
}

// SearchIssuesJSON runs a JQL search for up to limit work items and returns
// flattened work item JSON for ParseFacts.
func SearchIssuesJSON(jql string, fields []string, limit int) ([]json.RawMessage, error) {
	return SearchIssuesJSONContext(context.Background(), jql, fields, limit)
}

// SearchIssuesJSONContext is SearchIssuesJSON, stopped when ctx is done.
func SearchIssuesJSONContext(ctx context.Context, jql string, fields []string, limit int) ([]json.RawMessage, error) {
	if limit <= 0 {
		return nil, nil
	}
	var issues []json.RawMessage
	cursor := ""
	seen := map[string]bool{}
	for len(issues) < limit {
		pageSize := min(100, limit-len(issues))
		args := []string{"jira", "workitem", "query", "--jql", jql, "--limit", fmt.Sprintf("%d", pageSize)}
		if len(fields) > 0 {
			args = append(args, "--fields", strings.Join(fields, ","))
		}
		if cursor != "" {
			args = append(args, "--after", cursor)
		}
		data, err := runContext(ctx, args...)
		if err != nil {
			return nil, fmt.Errorf("search issues: %w", err)
		}
		var page struct {
			Issues   []json.RawMessage `json:"issues"`
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				NextCursor  string `json:"nextCursor"`
			} `json:"pageInfo"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("parse search results: %w", err)
		}
		issues = append(issues, page.Issues...)
		if len(issues) > limit {
			issues = issues[:limit]
		}
		if !page.PageInfo.HasNextPage {
			break
		}
		if page.PageInfo.NextCursor == "" || seen[page.PageInfo.NextCursor] {
			return nil, fmt.Errorf("search issues: twg returned a repeated or empty pagination cursor")
		}
		cursor = page.PageInfo.NextCursor
		seen[cursor] = true
	}
	return issues, nil
}

func decodeIssue(raw json.RawMessage) (Issue, error) {
	var flat struct {
		Key string `json:"key"`
		IssueFields
	}
	if err := json.Unmarshal(raw, &flat); err != nil {
		return Issue{}, err
	}
	if flat.Key == "" {
		return Issue{}, errors.New("work item has no key")
	}
	return Issue{Key: flat.Key, Fields: flat.IssueFields}, nil
}

// SprintTicketsJQL builds a JQL query for a sprint's tickets assigned to
// any of assignees (account IDs, emails, or currentUser(), which is passed
// unquoted) and, when unassigned is set, its tickets assigned to no one.
// Callers name at least one of the two. The most recently updated come
// first, so a bounded search keeps the liveliest tickets.
func SprintTicketsJQL(sprintID int, assignees []string, unassigned bool) string {
	var who []string
	for _, a := range assignees {
		if a == "currentUser()" {
			who = append(who, a)
		} else {
			who = append(who, quoteJQL(a))
		}
	}
	var clause string
	switch {
	case len(who) > 0 && unassigned:
		clause = fmt.Sprintf("(assignee in (%s) OR assignee is EMPTY)", strings.Join(who, ","))
	case len(who) > 0:
		clause = fmt.Sprintf("assignee in (%s)", strings.Join(who, ","))
	default:
		clause = "assignee is EMPTY"
	}
	return fmt.Sprintf("sprint = %d AND %s ORDER BY updated DESC", sprintID, clause)
}

// GetComments fetches up to limit comments for the given ticket, newest last.
func GetComments(ticket string, limit int) ([]Comment, error) {
	return GetCommentsContext(context.Background(), ticket, limit)
}

// GetCommentsContext is GetComments, stopped when ctx is done.
func GetCommentsContext(ctx context.Context, ticket string, limit int) ([]Comment, error) {
	data, err := runContext(ctx, "jira", "workitem", "comment", "query",
		"--issue-id", ticket, "--first", fmt.Sprintf("%d", limit), "--order-by", "+created")
	if err != nil {
		return nil, fmt.Errorf("list comments %s: %w", ticket, err)
	}
	return parseComments(data)
}

// GetWatchers fetches the watcher list for the given ticket.
func GetWatchers(ticket string) ([]User, error) {
	return GetWatchersContext(context.Background(), ticket)
}

// GetWatchersContext is GetWatchers, stopped when ctx is done.
func GetWatchersContext(ctx context.Context, ticket string) ([]User, error) {
	data, err := runContext(ctx, "jira", "workitem", "watcher", "query", "--issue-id", ticket)
	if err != nil {
		return nil, fmt.Errorf("list watchers %s: %w", ticket, err)
	}
	return parseWatchers(data)
}

// CreateIssue creates a new Jira work item and returns the new ticket key (e.g. "WGO-123").
func CreateIssue(project, summary, issueType, description string) (string, error) {
	return CreateIssueContext(context.Background(), project, summary, issueType, description)
}

// CreateIssueContext is CreateIssue, stopped when ctx is done.
func CreateIssueContext(ctx context.Context, project, summary, issueType, description string) (string, error) {
	args := []string{
		"jira", "workitem", "create",
		"--space", project,
		"--summary", summary,
		"--type", issueType,
		"--yes",
	}
	if description != "" {
		args = append(args, "--description", description, "--description-format", "markdown")
	}
	data, err := runContext(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("create issue in %s: %w", project, err)
	}
	var result struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("parse create response: %w", err)
	}
	if result.Key == "" {
		return "", fmt.Errorf("create returned no ticket key; response: %s", data)
	}
	return result.Key, nil
}

// UpdateIssue pushes a new summary and description to an existing Jira ticket.
func UpdateIssue(ticket, summary, description string) error {
	return UpdateIssueContext(context.Background(), ticket, summary, description)
}

// UpdateIssueContext is UpdateIssue, stopped when ctx is done.
func UpdateIssueContext(ctx context.Context, ticket, summary, description string) error {
	args := []string{
		"jira", "workitem", "update",
		"--id", ticket,
	}
	if summary != "" {
		args = append(args, "--summary", summary)
	}
	if description != "" {
		// Truncate at 32 KB to stay within Jira ADF limits.
		if len(description) > 32*1024 {
			description = description[:32*1024]
		}
		args = append(args, "--description", description, "--description-format", "markdown")
	}
	_, err := runContext(ctx, args...)
	if err != nil {
		return fmt.Errorf("update issue %s: %w", ticket, err)
	}
	return nil
}

// TransitionIssue moves a Jira ticket to the given transition ID or status name.
func TransitionIssue(ticket, jiraStatus string) error {
	return TransitionIssueContext(context.Background(), ticket, jiraStatus)
}

// TransitionIssueContext is TransitionIssue, stopped when ctx is done.
func TransitionIssueContext(ctx context.Context, ticket, jiraStatus string) error {
	_, err := runContext(ctx, "jira", "workitem", "transition", "--id", ticket, "--transition-id", jiraStatus)
	if err != nil {
		return fmt.Errorf("transition %s to %q: %w", ticket, jiraStatus, err)
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

// --- JSON parsing helpers ---

// rawSprintList matches the data from `twg jira board sprints query`.
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

// parseSprints decodes the sprints from one page's data.
func parseSprints(raw []byte) ([]Sprint, error) {
	var wrapper rawSprintList
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("parse sprints: %w", err)
	}
	var sprints []Sprint
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
	return sprints, nil
}

// rawComment matches `twg jira workitem comment query` data.
type rawComment struct {
	ID      string          `json:"id"`
	Author  User            `json:"author"`
	Body    json.RawMessage `json:"body"`
	Created string          `json:"created"`
}

func parseComments(raw []byte) ([]Comment, error) {
	var raws []rawComment
	if err := json.Unmarshal(raw, &raws); err != nil {
		return nil, fmt.Errorf("parse comments: %w", err)
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

func parseWatchers(raw []byte) ([]User, error) {
	var wrapper rawWatcherList
	if err := json.Unmarshal(raw, &wrapper); err != nil {
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
		time.RFC3339Nano,
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
