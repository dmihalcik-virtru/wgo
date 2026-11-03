package dash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/inventory"
	"github.com/virtru/wgo/internal/issuecache"
	"github.com/virtru/wgo/internal/prcache"
	"github.com/virtru/wgo/models"
)

// fakeInventory serves canned inventory sets by query and a login.
type fakeInventory struct {
	mu    sync.Mutex
	sets  map[string][]github.PRFacts // inventory.Key.String() -> PRs
	err   error
	login string
	calls atomic.Int32
}

func (f *fakeInventory) SearchPullRequests(_ context.Context, q string) (*github.PRSearchResult, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	for name, prs := range f.sets {
		login, set, _ := strings.Cut(name, "/")
		if q == (inventory.Key{Login: login, Set: inventory.Set(set)}).Query(time.Time{}) ||
			(set == string(inventory.ClosedRecent) && strings.HasPrefix(q, "is:pr is:closed archived:false author:"+login+" ")) {
			return &github.PRSearchResult{PRs: prs, IssueCount: len(prs)}, nil
		}
	}
	return &github.PRSearchResult{PRs: []github.PRFacts{}}, nil
}

func (f *fakeInventory) Viewer(context.Context) (string, error) {
	f.calls.Add(1)
	if f.login == "" {
		return "", errors.New("no login")
	}
	return f.login, nil
}

// seedInventory writes person/set entries into the cache, fetched now.
func seedInventory(t testing.TB, since time.Time, sets map[string][]github.PRFacts) {
	t.Helper()
	f := &fakeInventory{sets: sets}
	for name := range sets {
		login, set, _ := strings.Cut(name, "/")
		if _, err := inventory.Refresh(context.Background(), f, inventory.Key{Login: login, Set: inventory.Set(set)}, since); err != nil {
			t.Fatal(err)
		}
	}
}

func ip(n int) *int { return &n }

// openPR is an open PR in acme/widgets with every fact reported.
func openPR(n int, author, head string) github.PRFacts {
	created := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	return github.PRFacts{
		Repo: asmSlug, Number: n, URL: fmt.Sprintf("https://github.com/%s/pull/%d", asmSlug, n), Title: fmt.Sprintf("PR %d", n),
		State: "open", Author: author, CreatedAt: created, UpdatedAt: created.Add(48 * time.Hour), ReadyAt: created,
		Additions: ip(10), Deletions: ip(0), ChangedFiles: ip(2), Mergeable: "unknown", BaseRef: "main", HeadRef: head,
		ReviewDecision: "REVIEW_REQUIRED", ReviewRequests: []github.ReviewRequestFact{}, Reviews: []github.ReviewFact{},
		Checks: "success", CheckDetails: []github.CheckFact{{Name: "build", State: "success"}},
	}
}

// inventoryAsm is an assemble fixture whose collector follows alice (me)
// and bob, at the real clock so that freshly seeded sets are fresh.
func inventoryAsm(t *testing.T) *asmFixture {
	t.Helper()
	a := newAsm(t)
	a.ls.at = time.Now()
	a.c.cfg.People = []Person{{Login: "alice"}, {Login: "bob"}}
	a.c.cfg.Identity = func() (string, string) { return "alice", "" }
	return a
}

func (a *asmFixture) since() time.Time { return inventory.ClosedSince(a.ls.at, a.ls.days) }

