package dash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/inventory"
)

// fakeTimelineGraphQL is a GitHub GraphQL endpoint serving PR timelines: each
// PR was opened ready, with bob requested an hour later, and reports the
// updated_at in updated (set by the test). It counts the requests per PR.
type fakeTimelineGraphQL struct {
	t       *testing.T
	mu      sync.Mutex
	updated map[int]time.Time
	base    time.Time // when the seeded PRs' updated_at count back from
	calls   map[int]int
	order   []int
}

func (g *fakeTimelineGraphQL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(body, &req); err != nil || !strings.Contains(req.Query, "timelineItems(first:") {
		g.t.Errorf("unexpected request %s: %v", body, err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	n := int(req.Variables["number"].(float64))
	g.mu.Lock()
	if g.calls == nil {
		g.calls = map[int]int{}
	}
	g.calls[n]++
	g.order = append(g.order, n)
	updated := g.updated[n]
	g.mu.Unlock()
	created := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
		"state": "OPEN", "isDraft": false, "createdAt": created, "updatedAt": updated, "author": map[string]any{"login": "alice"},
		"timelineItems": map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": []any{
			map[string]any{"__typename": "ReviewRequestedEvent", "createdAt": created.Add(time.Hour),
				"requestedReviewer": map[string]any{"__typename": "User", "login": "bob"}},
		}},
	}}}})
}

// fetches returns how many PRs were fetched and how many requests made.
func (g *fakeTimelineGraphQL) fetches() (prs, requests int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls), len(g.order)
}

