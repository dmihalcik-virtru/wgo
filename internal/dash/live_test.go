package dash

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/issuecache"
	"github.com/virtru/wgo/internal/jiracache"
	"github.com/virtru/wgo/models"
)

const (
	wsA = "ws-00000000000000aa"
	wsB = "ws-00000000000000bb"
	bmA = "bm-a"
	prA = "pr:acme/widgets#1"
)

// snapOpts shape a synthetic snapshot.
type snapOpts struct {
	changesA  []string
	errB      bool
	truncA    int
	prLookup  Freshness
	prState   string
	draft     bool
	review    string
	reviewers []string
	known     bool
}

func synth(o snapOpts) *Snapshot {
	if o.prLookup == "" {
		o.prLookup = Fresh
	}
	s := &Snapshot{Schema: SchemaVersion, GeneratedAt: time.Unix(1_800_000_000, 0), Days: 1, Sources: map[string]SourceStatus{}}
	s.Nodes = append(s.Nodes,
		Node{ID: wsA, Kind: KindWorkspace, Label: "a", Workspace: &WorkspaceInfo{Changes: o.changesA, ChangesTruncated: o.truncA}},
		Node{ID: wsB, Kind: KindWorkspace, Label: "b", Workspace: &WorkspaceInfo{Changes: []string{"b1"}}},
		Node{ID: bmA, Kind: KindBookmark, Label: "a", Bookmark: &BookmarkInfo{PRLookup: o.prLookup}},
	)
	if o.errB {
		s.Nodes[1].Workspace = &WorkspaceInfo{Error: "boom"}
	}
	if o.prState != "" && (o.prLookup == Fresh || o.prLookup == Stale) {
		s.Nodes = append(s.Nodes, Node{ID: prA, Kind: KindPR, Label: "#1", PR: &PRInfo{
			Number: 1, State: o.prState, IsDraft: o.draft, ReviewDecision: o.review,
			RequestedReviewers: o.reviewers, ReviewersKnown: o.known, BookmarkID: bmA, Freshness: o.prLookup,
		}})
	}
	return s
}