func TestInventoryMergesIntoBookmarkPR(t *testing.T) {
	a := inventoryAsm(t)
	a.effort("e1", "Auth", "TEAM-5-a")
	a.ws(asmMain, "TEAM-5-a")
	fetched := time.Now().Add(-time.Minute)
	if err := prcache.Write(asmOrigin, asmMain, "TEAM-5-a", []models.PRRef{{Number: 12, State: "OPEN", Title: "old title",
		URL: "https://github.com/acme/widgets/pull/12", UpdatedAt: fetched.Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	pr := openPR(12, "alice", "TEAM-5-a")
	pr.Title, pr.UpdatedAt = "new title", time.Now()
	seedInventory(t, a.since(), map[string][]github.PRFacts{"alice/open-authored": {pr}})
	s, _ := a.build()

	id, bm := prNodeID(asmSlug, 12), bookmarkID(asmMain, "TEAM-5-a")
	var prs []string
	for _, n := range s.Nodes {
		if n.PR != nil {
			prs = append(prs, n.ID)
		}
	}
	if !slices.Equal(prs, []string{id}) {
		t.Fatalf("PR nodes %v, want only %s", prs, id)
	}
	p := s.Node(id).PR
	if !slices.Equal(p.Sources, []string{PRSourceBookmark, PRSourceInventory}) || p.BookmarkID != bm || !hasEdge(s, bm, id, EdgePR) {
		t.Fatalf("merged PR: sources %v bookmark %q", p.Sources, p.BookmarkID)
	}
	if p.Author != "alice" || p.Title != "new title" || p.Additions == nil || *p.Additions != 10 || p.Deletions == nil || *p.Deletions != 0 {
		t.Fatalf("inventory facts not merged: %+v", p)
	}
	if !slices.Equal(p.TicketKeys, []string{"TEAM-5"}) || !hasEdge(s, id, jiraTicketID("TEAM-5"), EdgeTicket) {
		t.Fatalf("ticket keys %v", p.TicketKeys)
	}
	if countEdges(s, EdgeTracks) != 0 {
		t.Fatalf("a bookmark PR needs no tracks edge: %v", s.Edges)
	}
	if p.Freshness != Fresh || p.FetchedAt.IsZero() || !p.FetchedAt.Before(time.Now()) {
		t.Fatalf("freshness %s fetched_at %v", p.Freshness, p.FetchedAt)
	}
	if !slices.Equal(p.Involvement, []Involvement{{Person: "alice", Role: RoleAuthor}}) {
		t.Fatalf("involvement %+v", p.Involvement)
	}
}

// TestInventoryMergeTakesStateFromTheNewerSource: the state and the facts
// that go with it come from one source, the newer, never a mix.
func TestInventoryMergeTakesStateFromTheNewerSource(t *testing.T) {
	t.Run("bookmark newer", func(t *testing.T) {
		a := inventoryAsm(t)
		a.ws(asmMain, "TEAM-5-a")
		old := openPR(12, "alice", "TEAM-5-a")
		old.ReviewRequests = []github.ReviewRequestFact{{Reviewer: "bob", Kind: ReviewerUser}}
		old.Reviews = []github.ReviewFact{{Author: "bob", State: ReviewCommented}}
		old.Mergeable = MergeConflicting
		seedInventory(t, a.since(), map[string][]github.PRFacts{"bob/review-requested": {old}})
		if err := prcache.Write(asmOrigin, asmMain, "TEAM-5-a", []models.PRRef{{Number: 12, State: "MERGED", URL: "u",
			ReviewDecision: ReviewApproved, UpdatedAt: old.UpdatedAt.Add(time.Hour)}}); err != nil {
			t.Fatal(err)
		}
		s, _ := a.build()
		p := s.Node(prNodeID(asmSlug, 12)).PR
		if p.State != "merged" || p.ReviewDecision != ReviewApproved {
			t.Fatalf("state %q decision %q, want the bookmark's", p.State, p.ReviewDecision)
		}
		if p.ReviewRequests != nil || p.Reviews != nil || p.CheckDetails != nil || p.Mergeable != "" || !p.ReadyAt.IsZero() {
			t.Fatalf("facts of the older open copy leaked into the merged PR: %+v", p)
		}
		if p.Author != "alice" || p.Additions == nil || !slices.Equal(p.TicketKeys, []string{"TEAM-5"}) {
			t.Fatalf("stable facts missing: %+v", p)
		}
		if !slices.Equal(p.Involvement, []Involvement{{Person: "alice", Role: RoleAuthor}}) {
			t.Fatalf("involvement %+v: a merged PR awaits nobody's review", p.Involvement)
		}
	})
	t.Run("inventory newer", func(t *testing.T) {
		a := inventoryAsm(t)
		a.ws(asmMain, "feat-x")
		if err := prcache.Write(asmOrigin, asmMain, "feat-x", []models.PRRef{{Number: 12, State: "OPEN", URL: "u",
			ReviewDecision: ReviewApproved, RequestedReviewers: []string{"carol"}}}); err != nil {
			t.Fatal(err)
		}
		pr := openPR(12, "alice", "feat-x") // the approval was dismissed since
		pr.ReviewDecision = ""
		pr.Reviews = []github.ReviewFact{{Author: "bob", State: ReviewDismissed}}
		seedInventory(t, a.since(), map[string][]github.PRFacts{"alice/open-authored": {pr}})
		s, _ := a.build()
		p := s.Node(prNodeID(asmSlug, 12)).PR
		if p.ReviewDecision != "" || len(p.RequestedReviewers) != 0 || len(p.ReviewRequests) != 0 || len(p.Reviews) != 1 {
			t.Fatalf("decision %q reviewers %v requests %v reviews %v, want the inventory's", p.ReviewDecision, p.RequestedReviewers, p.ReviewRequests, p.Reviews)
		}
		want := []Involvement{{Person: "alice", Role: RoleAuthor}, {Person: "bob", Role: RoleReviewed}}
		if !slices.Equal(p.Involvement, want) {
			t.Fatalf("involvement %+v, want %+v", p.Involvement, want)
		}
	})
}

// TestInventoryAuthorIsNotAReviewer: replying to a thread on your own PR is
// no review of it.
func TestInventoryAuthorIsNotAReviewer(t *testing.T) {
	a := inventoryAsm(t)
	pr := openPR(3, "alice", "x")
	pr.Reviews = []github.ReviewFact{{Author: "alice", State: ReviewCommented}, {Author: "bob", State: ReviewApproved}}
	seedInventory(t, a.since(), map[string][]github.PRFacts{"alice/open-authored": {pr}})
	s, _ := a.build()
	want := []Involvement{{Person: "alice", Role: RoleAuthor}, {Person: "bob", Role: RoleReviewed}}
	if got := s.Node(prNodeID(asmSlug, 3)).PR.Involvement; !slices.Equal(got, want) {
		t.Fatalf("involvement %+v, want %+v", got, want)
	}
}

// TestInventoryHeadRefFalsePositives: GitHub's own branch names on other
// people's PRs are no tickets once any prefix is known, and a reference to
// what the issue cache knows is a PR is no ticket either.
func TestInventoryHeadRefFalsePositives(t *testing.T) {
	a := inventoryAsm(t)
	a.c.cfg.TicketPrefixes = []string{"TEAM"}
	a.ws(asmMain, "WGO-5-mine")
	if err := prcache.Write(asmOrigin, asmMain, "WGO-5-mine", []models.PRRef{{Number: 1, State: "OPEN", URL: "u"}}); err != nil {
		t.Fatal(err)
	}
	if err := issuecache.Write(issuecache.Key{Owner: "acme", Repo: "widgets", Number: 45}, issuecache.Info{Number: 45, State: "closed", IsPR: true}); err != nil {
		t.Fatal(err)
	}
	revert := openPR(2, "carol", "revert-45-fix")
	revert.Body = "Reverts acme/widgets#45"
	seedInventory(t, a.since(), map[string][]github.PRFacts{"alice/review-requested": {
		openPR(1, "alice", "WGO-5-mine"), revert, openPR(3, "carol", "patch-1"), openPR(4, "carol", "sha-256-speedup"),
		openPR(5, "carol", "TEAM-7-real"), openPR(6, "carol", "gh-9-issue"),
	}})
	s, _ := a.build()
	for n, want := range map[int][]string{1: {"WGO-5"}, 2: {}, 3: {}, 4: {}, 5: {"TEAM-7"}, 6: {"gh-9"}} {
		if got := s.Node(prNodeID(asmSlug, n)).PR.TicketKeys; !slices.Equal(got, want) {
			t.Errorf("#%d ticket keys %v, want %v", n, got, want)
		}
	}
	for _, id := range []string{jiraTicketID("REVERT-45"), jiraTicketID("PATCH-1"), jiraTicketID("SHA-256"), githubTicketID(asmSlug, 45)} {
		if s.Node(id) != nil {
			t.Errorf("false ticket %s", id)
		}
	}

	// Knowing no prefix at all, branch tickets count as for bookmarks.
	b := inventoryAsm(t)
	seedInventory(t, b.since(), map[string][]github.PRFacts{"alice/review-requested": {openPR(3, "carol", "OPS-3-x")}})
	s, _ = b.build()
	if got := s.Node(prNodeID(asmSlug, 3)).PR.TicketKeys; !slices.Equal(got, []string{"OPS-3"}) {
		t.Errorf("ticket keys %v with no known prefix", got)
	}
}

// TestInventoryMergedNeedsClosedRecent: merged counts are omitted, not
// zeros, while someone's closed-recent set is missing or too short.
func TestInventoryMergedNeedsClosedRecent(t *testing.T) {
	a := inventoryAsm(t)
	seedInventory(t, a.since(), map[string][]github.PRFacts{"alice/closed-recent": {}})
	if s, _ := a.build(); s.Counts.Merged != nil {
		t.Fatalf("merged %v while bob's closed-recent has no data", s.Counts.Merged)
	}
	seedInventory(t, a.since(), map[string][]github.PRFacts{"bob/closed-recent": {}})
	if s, _ := a.build(); s.Counts.Merged == nil {
		t.Fatal("merged omitted though every closed-recent set is known")
	}
	// bob's set covering only today does not cover the 3-day window.
	seedInventory(t, time.Now(), map[string][]github.PRFacts{"bob/closed-recent": {}})
	if s, _ := a.build(); s.Counts.Merged != nil {
		t.Fatalf("merged %v from a closed-recent set shorter than the window", s.Counts.Merged)
	}
	// Nobody followed: nobody's merges are known.
	a.c.cfg.People, a.c.cfg.Identity = nil, nil
	if s, _ := a.build(); s.Counts.Merged != nil {
		t.Fatalf("merged %v with nobody followed", s.Counts.Merged)
	}
}

// TestInventoryPRMergedBetweenRefreshes: a PR that leaves open-authored
// stays in the snapshot, stale, and closed-recent is due at once, until
// closed-recent shows it merged.
func TestInventoryPRMergedBetweenRefreshes(t *testing.T) {
	a := inventoryAsm(t)
	a.c.cfg.People = []Person{{Login: "alice"}}
	since := a.since()
	seedInventory(t, since, map[string][]github.PRFacts{
		"alice/open-authored": {openPR(1, "alice", "a"), openPR(2, "alice", "b")},
		"alice/closed-recent": {},
	})
	time.Sleep(10 * time.Millisecond) // the drop is after closed-recent's fetch
	seedInventory(t, since, map[string][]github.PRFacts{"alice/open-authored": {openPR(1, "alice", "a")}})
	a.ls.at = time.Now()
	s, jobs := a.build()
	n := s.Node(prNodeID(asmSlug, 2))
	if n == nil || n.PR.Freshness != Stale || n.PR.State != "open" {
		t.Fatalf("the PR that left open-authored: %+v", n)
	}
	part := s.Sources[SourceGitHubInventory].Parts[2]
	if part.Name != "alice/closed-recent" || part.State != Stale {
		t.Fatalf("closed-recent part %+v, want stale (due)", part)
	}
	if !slices.ContainsFunc(jobs, func(j Job) bool { return j.Kind == JobInventory && j.Inventory.Set == inventory.ClosedRecent }) {
		t.Fatalf("closed-recent not queued: %v", jobs)
	}
	if s.Counts.Merged != nil {
		t.Fatalf("merged %v while closed-recent lags", s.Counts.Merged)
	}

	merged := openPR(2, "alice", "b")
	merged.State, merged.MergedAt, merged.ClosedAt = "merged", time.Now(), time.Now()
	seedInventory(t, since, map[string][]github.PRFacts{"alice/closed-recent": {merged}})
	a.ls.at = time.Now()
	s, _ = a.build()
	if p := s.Node(prNodeID(asmSlug, 2)).PR; p.State != "merged" || p.Freshness != Fresh {
		t.Fatalf("after closed-recent: %+v", p)
	}
	if s.Counts.Merged == nil {
		t.Fatal("merged still omitted once closed-recent caught up")
	}
}

// blockingInventory blocks every search until its context is done.
type blockingInventory struct{ started chan struct{} }

func (b *blockingInventory) SearchPullRequests(ctx context.Context, _ string) (*github.PRSearchResult, error) {
	b.started <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *blockingInventory) Viewer(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// TestRefresherCloseCancelsInventoryFetches: closing the Refresher stops a
// running inventory search instead of waiting out its pages and retries.
func TestRefresherCloseCancelsInventoryFetches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bi := &blockingInventory{started: make(chan struct{}, 1)}
	r := NewRefresher(Fetchers{Inventory: bi}, RefresherOptions{IgnoreLeases: true})
	done := r.Submit([]Job{{Kind: JobInventory, Inventory: inventory.Key{Login: "alice", Set: inventory.OpenAuthored}}})
	<-bi.started
	closed := make(chan struct{})
	go func() { r.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited for a running inventory search")
	}
	if res := <-done; len(res.Failed) != 1 || !strings.Contains(res.Failed[0], "context canceled") {
		t.Fatalf("result %+v", res)
	}
}

// TestInventoryMergedFreshnessIsTheWorse: a PR whose bookmark lookup is
// fresh but whose inventory set is stale is stale, at the older fetch.
func TestInventoryMergedFreshnessIsTheWorse(t *testing.T) {
	a := inventoryAsm(t)
	a.ws(asmMain, "feat-x")
	seedInventory(t, a.since(), map[string][]github.PRFacts{"alice/open-authored": {openPR(12, "alice", "feat-x")}})
	invFetched := inventory.Read(inventory.Key{Login: "alice", Set: inventory.OpenAuthored}, time.Hour, a.since(), time.Now()).FetchedAt
	a.ls.at = time.Now().Add(DefaultInventoryTTL + time.Minute) // the set is now stale
	a.c.cfg.PRTTL = time.Hour                                   // the bookmark lookup is not
	if err := prcache.Write(asmOrigin, asmMain, "feat-x", []models.PRRef{{Number: 12, State: "OPEN", URL: "u"}}); err != nil {
		t.Fatal(err)
	}
	s, _ := a.build()
	p := s.Node(prNodeID(asmSlug, 12)).PR
	if p.Freshness != Stale || !p.FetchedAt.Equal(invFetched) {
		t.Fatalf("freshness %s fetched_at %v, want stale at %v", p.Freshness, p.FetchedAt, invFetched)
	}
	if bi := s.Node(bookmarkID(asmMain, "feat-x")).Bookmark; bi.PRLookup != Fresh {
		t.Fatalf("bookmark lookup %s", bi.PRLookup)
	}
}

// TestInventoryAttachment covers gh-97 requirement 14, case by case, and
// that the first matching case wins.
func TestInventoryAttachment(t *testing.T) {
	type want struct {
		bookmark string // bookmark name for case 1
		effort   string // effort node ID tracking the PR for cases 2 to 4
	}
	for _, tc := range []struct {
		name  string
		setup func(a *asmFixture)
		pr    func() github.PRFacts
		want  want
	}{
		{
			name:  "1: workspace bookmark equals the head ref",
			setup: func(a *asmFixture) { a.ws(asmMain, "feat-x") },
			pr:    func() github.PRFacts { return openPR(1, "alice", "feat-x") },
			want:  want{bookmark: "feat-x"},
		},
		{
			name:  "1 wins over a ticket match",
			setup: func(a *asmFixture) { a.ws(asmMain, "TEAM-7-y") },
			pr:    func() github.PRFacts { p := openPR(1, "alice", "TEAM-7-y"); p.Title = "TEAM-7"; return p },
			want:  want{bookmark: "TEAM-7-y"},
		},
		{
			name:  "1 skips a head ref equal to the base (a fork's main)",
			setup: func(a *asmFixture) { a.ws(asmMain, "main") },
			pr:    func() github.PRFacts { return openPR(1, "alice", "main") },
			want:  want{effort: effortNodeID(repoEffortKey(asmSlug))},
		},
		{
			name:  "2: the ticket-<id> effort of a title key",
			setup: func(a *asmFixture) { a.ws(asmMain, "TEAM-7-y") },
			pr:    func() github.PRFacts { p := openPR(1, "bob", "other"); p.Title = "Follow-up for TEAM-7"; return p },
			want:  want{effort: effortNodeID("ticket-team-7")},
		},
		{
			name: "2 wins over 3",
			setup: func(a *asmFixture) {
				a.effort("e1", "Auth", "TEAM-9-b")
				a.ws("/w/acme/widgets-two", "TEAM-9-b")
				a.ws("/w/acme/widgets-three", "TEAM-9-a")
			},
			pr:   func() github.PRFacts { return openPR(1, "bob", "TEAM-9-c") },
			want: want{effort: effortNodeID("ticket-team-9")},
		},
		{
			name: "3: the effort of a workspace on the ticket",
			setup: func(a *asmFixture) {
				a.effort("e1", "Auth", "TEAM-8-z")
				a.ws(asmMain, "TEAM-8-z")
			},
			pr:   func() github.PRFacts { return openPR(1, "bob", "TEAM-8-other") },
			want: want{effort: effortNodeID("e1")},
		},
		{
			name:  "2 with a GitHub issue in the same repository",
			setup: func(a *asmFixture) { a.ws(asmMain, "gh-70-x") },
			pr:    func() github.PRFacts { p := openPR(1, "bob", "other"); p.Body = "Part of #70"; return p },
			want:  want{effort: effortNodeID("ticket-gh-70")},
		},
		{
			name:  "a GitHub issue of another repository never matches",
			setup: func(a *asmFixture) { a.ws(asmMain, "gh-70-x") },
			pr: func() github.PRFacts {
				p := openPR(1, "bob", "other")
				p.Repo, p.Body = "acme/other", "Part of #70"
				return p
			},
			want: want{effort: effortNodeID(repoEffortKey("acme/other"))},
		},
		{
			name:  "1 needs the same repository",
			setup: func(a *asmFixture) { a.ws(asmMain, "feat-x") },
			pr:    func() github.PRFacts { p := openPR(1, "alice", "feat-x"); p.Repo = "acme/other"; return p },
			want:  want{effort: effortNodeID(repoEffortKey("acme/other"))},
		},
		{
			name:  "4: the repository group",
			setup: func(a *asmFixture) { a.ws(asmMain, "unrelated") },
			pr:    func() github.PRFacts { return openPR(1, "alice", "feat-z") },
			want:  want{effort: effortNodeID(repoEffortKey(asmSlug))},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := inventoryAsm(t)
			tc.setup(a)
			pr := tc.pr()
			seedInventory(t, a.since(), map[string][]github.PRFacts{"alice/open-authored": {pr}})
			s, _ := a.build() // validates: an orphan PR would fail here
			id := prNodeID(pr.Repo, pr.Number)
			p := s.Node(id).PR
			if !slices.Equal(p.Sources, []string{PRSourceInventory}) {
				t.Fatalf("sources %v", p.Sources)
			}
			var trackedBy []string
			for _, e := range s.Edges {
				if e.Target == id && e.Kind == EdgeTracks {
					trackedBy = append(trackedBy, e.Source)
				}
			}
			if tc.want.bookmark != "" {
				bm := bookmarkID(asmMain, tc.want.bookmark)
				if p.BookmarkID != bm || !hasEdge(s, bm, id, EdgePR) || len(trackedBy) != 0 {
					t.Fatalf("bookmark_id %q tracked by %v, want bookmark %s", p.BookmarkID, trackedBy, tc.want.bookmark)
				}
				return
			}
			if p.BookmarkID != "" || !slices.Equal(trackedBy, []string{tc.want.effort}) {
				t.Fatalf("bookmark_id %q tracked by %v, want %s", p.BookmarkID, trackedBy, tc.want.effort)
			}
			if s.Counts.PRStates[tc.want.effort]["open"] != 1 {
				t.Fatalf("pr_states %v: the tracked PR is not counted", s.Counts.PRStates[tc.want.effort])
			}
		})
	}
}

// TestInventoryTeamOnlyRequest: a PR in bob's review-requested set that asks
// only one of his teams is review_requested for bob.
func TestInventoryTeamOnlyRequest(t *testing.T) {
	a := inventoryAsm(t)
	pr := openPR(3, "carol", "carol-change")
	pr.ReviewRequests = []github.ReviewRequestFact{{Reviewer: "team:web", Kind: ReviewerTeam}}
	direct := openPR(4, "carol", "carol-other")
	direct.ReviewRequests = []github.ReviewRequestFact{{Reviewer: "alice", Kind: ReviewerUser, RequestedAt: time.Now()}}
	direct.Reviews = []github.ReviewFact{{Author: "bob", State: ReviewCommented}}
	seedInventory(t, a.since(), map[string][]github.PRFacts{"bob/review-requested": {pr, direct}})
	s, _ := a.build()
	p := s.Node(prNodeID(asmSlug, 3)).PR
	if !slices.Equal(p.Involvement, []Involvement{{Person: "bob", Role: RoleReviewRequested}}) {
		t.Fatalf("involvement %+v", p.Involvement)
	}
	if !slices.Equal(p.RequestedReviewers, []string{"team:web"}) || !p.ReviewersKnown || p.Author != "carol" {
		t.Fatalf("reviewers %v known %v author %q", p.RequestedReviewers, p.ReviewersKnown, p.Author)
	}
	// A direct request names alice even though her own set was not read.
	q := s.Node(prNodeID(asmSlug, 4)).PR
	want := []Involvement{{Person: "alice", Role: RoleReviewRequested}, {Person: "bob", Role: RoleReviewRequested}, {Person: "bob", Role: RoleReviewed}}
	if !slices.Equal(q.Involvement, want) {
		t.Fatalf("involvement %+v, want %+v", q.Involvement, want)
	}
}

// TestInventoryTicketsAndCounts: ticket keys follow the known-prefix rule
// and become ticket nodes that are looked up; PR states and merged counts
// include inventory PRs.
func TestInventoryTicketsAndCounts(t *testing.T) {
	a := inventoryAsm(t)
	a.c.cfg.TicketPrefixes = []string{"ops"}
	a.ws(asmMain, "TEAM-1-local")
	pr := openPR(20, "alice", "feature")
	pr.Title = "TEAM-2: move to SHA-256"
	pr.Body = "Also OPS-3, P-384, fixes #5, see #21 and UTF-8"
	other := openPR(21, "alice", "other")
	merged := openPR(22, "alice", "old")
	merged.State, merged.MergedAt, merged.ClosedAt = "merged", a.ls.at.Add(-time.Hour), a.ls.at.Add(-time.Hour)
	seedInventory(t, a.since(), map[string][]github.PRFacts{
		"alice/open-authored": {pr, other},
		"alice/closed-recent": {merged},
		// Merged counts need every followed person's closed-recent set.
		"bob/closed-recent": {},
	})
	s, jobs := a.build()
	p := s.Node(prNodeID(asmSlug, 20)).PR
	if want := []string{"TEAM-2", "OPS-3", "gh-5"}; !slices.Equal(p.TicketKeys, want) {
		t.Fatalf("ticket keys %v, want %v (SHA-256, P-384 and UTF-8 are not tickets; #21 is a PR)", p.TicketKeys, want)
	}
	for _, tid := range []string{jiraTicketID("TEAM-2"), jiraTicketID("OPS-3"), githubTicketID(asmSlug, 5)} {
		if !hasEdge(s, prNodeID(asmSlug, 20), tid, EdgeTicket) {
			t.Errorf("no pr -> ticket edge to %s", tid)
		}
	}
	queued := map[string]bool{}
	for _, j := range jobs {
		queued[j.describe()] = true
	}
	for _, want := range []string{"jira TEAM-2", "jira OPS-3", "issue acme/widgets#5"} {
		if !queued[want] {
			t.Errorf("no lookup queued for %s: %v", want, queued)
		}
	}
	if q := s.Node(prNodeID(asmSlug, 21)).PR; q.TicketKeys == nil || len(q.TicketKeys) != 0 {
		t.Errorf("a PR without tickets reports none: %#v", q.TicketKeys)
	}
	repo := effortNodeID(repoEffortKey(asmSlug))
	if got := s.Counts.PRStates[repo]; got["open"] != 2 || got["merged"] != 1 {
		t.Errorf("pr_states %v", got)
	}
	if got := s.Counts.Merged[repo]; len(got) != s.Days || got[len(got)-1] != 1 {
		t.Errorf("merged %v: the PR merged today is not counted", got)
	}
}

// TestInventoryWithoutCredentials: with no GitHub credentials nothing is
// queued and github_inventory says why.
func TestInventoryWithoutCredentials(t *testing.T) {
	a := inventoryAsm(t)
	a.c.cfg.Identity = func() (string, string) { return "", "" }
	a.c.cfg.GitHubUnavailable = github.ErrNoAuth.Error()
	s, jobs := a.build()
	st := s.Sources[SourceGitHubInventory]
	if st.State != Unknown || st.Detail != github.ErrNoAuth.Error() || len(st.Parts) != 6 {
		t.Fatalf("github_inventory %+v", st)
	}
	for _, j := range jobs {
		if j.Kind == JobInventory || j.Kind == JobIdentity {
			t.Fatalf("queued %s without credentials", j.describe())
		}
	}
}

func TestInventoryWithNobodyToFollow(t *testing.T) {
	a := inventoryAsm(t)
	a.c.cfg.People = nil
	a.c.cfg.Identity = func() (string, string) { return "", "" }
	s, jobs := a.build()
	if st := s.Sources[SourceGitHubInventory]; st.State != Unknown || st.Detail != noPeopleDetail {
		t.Fatalf("github_inventory %+v", st)
	}
	if !slices.ContainsFunc(jobs, func(j Job) bool { return j.Kind == JobIdentity }) {
		t.Fatalf("no identity lookup queued: %v", jobs)
	}
	// Without an identity source (tests, older callers) none is queued.
	a.c.cfg.Identity = nil
	if _, jobs := a.build(); slices.ContainsFunc(jobs, func(j Job) bool { return j.Kind == JobIdentity }) {
		t.Fatalf("identity lookup queued with no Identity: %v", jobs)
	}
}

func TestInventoryPartsAndJobs(t *testing.T) {
	a := inventoryAsm(t)
	seedInventory(t, a.since(), map[string][]github.PRFacts{"alice/open-authored": {}})
	s, jobs := a.build()
	st := s.Sources[SourceGitHubInventory]
	var names []string
	for _, p := range st.Parts {
		names = append(names, p.Name+"="+string(p.State))
	}
	want := []string{"alice/open-authored=fresh", "alice/review-requested=unknown", "alice/closed-recent=unknown",
		"bob/open-authored=unknown", "bob/review-requested=unknown", "bob/closed-recent=unknown"}
	if !slices.Equal(names, want) || st.State != Unknown || st.Fresh != 1 || st.Unknown != 5 || st.OldestFetch.IsZero() {
		t.Fatalf("parts %v state %s: %+v", names, st.State, st)
	}
	var queued []string
	for _, j := range jobs {
		if j.Kind == JobInventory {
			queued = append(queued, j.Inventory.String())
			if !j.Since.Equal(a.since()) {
				t.Errorf("%s since %v", j.describe(), j.Since)
			}
		}
	}
	if len(queued) != 5 || slices.Contains(queued, "alice/open-authored") {
		t.Fatalf("queued %v, want the five sets that are not fresh", queued)
	}
}

func TestInventoryTruncatedSetIsDiagnosed(t *testing.T) {
	a := inventoryAsm(t)
	f := &truncatingInventory{}
	if _, err := inventory.Refresh(context.Background(), f, inventory.Key{Login: "alice", Set: inventory.ReviewRequested}, a.since()); err != nil {
		t.Fatal(err)
	}
	s, _ := a.build()
	part := s.Sources[SourceGitHubInventory].Parts[1]
	if part.Name != "alice/review-requested" || !part.Truncated {
		t.Fatalf("part %+v", part)
	}
	if !slices.ContainsFunc(s.Diagnostics, func(d string) bool { return strings.Contains(d, "alice/review-requested matched 1500 PRs") }) {
		t.Fatalf("diagnostics %v", s.Diagnostics)
	}
}

type truncatingInventory struct{ fakeInventory }

func (*truncatingInventory) SearchPullRequests(context.Context, string) (*github.PRSearchResult, error) {
	return &github.PRSearchResult{PRs: []github.PRFacts{openPR(1, "carol", "x")}, IssueCount: 1500, Truncated: true}, nil
}

// inventoryDash is a Dash over no workspaces that follows alice, at a clock
// offset from now.
func inventoryDash(t *testing.T, offset time.Duration, f inventory.Fetcher) *Dash {
	t.Helper()
	c := NewCollector(Config{
		Now:    func() time.Time { return time.Now().Add(offset) },
		People: []Person{{Login: "alice"}},
		Identity: func() (string, string) {
			login, _ := inventory.ReadIdentity(inventory.IdentityTTL, time.Now())
			return login, ""
		},
	})
	opts := Options{Dir: t.TempDir(), Collector: c}
	if f != nil {
		r := NewRefresher(Fetchers{Inventory: f}, RefresherOptions{Workers: 2, IgnoreLeases: true})
		t.Cleanup(r.Close)
		opts.Refresher = r
	}
	d, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestInventoryFailedRefreshKeepsStaleData: a refresh that fails leaves the
// previous inventory in the snapshot, stale, with the error on its part.
func TestInventoryFailedRefreshKeepsStaleData(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	since := inventory.ClosedSince(time.Now(), DefaultDays)
	seedInventory(t, since, map[string][]github.PRFacts{"alice/open-authored": {openPR(7, "alice", "feat")}})
	boom := errors.New("GitHub GraphQL search: HTTP 502 on page 1 after 3 retries")
	d := inventoryDash(t, DefaultInventoryTTL+time.Minute, &fakeInventory{err: boom, login: "alice"})
	err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true})
	if err == nil || !strings.Contains(err.Error(), "inventory alice/open-authored: "+boom.Error()) {
		t.Fatalf("Refresh = %v, want the failed set", err)
	}
	s := d.Current().Snapshot
	part := s.Sources[SourceGitHubInventory].Parts[0]
	if part.Name != "alice/open-authored" || part.State != Stale || part.Error != boom.Error() || part.FetchedAt.IsZero() {
		t.Fatalf("part %+v, want stale with the error", part)
	}
	n := s.Node(prNodeID(asmSlug, 7))
	if n == nil || n.PR.Freshness != Stale {
		t.Fatalf("the previous inventory PR is gone or not stale: %+v", n)
	}
	if st := s.Sources[SourceGitHubInventory].State; st != Error {
		// The other two sets failed with no earlier data to fall back on.
		t.Fatalf("source state %s, want error", st)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

// fakeGraphQL is a GitHub GraphQL endpoint serving viewer and the three
// inventory searches of alice: open-authored holds n PRs, the other sets
// none. It fails the first attempt at page failPage with a 502.
type fakeGraphQL struct {
	t        *testing.T
	n        int
	failPage int
	// nodes, when set, are open-authored's PRs instead of n generated ones.
	nodes    []any
	mu       sync.Mutex
	attempts map[string]int
	requests atomic.Int32
}

func (g *fakeGraphQL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.requests.Add(1)
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(body, &req); err != nil || r.URL.Path != "/graphql" {
		g.t.Errorf("bad request %s: %v", r.URL.Path, err)
	}
	if strings.HasPrefix(req.Query, "query { viewer") {
		_, _ = w.Write([]byte(`{"data": {"viewer": {"login": "alice"}}}`))
		return
	}
	q, _ := req.Variables["q"].(string)
	page := 1
	if after, _ := req.Variables["after"].(string); after != "" {
		page, _ = strconv.Atoi(after)
		page++
	}
	g.mu.Lock()
	if g.attempts == nil {
		g.attempts = map[string]int{}
	}
	key := q + "#" + strconv.Itoa(page)
	g.attempts[key]++
	first := g.attempts[key] == 1
	g.mu.Unlock()
	n := 0
	if strings.Contains(q, "is:open") && strings.Contains(q, "author:alice") {
		n = g.n
		if page == g.failPage && first {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
	}
	if g.nodes != nil && n > 0 {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"search": map[string]any{
			"issueCount": len(g.nodes), "pageInfo": map[string]any{"hasNextPage": false}, "nodes": g.nodes,
		}}})
		return
	}
	lo, hi := (page-1)*github.PRSearchPageSize, min(page*github.PRSearchPageSize, n)
	nodes := []any{}
	for i := lo + 1; i <= hi; i++ {
		nodes = append(nodes, map[string]any{
			"number": i, "title": fmt.Sprintf("PR %d", i), "url": fmt.Sprintf("https://github.com/acme/api/pull/%d", i),
			"state": "OPEN", "isDraft": i%10 == 0, "author": map[string]any{"login": "alice"},
			"headRefName": fmt.Sprintf("branch-%d", i), "baseRefName": "main",
			"repository": map[string]any{"nameWithOwner": "acme/api", "isArchived": false},
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"search": map[string]any{
		"issueCount": n,
		"pageInfo":   map[string]any{"hasNextPage": hi < n, "endCursor": strconv.Itoa(page)},
		"nodes":      nodes,
	}}})
}

// TestInventory195OpenPRs is gh-97's first acceptance criterion: 195 open
// authored PRs over 10 pages, with one transient 502 on page 4, all appear
// in the snapshot. The login is unknown at first, so this also covers the
// follow-up round that fetches the inventory once the identity is in.
func TestInventory195OpenPRs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	g := &fakeGraphQL{t: t, n: 195, failPage: 4}
	srv := httptest.NewServer(g)
	defer srv.Close()
	gc := github.NewClient(github.WithBaseURL(srv.URL), github.WithToken("test-token"), github.WithRetryBackoff(0, 0, 0))
	d := inventoryDash(t, 0, gc)
	d.opts.Collector.cfg.People = nil // only the authenticated user, once known
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true}); err != nil {
		t.Fatal(err)
	}
	s := d.Current().Snapshot
	open := 0
	for _, n := range s.Nodes {
		if n.PR != nil && n.PR.State == "open" {
			open++
		}
	}
	if open != 195 {
		t.Fatalf("%d open PRs in the snapshot, want 195", open)
	}
	if !slices.Equal(s.People, []Person{{Login: "alice", Jira: JiraCurrentUser, Me: true}}) {
		t.Fatalf("people %+v", s.People)
	}
	if st := s.Sources[SourceGitHubInventory]; st.State != Fresh || st.Fresh != 3 {
		t.Fatalf("github_inventory %+v", st)
	}
	if got := s.Counts.PRStates[effortNodeID(repoEffortKey("acme/api"))]; got["open"]+got["draft"] != 195 || got["draft"] != 19 {
		t.Fatalf("pr_states %v", got)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	// 1 viewer + 11 open-authored pages (10 and a retry) + 2 empty sets.
	if n := g.requests.Load(); n != 14 {
		t.Fatalf("%d requests, want 14", n)
	}
}

