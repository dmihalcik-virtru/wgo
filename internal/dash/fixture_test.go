package dash

import (
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// liveFixture is a small snapshot that exercises every node kind and every
// data state the live page renders: named, ticket-fallback and Ungrouped
// efforts, an attribution conflict, a stack, PRs in each state, fresh, stale,
// unknown and unavailable lookups, and agents with a same-bookmark conflict
// and uncertain liveness.
func liveFixture(now time.Time) *Snapshot {
	day := func(d int) string { return now.AddDate(0, 0, d-13).Format("2006-01-02") }
	var days []string
	for i := 0; i < 14; i++ {
		days = append(days, day(i))
	}
	ws1 := WorkspaceID("/src/wgo", "/src/worktrees/gh-70-dash")
	ws2 := WorkspaceID("/src/wgo", "/src/worktrees/gh-70-snapshot")
	ws3 := WorkspaceID("/src/sdk", "/src/worktrees/WGO-12-fix")
	ws4 := WorkspaceID("/src/web", "/src/web")
	ws5 := WorkspaceID("/src/web", "/src/worktrees/spike")
	bm1, bm2, bm3, bm5 := bookmarkID("/src/wgo", "gh-70-dash-explorer"), bookmarkID("/src/wgo", "gh-70-dash-snapshot"), bookmarkID("/src/sdk", "WGO-12-fix"), bookmarkID("/src/web", "spike")
	pr1, pr2, pr3 := prNodeID("virtru/wgo", 76), prNodeID("virtru/wgo", 75), prNodeID("opentdf/sdk", 12)
	t1, t2 := jiraTicketID("WGO-12"), githubTicketID("virtru/wgo", 70)
	e1, e2 := effortNodeID("gh-70-dash"), effortNodeID("ticket:WGO-12")
	a1, a2, a3 := agentNodeID("claude-1"), agentNodeID("codex-2"), agentNodeID("claude-3")
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	stack := &StackPosition{Position: 2, Size: 2, Bookmarks: []string{"gh-70-dash-snapshot", "gh-70-dash-explorer"}}
	s := &Snapshot{
		Schema: SchemaVersion, GeneratedAt: now, Days: 14,
		Sources: map[string]SourceStatus{
			"jj":            {State: Fresh, Fresh: 4, Error: 1},
			"agents":        {State: Fresh, Fresh: 3},
			"github_prs":    {State: Stale, Fresh: 2, Stale: 1, Unknown: 1, OldestFetch: ago(3 * time.Hour)},
			"github_issues": {State: Unknown, Unknown: 1},
			"jira":          {State: Fresh, Fresh: 1},
		},
		Diagnostics: []string{"jj failed in /src/web: repository is locked"},
		Nodes: []Node{
			{ID: e1, Kind: KindEffort, Label: "gh-70 dash", Effort: &EffortInfo{Key: "gh-70-dash", Group: GroupEffort, Description: "Live dashboard for local work", Source: "plan"}},
			{ID: e2, Kind: KindEffort, Label: "WGO-12", Effort: &EffortInfo{Key: "ticket:WGO-12", Group: GroupTicket}},
			{ID: UngroupedID, Kind: KindEffort, Label: "Ungrouped", Effort: &EffortInfo{Key: "ungrouped", Group: GroupUngrouped,
				Conflicts: []EffortConflict{{WorkspaceID: ws5, ClaimedBy: []string{"gh-70-dash", "review-graph"}}}}},
			{ID: ws1, Kind: KindWorkspace, Label: "wgo/gh-70-dash", Workspace: &WorkspaceInfo{Slug: "gh-70-dash", Repo: "wgo", RepoSlug: "virtru/wgo", Path: "/src/worktrees/gh-70-dash", MainClone: "/src/wgo",
				Bookmark: "gh-70-dash-explorer", ChangeID: "kwrunqxk", Description: "feat(dash): live explorer", Stack: stack, Changes: []string{"a", "b", "c"}, LastActivity: ago(20 * time.Minute), EffortID: e1}},
			{ID: ws2, Kind: KindWorkspace, Label: "wgo/gh-70-snapshot", Workspace: &WorkspaceInfo{Slug: "gh-70-snapshot", Repo: "wgo", RepoSlug: "virtru/wgo", Path: "/src/worktrees/gh-70-snapshot", MainClone: "/src/wgo",
				Bookmark: "gh-70-dash-snapshot", Stack: &StackPosition{Position: 1, Size: 2, Bookmarks: stack.Bookmarks}, LastActivity: ago(26 * time.Hour), EffortID: e1}},
			{ID: ws3, Kind: KindWorkspace, Label: "sdk/WGO-12-fix", Workspace: &WorkspaceInfo{Slug: "WGO-12-fix", Repo: "sdk", RepoSlug: "opentdf/sdk", Path: "/src/worktrees/WGO-12-fix", MainClone: "/src/sdk",
				Bookmark: "WGO-12-fix", Annotation: "waiting on review", LastActivity: ago(4 * 24 * time.Hour), EffortID: e2}},
			{ID: ws4, Kind: KindWorkspace, Label: "web", Workspace: &WorkspaceInfo{Slug: "web", Repo: "web", Path: "/src/web", MainClone: "/src/web", IsMain: true, EffortID: UngroupedID, Error: "repository is locked"}},
			{ID: ws5, Kind: KindWorkspace, Label: "web/spike", Workspace: &WorkspaceInfo{Slug: "spike", Repo: "web", Path: "/src/worktrees/spike", MainClone: "/src/web", Bookmark: "spike", LastActivity: ago(2 * time.Hour), EffortID: UngroupedID}},
			{ID: bm1, Kind: KindBookmark, Label: "gh-70-dash-explorer", Bookmark: &BookmarkInfo{Name: "gh-70-dash-explorer", Repo: "virtru/wgo", PRLookup: Fresh, PRFetchedAt: ago(time.Minute), PRCount: 1}},
			{ID: bm2, Kind: KindBookmark, Label: "gh-70-dash-snapshot", Bookmark: &BookmarkInfo{Name: "gh-70-dash-snapshot", Repo: "virtru/wgo", PRLookup: Fresh, PRFetchedAt: ago(time.Minute), PRCount: 1}},
			{ID: bm3, Kind: KindBookmark, Label: "WGO-12-fix", Bookmark: &BookmarkInfo{Name: "WGO-12-fix", Repo: "opentdf/sdk", PRLookup: Stale, PRFetchedAt: ago(3 * time.Hour), PRCount: 1}},
			{ID: bm5, Kind: KindBookmark, Label: "spike", Bookmark: &BookmarkInfo{Name: "spike", Repo: "", PRLookup: Unknown}},
			{ID: pr1, Kind: KindPR, Label: "virtru/wgo#76", PR: &PRInfo{Number: 76, Repo: "virtru/wgo", URL: "https://github.com/virtru/wgo/pull/76", Title: "feat(dash): live explorer", State: "open", IsDraft: true,
				Checks: "PENDING", ReviewersKnown: true, UpdatedAt: ago(30 * time.Minute), Freshness: Fresh, BookmarkID: bm1}},
			{ID: pr2, Kind: KindPR, Label: "virtru/wgo#75", PR: &PRInfo{Number: 75, Repo: "virtru/wgo", URL: "https://github.com/virtru/wgo/pull/75", Title: "feat(dash): snapshot API", State: "open",
				ReviewDecision: "REVIEW_REQUIRED", Checks: "SUCCESS", RequestedReviewers: []string{"octocat"}, ReviewersKnown: true, UpdatedAt: ago(2 * time.Hour), Freshness: Fresh, BookmarkID: bm2}},
			{ID: pr3, Kind: KindPR, Label: "opentdf/sdk#12", PR: &PRInfo{Number: 12, Repo: "opentdf/sdk", URL: "https://github.com/opentdf/sdk/pull/12", Title: "fix: retry manifests", State: "merged",
				ReviewDecision: "APPROVED", Checks: "FAILURE", ReviewersKnown: false, Freshness: Stale, BookmarkID: bm3}},
			{ID: t1, Kind: KindTicket, Label: "WGO-12", Ticket: &TicketInfo{Key: "WGO-12", System: "jira", Status: "In Review", Title: "Retry manifest fetches", URL: "https://example.atlassian.net/browse/WGO-12", Freshness: Fresh}},
			{ID: t2, Kind: KindTicket, Label: "virtru/wgo#70", Ticket: &TicketInfo{Key: "virtru/wgo#70", System: "github", URL: "https://github.com/virtru/wgo/issues/70", Freshness: Unknown}},
			{ID: a1, Kind: KindAgent, Label: "claude", Agent: &AgentInfo{SessionID: "claude-1", Tool: "claude", Status: "working", Liveness: "active", Source: "hook", Branch: "gh-70-dash-explorer",
				Path: "/src/worktrees/gh-70-dash", WorkspaceID: ws1, StartTime: ago(3 * time.Hour), LastActivity: ago(2 * time.Minute), Conflict: true, ConflictsWith: []string{"codex-2"}, EffortID: e1}},
			{ID: a2, Kind: KindAgent, Label: "codex", Agent: &AgentInfo{SessionID: "codex-2", Tool: "codex", Status: "idle", Liveness: "live", Branch: "gh-70-dash-explorer",
				Path: "/src/worktrees/gh-70-dash", WorkspaceID: ws1, StartTime: ago(time.Hour), LastActivity: ago(8 * time.Minute), Conflict: true, ConflictsWith: []string{"claude-1"}, EffortID: e1}},
			{ID: a3, Kind: KindAgent, Label: "claude", Agent: &AgentInfo{SessionID: "claude-3", Tool: "claude", Status: "waiting", Liveness: "uncertain", Branch: "spike",
				Path: "/src/worktrees/spike", WorkspaceID: ws5, StartTime: ago(30 * time.Hour), LastActivity: ago(25 * time.Hour), EffortID: UngroupedID}},
		},
		Edges: []Edge{
			{e1, ws1, EdgeContains}, {e1, ws2, EdgeContains}, {e2, ws3, EdgeContains}, {UngroupedID, ws4, EdgeContains}, {UngroupedID, ws5, EdgeContains},
			{ws1, bm1, EdgeOn}, {ws2, bm2, EdgeOn}, {ws3, bm3, EdgeOn}, {ws5, bm5, EdgeOn},
			{bm1, pr1, EdgePR}, {bm2, pr2, EdgePR}, {bm3, pr3, EdgePR},
			{pr1, t2, EdgeTicket}, {pr2, t2, EdgeTicket}, {pr3, t1, EdgeTicket},
			{e1, a1, EdgeRuns}, {e1, a2, EdgeRuns}, {UngroupedID, a3, EdgeRuns},
			{a1, ws1, EdgeWorksIn}, {a2, ws1, EdgeWorksIn}, {a3, ws5, EdgeWorksIn},
		},
		Counts: Counts{
			Days: days,
			Activity: map[string][]int{
				e1:          {0, 0, 1, 3, 0, 0, 2, 5, 4, 0, 0, 6, 8, 3},
				e2:          {2, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0},
				UngroupedID: {0, 0, 0, 0, 1, 0, 0, 0, 0, 2, 0, 0, 1, 1},
			},
			PRStates: map[string]map[string]int{e1: {"draft": 1, "open": 1}, e2: {"merged": 1}, UngroupedID: {"unknown": 1}},
			Agents:   map[string]map[string]int{e1: {"active": 1, "live": 1}, UngroupedID: {"uncertain": 1}},
		},
	}
	return s
}

func fixtureDash(t testing.TB) *Dash {
	t.Helper()
	d, err := Open(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Publish(liveFixture(time.Now())); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestServeFixture serves the fixture for manual and headless-browser checks
// when WGO_DASH_FIXTURE_ADDR is set (for example 127.0.0.1:8799); otherwise
// it is skipped. It never runs in CI and needs no browser itself.
func TestServeFixture(t *testing.T) {
	addr := os.Getenv("WGO_DASH_FIXTURE_ADDR")
	if addr == "" {
		t.Skip("set WGO_DASH_FIXTURE_ADDR to serve the fixture")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	runs := t.TempDir()
	writeReviewRun(t, runs, "2026-test", time.Now().Add(-time.Hour))
	ix := NewReviewIndex(runs)
	if err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	t.Logf("serving the fixture on http://%s/", addr)
	h, err := NewHandler(HandlerOptions{Source: fixtureDash(t), Host: addr, Reviews: ix})
	if err != nil {
		t.Fatal(err)
	}
	_ = http.Serve(ln, h)
}
