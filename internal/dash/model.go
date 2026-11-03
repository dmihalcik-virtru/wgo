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
	KindSprint    NodeKind = "sprint" // a Jira sprint of [jira] board (gh-98)
)

// Source names used in Snapshot.Sources.
const (
	SourceJJ           = "jj"
	SourcePlan         = "plan"
	SourceAgents       = "agents"
	SourceGitHubPRs    = "github_prs"
	SourceGitHubIssues = "github_issues"
	SourceJira         = "jira"
	// SourceGitHubInventory is the per-person PR inventory (gh-97).
	SourceGitHubInventory = "github_inventory"
	// SourceJiraSprints is the board's sprints and their tickets (gh-98).
	SourceJiraSprints = "jira_sprints"
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
	// People are the people the dash follows (gh-97), resolved by
	// ResolvePeople; the query macros @me and @people read them. Omitted
	// when nobody is known.
	People []Person `json:"people,omitempty"`
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
	// Parts break a source that fetches a few named units down by unit:
	// one per (person, inventory set) for github_inventory, one per board
	// or sprint for jira_sprints. The counts above then count parts.
	Parts []SourcePart `json:"parts,omitempty"`
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
	Sprint    *SprintInfo    `json:"sprint,omitempty"`
}

// Effort groups.
const (
	GroupEffort    = "effort"    // a plan or state effort
	GroupTicket    = "ticket"    // gh-71's ticket-ID fallback
	GroupTheme     = "theme"     // an agent ThemeID naming no known effort
	GroupUngrouped = "ungrouped" // unattributed work and conflicts
	GroupRepo      = "repo"      // inventory PRs no local work leads to, per repository (gh-97)
	GroupSprint    = "sprint"    // sprint tickets nothing else leads to, per sprint (gh-98)
)

// UngroupedID is the node ID of the Ungrouped effort.
const UngroupedID = "effort:ungrouped"

