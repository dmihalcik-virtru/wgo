package dash

import (
	"fmt"
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