// fullNode reports every fact the inventory reads, for a merged PR.
const fullNode = `{
  "number": 9, "title": "TEAM-9: rotate keys", "url": "https://github.com/acme/api/pull/9",
  "state": "MERGED", "isDraft": false, "body": "Fixes #4",
  "createdAt": "2026-10-20T09:00:00Z", "updatedAt": "2026-10-29T14:10:00Z",
  "mergedAt": "2026-10-29T14:00:00Z", "closedAt": "2026-10-29T14:00:00Z",
  "additions": 0, "deletions": 4, "changedFiles": 1, "mergeable": "UNKNOWN",
  "baseRefName": "main", "headRefName": "TEAM-9-rotate", "reviewDecision": "APPROVED",
  "author": {"login": "alice"},
  "repository": {"nameWithOwner": "acme/api", "isArchived": false},
  "reviewRequests": {"nodes": [{"requestedReviewer": {"__typename": "Team", "slug": "web"}}]},
  "latestReviews": {"nodes": [{"author": {"login": "bob"}, "state": "APPROVED", "submittedAt": "2026-10-28T10:00:00Z"}]},
  "timelineItems": {"nodes": [
    {"__typename": "ReadyForReviewEvent", "createdAt": "2026-10-21T15:30:00Z"},
    {"__typename": "ReviewRequestedEvent", "createdAt": "2026-10-22T08:00:00Z", "requestedReviewer": {"__typename": "Team", "slug": "web"}}
  ]},
  "commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "FAILURE", "contexts": {"totalCount": 101, "nodes": [
    {"__typename": "CheckRun", "name": "vendor-hsm-tests", "status": "COMPLETED", "conclusion": "FAILURE", "detailsUrl": "https://ci.example/1"}
  ]}}}}]}
}`

