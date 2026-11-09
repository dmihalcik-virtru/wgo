package dash

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/inventory"
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

// teamPrefix starts a team reviewer, "team:<slug>", and only a team one.
const teamPrefix = "team:"

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

// Inventory sets (gh-97), as internal/inventory names them. The
// github_inventory source has one part per person and set, named by
// inventoryPart.
const (
	InventoryOpenAuthored    = string(inventory.OpenAuthored)
	InventoryReviewRequested = string(inventory.ReviewRequested)
	InventoryClosedRecent    = string(inventory.ClosedRecent)
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
	// TicketScope names the population of the sprint's ticket search
	// (TicketScopeConfiguredPeopleAndUnassigned). Its counts are never
	// board-wide sprint totals.
	TicketScope string `json:"ticket_scope,omitempty"`
	// TicketLookup and TicketFetchedAt are the freshness of that search.
	// Both are empty, and omitted, when no search has run.
	TicketLookup    Freshness `json:"ticket_lookup,omitempty"`
	TicketFetchedAt time.Time `json:"ticket_fetched_at,omitzero"`
	// TicketCount is the number of tickets the search returned. It is nil
	// when no ticket result is cached, so an unknown count is never zero.
	TicketCount *int `json:"ticket_count,omitempty"`
	// Truncated is set when the sprint's ticket search hit its bound, so
	// some of its tickets are missing.
	Truncated bool `json:"truncated,omitempty"`
}

// TicketScopeConfiguredPeopleAndUnassigned is SprintInfo.TicketScope for the
// sprint search gh-98 runs: tickets assigned to configured people, plus
// unassigned tickets.
const TicketScopeConfiguredPeopleAndUnassigned = "configured_people_and_unassigned"

// SourcePart is the freshness of one unit a source fetches separately.
type SourcePart struct {
	// Name identifies the unit, such as "alice/open-authored" or "board 42".
	Name      string    `json:"name"`
	State     Freshness `json:"state"`
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	// Truncated is set when a remote limit cut the unit's results short.
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
	// Incomplete is set when the unit's last fetch is known to have missed
	// results it should hold: errors came back beside partial data, or
	// fewer results than counted. Like a truncated unit, it covers nothing
	// it lacks.
	Incomplete bool `json:"incomplete,omitempty"`
	// CoverageFrom is the earliest time the unit's search reaches, when it
	// is bounded in time: the cutoff of a closed-recent inventory search
	// (gh-97), recorded even when the search found nothing. A period before
	// it is incompletely covered however fresh the part is.
	CoverageFrom time.Time `json:"coverage_from,omitzero"`
}

// inventoryPart names the github_inventory part holding person login's
// inventory set, such as "alice/open-authored": inventory.Key's String.
func inventoryPart(login, set string) string {
	return inventory.Key{Login: login, Set: inventory.Set(set)}.String()
}

// inventoryCoverage returns the names of s's github_inventory parts whose
// PR list is complete: they hold data, fresh or stale, and were neither
// truncated nor incomplete. A PR missing from a covered part is not in that
// set; whether a PR is in any other part is unknown.
func inventoryCoverage(s *Snapshot) map[string]bool {
	return inventoryParts(s, func(p SourcePart) bool { return p.State.hasData() && !p.Truncated && !p.Incomplete })
}

// inventoryParts returns the lowercased names of s's github_inventory parts
// for which ok holds. A name listed more than once qualifies only if every
// copy does, so a failed duplicate is never hidden by a good one.
func inventoryParts(s *Snapshot, ok func(SourcePart) bool) map[string]bool {
	good := map[string]bool{}
	for _, p := range s.Sources[SourceGitHubInventory].Parts {
		name := strings.ToLower(p.Name)
		prev, seen := good[name]
		good[name] = ok(p) && (!seen || prev)
	}
	maps.DeleteFunc(good, func(_ string, v bool) bool { return !v })
	return good
}

// mergedKnown reports whether the snapshot can show merges at all. Merge
// times come from the inventory's closed-recent sets, so it needs the
// authenticated user among People (otherwise their own merges were never
// searched) and every person's closed-recent set fresh, untruncated and
// complete: a stale set lacks the merges since it was fetched. Collection
// also requires each set to cover the counts window and to have been
// fetched since PRs last left the person's open-authored set (addInventory);
// Counts.Merged documents the whole rule.
func mergedKnown(s *Snapshot) bool {
	if !slices.ContainsFunc(s.People, func(p Person) bool { return p.Me }) {
		return false
	}
	fresh := inventoryParts(s, func(p SourcePart) bool { return p.State == Fresh && !p.Truncated && !p.Incomplete })
	return !slices.ContainsFunc(s.People, func(p Person) bool {
		return !fresh[inventoryPart(p.Login, InventoryClosedRecent)]
	})
}

// inventoryHolders returns, per involvement an inventory search finds, the
// parts that hold or held p: an author's open-authored and closed-recent
// sets, since a PR moves from one to the other when it closes, or a
// requested reviewer's review-requested set. A PR the inventory did not find
// has none. If every part of one group was covered, the inventory would
// have found p then.
func inventoryHolders(p *PRInfo) [][]string {
	if !slices.Contains(p.Sources, PRSourceInventory) {
		return nil
	}
	var out [][]string
	for _, in := range p.Involvement {
		switch in.Role {
		case RoleAuthor:
			out = append(out, []string{inventoryPart(in.Person, InventoryOpenAuthored), inventoryPart(in.Person, InventoryClosedRecent)})
		case RoleReviewRequested:
			out = append(out, []string{inventoryPart(in.Person, InventoryReviewRequested)})
		}
	}
	return out
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
