package dash

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/jjtest"
	"github.com/virtru/wgo/internal/store"
)

// memSource is an in-memory effort.Source.
type memSource struct {
	state *store.State
	plan  string
}

func (m memSource) LoadState() (*store.State, error) {
	if m.state == nil {
		return store.NewState(), nil
	}
	return m.state, nil
}
func (m memSource) LoadPlan() (string, error) { return m.plan, nil }

// fixture is a jj repo with two workspaces plus a bogus workspace.
type fixture struct {
	repo, ws, bogus string
	discover        func() ([]discovery.DiscoveredRepo, error)
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repo, jjc := jjtest.NewRepo(t)
	if err := jjc.GitRemoteAdd(repo, "origin", "https://github.com/acme/widgets.git"); err != nil {
		t.Fatal(err)
	}
	jjtest.Commit(t, repo, "feat: a", map[string]string{"a.txt": "a"})
	jjtest.Bookmark(t, repo, "gh-70-a", "@-")
	ws := jjtest.NewWorkspace(t, repo, "two")
	jjtest.Commit(t, ws, "feat: b", map[string]string{"b.txt": "b"})
	jjtest.Bookmark(t, ws, "WGO-5-b", "@-")

	// A directory that looks like a workspace but jj cannot read.
	bogus := filepath.Join(t.TempDir(), "bogus")
	if err := os.MkdirAll(filepath.Join(bogus, ".jj"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := fixture{repo: repo, ws: ws, bogus: bogus}
	f.discover = func() ([]discovery.DiscoveredRepo, error) {
		// The main clone twice (two spellings) and a bogus workspace. The
		// second workspace is found only through `jj workspace list`.
		return []discovery.DiscoveredRepo{
			{Path: repo, Name: "repo"},
			{Path: repo + string(filepath.Separator) + ".", Name: "repo"},
			{Path: bogus, Name: "bogus"},
		}, nil
	}
	return f
}

func (f fixture) collector(t *testing.T, now time.Time, src memSource) *Collector {
	_, jjc := jjtest.NewRepo(t) // any CLI client; NewRepo just pins identity
	return NewCollector(Config{
		JJ:          jjc,
		Discover:    f.discover,
		Store:       src,
		AgentPolicy: store.AgentPolicy{Window: 10 * time.Minute, MaxAge: 24 * time.Hour},
		Now:         func() time.Time { return now },
	})
}

func workspaces(s *Snapshot) map[string]*WorkspaceInfo {
	out := map[string]*WorkspaceInfo{}
	for i := range s.Nodes {
		if w := s.Nodes[i].Workspace; w != nil {
			out[filepath.Base(w.Path)] = w
		}
	}
	return out
}

func TestCollectorJJ(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	sess := store.AgentSession{
		ID: "claude-aaaaaaaaaaaa", Tool: "claude", Status: "working", Source: store.SourceExplicit,
		WorktreePath: f.ws, RepoPath: f.repo, Branch: "WGO-5-b",
		StartTime: now.Add(-time.Hour), LastActivity: now.Add(-time.Minute),
	}
	stale := sess
	stale.ID, stale.LastActivity = "codex-bbbbbbbbbbbb", now.Add(-3*time.Hour)
	stale.Tool = "codex"
	state := store.NewState()
	state.AgentSessions = map[string]store.AgentSession{sess.ID: sess, stale.ID: stale}
	c := f.collector(t, now, memSource{state: state})

	// Dirty both working copies: collecting must not snapshot them.
	for _, dir := range []string{f.repo, f.ws} {
		if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before := jjtest.OpCount(t, f.repo)
	stateBefore, _ := json.Marshal(state)

	s, jobs, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after := jjtest.OpCount(t, f.repo); after != before {
		t.Fatalf("op log changed from %d to %d operations: collecting snapshotted a workspace", before, after)
	}
	if stateAfter, _ := json.Marshal(state); string(stateAfter) != string(stateBefore) {
		t.Fatal("collecting modified agent sessions")
	}

	wss := workspaces(s)
	if len(wss) != 3 {
		t.Fatalf("want 3 deduplicated workspaces, got %d: %v", len(wss), keys(wss))
	}
	main := wss[filepath.Base(f.repo)]
	two := wss["two-ws"]
	bad := wss["bogus"]
	if main == nil || two == nil || bad == nil {
		t.Fatalf("missing workspaces: %v", keys(wss))
	}
	if bad.Error == "" || bad.Bookmark != "" {
		t.Fatalf("bogus workspace should carry an error: %+v", bad)
	}
	if !main.IsMain || main.Bookmark != "gh-70-a" || two.Bookmark != "WGO-5-b" {
		t.Fatalf("bookmarks: main=%+v two=%+v", main, two)
	}
	if two.Stack == nil || two.Stack.Position != 2 || two.Stack.Size != 2 ||
		strings.Join(two.Stack.Bookmarks, ",") != "gh-70-a,WGO-5-b" {
		t.Fatalf("stack from the jj DAG: %+v", two.Stack)
	}
	if two.RepoSlug != "acme/widgets" || two.Description != "feat: b" {
		t.Fatalf("workspace info: %+v", two)
	}
	for _, id := range two.Changes {
		if id == two.ChangeID {
			t.Fatal("the empty working-copy change must not be listed")
		}
	}
	if len(two.Changes) == 0 || main.EffortID != effortNodeID("ticket-gh-70") ||
		two.EffortID != effortNodeID("ticket-wgo-5") || bad.EffortID != UngroupedID {
		t.Fatalf("attribution: main=%s two=%s bad=%s changes=%v", main.EffortID, two.EffortID, bad.EffortID, two.Changes)
	}

	if src := s.Sources[SourceJJ]; src.State != Error || src.Error != 1 || src.Fresh != 2 {
		t.Fatalf("jj source: %+v", src)
	}

	// Agents: the active session is attributed through its workspace; the
	// quiet unverifiable one is shown as uncertain; nothing conflicts.
	a := s.Node(agentNodeID(sess.ID))
	u := s.Node(agentNodeID(stale.ID))
	if a == nil || u == nil {
		t.Fatal("agent nodes missing")
	}
	if a.Agent.Liveness != "active" || a.Agent.EffortID != two.EffortID || a.Agent.WorkspaceID != WorkspaceID(f.repo, f.ws) {
		t.Fatalf("agent: %+v", a.Agent)
	}
	if u.Agent.Liveness != "uncertain" || a.Agent.Conflict {
		t.Fatalf("liveness/conflict: %+v %+v", u.Agent, a.Agent)
	}
	if got := s.Counts.Agents[two.EffortID]; got["active"] != 1 || got["uncertain"] != 1 {
		t.Fatalf("agent counts: %v", got)
	}
	if act := s.Counts.Activity[two.EffortID]; act[len(act)-1] == 0 {
		t.Fatalf("today's activity should count feat: b: %v", act)
	}

	// Cold cache: every remote field is unknown and each is queued.
	for _, src := range []string{SourceGitHubPRs, SourceGitHubIssues, SourceJira} {
		if st := s.Sources[src]; st.State != Unknown || st.Unknown == 0 {
			t.Fatalf("%s on a cold cache: %+v", src, st)
		}
	}
	for _, n := range s.Nodes {
		switch {
		case n.Bookmark != nil && n.Bookmark.PRLookup != Unknown:
			t.Fatalf("bookmark %s: %+v", n.ID, n.Bookmark)
		case n.Ticket != nil && (n.Ticket.Freshness != Unknown || n.Ticket.Status != ""):
			t.Fatalf("ticket %s: %+v", n.ID, n.Ticket)
		case n.PR != nil:
			t.Fatalf("no PR can be known on a cold cache: %+v", n.PR)
		}
	}
	if pr := s.Counts.PRStates[two.EffortID]; pr["unknown"] != 1 || pr["none"] != 0 {
		t.Fatalf("unknown PR lookups must not count as none: %v", pr)
	}
	kinds := map[JobKind]int{}
	for _, j := range jobs {
		kinds[j.Kind]++
	}
	if kinds[JobPR] != 2 || kinds[JobJira] != 1 || kinds[JobIssue] != 1 {
		t.Fatalf("refresh jobs: %v", jobs)
	}
	if s.Node(githubTicketID("acme/widgets", 70)) == nil || s.Node(jiraTicketID("WGO-5")) == nil {
		t.Fatal("ticket nodes missing")
	}

	// Determinism: a second collection with the same clock is identical.
	s2, _, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	j1, _ := json.Marshal(s)
	j2, _ := json.Marshal(s2)
	if string(j1) != string(j2) {
		t.Fatalf("snapshots differ:\n%s\n%s", j1, j2)
	}
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestWorkspaceIDStable(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	ws := filepath.Join(dir, "ws")
	for _, d := range []string{main, ws} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(ws, link); err != nil {
		t.Fatal(err)
	}
	id := WorkspaceID(main, ws)
	if !ValidWorkspaceID(id) {
		t.Fatalf("malformed id %q", id)
	}
	for _, alt := range []string{ws + "/", ws + "/.", link} {
		if got := WorkspaceID(main, alt); got != id {
			t.Fatalf("WorkspaceID(%q) = %s, want %s", alt, got, id)
		}
	}
	if WorkspaceID(ws, ws) == id || WorkspaceID(main, main) == id {
		t.Fatal("different workspaces must not share an id")
	}
	if strings.Contains(id, "ws/") || strings.Contains(id, dir) {
		t.Fatal("id must be opaque")
	}
}