func openDash(t *testing.T, dir string) *Dash {
	t.Helper()
	d, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func publish(t *testing.T, d *Dash, s *Snapshot) uint64 {
	t.Helper()
	if err := d.Publish(s); err != nil {
		t.Fatal(err)
	}
	return s.Generation
}

func TestDeltaNoPreviousLook(t *testing.T) {
	d := openDash(t, t.TempDir())
	if d.Current() != nil {
		t.Fatal("an empty dash has no view")
	}
	publish(t, d, synth(snapOpts{changesA: []string{"a1"}}))
	dl := d.Current().Delta
	if dl.Status != DeltaNoPreviousLook || dl.Message != "No previous look" {
		t.Fatalf("delta without a baseline: %+v", dl)
	}
}

func TestDeltaSurvivesRefreshAndRestart(t *testing.T) {
	dir := t.TempDir()
	d := openDash(t, dir)
	g1 := publish(t, d, synth(snapOpts{changesA: []string{"a1"}, prState: "open", draft: true, known: true}))
	if err := d.Acknowledge(g1); err != nil {
		t.Fatal(err)
	}
	if got := d.Current().Delta; got.Status != DeltaCompared || got.Summary.NewChanges != 0 || got.Summary.Unknown != 0 {
		t.Fatalf("acknowledged view should show nothing new: %+v", got)
	}
	baseFile := filepath.Join(dir, BaselineFile)
	saved, err := os.ReadFile(baseFile)
	if err != nil {
		t.Fatal(err)
	}

	// New work: a change, the PR leaves draft and a review is requested.
	next := snapOpts{changesA: []string{"a2", "a1"}, prState: "open", review: "CHANGES_REQUESTED", reviewers: []string{"bob"}, known: true}
	publish(t, d, synth(next))
	// Background refreshes do not advance the baseline.
	publish(t, d, synth(next))
	g := publish(t, d, synth(next))

	check := func(dl *Delta) {
		t.Helper()
		if dl.BaselineGeneration != g1 || dl.Summary.NewChanges != 1 || dl.Summary.PRStateChanges != 1 || dl.Summary.NewReviewRequests != 1 {
			t.Fatalf("delta: %+v", dl)
		}
		if len(dl.PRs) != 1 || dl.PRs[0].FromState != "draft" || dl.PRs[0].ToState != "open" || dl.PRs[0].ToReview != "CHANGES_REQUESTED" {
			t.Fatalf("pr delta: %+v", dl.PRs)
		}
		if len(dl.ReviewRequests) != 1 || dl.ReviewRequests[0].Added[0] != "bob" {
			t.Fatalf("review delta: %+v", dl.ReviewRequests)
		}
	}
	check(d.Current().Delta)
	if now, _ := os.ReadFile(baseFile); !bytes.Equal(now, saved) {
		t.Fatal("publishing rewrote last-seen.json")
	}

	// Restart: the persisted snapshot and baseline reload.
	d2 := openDash(t, dir)
	v := d2.Current()
	if v == nil || !v.Loaded || v.Snapshot.Generation != g {
		t.Fatalf("reloaded view: %+v", v)
	}
	check(v.Delta)
	// Generations keep increasing across restarts.
	if g2 := publish(t, d2, synth(next)); g2 != g+1 {
		t.Fatalf("generation after restart = %d, want %d", g2, g+1)
	}
	check(d2.Current().Delta)

	// Acknowledging the current view clears the delta; an older generation
	// is rejected and an unknown one too.
	if err := d2.Acknowledge(g + 1); err != nil {
		t.Fatal(err)
	}
	if dl := d2.Current().Delta; dl.Summary.NewChanges != 0 || dl.Summary.PRStateChanges != 0 || dl.Summary.NewReviewRequests != 0 {
		t.Fatalf("delta after acknowledge: %+v", dl)
	}
	if err := d2.Acknowledge(g); !errors.Is(err, ErrOldGeneration) {
		t.Fatalf("acknowledging an older generation: %v", err)
	}
	if err := d2.Acknowledge(999); !errors.Is(err, ErrUnknownGen) {
		t.Fatalf("acknowledging an unknown generation: %v", err)
	}
}

func TestDeltaUnknownIsNotUnchanged(t *testing.T) {
	d := openDash(t, t.TempDir())
	// At the baseline, b is unreadable and the PR lookup is unknown.
	g1 := publish(t, d, synth(snapOpts{changesA: []string{"a1"}, errB: true, prLookup: Unknown}))
	if err := d.Acknowledge(g1); err != nil {
		t.Fatal(err)
	}
	publish(t, d, synth(snapOpts{changesA: []string{"a1"}, prState: "open", known: true}))
	dl := d.Current().Delta
	// a unchanged; b unknown (unread at baseline); PR unknown (its
	// bookmark's PR list was not known at baseline, so it is not "new").
	// The PR's unknown reviewers do not count a second time.
	if dl.Summary.Unchanged != 1 || dl.Summary.Unknown != 2 || dl.Summary.PRStateChanges != 0 || dl.Summary.NewChanges != 0 {
		t.Fatalf("summary: %+v", dl.Summary)
	}
	if len(dl.Workspaces) != 1 || dl.Workspaces[0].ID != wsB || dl.Workspaces[0].Status != ItemUnknown {
		t.Fatalf("workspaces: %+v", dl.Workspaces)
	}
	if len(dl.PRs) != 1 || dl.PRs[0].Status != ItemUnknown {
		t.Fatalf("prs: %+v", dl.PRs)
	}

	// Now acknowledge a fully known view, then lose the PR source and b.
	g := publish(t, d, synth(snapOpts{changesA: []string{"a1"}, prState: "open", known: true}))
	if err := d.Acknowledge(g); err != nil {
		t.Fatal(err)
	}
	publish(t, d, synth(snapOpts{changesA: []string{"a1"}, errB: true, prLookup: Unknown}))
	dl = d.Current().Delta
	if dl.Summary.Unknown != 2 || dl.Summary.Unchanged != 1 {
		t.Fatalf("unknown now must not read as unchanged: %+v", dl.Summary)
	}
}

func TestDeltaTruncation(t *testing.T) {
	d := openDash(t, t.TempDir())
	g := publish(t, d, synth(snapOpts{changesA: []string{"a1"}}))
	if err := d.Acknowledge(g); err != nil {
		t.Fatal(err)
	}
	publish(t, d, synth(snapOpts{changesA: []string{"a3", "a2", "a1"}, truncA: 40}))
	dl := d.Current().Delta
	if dl.Summary.Truncated != 1 || dl.Summary.NewChanges != 2 {
		t.Fatalf("summary: %+v", dl.Summary)
	}
	if dl.Workspaces[0].ID != wsA || dl.Workspaces[0].Truncated != 40 {
		t.Fatalf("workspace delta: %+v", dl.Workspaces)
	}
}

func TestBaselineWindowBounded(t *testing.T) {
	var many []string
	for i := 0; i < ChangeWindow+50; i++ {
		many = append(many, string(rune('a'+i%26))+time.Duration(i).String())
	}
	b := baselineFrom(synth(snapOpts{changesA: many}), time.Now())
	if got := len(b.Workspaces[wsA].Changes); got != ChangeWindow {
		t.Fatalf("baseline keeps %d changes, want %d", got, ChangeWindow)
	}
}

func TestCorruptSnapshotIgnored(t *testing.T) {
	dir := t.TempDir()
	d := openDash(t, dir)
	publish(t, d, synth(snapOpts{changesA: []string{"a1"}}))
	// A torn write never lands on snapshot.json (atomicfile); a stray temp
	// file is not read, and a corrupt file is ignored, not fatal.
	if err := os.WriteFile(filepath.Join(dir, SnapshotFile+".tmp123"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v := openDash(t, dir).Current(); v == nil || v.Snapshot.Generation != 1 {
		t.Fatalf("good snapshot not loaded: %+v", v)
	}
	if err := os.WriteFile(filepath.Join(dir, SnapshotFile), []byte(`{"schema":1,"nodes":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	d2 := openDash(t, dir)
	if d2.Current() != nil || d2.snapshotLoadErr == "" {
		t.Fatalf("corrupt snapshot should be ignored with a diagnostic: %q", d2.snapshotLoadErr)
	}
}

// blockingFetchers block every fetch until release is closed.
type blockingFetchers struct {
	release chan struct{}
	calls   atomic.Int32
}

func (b *blockingFetchers) FetchPRs(repoPath, branch string) ([]models.PRRef, error) {
	b.calls.Add(1)
	<-b.release
	return []models.PRRef{{Number: 7, Title: "t", State: "open", URL: "https://github.com/acme/widgets/pull/7",
		RequestedReviewers: []string{"carol"}}}, nil
}

func (b *blockingFetchers) FetchJira(ticket string) (jiracache.Info, error) {
	b.calls.Add(1)
	<-b.release
	return jiracache.Info{Status: "In Progress"}, nil
}

func (b *blockingFetchers) FetchIssue(k issuecache.Key) (issuecache.Info, error) {
	b.calls.Add(1)
	<-b.release
	return issuecache.Info{Number: k.Number, State: "open", Title: "dash"}, nil
}

func TestColdCacheNeverFetchesSynchronously(t *testing.T) {
	f := newFixture(t)
	bf := &blockingFetchers{release: make(chan struct{})}
	r := NewRefresher(Fetchers{PR: bf, Jira: bf, Issue: bf}, RefresherOptions{Workers: 2})
	defer r.Close()
	d, err := Open(Options{Dir: t.TempDir(), Collector: f.collector(t, time.Now(), memSource{}), Refresher: r})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 20*time.Second {
		t.Fatal("refresh waited on the network")
	}
	v := d.Current()
	if v == nil {
		t.Fatal("no view published")
	}
	for _, src := range []string{SourceGitHubPRs, SourceGitHubIssues, SourceJira} {
		if v.Snapshot.Sources[src].State != Unknown {
			t.Fatalf("%s: %+v", src, v.Snapshot.Sources[src])
		}
	}
	g := v.Snapshot.Generation

	// Fetchers run only in the background; releasing them republishes.
	close(bf.release)
	deadline := time.Now().Add(30 * time.Second)
	for d.Current().Snapshot.Generation == g {
		if time.Now().After(deadline) {
			t.Fatal("background refresh never republished")
		}
		time.Sleep(20 * time.Millisecond)
	}
	s := d.Current().Snapshot
	if bf.calls.Load() != 4 {
		t.Fatalf("fetch calls = %d, want 4 (2 PR, 1 Jira, 1 issue)", bf.calls.Load())
	}
	if s.Sources[SourceGitHubPRs].State != Fresh || s.Sources[SourceJira].State != Fresh || s.Sources[SourceGitHubIssues].State != Fresh {
		t.Fatalf("sources after refresh: %+v", s.Sources)
	}
	n := s.Node(prNodeID("acme/widgets", 7))
	if n == nil || !n.PR.ReviewersKnown || n.PR.RequestedReviewers[0] != "carol" {
		t.Fatalf("pr node: %+v", n)
	}
	if tk := s.Node(jiraTicketID("WGO-5")); tk.Ticket.Status != "In Progress" {
		t.Fatalf("jira ticket: %+v", tk.Ticket)
	}
	if tk := s.Node(githubTicketID("acme/widgets", 70)); tk.Ticket.Status != "open" || tk.Ticket.Title != "dash" {
		t.Fatalf("issue ticket: %+v", tk.Ticket)
	}
	// The background refresh did not touch the baseline.
	if _, err := os.Stat(filepath.Join(d.opts.Dir, BaselineFile)); !os.IsNotExist(err) {
		t.Fatalf("baseline written without Acknowledge: %v", err)
	}
}

func TestResolve(t *testing.T) {
	f := newFixture(t)
	d, err := Open(Options{Dir: t.TempDir(), Collector: f.collector(t, time.Now(), memSource{})})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Refresh(context.Background(), RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	id := WorkspaceID(f.repo, f.ws)
	tg, err := d.Resolve(id)
	if err != nil || canonicalPath(tg.Root) != canonicalPath(f.ws) {
		t.Fatalf("Resolve(%s) = %+v, %v", id, tg, err)
	}
	for _, bad := range []string{"", "../etc", "ws-zz", "ws-0123456789abcdef", f.ws} {
		if _, err := d.Resolve(bad); !errors.Is(err, ErrUnknownWorkspace) {
			t.Fatalf("Resolve(%q) = %v, want unknown", bad, err)
		}
	}
	// The workspace disappears: its ID is in the snapshot but stale.
	if err := os.RemoveAll(f.ws); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Resolve(id); !errors.Is(err, ErrStaleWorkspace) {
		t.Fatalf("Resolve(removed) = %v, want stale", err)
	}
}

func viewJSON(t *testing.T, v *View) string {
	t.Helper()
	var buf bytes.Buffer
	if err := v.WriteJSON(&buf, time.Now()); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func hasDiag(v *View, sub string) bool {
	for _, d := range v.Diagnostics {
		if strings.Contains(d, sub) {
			return true
		}
	}
	return false
}

// readOnlyDir returns a directory nothing can be written into.
func readOnlyDir(t *testing.T) string {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("root can write into read-only directories")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dir
}

func TestPublishPersistFailureIsVisible(t *testing.T) {
	dir := readOnlyDir(t)
	d := openDash(t, dir)
	err := d.Publish(synth(snapOpts{changesA: []string{"a1"}}))
	if !errors.Is(err, ErrPersist) {
		t.Fatalf("Publish into a read-only dir = %v, want ErrPersist", err)
	}
	// The newest snapshot is still served, with the failure on it.
	v := d.Current()
	if v == nil || v.Snapshot.Generation != 1 || !hasDiag(v, "snapshot not saved") {
		t.Fatalf("view after failed write: %+v", v)
	}
	if body := viewJSON(t, v); !strings.Contains(body, `"diagnostics":["snapshot not saved`) {
		t.Fatalf("API body hides the write failure: %.200s", body)
	}

	// The next successful write clears it.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := d.Publish(synth(snapOpts{changesA: []string{"a1"}})); err != nil {
		t.Fatal(err)
	}
	if v := d.Current(); len(v.Diagnostics) != 0 || !strings.Contains(viewJSON(t, v), `"diagnostics":[]`) {
		t.Fatalf("diagnostics not cleared: %v", v.Diagnostics)
	}
}

func TestCorruptBaselineIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, BaselineFile), []byte(`{"schema":1,`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := openDash(t, dir)
	publish(t, d, synth(snapOpts{changesA: []string{"a1"}}))
	v := d.Current()
	if v.Delta.Status != DeltaNoPreviousLook {
		t.Fatalf("a corrupt baseline is no baseline: %+v", v.Delta)
	}
	if !hasDiag(v, "ignoring last-seen baseline") || !strings.Contains(viewJSON(t, v), "ignoring last-seen baseline") {
		t.Fatalf("load diagnostic not served: %v", v.Diagnostics)
	}
}

func TestBackgroundRepublishFailureIsVisible(t *testing.T) {
	f := newFixture(t)
	var logs atomic.Int32
	old := Logf
	Logf = func(string, ...any) { logs.Add(1) }
	t.Cleanup(func() { Logf = old })

	bf := &blockingFetchers{release: make(chan struct{})}
	r := NewRefresher(Fetchers{PR: bf, Jira: bf, Issue: bf}, RefresherOptions{Workers: 2})
	defer r.Close()
	d, err := Open(Options{Dir: readOnlyDir(t), Collector: f.collector(t, time.Now(), memSource{}), Refresher: r})
	if err != nil {
		t.Fatal(err)
	}
	// The write fails, but the snapshot is served and the remote refresh
	// still starts.
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true}); !errors.Is(err, ErrPersist) {
		t.Fatalf("Refresh = %v, want ErrPersist", err)
	}
	g := d.Current().Snapshot.Generation
	logsBefore := logs.Load()
	close(bf.release)
	deadline := time.Now().Add(30 * time.Second)
	for d.Current().Snapshot.Generation == g {
		if time.Now().After(deadline) {
			t.Fatal("background refresh never republished")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The republished snapshot could not be saved either: that is shown
	// once, as a write failure, not again as a refresh failure.
	v := d.Current()
	if !hasDiag(v, "snapshot not saved") || len(v.Diagnostics) != 1 {
		t.Fatalf("background write failure: diags=%v", v.Diagnostics)
	}
	if logs.Load() == logsBefore {
		t.Fatal("background write failure was not logged")
	}
}

func TestDeltaSeveralPRStateChanges(t *testing.T) {
	mk := func(prs ...Node) *Snapshot {
		s := &Snapshot{Schema: SchemaVersion, Sources: map[string]SourceStatus{}, Nodes: []Node{
			{ID: "bm-1", Kind: KindBookmark, Bookmark: &BookmarkInfo{PRLookup: Fresh}},
			{ID: "bm-2", Kind: KindBookmark, Bookmark: &BookmarkInfo{PRLookup: Fresh}},
		}}
		s.Nodes = append(s.Nodes, prs...)
		return s
	}
	pr := func(id, bm, state string, draft bool) Node {
		return Node{ID: id, Kind: KindPR, Label: id, PR: &PRInfo{State: state, IsDraft: draft, BookmarkID: bm, ReviewersKnown: true, Freshness: Fresh}}
	}
	d := openDash(t, t.TempDir())
	g := publish(t, d, mk(pr("pr:x#1", "bm-1", "open", false), pr("pr:x#2", "bm-1", "open", true)))
	if err := d.Acknowledge(g); err != nil {
		t.Fatal(err)
	}
	publish(t, d, mk(pr("pr:x#1", "bm-1", "merged", false), pr("pr:x#2", "bm-1", "open", false), pr("pr:x#3", "bm-2", "open", false)))
	dl := d.Current().Delta
	if dl.Summary.PRStateChanges != 3 || dl.Summary.Unknown != 0 {
		t.Fatalf("summary: %+v", dl.Summary)
	}
	got := map[string]PRDelta{}
	for _, p := range dl.PRs {
		got[p.ID] = p
	}
	want := map[string][3]string{
		"pr:x#1": {ItemChanged, "open", "merged"},
		"pr:x#2": {ItemChanged, "draft", "open"},
		"pr:x#3": {ItemNew, "", "open"},
	}
	for id, w := range want {
		p := got[id]
		if p.Status != w[0] || p.FromState != w[1] || p.ToState != w[2] {
			t.Fatalf("%s: %+v, want %v", id, p, w)
		}
	}
}

func TestDeltaChangeWindowRotation(t *testing.T) {
	// Baseline: a full window, newest first (c199 ... c0).
	var base []string
	for i := ChangeWindow - 1; i >= 0; i-- {
		base = append(base, fmt.Sprintf("c%d", i))
	}
	d := openDash(t, t.TempDir())
	g := publish(t, d, synth(snapOpts{changesA: base, truncA: 10}))
	if err := d.Acknowledge(g); err != nil {
		t.Fatal(err)
	}
	// Three new changes push the three oldest out of the window.
	next := append([]string{"n3", "n2", "n1"}, base[:ChangeWindow-3]...)
	publish(t, d, synth(snapOpts{changesA: next, truncA: 13}))
	dl := d.Current().Delta
	if dl.Summary.NewChanges != 3 {
		t.Fatalf("new changes = %d, want 3 (rotated-out changes are not new or removed)", dl.Summary.NewChanges)
	}
	if len(dl.Workspaces) != 1 || dl.Workspaces[0].ID != wsA || dl.Workspaces[0].Status != ItemChanged ||
		dl.Workspaces[0].NewChanges != 3 || dl.Workspaces[0].Truncated != 13 {
		t.Fatalf("workspace delta: %+v", dl.Workspaces)
	}
	if dl.Summary.Truncated != 1 || dl.Summary.Unknown != 0 {
		t.Fatalf("summary: %+v", dl.Summary)
	}
}