// EffortInfo describes an effort group.
type EffortInfo struct {
	// Key is the attribution key: the effort ID, "ticket-<id>", a raw
	// ThemeID, "repo:<owner/repo>", "sprint:<id>", or "ungrouped".
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
	// Bookmark is the nearest local bookmark at or below @, or else the only
	// one in BookmarksAhead.
	Bookmark string `json:"bookmark,omitempty"`
	// BookmarksAhead is set when no bookmark is at or below @ but the nearest
	// bookmarks above it hold all of the workspace's own work: its bookmark
	// moved on (trunk merged into it, or a push from another workspace) and
	// left @ behind. One per commit, sorted.
	BookmarksAhead []string `json:"bookmarks_ahead,omitempty"`
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
	ChangesTruncated int `json:"changes_truncated,omitempty"`
	// ChangesTruncatedUnknown is set when the window was full but jj could not
	// count what lies beyond it, so ChangesTruncated is not trustworthy.
	ChangesTruncatedUnknown bool      `json:"changes_truncated_unknown,omitempty"`
	LastActivity            time.Time `json:"last_activity,omitzero"`
	Annotation              string    `json:"annotation,omitempty"`
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
	// Freshness is the worse of the freshness of the sources that found
	// the PR: the bookmark's PR lookup, and the inventory set its facts
	// were read from. FetchedAt is the older of their fetch times.
	Freshness Freshness `json:"freshness"`
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	// BookmarkID is the node ID of the bookmark the PR was found for. It is
	// empty for a PR that only the inventory found and that is attached to
	// an effort by a tracks edge instead (gh-97).
	BookmarkID string `json:"bookmark_id,omitempty"`
	// Sources says how the PR was found: PRSourceBookmark, PRSourceInventory
	// or both, sorted. It is set on every PR assemble builds; it is absent
	// only from snapshots older than the inventory.
	Sources []string `json:"sources,omitempty"`

	// The fields below are the inventory's facts (gh-97). Each is omitted
	// when GitHub did not report it, which is always the case for a PR
	// known only through a bookmark: unknown, never zero or "none". A list
	// distinguishes the two with omitzero: nil is unknown and omitted, while
	// a non-nil empty list is "reported, none" and encodes as [].

	// Author is the PR author's login.
	Author    string    `json:"author,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	MergedAt  time.Time `json:"merged_at,omitzero"`
	ClosedAt  time.Time `json:"closed_at,omitzero"`
	// ReadyAt is when the PR last left draft, or CreatedAt if it never was
	// a draft. It is omitted while the PR is a draft.
	ReadyAt      time.Time `json:"ready_at,omitzero"`
	Additions    *int      `json:"additions,omitempty"`
	Deletions    *int      `json:"deletions,omitempty"`
	ChangedFiles *int      `json:"changed_files,omitempty"`
	// Mergeable is MergeMergeable, MergeConflicting or MergeUnknown. GitHub
	// computes it lazily, so MergeUnknown is common and does not mean no.
	Mergeable string `json:"mergeable,omitempty"`
	BaseRef   string `json:"base_ref,omitempty"`
	HeadRef   string `json:"head_ref,omitempty"`
	// ReviewRequests adds kinds and request times to RequestedReviewers,
	// for the same outstanding requests; ReviewersKnown covers both.
	ReviewRequests []ReviewRequest `json:"review_requests,omitzero"`
	// Reviews holds the latest review per reviewer.
	Reviews []Review `json:"reviews,omitzero"`
	// CheckDetails lists the head commit's check runs and status contexts,
	// at most MaxCheckDetails of them; Checks stays the rollup.
	CheckDetails    []CheckDetail `json:"check_details,omitzero"`
	ChecksTruncated bool          `json:"checks_truncated,omitempty"`
	// TicketKeys are the ticket IDs read from the head branch, then the
	// title, then the body: ordered and deduplicated. A GitHub issue in the
	// same repository is "gh-<n>", matching TicketInfo.Key.
	TicketKeys []string `json:"ticket_keys,omitzero"`
	// Involvement lists each configured person's part in the PR.
	Involvement []Involvement `json:"involvement,omitzero"`
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

	// The fields below are Jira facts (gh-98), omitted when Jira did not
	// report them or the cache predates them. Title carries the summary.
	// Lists use omitzero like PRInfo's: nil is unknown, [] is none.

	// StatusCategory is StatusToDo, StatusInProgress or StatusDone.
	StatusCategory string      `json:"status_category,omitempty"`
	Type           string      `json:"type,omitempty"`
	Priority       string      `json:"priority,omitempty"`
	Sprints        []SprintRef `json:"sprints,omitzero"`
	// StoryPoints is nil when unestimated or unknown; a diagnostic says
	// when the site's story points field could not be read at all.
	StoryPoints *float64      `json:"story_points,omitempty"`
	Parent      *TicketParent `json:"parent,omitempty"`
	// Team is the team field's display value.
	Team       string    `json:"team,omitempty"`
	Components []string  `json:"components,omitzero"`
	Labels     []string  `json:"labels,omitzero"`
	Resolution string    `json:"resolution,omitempty"`
	ResolvedAt time.Time `json:"resolved_at,omitzero"`
	CreatedAt  time.Time `json:"created_at,omitzero"`
	UpdatedAt  time.Time `json:"updated_at,omitzero"`
	// AssigneeID is the assignee's account ID, for matching people.
	AssigneeID string `json:"assignee_id,omitempty"`
}

// AgentInfo describes a gh-72 agent session, read only.
type AgentInfo struct {
	SessionID    string    `json:"session_id"`
	Tool         string    `json:"tool"`
	Status       string    `json:"status"`
	Liveness     Liveness  `json:"liveness"`
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

// Liveness is the derived liveness of an agent session, mirroring the
// visible values of store.AgentLiveness ("gone" sessions are never shown).
type Liveness string

// Agent liveness values.
const (
	LivenessActive    Liveness = "active"
	LivenessLive      Liveness = "live"
	LivenessUncertain Liveness = "uncertain"
)

// EdgeKind names the kinds of snapshot edges.
type EdgeKind string

// Edge kinds.
const (
	EdgeContains EdgeKind = "contains" // effort -> workspace
	EdgeOn       EdgeKind = "on"       // workspace -> bookmark
	EdgePR       EdgeKind = "pr"       // bookmark -> pr
	EdgeTicket   EdgeKind = "ticket"   // pr (or bookmark without PRs) -> ticket
	EdgeRuns     EdgeKind = "runs"     // effort -> agent
	EdgeWorksIn  EdgeKind = "works_in" // agent -> workspace
	// EdgeTracks attaches a PR or ticket that no bookmark leads to:
	// effort -> pr (gh-97), effort -> ticket (gh-98).
	EdgeTracks   EdgeKind = "tracks"
	EdgeInSprint EdgeKind = "in_sprint" // ticket -> sprint
)

// Edge is a directed snapshot edge between node IDs.
type Edge struct {
	Source string   `json:"source"`
	Target string   `json:"target"`
	Kind   EdgeKind `json:"kind"`
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
	// A PR attached by a tracks edge counts under that effort. A bookmark
	// whose lookup is unknown counts as unknown even when the inventory
	// attached a PR to it, which counts too.
	PRStates map[string]map[string]int `json:"pr_states"`
	// Agents counts agent sessions by liveness.
	Agents map[string]map[string]int `json:"agents"`
	// Merged is the number of PRs merged per day of the window, by
	// merged_at, keyed like PRStates. An absent effort's count is unknown,
	// never zero: it has a PR of unknown state; a merged PR whose merge time
	// is unknown (one known only through a bookmark) and may fall in the
	// window, because its updated_at is unknown or not before the window; a
	// bookmark whose lookup is unknown or failed; or a workspace jj could not
	// read. Merged is omitted entirely unless People includes the
	// authenticated user (Me) and every person's closed-recent inventory set
	// is fresh, untruncated, covers the window and is no older than their
	// open-authored set: otherwise their merges could belong to any effort.
	Merged map[string][]int `json:"merged,omitzero"`
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
