package dash

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/effort"
	"github.com/virtru/wgo/internal/issuecache"
	"github.com/virtru/wgo/internal/jiracache"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/prcache"
	"github.com/virtru/wgo/internal/store"
	"github.com/virtru/wgo/models"
)

// These tests build a localState by hand, so they exercise assemble without
// jj, discovery or the store.

const (
	asmMain   = "/w/acme/widgets"
	asmOrigin = "https://github.com/acme/widgets.git"
	asmSlug   = "acme/widgets"
)

var asmZone = time.FixedZone("test", -7*3600)

// asmFixture is a hand-built localState plus the inputs of attribution.
type asmFixture struct {
	t        *testing.T
	c        *Collector
	ls       *localState
	efforts  map[string]effort.MergedEffort
	override map[string]string // workspace root -> theme
	extra    []effort.WorkspaceInfo
}

func newAsm(t *testing.T) *asmFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, asmZone)
	a := &asmFixture{
		t:        t,
		c:        NewCollector(Config{Days: 3, Now: func() time.Time { return now }, PRTTL: time.Hour, JiraTTL: time.Hour, IssueTTL: time.Hour}),
		efforts:  map[string]effort.MergedEffort{},
		override: map[string]string{},
		ls: &localState{
			at:          now,
			days:        3,
			origins:     map[string]string{asmMain: asmOrigin},
			ghSlugs:     map[string]string{asmMain: asmSlug},
			noGitHub:    map[string]string{},
			annotations: map[string]string{},
			sources:     map[string]SourceStatus{},
			conflicts:   map[string][]string{},
			clones:      []effort.MainCloneInfo{{Path: asmMain, Name: "widgets", Owner: "acme", Repo: "widgets"}},
		},
	}
	return a
}

// effort registers a merged effort claiming the given bookmarks of the clone.
func (a *asmFixture) effort(id, name string, bookmarks ...string) {
	var refs []string
	for _, b := range bookmarks {
		refs = append(refs, "widgets:"+b)
	}
	a.efforts[id] = effort.MergedEffort{ID: id, Name: name, Description: name + " description", Branches: refs, Source: effort.SourceState}
}

// ws adds a healthy workspace at root on bookmark.
func (a *asmFixture) ws(root, bookmark string, changes ...jj.Change) *wsData {
	w := &wsData{
		id:        WorkspaceID(asmMain, root),
		root:      root,
		canonical: canonicalPath(root),
		mainClone: asmMain,
		isMain:    root == asmMain,
		bookmark:  bookmark,
		current:   jj.Change{ChangeID: "wc-" + filepath.Base(root), AuthorTimestamp: a.ls.at},
		changes:   changes,
	}
	a.ls.workspaces = append(a.ls.workspaces, w)
	return w
}

func (a *asmFixture) session(id string, live store.AgentLiveness, w *wsData, branch, theme string) {
	s := store.AgentSession{ID: id, Tool: "claude", Status: "working", Source: store.SourceExplicit,
		Branch: branch, ThemeID: theme, RepoPath: asmMain, StartTime: a.ls.at.Add(-time.Hour), LastActivity: a.ls.at}
	if w != nil {
		s.WorktreePath = w.root
	} else {
		s.WorktreePath = "/elsewhere/" + id
	}
	a.ls.sessions = append(a.ls.sessions, store.ObservedSession{AgentSession: s, Liveness: live})
}

// build attributes the workspaces and assembles, then validates.
func (a *asmFixture) build() (*Snapshot, []Job) {
	a.t.Helper()
	var infos []effort.WorkspaceInfo
	for _, w := range a.ls.workspaces {
		infos = append(infos, effort.WorkspaceInfo{Path: w.root, MainRepoPath: w.mainClone, Bookmark: w.bookmark})
	}
	infos = append(infos, a.extra...)
	a.ls.attribution = effort.AttributeWorkspaces(infos, a.ls.clones, a.efforts, func(p string) string { return a.override[p] })
	s, jobs := a.c.assemble(a.ls)
	if err := s.Validate(); err != nil {
		a.t.Fatalf("assembled snapshot is invalid: %v", err)
	}
	return s, jobs
}

func change(id string, at time.Time) jj.Change {
	return jj.Change{ChangeID: id, Description: "change " + id, AuthorTimestamp: at}
}