// TestInventoryFactsFromGitHub: every fact in gh-97's table reaches the
// snapshot from the GraphQL response, and a PR GitHub reported nothing more
// about omits them rather than showing zeros.
func TestInventoryFactsFromGitHub(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var full map[string]any
	if err := json.Unmarshal([]byte(fullNode), &full); err != nil {
		t.Fatal(err)
	}
	sparse := map[string]any{"number": 10, "url": "https://github.com/acme/api/pull/10", "state": "OPEN",
		"repository": map[string]any{"nameWithOwner": "acme/api", "isArchived": false}}
	g := &fakeGraphQL{t: t, n: 1, nodes: []any{full, sparse}}
	srv := httptest.NewServer(g)
	defer srv.Close()
	gc := github.NewClient(github.WithBaseURL(srv.URL), github.WithToken("test-token"), github.WithRetryBackoff())
	d := inventoryDash(t, 0, gc)
	d.opts.Collector.cfg.People = []Person{{Login: "alice"}, {Login: "bob"}}
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true}); err != nil {
		t.Fatal(err)
	}
	s := d.Current().Snapshot
	keysOf := func(id string) map[string]any {
		n := s.Node(id)
		if n == nil {
			t.Fatalf("no node %s", id)
		}
		data, _ := json.Marshal(n.PR)
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		return m
	}
	facts := []string{"author", "created_at", "merged_at", "closed_at", "ready_at", "additions", "deletions", "changed_files",
		"mergeable", "base_ref", "head_ref", "review_requests", "reviews", "check_details", "checks_truncated", "ticket_keys",
		"involvement", "sources"}
	got := keysOf(prNodeID("acme/api", 9))
	for _, k := range facts {
		if _, ok := got[k]; !ok {
			t.Errorf("fact %s missing from the full PR: %v", k, got)
		}
	}
	p := s.Node(prNodeID("acme/api", 9)).PR
	if *p.Additions != 0 || p.Mergeable != MergeUnknown || p.Checks != "failure" || !p.ChecksTruncated ||
		!slices.Equal(p.TicketKeys, []string{"TEAM-9", "gh-4"}) ||
		!slices.Equal(p.ReviewRequests, []ReviewRequest{{Reviewer: "team:web", Kind: ReviewerTeam, RequestedAt: time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)}}) ||
		!slices.Equal(p.Involvement, []Involvement{{Person: "alice", Role: RoleAuthor}, {Person: "bob", Role: RoleReviewed}}) {
		t.Errorf("facts %+v", p)
	}
	got = keysOf(prNodeID("acme/api", 10))
	for _, k := range facts {
		_, ok := got[k]
		if want := k == "ticket_keys" || k == "involvement" || k == "sources"; ok != want {
			t.Errorf("sparse PR: fact %s present %v, want %v: %v", k, ok, want, got)
		}
	}
	if got["ticket_keys"] == nil || len(got["ticket_keys"].([]any)) != 0 {
		t.Errorf("sparse PR ticket_keys %v, want [] (wgo looked, found none)", got["ticket_keys"])
	}
}

