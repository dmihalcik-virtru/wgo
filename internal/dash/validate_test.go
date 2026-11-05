package dash

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

const (
	tEffort = "effort:e1"
	tWS     = "ws-0123456789abcdef"
	tBM     = "bm-0123456789abcdef"
	tPR     = "pr:acme/widgets#1"
	tTicket = "ticket:jira:WGO-1"
	tAgent  = "agent:claude-aaaaaaaaaaaa"
)

// validSnapshot is a small hand-built snapshot using every node and edge kind.
func validSnapshot() *Snapshot {
	days := []string{"2026-10-08", "2026-10-09", "2026-10-10"}
	return &Snapshot{
		Schema: SchemaVersion,
		Days:   len(days),
		Nodes: []Node{
			{ID: tEffort, Kind: KindEffort, Label: "e1", Effort: &EffortInfo{Key: "e1", Group: GroupEffort}},
			{ID: UngroupedID, Kind: KindEffort, Label: "Ungrouped", Effort: &EffortInfo{Key: GroupUngrouped, Group: GroupUngrouped,
				Conflicts: []EffortConflict{{WorkspaceID: tWS, ClaimedBy: []string{"a", "b"}}}}},
			{ID: tWS, Kind: KindWorkspace, Label: "ws", Workspace: &WorkspaceInfo{Slug: "ws", EffortID: tEffort}},
			{ID: tBM, Kind: KindBookmark, Label: "WGO-1-x", Bookmark: &BookmarkInfo{Name: "WGO-1-x", PRLookup: Fresh, PRCount: 1}},
			{ID: tPR, Kind: KindPR, Label: "#1", PR: &PRInfo{Number: 1, State: "open", Freshness: Fresh, BookmarkID: tBM}},
			{ID: tTicket, Kind: KindTicket, Label: "WGO-1", Ticket: &TicketInfo{Key: "WGO-1", System: "jira", Freshness: Unknown}},
			{ID: tAgent, Kind: KindAgent, Label: "claude", Agent: &AgentInfo{SessionID: "claude-aaaaaaaaaaaa", Liveness: LivenessActive, WorkspaceID: tWS, EffortID: tEffort}},
			{ID: "agent:codex-bbbbbbbbbbbb", Kind: KindAgent, Label: "codex", Agent: &AgentInfo{SessionID: "codex-bbbbbbbbbbbb", Liveness: LivenessUncertain, EffortID: UngroupedID}},
		},
		Edges: []Edge{
			{tEffort, tWS, EdgeContains},
			{tWS, tBM, EdgeOn},
			{tBM, tPR, EdgePR},
			{tPR, tTicket, EdgeTicket},
			{tBM, tTicket, EdgeTicket},
			{tEffort, tAgent, EdgeRuns},
			{tAgent, tWS, EdgeWorksIn},
		},
		Counts: Counts{
			Days:     days,
			Activity: map[string][]int{tEffort: {0, 1, 2}, UngroupedID: {0, 0, 0}},
			PRStates: map[string]map[string]int{tEffort: {"open": 1}, UngroupedID: {}},
			Agents:   map[string]map[string]int{tEffort: {"active": 1}, UngroupedID: {"uncertain": 1}},
		},
	}
}

func TestValidateValidSnapshot(t *testing.T) {
	if err := validSnapshot().Validate(); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
}