func hasEdge(s *Snapshot, src, dst string, kind EdgeKind) bool {
	return slices.Contains(s.Edges, Edge{Source: src, Target: dst, Kind: kind})
}

func countEdges(s *Snapshot, kind EdgeKind) int {
	n := 0
	for _, e := range s.Edges {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func TestAssembleEffortGrouping(t *testing.T) {
	a := newAsm(t)
	a.effort("e1", "Login", "feat-login")
	main := a.ws(asmMain, "feat-login")
	tick := a.ws("/w/acme/widgets-two", "gh-70-x")
	plain := a.ws("/w/acme/widgets-three", "plain")
	themed := a.ws("/w/acme/widgets-four", "other")
	a.override[themed.root] = "Made Up Theme"
	s, _ := a.build()

	effortOf := func(w *wsData) *Node { return s.Node(s.Node(w.id).Workspace.EffortID) }
	if n := effortOf(main); n.ID != effortNodeID("e1") || n.Label != "Login" || n.Effort.Group != GroupEffort ||
		n.Effort.Description != "Login description" || n.Effort.Source != "state" {
		t.Fatalf("effort group: %+v %+v", n, n.Effort)
	}
	if n := effortOf(tick); n.ID != effortNodeID("ticket-gh-70") || n.Label != "GH-70" || n.Effort.Group != GroupTicket {
		t.Fatalf("ticket group: %+v %+v", n, n.Effort)
	}
	if n := effortOf(themed); n.ID != effortNodeID("Made Up Theme") || n.Label != "Made Up Theme" || n.Effort.Group != GroupTheme {
		t.Fatalf("theme group: %+v %+v", n, n.Effort)
	}
	if n := effortOf(plain); n.ID != UngroupedID || n.Label != "Ungrouped" || n.Effort.Group != GroupUngrouped {
		t.Fatalf("ungrouped: %+v %+v", n, n.Effort)
	}
	for _, w := range []*wsData{main, tick, plain, themed} {
		if !hasEdge(s, s.Node(w.id).Workspace.EffortID, w.id, EdgeContains) {
			t.Fatalf("no contains edge into %s", w.root)
		}
	}
	if countEdges(s, EdgeContains) != 4 {
		t.Fatalf("want 4 contains edges, got %d", countEdges(s, EdgeContains))
	}
}

func TestAssembleEdgesOnePerKind(t *testing.T) {
	a := newAsm(t)
	a.effort("e1", "Auth", "WGO-5-a", "WGO-6-b")
	withPR := a.ws(asmMain, "WGO-5-a")
	noPR := a.ws("/w/acme/widgets-two", "WGO-6-b")
	a.session("claude-1", store.LivenessActive, withPR, "WGO-5-a", "")
	if err := prcache.Write(asmOrigin, asmMain, "WGO-5-a", []models.PRRef{{Number: 12, State: "OPEN", URL: "https://github.com/acme/widgets/pull/12"}}); err != nil {
		t.Fatal(err)
	}
	s, _ := a.build()

	e1 := effortNodeID("e1")
	bmA, bmB := bookmarkID(asmMain, "WGO-5-a"), bookmarkID(asmMain, "WGO-6-b")
	pr := prNodeID(asmSlug, 12)
	agent := agentNodeID("claude-1")
	want := []Edge{
		{e1, withPR.id, EdgeContains}, {e1, noPR.id, EdgeContains},
		{withPR.id, bmA, EdgeOn}, {noPR.id, bmB, EdgeOn},
		{bmA, pr, EdgePR},
		{pr, jiraTicketID("WGO-5"), EdgeTicket},  // ticket hangs off the PR...
		{bmB, jiraTicketID("WGO-6"), EdgeTicket}, // ...or off a bookmark with none
		{e1, agent, EdgeRuns}, {agent, withPR.id, EdgeWorksIn},
	}
	for _, e := range want {
		if !hasEdge(s, e.Source, e.Target, e.Kind) {
			t.Errorf("missing edge %+v", e)
		}
	}
	if len(s.Edges) != len(want) {
		t.Errorf("want exactly %d edges, got %d: %+v", len(want), len(s.Edges), s.Edges)
	}
	if hasEdge(s, bmA, jiraTicketID("WGO-5"), EdgeTicket) {
		t.Error("a bookmark with PRs must link its ticket through the PR only")
	}
}

func TestAssembleActivityBucketing(t *testing.T) {
	a := newAsm(t) // window: Oct 8, 9, 10 in asmZone
	day := func(d, h, m, s int) time.Time { return time.Date(2026, 10, d, h, m, s, 0, asmZone) }
	a.effort("e1", "Work", "a", "b")
	a.ws(asmMain, "a",
		change("edge", day(8, 0, 0, 0)),      // first instant of the window
		change("before", day(7, 23, 59, 59)), // just outside
		change("shared", day(9, 12, 0, 0)))
	a.ws("/w/acme/widgets-two", "b",
		change("shared", day(9, 12, 0, 0)), // same change seen from a second workspace
		change("today", day(10, 0, 0, 1)),
		change("future", day(11, 0, 0, 0))) // after the window
	// A workspace of another effort sees the shared change too: de-dup is per effort.
	a.ws("/w/acme/widgets-three", "plain", change("shared", day(9, 12, 0, 0)))
	// An unreadable workspace contributes nothing.
	bad := a.ws("/w/acme/widgets-four", "")
	bad.err = errors.New("boom")
	bad.changes = []jj.Change{change("ghost", day(10, 1, 0, 0))}
	s, _ := a.build()

	if got := strings.Join(s.Counts.Days, ","); got != "2026-10-08,2026-10-09,2026-10-10" {
		t.Fatalf("days: %s", got)
	}
	if got := s.Counts.Activity[effortNodeID("e1")]; !slices.Equal(got, []int{1, 1, 1}) {
		t.Fatalf("e1 activity (edge, shared once, today): %v", got)
	}
	if got := s.Counts.Activity[UngroupedID]; !slices.Equal(got, []int{0, 1, 0}) {
		t.Fatalf("ungrouped activity: %v", got)
	}
}

func TestAssemblePRStateBuckets(t *testing.T) {
	a := newAsm(t)
	names := []string{"b-open", "b-draft", "b-merged", "b-closed", "b-none", "b-miss", "b-fail"}
	a.effort("e1", "Prs", names...)
	for i, n := range names {
		a.ws(fmt.Sprintf("/w/acme/ws%d", i), n)
	}
	put := func(branch string, refs []models.PRRef) {
		if err := prcache.Write(asmOrigin, asmMain, branch, refs); err != nil {
			t.Fatal(err)
		}
	}
	put("b-open", []models.PRRef{{Number: 1, State: "OPEN"}})
	put("b-draft", []models.PRRef{{Number: 2, State: "OPEN", IsDraft: true}})
	put("b-merged", []models.PRRef{{Number: 3, State: "MERGED"}, {Number: 4, State: "MERGED"}})
	put("b-closed", []models.PRRef{{Number: 5, State: "CLOSED"}})
	put("b-none", []models.PRRef{}) // genuinely no PRs
	if err := prcache.WriteFailure(asmOrigin, asmMain, "b-fail", errors.New("gh: HTTP 502")); err != nil {
		t.Fatal(err)
	}
	s, _ := a.build()

	got := s.Counts.PRStates[effortNodeID("e1")]
	want := map[string]int{"open": 1, "draft": 1, "merged": 2, "closed": 1, "none": 1, "unknown": 2}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("bucket %s: got %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected buckets: %v", got)
	}
}

func TestAssembleSharedBookmarkCountsUnderEachEffort(t *testing.T) {
	a := newAsm(t)
	a.effort("e1", "One")
	a.effort("e2", "Two")
	w1 := a.ws(asmMain, "shared")
	w2 := a.ws("/w/acme/widgets-two", "shared")
	a.override[w1.root], a.override[w2.root] = "e1", "e2"
	if err := prcache.Write(asmOrigin, asmMain, "shared", []models.PRRef{{Number: 9, State: "OPEN"}}); err != nil {
		t.Fatal(err)
	}
	s, _ := a.build()

	bm := bookmarkID(asmMain, "shared")
	n := 0
	for _, node := range s.Nodes {
		if node.ID == bm {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("bookmark node must be shared, found %d", n)
	}
	for _, e := range []string{"e1", "e2"} {
		if got := s.Counts.PRStates[effortNodeID(e)]; got["open"] != 1 {
			t.Errorf("%s PR states: %v", e, got)
		}
	}
	if !hasEdge(s, w1.id, bm, EdgeOn) || !hasEdge(s, w2.id, bm, EdgeOn) {
		t.Error("both workspaces must point at the shared bookmark")
	}
}

func TestAssembleAgentCountsByLiveness(t *testing.T) {
	a := newAsm(t)
	a.effort("e1", "Login", "feat")
	w := a.ws(asmMain, "feat")
	plain := a.ws("/w/acme/widgets-two", "plain")
	a.session("s-active", store.LivenessActive, w, "feat", "")
	a.session("s-live", store.LivenessLive, w, "feat", "")
	a.session("s-themed", store.LivenessUncertain, plain, "plain", "login") // theme names effort e1
	a.session("s-orphan", store.LivenessUncertain, nil, "x", "")
	a.ls.conflicts = map[string][]string{"s-active": {"s-live"}, "s-live": {"s-active"}}
	s, _ := a.build()

	if got := s.Counts.Agents[effortNodeID("e1")]; got["active"] != 1 || got["live"] != 1 || got["uncertain"] != 1 {
		t.Fatalf("e1 agents: %v", got)
	}
	if got := s.Counts.Agents[UngroupedID]; got["uncertain"] != 1 || len(got) != 1 {
		t.Fatalf("ungrouped agents: %v", got)
	}
	orphan := s.Node(agentNodeID("s-orphan")).Agent
	if orphan.WorkspaceID != "" || orphan.EffortID != UngroupedID {
		t.Fatalf("orphan agent: %+v", orphan)
	}
	if countEdges(s, EdgeWorksIn) != 3 {
		t.Fatalf("an agent outside any workspace has no works_in edge; got %d", countEdges(s, EdgeWorksIn))
	}
	if ag := s.Node(agentNodeID("s-themed")).Agent; ag.EffortID != effortNodeID("e1") || ag.WorkspaceID != plain.id {
		t.Fatalf("themed agent: %+v", ag)
	}
	if ag := s.Node(agentNodeID("s-active")).Agent; !ag.Conflict || !slices.Equal(ag.ConflictsWith, []string{"s-live"}) {
		t.Fatalf("conflict: %+v", ag)
	}
}

func TestAssembleFreshnessPRs(t *testing.T) {
	a := newAsm(t)
	a.effort("e1", "Prs", "p-fresh", "p-err", "p-miss")
	a.ws(asmMain, "p-fresh")
	a.ws("/w/acme/widgets-two", "p-err")
	a.ws("/w/acme/widgets-three", "p-miss")
	if err := prcache.Write(asmOrigin, asmMain, "p-fresh", []models.PRRef{{Number: 1, State: "OPEN"}}); err != nil {
		t.Fatal(err)
	}
	if err := prcache.WriteFailure(asmOrigin, asmMain, "p-err", errors.New("gh: HTTP 502")); err != nil {
		t.Fatal(err)
	}
	bm := func(s *Snapshot, name string) *BookmarkInfo { return s.Node(bookmarkID(asmMain, name)).Bookmark }

	s, jobs := a.build()
	if bm(s, "p-fresh").PRLookup != Fresh || bm(s, "p-err").PRLookup != Error || bm(s, "p-miss").PRLookup != Unknown {
		t.Fatalf("lookups: %+v %+v %+v", bm(s, "p-fresh"), bm(s, "p-err"), bm(s, "p-miss"))
	}
	if !strings.Contains(bm(s, "p-err").PRError, "HTTP 502") {
		t.Fatalf("error not surfaced: %+v", bm(s, "p-err"))
	}
	if pr := s.Node(prNodeID(asmSlug, 1)).PR; pr.Freshness != Fresh {
		t.Fatalf("PR freshness: %+v", pr)
	}
	st := s.Sources[SourceGitHubPRs]
	if st.State != Error || st.Fresh != 1 || st.Error != 1 || st.Unknown != 1 || st.OldestFetch.IsZero() {
		t.Fatalf("pr source: %+v", st)
	}
	if n := countJobs(jobs, JobPR); n != 2 {
		t.Fatalf("only the non-fresh bookmarks are queued, got %d jobs", n)
	}

	// Past the TTL the good entry is stale but still carries data.
	a.c.cfg.PRTTL = time.Nanosecond
	time.Sleep(2 * time.Millisecond)
	s, jobs = a.build()
	if bm(s, "p-fresh").PRLookup != Stale {
		t.Fatalf("want stale: %+v", bm(s, "p-fresh"))
	}
	if pr := s.Node(prNodeID(asmSlug, 1)).PR; pr == nil || pr.Freshness != Stale {
		t.Fatalf("a stale lookup still shows its PR: %+v", pr)
	}
	if st := s.Sources[SourceGitHubPRs]; st.Stale != 1 {
		t.Fatalf("pr source: %+v", st)
	}
	if n := countJobs(jobs, JobPR); n != 3 {
		t.Fatalf("stale entries are refreshed too, got %d jobs", n)
	}

	// With only good entries the aggregate is the worst of fresh/stale.
	delete(a.efforts, "e1")
	a.ls.workspaces = a.ls.workspaces[:1]
	s, _ = a.build()
	if st := s.Sources[SourceGitHubPRs]; st.State != Stale || st.Stale != 1 || st.Error+st.Unknown != 0 {
		t.Fatalf("aggregate: %+v", st)
	}
}

func TestAssembleFreshnessGitHubIssues(t *testing.T) {
	a := newAsm(t)
	a.ws(asmMain, "gh-1-fresh")
	a.ws("/w/acme/widgets-two", "gh-2-err")
	a.ws("/w/acme/widgets-three", "gh-3-miss")
	k := func(n int) issuecache.Key { return issuecache.Key{Owner: "acme", Repo: "widgets", Number: n} }
	if err := issuecache.Write(k(1), issuecache.Info{Number: 1, Title: "T", State: "open", URL: "https://github.com/acme/widgets/issues/1"}); err != nil {
		t.Fatal(err)
	}
	if err := issuecache.WriteFailure(k(2), errors.New("gh: HTTP 404")); err != nil {
		t.Fatal(err)
	}
	tk := func(s *Snapshot, n int) *TicketInfo { return s.Node(githubTicketID(asmSlug, n)).Ticket }

	s, jobs := a.build()
	if t1 := tk(s, 1); t1.Freshness != Fresh || t1.Status != "open" || t1.Title != "T" || t1.FetchedAt.IsZero() {
		t.Fatalf("fresh issue: %+v", t1)
	}
	if t2 := tk(s, 2); t2.Freshness != Error || !strings.Contains(t2.Error, "404") || t2.Status != "" {
		t.Fatalf("failed issue: %+v", t2)
	}
	if t3 := tk(s, 3); t3.Freshness != Unknown || t3.Status != "" || t3.URL != "https://github.com/acme/widgets/issues/3" {
		t.Fatalf("missing issue: %+v", t3)
	}
	st := s.Sources[SourceGitHubIssues]
	if st.State != Error || st.Fresh != 1 || st.Error != 1 || st.Unknown != 1 || st.OldestFetch.IsZero() {
		t.Fatalf("issue source: %+v", st)
	}
	if n := countJobs(jobs, JobIssue); n != 2 {
		t.Fatalf("issue jobs: %d", n)
	}

	a.c.cfg.IssueTTL = time.Nanosecond
	time.Sleep(2 * time.Millisecond)
	s, _ = a.build()
	if t1 := tk(s, 1); t1.Freshness != Stale || t1.Status != "open" {
		t.Fatalf("stale issue keeps its data: %+v", t1)
	}
}

func TestAssembleFreshnessJira(t *testing.T) {
	a := newAsm(t)
	a.ws(asmMain, "WGO-1-fresh")
	a.ws("/w/acme/widgets-two", "WGO-2-err")
	a.ws("/w/acme/widgets-three", "WGO-3-miss")
	if err := jiracache.Write("WGO-1", jiracache.Info{Status: "In Review", Assignee: "Ann", Site: "acme.atlassian.net"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := jiracache.Resolve(&failingFetchers{}, "WGO-2", jiracache.Opts{Synchronous: true}); err == nil {
		t.Fatal("failing fetcher succeeded")
	}
	tk := func(s *Snapshot, key string) *TicketInfo { return s.Node(jiraTicketID(key)).Ticket }

	s, jobs := a.build()
	if t1 := tk(s, "WGO-1"); t1.Freshness != Fresh || t1.Status != "In Review" || t1.Assignee != "Ann" ||
		t1.URL != "https://acme.atlassian.net/browse/WGO-1" {
		t.Fatalf("fresh jira: %+v", t1)
	}
	if t2 := tk(s, "WGO-2"); t2.Freshness != Error || t2.Error == "" || t2.Status != "" {
		t.Fatalf("failed jira: %+v", t2)
	}
	if t3 := tk(s, "WGO-3"); t3.Freshness != Unknown {
		t.Fatalf("missing jira: %+v", t3)
	}
	st := s.Sources[SourceJira]
	if st.State != Error || st.Fresh != 1 || st.Error != 1 || st.Unknown != 1 || !st.OldestFetch.IsZero() {
		t.Fatalf("jira source (no fetch times are recorded): %+v", st)
	}
	if n := countJobs(jobs, JobJira); n != 2 {
		t.Fatalf("jira jobs: %d", n)
	}

	a.c.cfg.JiraTTL = time.Nanosecond
	time.Sleep(2 * time.Millisecond)
	s, _ = a.build()
	if t1 := tk(s, "WGO-1"); t1.Freshness != Stale || t1.Status != "In Review" {
		t.Fatalf("stale jira keeps its data: %+v", t1)
	}
}

func TestAssembleStackPosition(t *testing.T) {
	a := newAsm(t)
	in := a.ws(asmMain, "b")
	in.stack = []string{"a", "b", "c"}
	out := a.ws("/w/acme/widgets-two", "z")
	out.stack = []string{"a"}
	none := a.ws("/w/acme/widgets-three", "")
	s, _ := a.build()

	st := s.Node(in.id).Workspace.Stack
	if st == nil || st.Position != 2 || st.Size != 3 || strings.Join(st.Bookmarks, ",") != "a,b,c" {
		t.Fatalf("stack: %+v", st)
	}
	if got := s.Node(out.id).Workspace.Stack; got != nil {
		t.Fatalf("a bookmark absent from its stack has no position: %+v", got)
	}
	if got := s.Node(none.id).Workspace.Stack; got != nil {
		t.Fatalf("no bookmark, no position: %+v", got)
	}
}

func TestAssembleConflicts(t *testing.T) {
	a := newAsm(t)
	a.effort("e1", "One", "shared")
	a.effort("e2", "Two", "shared")
	w := a.ws(asmMain, "shared")
	// A conflicted workspace that discovery never returned.
	a.extra = append(a.extra, effort.WorkspaceInfo{Path: "/w/acme/vanished", MainRepoPath: asmMain, Bookmark: "shared"})
	s, _ := a.build()

	cf := s.Node(UngroupedID).Effort.Conflicts
	if len(cf) != 1 || cf[0].WorkspaceID != w.id || !slices.Equal(cf[0].ClaimedBy, []string{"e1", "e2"}) {
		t.Fatalf("conflicts: %+v", cf)
	}
	found := false
	for _, d := range s.Diagnostics {
		if strings.Contains(d, "/w/acme/vanished") && strings.Contains(d, "not a discovered workspace") {
			found = true
		}
	}
	if !found {
		t.Fatalf("undiscovered conflicted workspace not reported: %v", s.Diagnostics)
	}
}

func TestAssembleHugeGitHubTicketNumber(t *testing.T) {
	a := newAsm(t)
	w := a.ws(asmMain, "gh-99999999999999999999999-x")
	s, jobs := a.build()

	id := ""
	for _, n := range s.Nodes {
		if n.Ticket != nil {
			id = n.ID
			tk := n.Ticket
			if tk.Freshness != Unknown || tk.Error != "issue number out of range" || tk.System != "github" {
				t.Fatalf("overflow ticket: %+v", tk)
			}
		}
	}
	if id == "" {
		t.Fatal("overflow ticket node missing")
	}
	if !hasEdge(s, bookmarkID(asmMain, w.bookmark), id, EdgeTicket) {
		t.Fatal("overflow ticket not linked to its bookmark")
	}
	if countJobs(jobs, JobIssue) != 0 {
		t.Fatal("an impossible issue number must not be looked up")
	}
}

func countJobs(jobs []Job, kind JobKind) int {
	n := 0
	for _, j := range jobs {
		if j.Kind == kind {
			n++
		}
	}
	return n
}