// TestInventoryWarmCachePerformance: collecting from a warm cache of 500
// inventory PRs stays under a second and the snapshot under 5 MB.
func TestInventoryWarmCachePerformance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	since := inventory.ClosedSince(time.Now(), DefaultDays)
	mk := func(n int, repo, author string) github.PRFacts {
		p := openPR(n, author, fmt.Sprintf("TEAM-%d-work", n))
		p.Repo, p.URL = repo, fmt.Sprintf("https://github.com/%s/pull/%d", repo, n)
		p.Title = fmt.Sprintf("TEAM-%d: change number %d with a title of ordinary length", n, n)
		p.Body = fmt.Sprintf("Fixes #%d. Uses SHA-256. Follow-up of TEAM-%d.", n+1000, n+1)
		p.ReviewRequests = []github.ReviewRequestFact{{Reviewer: "bob", Kind: "user", RequestedAt: p.CreatedAt}, {Reviewer: "team:web", Kind: "team"}}
		p.Reviews = []github.ReviewFact{{Author: "carol", State: "COMMENTED", SubmittedAt: p.UpdatedAt}, {Author: "dave", State: "APPROVED", SubmittedAt: p.UpdatedAt}}
		p.CheckDetails = nil
		for c := range 8 {
			p.CheckDetails = append(p.CheckDetails, github.CheckFact{Name: fmt.Sprintf("check-%d", c), State: "success",
				URL: fmt.Sprintf("https://github.com/%s/actions/runs/%d", repo, n*10+c)})
		}
		return p
	}
	sets := map[string][]github.PRFacts{}
	for i := range 300 {
		sets["alice/open-authored"] = append(sets["alice/open-authored"], mk(i+1, fmt.Sprintf("acme/repo-%d", i%20), "alice"))
	}
	for i := range 200 {
		sets["alice/review-requested"] = append(sets["alice/review-requested"], mk(i+1, fmt.Sprintf("acme/other-%d", i%20), "carol"))
	}
	sets["alice/closed-recent"] = []github.PRFacts{}
	seedInventory(t, since, sets)
	c := NewCollector(Config{People: []Person{{Login: "alice"}}, Identity: func() (string, string) { return "alice", "" }})

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
	prs := 0
	for _, n := range s.Nodes {
		if n.PR != nil {
			prs++
		}
	}
	t.Logf("warm inventory of %d PRs: collected %d nodes, %d edges in %v; snapshot %d bytes (%.2f MB)",
		prs, len(s.Nodes), len(s.Edges), elapsed, len(data), float64(len(data))/(1<<20))
	if prs != 500 {
		t.Fatalf("%d PRs, want 500", prs)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(data) >= 5<<20 {
		t.Fatalf("snapshot is %d bytes, want under 5 MB", len(data))
	}
	if enforce := !testing.Short() && !raceEnabled; enforce && elapsed >= time.Second {
		t.Fatalf("collection took %v, want under 1s", elapsed)
	}
}

