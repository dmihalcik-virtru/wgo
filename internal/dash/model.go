// Package dash builds the live snapshot behind `wgo dash` (gh-70): every
// active effort with its workspaces, bookmarks, PRs, tickets and agent
// sessions, plus freshness metadata, small-multiple counts and a
// "since last look" delta.
//
// The snapshot model is deliberately separate from review.Graph: it carries
// live state (freshness, liveness, errors) that the historical review graph
// has no notion of. A Collector builds snapshots outside any HTTP request,
// reading jj with --ignore-working-copy so that viewing can never snapshot a
// workspace, and reading remote data from on-disk caches only. A Dash holds
// the current snapshot as an immutable value swapped atomically, persists it,
// and owns the last-seen baseline. Handler only serializes what Dash holds.
package dash

import "time"

// SchemaVersion is the snapshot JSON layout version.
const SchemaVersion = 1

// DefaultDays is the default activity window for the small multiples.
const DefaultDays = 14

// ChangeWindow bounds how many visible change IDs are kept per workspace, in
// the snapshot and in the last-seen baseline. Only the newest ChangeWindow
// unmerged changes (those in trunk()..@, pushed or not) are read; older ones
// are reported as a truncated count and are not compared by the
// since-last-look delta.
const ChangeWindow = 200

// Freshness is the state of a piece of data or of a whole source.
type Freshness string

const (
	// Fresh data is within its TTL.
	Fresh Freshness = "fresh"
	// Stale data is older than its TTL but is the last known good value.
	Stale Freshness = "stale"
	// Unknown means no data: a cold cache, or a source that was not read.
	// It never means "no PR" or "closed".
	Unknown Freshness = "unknown"
	// Error means the last attempt failed and there is no good data.
	Error Freshness = "error"
)

// hasData reports whether f carries a last known good value: Fresh or Stale.
func (f Freshness) hasData() bool { return f == Fresh || f == Stale }

// NodeKind names the kinds of snapshot nodes.
type NodeKind string

// Node kinds.
const (
	KindEffort    NodeKind = "effort"
	KindWorkspace NodeKind = "workspace"
	KindBookmark  NodeKind = "bookmark"
	KindPR        NodeKind = "pr"
	KindTicket    NodeKind = "ticket"
	KindAgent     NodeKind = "agent"
)

// Source names used in Snapshot.Sources.
const (
	SourceJJ           = "jj"
	SourcePlan         = "plan"
	SourceAgents       = "agents"
	SourceGitHubPRs    = "github_prs"
	SourceGitHubIssues = "github_issues"
	SourceJira         = "jira"
)

// Snapshot is one immutable view of ongoing work. Once published by a Dash
// it must not be modified.
type Snapshot struct {
	Schema      int       `json:"schema"`
	Generation  uint64    `json:"generation"`
	GeneratedAt time.Time `json:"generated_at"`
	// Days is the activity window of Counts.
	Days int `json:"days"`
	// Sources records per-source freshness.
	Sources     map[string]SourceStatus `json:"sources"`
	Nodes       []Node                  `json:"nodes"`
	Edges       []Edge                  `json:"edges"`
	Counts      Counts                  `json:"counts"`
	Diagnostics []string                `json:"diagnostics,omitempty"`
}

// SourceStatus is the freshness of one data source. For remote sources the
// counts say how many items were in each state; State is the worst of them
// (error, then unknown, then stale, else fresh).
type SourceStatus struct {
	State   Freshness `json:"state"`
	Fresh   int       `json:"fresh,omitempty"`
	Stale   int       `json:"stale,omitempty"`
	Unknown int       `json:"unknown,omitempty"`
	Error   int       `json:"error,omitempty"`
	// OldestFetch is the oldest successful fetch time among cached items.
	// The jira cache does not record one, so it is never set for jira.
	OldestFetch time.Time `json:"oldest_fetch,omitzero"`
	Detail      string    `json:"detail,omitempty"`
}

