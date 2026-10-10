package dash

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/store"
)

// TestCollectorEffortConflict: two state efforts claiming one workspace's
// bookmark land the workspace in Ungrouped, recorded as a conflict that names
// the workspace by ID and both claimants by effort ID.
func TestCollectorEffortConflict(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	state := store.NewState()
	state.Efforts = map[string]store.Effort{
		"eff-one": {Name: "One", Branches: []string{"acme/widgets:WGO-5-b"}},
		"eff-two": {Name: "Two", Branches: []string{"acme/widgets:WGO-5-b"}},
	}
	c := f.collector(t, now, memSource{state: state})
	s, _, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("invalid snapshot: %v", err)
	}
	cf := s.Node(UngroupedID).Effort.Conflicts
	if len(cf) != 1 {
		t.Fatalf("want one conflict, got %+v (diagnostics %v)", cf, s.Diagnostics)
	}
	if cf[0].WorkspaceID != WorkspaceID(f.repo, f.ws) {
		t.Fatalf("conflict workspace %s, want %s", cf[0].WorkspaceID, WorkspaceID(f.repo, f.ws))
	}
	if !slices.Equal(cf[0].ClaimedBy, []string{"eff-one", "eff-two"}) {
		t.Fatalf("claimed by %v", cf[0].ClaimedBy)
	}
	if got := s.Node(cf[0].WorkspaceID).Workspace.EffortID; got != UngroupedID {
		t.Fatalf("a conflicted workspace is ungrouped, got %s", got)
	}
}

// TestCollectorAgentConflict: two recent sessions on one repo and branch flag
// each other.
func TestCollectorAgentConflict(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	mk := func(id, tool, path string) store.AgentSession {
		return store.AgentSession{
			ID: id, Tool: tool, Status: "working", Source: store.SourceExplicit,
			WorktreePath: path, RepoPath: f.repo, Branch: "WGO-5-b",
			StartTime: now.Add(-time.Hour), LastActivity: now.Add(-time.Minute),
		}
	}
	a, b := mk("claude-aaaaaaaaaaaa", "claude", f.ws), mk("codex-bbbbbbbbbbbb", "codex", f.repo)
	state := store.NewState()
	state.AgentSessions = map[string]store.AgentSession{a.ID: a, b.ID: b}
	c := f.collector(t, now, memSource{state: state})
	s, _, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("invalid snapshot: %v", err)
	}
	na, nb := s.Node(agentNodeID(a.ID)), s.Node(agentNodeID(b.ID))
	if na == nil || nb == nil {
		t.Fatal("agent nodes missing")
	}
	if !na.Agent.Conflict || !slices.Equal(na.Agent.ConflictsWith, []string{b.ID}) {
		t.Fatalf("first agent: %+v", na.Agent)
	}
	if !nb.Agent.Conflict || !slices.Equal(nb.Agent.ConflictsWith, []string{a.ID}) {
		t.Fatalf("second agent: %+v", nb.Agent)
	}
}