// TestDeltaTreatsInventoryPRs: a PR only the inventory found is new when
// every set that could hold it for one of its involvements was known at the
// last look, and unknown otherwise; one that closed before the last look is
// never new; and one that disappears while one of its sets has no data is
// unknown, not silently gone.
func TestDeltaTreatsInventoryPRs(t *testing.T) {
	ack := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	part := func(name string, st Freshness) SourcePart { return SourcePart{Name: name, State: st} }
	snap := func(parts []SourcePart, prs ...Node) *Snapshot {
		s := &Snapshot{Sources: map[string]SourceStatus{SourceGitHubInventory: {Parts: parts}}}
		s.Nodes = append(s.Nodes, prs...)
		return s
	}
	invPR := func(n int, state string, closed time.Time, inv ...Involvement) Node {
		return Node{ID: prNodeID(asmSlug, n), Kind: KindPR, Label: "#" + strconv.Itoa(n), PR: &PRInfo{Number: n, State: state, ClosedAt: closed,
			Sources: []string{PRSourceInventory}, Involvement: inv, ReviewersKnown: true, RequestedReviewers: []string{"bob"}}}
	}
	alice := Involvement{Person: "alice", Role: RoleAuthor}
	carol := Involvement{Person: "carol", Role: RoleAuthor}
	bobAsked := Involvement{Person: "bob", Role: RoleReviewRequested}

	before := snap([]SourcePart{part("alice/open-authored", Fresh), part("alice/closed-recent", Fresh), part("bob/review-requested", Unknown),
		part("carol/open-authored", Fresh), part("carol/closed-recent", Error)},
		invPR(1, "open", time.Time{}, alice))
	b := baselineFrom(before, ack)
	if want := []string{"alice/closed-recent", "alice/open-authored", "carol/open-authored"}; !slices.Equal(b.InventoryParts, want) {
		t.Fatalf("baseline parts %v, want %v", b.InventoryParts, want)
	}
	if got, want := b.PRs[prNodeID(asmSlug, 1)].InventoryParts, []string{"alice/closed-recent", "alice/open-authored"}; !slices.Equal(got, want) {
		t.Fatalf("baseline PR parts %v, want %v", got, want)
	}

	now := snap([]SourcePart{part("alice/open-authored", Fresh), part("alice/closed-recent", Fresh), part("bob/review-requested", Fresh),
		part("carol/open-authored", Fresh), part("carol/closed-recent", Fresh)},
		invPR(1, "open", time.Time{}, alice),
		invPR(2, "open", time.Time{}, alice),              // both of alice's sets were known: new
		invPR(3, "open", time.Time{}, bobAsked),           // bob/review-requested was not: unknown
		invPR(4, "closed", ack.Add(time.Hour), carol),     // carol/closed-recent was not known: unknown
		invPR(5, "closed", ack.Add(-48*time.Hour), alice)) // closed before the look: unknown
	d := computeDelta(b, now)
	status := map[int]string{}
	for _, p := range d.PRs {
		n, _ := strconv.Atoi(p.ID[strings.LastIndex(p.ID, "#")+1:])
		status[n] = p.Status
	}
	want := map[int]string{2: ItemNew, 3: ItemUnknown, 4: ItemUnknown, 5: ItemUnknown}
	if !maps.Equal(status, want) || d.Summary.Unchanged != 1 {
		t.Fatalf("delta %v, want %v and #1 unchanged", status, want)
	}
	if d.Summary.NewReviewRequests != 1 {
		t.Fatalf("new review requests %d, want #2's", d.Summary.NewReviewRequests)
	}

	// With closed-recent known at the look, a PR closed after it is new.
	b2 := baselineFrom(now, ack)
	later := snap(now.Sources[SourceGitHubInventory].Parts, invPR(6, "merged", ack.Add(time.Hour), alice))
	later.Nodes[0].PR.MergedAt, later.Nodes[0].PR.ClosedAt = ack.Add(time.Hour), time.Time{}
	if d := computeDelta(b2, later); len(d.PRs) == 0 || d.PRs[0].Status != ItemNew {
		t.Fatalf("delta %+v, want #6 new", d.PRs)
	}

	// A baseline from before the inventory knew no sets: nothing is new.
	old := baselineFrom(snap(nil), ack)
	if d := computeDelta(old, now); d.Summary.PRStateChanges != 0 || d.Summary.Unknown != 5 {
		t.Fatalf("delta against an old baseline: %+v", d.Summary)
	}

	// #1 disappears while its own set has no data, though another has.
	gone := snap([]SourcePart{part("alice/open-authored", Error), part("bob/review-requested", Fresh)})
	d = computeDelta(b, gone)
	if len(d.PRs) != 1 || d.PRs[0].Status != ItemUnknown || d.PRs[0].ID != prNodeID(asmSlug, 1) {
		t.Fatalf("delta %+v, want #1 unknown", d.PRs)
	}
	// It disappears while its sets are fresh: it is simply gone.
	if d := computeDelta(b, snap([]SourcePart{part("alice/open-authored", Fresh), part("alice/closed-recent", Fresh)})); len(d.PRs) != 0 {
		t.Fatalf("delta %+v, want nothing", d.PRs)
	}
}

