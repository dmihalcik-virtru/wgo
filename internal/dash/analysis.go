package dash

import (
	"cmp"
	"strconv"
	"strings"
	"time"
)

// This file holds the snapshot types the analysis epic (gh-96) adds: PR facts
// from the inventory (gh-97), Jira ticket and sprint facts (gh-98), and how
// the new effort groups sort.

// MaxCheckDetails bounds PRInfo.CheckDetails; beyond it ChecksTruncated is set.
const MaxCheckDetails = 100

// PR mergeability (PRInfo.Mergeable).
const (
	MergeMergeable   = "mergeable"
	MergeConflicting = "conflicting"
	MergeUnknown     = "unknown"
)

// Review states (Review.State), as GitHub names them.
const (
	ReviewApproved         = "APPROVED"
	ReviewChangesRequested = "CHANGES_REQUESTED"
	ReviewCommented        = "COMMENTED"
	ReviewDismissed        = "DISMISSED"
)

// Check states (CheckDetail.State), normalized from check runs' conclusions
// and status contexts' states.
const (
	CheckSuccess = "success"
	CheckFailure = "failure"
	CheckPending = "pending"
	CheckSkipped = "skipped"
	CheckNeutral = "neutral"
)

// Reviewer kinds (ReviewRequest.Kind).
const (
	ReviewerUser = "user"
	ReviewerTeam = "team"
)

// Involvement roles (Involvement.Role).
const (
	RoleAuthor = "author"
	// RoleReviewRequested means the person's review-requested inventory set
	// holds the PR, directly or through one of their teams. It is present
	// only while the request is outstanding.
	RoleReviewRequested = "review_requested"
	RoleReviewed        = "reviewed"
)

// PR sources (PRInfo.Sources).
const (
	PRSourceBookmark  = "bookmark"
	PRSourceInventory = "inventory"
)

// Jira status categories (TicketInfo.StatusCategory).
const (
	StatusToDo       = "todo"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
)

// Sprint states (SprintInfo.State, SprintRef.State).
const (
	SprintActive = "active"
	SprintFuture = "future"
	SprintClosed = "closed"
)

// ReviewRequest is one outstanding review request on a PR.
type ReviewRequest struct {
	// Reviewer is a login, or "team:<slug>" for a team, as in
	// PRInfo.RequestedReviewers.
	Reviewer string `json:"reviewer"`
	// Kind is ReviewerUser or ReviewerTeam.
	Kind string `json:"kind"`
	// RequestedAt is the latest ReviewRequestedEvent for this reviewer,
	// omitted when none was found.
	RequestedAt time.Time `json:"requested_at,omitzero"`
}

// Review is a reviewer's latest review of a PR.
type Review struct {
	Author string `json:"author"`
	// State is ReviewApproved, ReviewChangesRequested, ReviewCommented or
	// ReviewDismissed.
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at,omitzero"`
}

// CheckDetail is one check run or status context on a PR's head commit.
type CheckDetail struct {
	Name string `json:"name"`
	// State is CheckSuccess, CheckFailure, CheckPending, CheckSkipped or
	// CheckNeutral.
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
}

// Involvement is a configured person's part in a PR.
type Involvement struct {
	// Person is the configured person's GitHub login (Person.Login).
	Person string `json:"person"`
	// Role is RoleAuthor, RoleReviewRequested or RoleReviewed.
	Role string `json:"role"`
}

// SprintRef names a sprint a ticket is or was in.
type SprintRef struct {
	ID   int    `json:"id"`
	Name string `json:"name,omitempty"`
	// State is SprintActive, SprintFuture or SprintClosed.
	State string `json:"state,omitempty"`
}

// TicketParent is a ticket's epic or parent issue.
type TicketParent struct {
	Key     string `json:"key"`
	Summary string `json:"summary,omitempty"`
	Type    string `json:"type,omitempty"`
}

// SprintInfo describes a sprint of [jira] board (gh-98).
type SprintInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	// State is SprintActive, SprintFuture or SprintClosed.
	State string    `json:"state"`
	Start time.Time `json:"start,omitzero"`
	End   time.Time `json:"end,omitzero"`
	Goal  string    `json:"goal,omitempty"`
	Board int       `json:"board,omitempty"`
	// Freshness and FetchedAt are those of the cached sprint list.
	Freshness Freshness `json:"freshness"`
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	// Truncated is set when the sprint's ticket search hit its bound, so
	// some of its tickets are missing.
	Truncated bool `json:"truncated,omitempty"`
}

// SourcePart is the freshness of one unit a source fetches separately.
type SourcePart struct {
	// Name identifies the unit, such as "alice/open-authored" or "board 42".
	Name      string    `json:"name"`
	State     Freshness `json:"state"`
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	// Truncated is set when a remote limit cut the unit's results short.
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
}

// groupRank orders effort groups for display: named efforts, ticket groups,
// agent themes, repository groups, sprint groups, then Ungrouped. An unknown
// group sorts with themes. L.effortOrder in internal/review/web/live.js
// mirrors it.
var groupRank = map[string]int{
	GroupEffort: 0, GroupTicket: 1, GroupTheme: 2, GroupRepo: 3, GroupSprint: 4, GroupUngrouped: 5,
}

// CompareEfforts orders effort nodes as the page lists them: by group, then
// case-insensitive label, then ID. It suits slices.SortFunc.
func CompareEfforts(a, b *Node) int {
	return cmp.Or(
		cmp.Compare(effortGroupRank(a), effortGroupRank(b)),
		cmp.Compare(strings.ToLower(a.Label), strings.ToLower(b.Label)),
		cmp.Compare(a.ID, b.ID),
	)
}

func effortGroupRank(n *Node) int {
	if n.Effort != nil {
		if r, ok := groupRank[n.Effort.Group]; ok {
			return r
		}
	}
	return groupRank[GroupTheme]
}

// Effort key prefixes of the repo and sprint groups.
const (
	repoEffortPrefix   = "repo:"
	sprintEffortPrefix = "sprint:"
)

// repoEffortKey is the key of the group effort holding repository repo's
// inventory PRs. Like prNodeID it lowercases the case-insensitive slug.
func repoEffortKey(repo string) string { return repoEffortPrefix + strings.ToLower(repo) }

// sprintEffortKey is the key of the group effort holding sprint id's
// tickets that nothing else leads to.
func sprintEffortKey(id int) string { return sprintEffortPrefix + strconv.Itoa(id) }

func sprintNodeID(id int) string { return "sprint:" + strconv.Itoa(id) }