// timelineDash is a Dash following alice, whose inventory the test seeds,
// with a Refresher that fetches timelines from gc and nothing else.
func timelineDash(t *testing.T, gc inventory.TimelineFetcher, ignoreLeases bool) *Dash {
	t.Helper()
	c := NewCollector(Config{People: []Person{{Login: "alice"}}, Identity: func() (string, string) { return "alice", "" }})
	r := NewRefresher(Fetchers{Timelines: gc}, RefresherOptions{Workers: 2, IgnoreLeases: ignoreLeases})
	t.Cleanup(r.Close)
	d, err := Open(Options{Dir: t.TempDir(), Collector: c, Refresher: r})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// seedTimelinePRs seeds alice's inventory with open PRs 1..open, updated an
// hour apart (PR 1 most recently), and merged PRs 1001..1000+merged.
func seedTimelinePRs(t *testing.T, g *fakeTimelineGraphQL, open, merged int, bump map[int]time.Duration) {
	t.Helper()
	var openPRs, closedPRs []github.PRFacts
	g.mu.Lock()
	if g.updated == nil {
		g.updated, g.base = map[int]time.Time{}, time.Now().UTC().Truncate(time.Second)
	}
	now := g.base
	for i := 1; i <= open+merged; i++ {
		n := i
		if i > open {
			n = 1000 + i - open
		}
		p := openPR(n, "alice", fmt.Sprintf("feat-%d", n))
		p.UpdatedAt = now.Add(-time.Duration(i) * time.Hour).Add(bump[n])
		g.updated[n] = p.UpdatedAt
		if i > open {
			p.State, p.MergedAt, p.ClosedAt = "merged", p.UpdatedAt, p.UpdatedAt
			closedPRs = append(closedPRs, p)
		} else {
			openPRs = append(openPRs, p)
		}
	}
	g.mu.Unlock()
	seedInventory(t, inventory.ClosedSince(time.Now(), DefaultDays), map[string][]github.PRFacts{
		"alice/open-authored": openPRs, "alice/review-requested": {}, "alice/closed-recent": closedPRs,
	})
}

// TestTimelinesRefreshOnlyChangedPRsAtMost50PerCycle is gh-104's refresh
// criterion, against a fake GraphQL server: a cycle fetches at most 50
// timelines, open PRs first and the most recently updated first; a PR whose
// updated_at is unchanged is never fetched again; one whose updated_at
// changed is, and nothing else. github_timelines reports the coverage.
func TestTimelinesRefreshOnlyChangedPRsAtMost50PerCycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	g := &fakeTimelineGraphQL{t: t}
	srv := httptest.NewServer(g)
	defer srv.Close()
	gc := github.NewClient(github.WithBaseURL(srv.URL), github.WithToken("test-token"), github.WithRetryBackoff(0, 0, 0))
	seedTimelinePRs(t, g, 120, 5, nil)
	d := timelineDash(t, gc, true)

	cycle := func() *Snapshot {
		t.Helper()
		if err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true}); err != nil {
			t.Fatal(err)
		}
		return d.Current().Snapshot
	}
	src := func(s *Snapshot) SourceStatus { return s.Sources[SourceGitHubTimelines] }

	s := cycle()
	prs, reqs := g.fetches()
	if prs != 50 || reqs != 50 {
		t.Fatalf("first cycle fetched %d PRs in %d requests, want 50 and 50", prs, reqs)
	}
	// Open PRs first, the most recently updated first: PRs 1 to 50.
	if want := intRange(1, 50); !slices.Equal(sortedInts(g.order), want) {
		t.Fatalf("first cycle fetched %v", sortedInts(g.order))
	}
	if st := src(s); st.Fresh != 50 || st.Unknown != 75 || st.State != Unknown || st.Detail != "50 of 125 PRs have a cached history" || st.OldestFetch.IsZero() {
		t.Fatalf("github_timelines after one cycle: %+v", st)
	}
	if p := s.Node(prNodeID(asmSlug, 1)).PR; p.Timeline == nil || p.Timeline.Freshness != Fresh || segText(p.Timeline.Segments) == "" {
		t.Fatalf("PR 1's timeline: %+v", p.Timeline)
	}
	if p := s.Node(prNodeID(asmSlug, 51)).PR; p.Timeline != nil {
		t.Fatalf("PR 51 has a timeline before it was fetched: %+v", p.Timeline)
	}

	cycle()
	s = cycle()
	if prs, reqs = g.fetches(); prs != 125 || reqs != 125 {
		t.Fatalf("after three cycles %d PRs in %d requests, want every one of the 125 once", prs, reqs)
	}
	if last := g.order[100:]; !slices.Equal(sortedInts(last), append(intRange(101, 120), intRange(1001, 1005)...)) {
		t.Fatalf("the third cycle fetched %v: the rest of the open PRs, then the merged ones", last)
	}
	if st := src(s); st.Fresh != 125 || st.State != Fresh || st.Detail != "125 of 125 PRs have a cached history" {
		t.Fatalf("github_timelines when every history is cached: %+v", st)
	}

	// Nothing changed: no requests at all.
	cycle()
	if _, reqs = g.fetches(); reqs != 125 {
		t.Fatalf("%d requests after an idle cycle, want still 125", reqs)
	}

	// Three PRs change on GitHub: only they are fetched again.
	seedTimelinePRs(t, g, 120, 5, map[int]time.Duration{7: time.Minute, 64: time.Minute, 1003: time.Minute})
	s = cycle()
	if _, reqs = g.fetches(); reqs != 128 {
		t.Fatalf("%d requests after three PRs changed, want 128", reqs)
	}
	if again := sortedInts(g.order[125:]); !slices.Equal(again, []int{7, 64, 1003}) {
		t.Fatalf("refetched %v, want the changed PRs 7, 64 and 1003", again)
	}
	if p := s.Node(prNodeID(asmSlug, 64)).PR; p.Timeline == nil || p.Timeline.Freshness != Fresh {
		t.Fatalf("PR 64's timeline after its refetch: %+v", p.Timeline)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestTimelinesFollowTheBackgroundRules: a background Refresher honours the
// per-PR lease and failure back-off, and a rate limit pauses every timeline
// fetch, which github_timelines says; a PR changed on GitHub but not yet
// refetched keeps its old history, marked stale and not used by the facts.
func TestTimelinesFollowTheBackgroundRules(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	g := &fakeTimelineGraphQL{t: t}
	srv := httptest.NewServer(g)
	defer srv.Close()
	gc := github.NewClient(github.WithBaseURL(srv.URL), github.WithToken("test-token"), github.WithRetryBackoff(0, 0, 0))
	seedTimelinePRs(t, g, 3, 0, nil)

	// PR 2 failed a moment ago: it backs off, so it is neither queued nor
	// fetched; PR 3's lease is held by another process.
	if _, err := inventory.RefreshTimeline(context.Background(), failingTimelines{}, inventory.TimelineKey{Repo: asmSlug, Number: 2}); err == nil {
		t.Fatal("want a failure")
	}
	if !inventory.LockTimelineRefresh(inventory.TimelineKey{Repo: asmSlug, Number: 3}, time.Minute) {
		t.Fatal("lease")
	}
	d := timelineDash(t, gc, false)
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sortedInts(g.order), []int{1}) {
		t.Fatalf("fetched %v, want only PR 1", g.order)
	}
	st := d.Current().Snapshot.Sources[SourceGitHubTimelines]
	if st.Fresh != 1 || st.Error != 1 || st.Unknown != 1 || st.State != Error {
		t.Fatalf("github_timelines %+v", st)
	}

	// PR 1 changes on GitHub; before it is refetched its history is stale.
	seedTimelinePRs(t, g, 3, 0, map[int]time.Duration{1: time.Minute})
	pauseUntil := time.Now().Add(time.Hour)
	if _, err := inventory.RefreshTimeline(context.Background(), rateLimitedTimelines{pauseUntil}, inventory.TimelineKey{Repo: asmSlug, Number: 9}); err == nil {
		t.Fatal("want a rate limit")
	}
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true}); err != nil {
		t.Fatal(err)
	}
	if len(g.order) != 1 {
		t.Fatalf("fetched %v while paused by a rate limit", g.order)
	}
	s := d.Current().Snapshot
	st = s.Sources[SourceGitHubTimelines]
	if st.Stale != 1 || !strings.Contains(st.Detail, "paused by a GitHub rate limit until") {
		t.Fatalf("github_timelines while paused: %+v", st)
	}
	p := s.Node(prNodeID(asmSlug, 1)).PR
	if p.Timeline == nil || p.Timeline.Freshness != Stale {
		t.Fatalf("PR 1's changed history: %+v", p.Timeline)
	}
	if p.Facts.WaitingSource != WaitingEstimated || !strings.Contains(p.Facts.Reasons[0].Evidence, "predates its last update") {
		t.Fatalf("a stale timeline refined the facts: %+v", p.Facts)
	}
}