func TestValidateRules(t *testing.T) {
	node := func(s *Snapshot, id string) *Node { return s.Node(id) }
	cases := []struct {
		name   string
		mutate func(s *Snapshot)
		want   string
	}{
		{"schema", func(s *Snapshot) { s.Schema = 99 }, "schema"},
		{"empty node id", func(s *Snapshot) { node(s, tTicket).ID = "" }, "node id"},
		{"duplicate node id", func(s *Snapshot) { s.Nodes = append(s.Nodes, s.Nodes[5]) }, "duplicate"},
		{"unknown node kind", func(s *Snapshot) { node(s, tTicket).Kind = "planet" }, "unknown kind"},
		{"kind info mismatch", func(s *Snapshot) { node(s, tTicket).Kind = KindPR }, "node kind"},
		{"two infos", func(s *Snapshot) { node(s, tTicket).Effort = &EffortInfo{} }, "exactly"},
		{"no info", func(s *Snapshot) { node(s, tTicket).Ticket = nil }, "exactly"},
		{"bad liveness", func(s *Snapshot) { node(s, tAgent).Agent.Liveness = "gone" }, "liveness"},
		{"missing edge source", func(s *Snapshot) { s.Edges[0].Source = "effort:nope" }, "edge source"},
		{"missing edge target", func(s *Snapshot) { s.Edges[0].Target = "ws-ffffffffffffffff" }, "edge target"},
		{"unknown edge kind", func(s *Snapshot) { s.Edges[0].Kind = "likes" }, "edge kind"},
		{"contains endpoints", func(s *Snapshot) { s.Edges[0] = Edge{tEffort, tBM, EdgeContains} }, "edge endpoints"},
		{"on endpoints", func(s *Snapshot) { s.Edges[1] = Edge{tBM, tWS, EdgeOn} }, "edge endpoints"},
		{"pr endpoints", func(s *Snapshot) { s.Edges[2] = Edge{tWS, tPR, EdgePR} }, "edge endpoints"},
		{"ticket endpoints", func(s *Snapshot) { s.Edges[3] = Edge{tWS, tTicket, EdgeTicket} }, "edge endpoints"},
		{"runs endpoints", func(s *Snapshot) { s.Edges[5] = Edge{tWS, tAgent, EdgeRuns} }, "edge endpoints"},
		{"works_in endpoints", func(s *Snapshot) { s.Edges[6] = Edge{tAgent, tBM, EdgeWorksIn} }, "edge endpoints"},
		{"days length", func(s *Snapshot) { s.Days = 14 }, "counts days"},
		{"activity length", func(s *Snapshot) { s.Counts.Activity[tEffort] = []int{1} }, "counts activity"},
		{"activity key", func(s *Snapshot) { s.Counts.Activity[tWS] = []int{0, 0, 0} }, "counts key"},
		{"pr_states key", func(s *Snapshot) { s.Counts.PRStates["effort:nope"] = map[string]int{} }, "counts key"},
		{"agents key", func(s *Snapshot) { s.Counts.Agents[tAgent] = map[string]int{} }, "counts key"},
		{"workspace effort", func(s *Snapshot) { node(s, tWS).Workspace.EffortID = "" }, "effort_id"},
		{"agent effort", func(s *Snapshot) { node(s, tAgent).Agent.EffortID = tWS }, "effort_id"},
		{"pr bookmark", func(s *Snapshot) { node(s, tPR).PR.BookmarkID = "bm-ffffffffffffffff" }, "bookmark_id"},
		{"conflict workspace empty", func(s *Snapshot) { node(s, UngroupedID).Effort.Conflicts[0].WorkspaceID = "" }, "conflict workspace_id"},
		{"agent workspace", func(s *Snapshot) { node(s, tAgent).Agent.WorkspaceID = "ws-ffffffffffffffff" }, "workspace_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSnapshot()
			tc.mutate(s)
			err := s.Validate()
			if err == nil {
				t.Fatal("invalid snapshot accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateAgentWithoutWorkspace(t *testing.T) {
	s := validSnapshot()
	s.Node(tAgent).Agent.WorkspaceID = ""
	s.Edges = s.Edges[:6] // drop works_in
	if err := s.Validate(); err != nil {
		t.Fatalf("a session outside any workspace is valid: %v", err)
	}
}

func TestValidateCapsErrors(t *testing.T) {
	s := validSnapshot()
	for i := range 30 {
		s.Edges = append(s.Edges, Edge{fmt.Sprintf("x%d", i), tWS, EdgeContains})
	}
	err := s.Validate()
	if err == nil {
		t.Fatal("want errors")
	}
	lines := strings.Split(err.Error(), "\n")
	if len(lines) != maxValidateErrors+1 || lines[len(lines)-1] != "and 10 more" {
		t.Fatalf("want %d errors then a summary, got %d: last %q", maxValidateErrors, len(lines), lines[len(lines)-1])
	}
}

const (
	tEffortRepo  = "effort:repo:acme/widgets"
	tPRInventory = "pr:acme/widgets#2"
	tTicketTwo   = "ticket:jira:WGO-2"
	tSprint      = "sprint:7"
)

// validAnalysisSnapshot extends validSnapshot with the analysis additions:
// a repo group tracking an inventory-only PR and a sprint ticket, a sprint,
// PR and ticket facts, merged counts and people.
func validAnalysisSnapshot() *Snapshot {
	s := validSnapshot()
	add, del := 4, 0
	s.Nodes = append(s.Nodes,
		Node{ID: tEffortRepo, Kind: KindEffort, Label: "acme/widgets", Effort: &EffortInfo{Key: "repo:acme/widgets", Group: GroupRepo}},
		Node{ID: tPRInventory, Kind: KindPR, Label: "#2", PR: &PRInfo{Number: 2, State: "open", Freshness: Fresh,
			Author: "carol", Additions: &add, Deletions: &del, Mergeable: MergeUnknown,
			ReviewRequests: []ReviewRequest{{Reviewer: "team:web", Kind: ReviewerTeam}},
			Reviews:        []Review{{Author: "bob", State: ReviewCommented}},
			CheckDetails:   []CheckDetail{{Name: "vendor-hsm-tests", State: CheckFailure}},
			Involvement:    []Involvement{{Person: "alice", Role: RoleReviewRequested}},
			Sources:        []string{PRSourceInventory}}},
		Node{ID: tTicketTwo, Kind: KindTicket, Label: "WGO-2", Ticket: &TicketInfo{Key: "WGO-2", System: "jira", Freshness: Fresh,
			StatusCategory: StatusToDo, Sprints: []SprintRef{{ID: 7, Name: "Sprint 7", State: SprintActive}}}},
		Node{ID: tSprint, Kind: KindSprint, Label: "Sprint 7", Sprint: &SprintInfo{ID: 7, Name: "Sprint 7", State: SprintActive, Freshness: Fresh}},
	)
	s.Edges = append(s.Edges,
		Edge{tEffortRepo, tPRInventory, EdgeTracks},
		Edge{tEffortRepo, tTicketTwo, EdgeTracks},
		Edge{tTicketTwo, tSprint, EdgeInSprint},
	)
	s.Counts.Activity[tEffortRepo] = []int{0, 0, 0}
	s.Counts.PRStates[tEffortRepo] = map[string]int{"open": 1}
	s.Counts.Agents[tEffortRepo] = map[string]int{}
	s.Counts.Merged = map[string][]int{tEffort: {0, 0, 0}, UngroupedID: {0, 0, 0}, tEffortRepo: {0, 1, 0}}
	s.People = []Person{{Login: "alice", Me: true}, {Login: "bob"}}
	s.Sources = map[string]SourceStatus{SourceGitHubInventory: {State: Fresh, Fresh: 2, Parts: []SourcePart{
		{Name: "alice/closed-recent", State: Fresh},
		{Name: "bob/closed-recent", State: Fresh},
	}}}
	return s
}

func TestValidateAnalysisSnapshot(t *testing.T) {
	if err := validAnalysisSnapshot().Validate(); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
}

func TestValidateAnalysisRules(t *testing.T) {
	node := func(s *Snapshot, id string) *Node { return s.Node(id) }
	pr := func(s *Snapshot) *PRInfo { return s.Node(tPRInventory).PR }
	dropEdge := func(s *Snapshot, e Edge) {
		s.Edges = slices.DeleteFunc(s.Edges, func(x Edge) bool { return x == e })
	}
	cases := []struct {
		name   string
		mutate func(s *Snapshot)
		want   string
	}{
		{"orphan pr", func(s *Snapshot) { dropEdge(s, Edge{tEffortRepo, tPRInventory, EdgeTracks}) }, "neither a bookmark_id nor an incoming tracks edge"},
		{"bookmark_id without pr edge", func(s *Snapshot) { dropEdge(s, Edge{tBM, tPR, EdgePR}) }, "has no pr edge"},
		{"orphan ticket", func(s *Snapshot) { dropEdge(s, Edge{tEffortRepo, tTicketTwo, EdgeTracks}) }, "neither an incoming ticket edge nor a tracks edge"},
		{"tracks endpoints", func(s *Snapshot) { s.Edges = append(s.Edges, Edge{tWS, tPRInventory, EdgeTracks}) }, "edge endpoints"},
		{"tracks to a sprint", func(s *Snapshot) { s.Edges = append(s.Edges, Edge{tEffortRepo, tSprint, EdgeTracks}) }, "edge endpoints"},
		{"in_sprint endpoints", func(s *Snapshot) { s.Edges = append(s.Edges, Edge{tPR, tSprint, EdgeInSprint}) }, "edge endpoints"},
		{"sprint without info", func(s *Snapshot) { node(s, tSprint).Sprint = nil }, "exactly"},
		{"sprint without id", func(s *Snapshot) { node(s, tSprint).Sprint.ID = 0 }, "no sprint id"},
		{"sprint state", func(s *Snapshot) { node(s, tSprint).Sprint.State = "running" }, `state "running"`},
		{"mergeable", func(s *Snapshot) { pr(s).Mergeable = "yes" }, `mergeable "yes"`},
		{"review request kind", func(s *Snapshot) { pr(s).ReviewRequests[0].Kind = "group" }, "review request kind"},
		{"review request reviewer", func(s *Snapshot) { pr(s).ReviewRequests[0].Reviewer = "" }, "no reviewer"},
		{"review state", func(s *Snapshot) { pr(s).Reviews[0].State = "PENDING" }, "review state"},
		{"check state", func(s *Snapshot) { pr(s).CheckDetails[0].State = "error" }, "check state"},
		{"check name", func(s *Snapshot) { pr(s).CheckDetails[0].Name = "" }, "check with no name"},
		{"too many checks", func(s *Snapshot) {
			pr(s).CheckDetails = slices.Repeat([]CheckDetail{{Name: "build", State: CheckSuccess}}, MaxCheckDetails+1)
		}, "more than 100"},
		{"involvement role", func(s *Snapshot) { pr(s).Involvement[0].Role = "assignee" }, "involvement role"},
		{"involvement person", func(s *Snapshot) { pr(s).Involvement[0].Person = "" }, "involvement with no person"},
		{"pr source", func(s *Snapshot) { pr(s).Sources = []string{"search"} }, "source"},
		// A reported entry must say what it is; only optional facts may be
		// empty (unknown).
		{"empty sprint state", func(s *Snapshot) { node(s, tSprint).Sprint.State = "" }, "has no state"},
		{"empty review request kind", func(s *Snapshot) { pr(s).ReviewRequests[0].Kind = "" }, "no review request kind"},
		{"empty review state", func(s *Snapshot) { pr(s).Reviews[0].State = "" }, "no review state"},
		{"review author", func(s *Snapshot) { pr(s).Reviews[0].Author = "" }, "review with no author"},
		{"empty check state", func(s *Snapshot) { pr(s).CheckDetails[0].State = "" }, "no check state"},
		{"empty involvement role", func(s *Snapshot) { pr(s).Involvement[0].Role = "" }, "no involvement role"},
		{"empty pr source", func(s *Snapshot) { pr(s).Sources = []string{""} }, "no source"},
		{"unsorted pr sources", func(s *Snapshot) { pr(s).Sources = []string{PRSourceInventory, PRSourceBookmark} }, "sorted without repeats"},
		{"repeated pr source", func(s *Snapshot) { pr(s).Sources = []string{PRSourceInventory, PRSourceInventory} }, "sorted without repeats"},
		{"bookmark source without bookmark_id", func(s *Snapshot) {
			pr(s).Sources = []string{PRSourceBookmark, PRSourceInventory}
		}, "no bookmark_id"},
		{"bookmark-less pr not from the inventory", func(s *Snapshot) { pr(s).Sources = []string{PRSourceBookmark} }, "no bookmark_id"},
		{"source state", func(s *Snapshot) { s.Sources[SourceGitHubInventory] = SourceStatus{State: "ok"} }, `state "ok"`},
		{"part state", func(s *Snapshot) { s.Sources[SourceGitHubInventory].Parts[1].State = "" }, "has no state"},
		{"part name", func(s *Snapshot) { s.Sources[SourceGitHubInventory].Parts[1].Name = "" }, "has no name"},
		{"duplicate part", func(s *Snapshot) { s.Sources[SourceGitHubInventory].Parts[1].Name = "Alice/closed-recent" }, "twice"},
		{"merged without me", func(s *Snapshot) { s.People[0].Me = false }, "counts merged: present"},
		{"merged with stale closed-recent", func(s *Snapshot) { s.Sources[SourceGitHubInventory].Parts[1].State = Stale }, "counts merged: present"},
		{"in_sprint to an unlisted sprint", func(s *Snapshot) { node(s, tTicketTwo).Ticket.Sprints[0].ID = 8 }, "a sprint it does not list"},
		{"listed sprint without in_sprint", func(s *Snapshot) { dropEdge(s, Edge{tTicketTwo, tSprint, EdgeInSprint}) }, "no in_sprint edge"},
		{"status category", func(s *Snapshot) { node(s, tTicketTwo).Ticket.StatusCategory = "blocked" }, "status_category"},
		{"ticket sprint state", func(s *Snapshot) { node(s, tTicketTwo).Ticket.Sprints[0].State = "open" }, "sprint state"},
		{"ticket sprint id", func(s *Snapshot) { node(s, tTicketTwo).Ticket.Sprints[0].ID = 0 }, "sprint with no id"},
		{"merged length", func(s *Snapshot) { s.Counts.Merged[tEffortRepo] = []int{1} }, "counts merged"},
		{"merged key", func(s *Snapshot) { s.Counts.Merged[tPR] = []int{0, 0, 0} }, "merged key"},
		{"person without login", func(s *Snapshot) { s.People[1].Login = "" }, "has no login"},
		{"duplicate person", func(s *Snapshot) { s.People[1].Login = "Alice" }, "duplicate login"},
		{"two of me", func(s *Snapshot) { s.People[1].Me = true }, "marked me"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validAnalysisSnapshot()
			tc.mutate(s)
			err := s.Validate()
			if err == nil {
				t.Fatal("invalid snapshot accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestValidateInventoryPRMayAlsoHaveABookmark covers gh-97 attachment case 1:
// an inventory-only PR on a workspace bookmark keeps bookmark_id and its pr
// edge and needs no tracks edge.
func TestValidateInventoryPRMayAlsoHaveABookmark(t *testing.T) {
	s := validAnalysisSnapshot()
	s.Node(tPRInventory).PR.BookmarkID = tBM
	s.Edges = slices.DeleteFunc(s.Edges, func(e Edge) bool { return e.Target == tPRInventory })
	s.Edges = append(s.Edges, Edge{tBM, tPRInventory, EdgePR})
	if err := s.Validate(); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
}