// Node is a snapshot node. Exactly one of the kind-specific fields is set.
type Node struct {
	ID        string         `json:"id"`
	Kind      NodeKind       `json:"kind"`
	Label     string         `json:"label"`
	Effort    *EffortInfo    `json:"effort,omitempty"`
	Workspace *WorkspaceInfo `json:"workspace,omitempty"`
	Bookmark  *BookmarkInfo  `json:"bookmark,omitempty"`
	PR        *PRInfo        `json:"pr,omitempty"`
	Ticket    *TicketInfo    `json:"ticket,omitempty"`
	Agent     *AgentInfo     `json:"agent,omitempty"`
}

// Effort groups.
const (
	GroupEffort    = "effort"    // a plan or state effort
	GroupTicket    = "ticket"    // gh-71's ticket-ID fallback
	GroupTheme     = "theme"     // an agent ThemeID naming no known effort
	GroupUngrouped = "ungrouped" // unattributed work and conflicts
)

// UngroupedID is the node ID of the Ungrouped effort.
const UngroupedID = "effort:ungrouped"

// EffortInfo describes an effort group.
type EffortInfo struct {
	// Key is the attribution key: the effort ID, "ticket-<id>", a raw
	// ThemeID, or "ungrouped".
	Key         string `json:"key"`
	Group       string `json:"group"`
	Description string `json:"description,omitempty"`
	// Source is "state", "plan" or "both" for real efforts.
	Source string `json:"source,omitempty"`
	// Conflicts lists workspaces claimed by several efforts (Ungrouped only).
	Conflicts []EffortConflict `json:"conflicts,omitempty"`
}

// EffortConflict is a workspace claimed by more than one effort.
type EffortConflict struct {
	WorkspaceID string   `json:"workspace_id"`
	ClaimedBy   []string `json:"claimed_by"`
}

// WorkspaceInfo describes a jj workspace.
type WorkspaceInfo struct {
	// Slug is the workspace directory name; Repo the main clone's name.
	Slug     string `json:"slug"`
	Repo     string `json:"repo"`
	RepoSlug string `json:"repo_slug,omitempty"` // owner/repo when known
	Path     string `json:"path"`
	// MainClone is the main workspace root of the repository.
	MainClone string `json:"main_clone"`
	IsMain    bool   `json:"is_main,omitempty"`
	// Bookmark is the nearest local bookmark at or below @.
	Bookmark string `json:"bookmark,omitempty"`
	// ChangeID is @ as of the workspace's last jj snapshot.
	ChangeID string `json:"change_id,omitempty"`
	// Description is the first line of the newest described change.
	Description string         `json:"description,omitempty"`
	Stack       *StackPosition `json:"stack,omitempty"`
	// Changes are the visible, non-empty-or-described change IDs in
	// trunk()..@, newest first, at most ChangeWindow of them.
	Changes []string `json:"changes,omitempty"`
	// ChangesTruncated counts changes in trunk()..@ beyond the ChangeWindow
	// read; it counts empty undescribed changes that Changes leaves out.
	ChangesTruncated int       `json:"changes_truncated,omitempty"`
	LastActivity     time.Time `json:"last_activity,omitzero"`
	Annotation       string    `json:"annotation,omitempty"`
	// EffortID is the node ID of the effort this workspace is attributed to.
	EffortID string `json:"effort_id"`
	// Error is set when jj could not be read for this workspace; the other
	// jj-derived fields are then empty.
	Error string `json:"error,omitempty"`
}

// StackPosition places a workspace's bookmark in its local stack, derived
// from the jj change DAG (bookmarks in trunk()..@), never from PR bases.
type StackPosition struct {
	// Position is 1-based from the bottom (nearest trunk).
	Position int `json:"position"`
	Size     int `json:"size"`
	// Bookmarks lists the stack from the bottom up.
	Bookmarks []string `json:"bookmarks"`
}

// BookmarkInfo describes a local bookmark and the state of its PR lookup.
type BookmarkInfo struct {
	Name string `json:"name"`
	Repo string `json:"repo"`
	// PRLookup is the freshness of the cached PR list. Unknown means the
	// cache holds nothing: it does not mean the bookmark has no PR.
	PRLookup    Freshness `json:"pr_lookup"`
	PRFetchedAt time.Time `json:"pr_fetched_at,omitzero"`
	PRError     string    `json:"pr_error,omitempty"`
	PRCount     int       `json:"pr_count"`
}