// TestTimelinesWithoutCredentials: without GitHub nothing is queued, cached
// histories still show, and the source says why.
func TestTimelinesWithoutCredentials(t *testing.T) {
	a := inventoryAsm(t)
	a.c.cfg.GitHubUnavailable = github.ErrNoAuth.Error()
	seedInventory(t, a.since(), map[string][]github.PRFacts{"alice/open-authored": {openPR(1, "alice", "feat")}})
	s, jobs := a.build()
	for _, j := range jobs {
		if j.Kind == JobTimelines {
			t.Fatalf("queued %+v without credentials", j)
		}
	}
	if st := s.Sources[SourceGitHubTimelines]; st.State != Unknown || st.Detail != "0 of 1 PR have a cached history; "+github.ErrNoAuth.Error() {
		t.Fatalf("github_timelines %+v", st)
	}
}

// cannedTimelines serves one timeline for any PR.
type cannedTimelines struct{ tl *github.PRTimeline }

func (c cannedTimelines) PRTimeline(_ context.Context, repo string, number int) (*github.PRTimeline, error) {
	tl := *c.tl
	tl.Repo, tl.Number = repo, number
	return &tl, nil
}

type failingTimelines struct{}

func (failingTimelines) PRTimeline(context.Context, string, int) (*github.PRTimeline, error) {
	return nil, errors.New("HTTP 502")
}