// TestRefresherHonoursInventoryBackoff: a set in its failure back-off is
// not fetched in the background, but an explicit refresh still fetches it.
func TestRefresherHonoursInventoryBackoff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	k := inventory.Key{Login: "alice", Set: inventory.OpenAuthored}
	if _, err := inventory.Refresh(context.Background(), &fakeInventory{err: errors.New("HTTP 502")}, k, time.Time{}); err == nil {
		t.Fatal("want a failure")
	}
	f := &fakeInventory{login: "alice"}
	r := NewRefresher(Fetchers{Inventory: f}, RefresherOptions{})
	res := <-r.Submit([]Job{{Kind: JobInventory, Inventory: k}})
	r.Close()
	if f.calls.Load() != 0 || len(res.Failed) != 0 {
		t.Fatalf("fetched during back-off: %d calls, %+v", f.calls.Load(), res)
	}
	r = NewRefresher(Fetchers{Inventory: f}, RefresherOptions{IgnoreLeases: true})
	<-r.Submit([]Job{{Kind: JobInventory, Inventory: k}})
	r.Close()
	if f.calls.Load() == 0 {
		t.Fatal("an explicit refresh did not fetch")
	}
}

func TestRefresherSkipsInventoryWithoutFetcher(t *testing.T) {
	r := NewRefresher(Fetchers{Missing: map[JobKind]string{JobInventory: "no GitHub token or gh"}}, RefresherOptions{})
	defer r.Close()
	res := <-r.Submit([]Job{{Kind: JobInventory, Inventory: inventory.Key{Login: "alice", Set: inventory.OpenAuthored}}, {Kind: JobIdentity}})
	if res.Jobs != 0 || res.Skipped[JobInventory] != 1 || res.Skipped[JobIdentity] != 1 {
		t.Fatalf("result %+v", res)
	}
	if got := r.skipSummary(res); !strings.Contains(got, "1 GitHub PR inventory lookups skipped: no GitHub token or gh") || !strings.Contains(got, "1 GitHub identity lookups skipped") {
		t.Fatalf("skip summary %q", got)
	}
}