// PRInfo describes a cached pull request.
type PRInfo struct {
	Number         int    `json:"number"`
	Repo           string `json:"repo"`
	URL            string `json:"url"`
	Title          string `json:"title,omitempty"`
	State          string `json:"state"`
	IsDraft        bool   `json:"is_draft,omitempty"`
	ReviewDecision string `json:"review_decision,omitempty"`
	Checks         string `json:"checks,omitempty"`
	// RequestedReviewers is meaningful only when ReviewersKnown.
	RequestedReviewers []string  `json:"requested_reviewers,omitempty"`
	ReviewersKnown     bool      `json:"reviewers_known"`
	UpdatedAt          time.Time `json:"updated_at,omitzero"`
	Freshness          Freshness `json:"freshness"`
	// BookmarkID is the node ID of the bookmark the PR was found for.
	BookmarkID string `json:"bookmark_id"`
}

// TicketInfo describes a Jira or GitHub issue ticket.
type TicketInfo struct {
	Key string `json:"key"`
	// System is "jira" or "github".
	System    string    `json:"system"`
	Status    string    `json:"status,omitempty"`
	Title     string    `json:"title,omitempty"`
	URL       string    `json:"url,omitempty"`
	Assignee  string    `json:"assignee,omitempty"`
	Freshness Freshness `json:"freshness"`
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	Error     string    `json:"error,omitempty"`
}

// AgentInfo describes a gh-72 agent session, read only.
type AgentInfo struct {
	SessionID string `json:"session_id"`
	Tool      string `json:"tool"`
	Status    string `json:"status"`
	// Liveness is active, live or uncertain.
	Liveness     string    `json:"liveness"`
	Source       string    `json:"source,omitempty"`
	Branch       string    `json:"branch,omitempty"`
	ThemeID      string    `json:"theme_id,omitempty"`
	Path         string    `json:"path"`
	WorkspaceID  string    `json:"workspace_id,omitempty"`
	StartTime    time.Time `json:"start_time,omitzero"`
	LastActivity time.Time `json:"last_activity,omitzero"`
	// Conflict flags another active session on the same repo and bookmark.
	Conflict      bool     `json:"conflict,omitempty"`
	ConflictsWith []string `json:"conflicts_with,omitempty"`
	EffortID      string   `json:"effort_id"`
}

// Edge kinds.
const (
	EdgeContains = "contains" // effort -> workspace
	EdgeOn       = "on"       // workspace -> bookmark
	EdgePR       = "pr"       // bookmark -> pr
	EdgeTicket   = "ticket"   // pr (or bookmark without PRs) -> ticket
	EdgeRuns     = "runs"     // effort -> agent
	EdgeWorksIn  = "works_in" // agent -> workspace
)

// Edge is a directed snapshot edge between node IDs.
type Edge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
}

// Counts are the precomputed small multiples, keyed by effort node ID.
type Counts struct {
	// Days lists the window's dates (YYYY-MM-DD, local time), oldest first.
	Days []string `json:"days"`
	// Activity is the number of distinct changes authored per day among
	// each workspace's listed Changes: unmerged work only, so changes drop
	// out once they land in trunk.
	Activity map[string][]int `json:"activity"`
	// PRStates counts PRs by state (open, draft, merged, closed) plus
	// bookmarks whose lookup is unknown or failed (unknown) or that have no
	// PR (none). A bookmark shared by several efforts counts under each.
	PRStates map[string]map[string]int `json:"pr_states"`
	// Agents counts agent sessions by liveness.
	Agents map[string]map[string]int `json:"agents"`
}

// Node returns the node with the given ID, or nil.
func (s *Snapshot) Node(id string) *Node {
	if s == nil {
		return nil
	}
	for i := range s.Nodes {
		if s.Nodes[i].ID == id {
			return &s.Nodes[i]
		}
	}
	return nil
}