type rateLimitedTimelines struct{ reset time.Time }

func (r rateLimitedTimelines) PRTimeline(context.Context, string, int) (*github.PRTimeline, error) {
	return nil, &github.RateLimitError{ResetAt: r.reset, Message: "API rate limit exceeded"}
}

func intRange(lo, hi int) []int {
	var out []int
	for i := lo; i <= hi; i++ {
		out = append(out, i)
	}
	return out
}

func sortedInts(s []int) []int {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

// TestTimelineWarmCachePerformance: collecting 500 inventory PRs whose
// histories are all cached (a draft round-trip, two reviewers, a change
// request, a re-request and an approval each) stays within gh-97's budget,
// under 1 s and a snapshot under 5 MB. It logs what the timelines add.
func TestTimelineWarmCachePerformance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var prs []github.PRFacts
	for i := range 500 {
		p := openPR(i+1, "alice", fmt.Sprintf("feat-%d", i+1))
		p.Repo = fmt.Sprintf("acme/repo-%d", i%20)
		prs = append(prs, p)
	}
	seedInventory(t, inventory.ClosedSince(time.Now(), DefaultDays), map[string][]github.PRFacts{
		"alice/open-authored": prs, "alice/review-requested": {}, "alice/closed-recent": {},
	})
	c := NewCollector(Config{People: []Person{{Login: "alice"}}, Identity: func() (string, string) { return "alice", "" }})
	collect := func() (*Snapshot, time.Duration, int) {
		start := time.Now()
		s, _, err := c.Collect(context.Background())
		elapsed := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		return s, elapsed, len(data)
	}
	_, bare, bareSize := collect()
	for _, p := range prs {
		c0 := p.CreatedAt
		tl := &github.PRTimeline{State: "open", Author: "alice", CreatedAt: c0, UpdatedAt: p.UpdatedAt, Events: []github.TimelineEvent{
			{Type: github.TimelineReady, At: c0.Add(2 * time.Hour)}, {Type: github.TimelineReviewRequested, At: c0.Add(2 * time.Hour), Reviewer: "bob"},
			{Type: github.TimelineReviewRequested, At: c0.Add(3 * time.Hour), Reviewer: "team:web"}, {Type: github.TimelineDraft, At: c0.Add(5 * time.Hour)},
			{Type: github.TimelineReady, At: c0.Add(7 * time.Hour)}, {Type: github.TimelineReview, At: c0.Add(9 * time.Hour), Reviewer: "bob", State: "CHANGES_REQUESTED"},
			{Type: github.TimelineReviewRequested, At: c0.Add(20 * time.Hour), Reviewer: "bob"}, {Type: github.TimelineReview, At: c0.Add(30 * time.Hour), Reviewer: "bob", State: "APPROVED"},
		}}
		if _, err := inventory.RefreshTimeline(context.Background(), cannedTimelines{tl}, inventory.TimelineKey{Repo: p.Repo, Number: p.Number}); err != nil {
			t.Fatal(err)
		}
	}
	s, elapsed, size := collect()
	if st := s.Sources[SourceGitHubTimelines]; st.Fresh != 500 || st.State != Fresh {
		t.Fatalf("github_timelines %+v", st)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	t.Logf("500 inventory PRs: collected in %v without timelines, %v with all 500 cached; snapshot %d bytes, %d with timelines (+%d, %.0f bytes per PR)",
		bare, elapsed, bareSize, size, size-bareSize, float64(size-bareSize)/500)
	if size >= 5<<20 {
		t.Fatalf("snapshot is %d bytes, want under 5 MB", size)
	}
	if enforce := !testing.Short() && !raceEnabled; enforce && elapsed >= time.Second {
		t.Fatalf("collection took %v, want under 1s", elapsed)
	}
}
