package dash

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/inventory"
	"github.com/virtru/wgo/internal/prcache"
	"github.com/virtru/wgo/models"
)

// These tests pin the inventory's "unknown is never complete" rules: a set
// GitHub served only partly, a login not yet known, a closed-recent set that
// failed, and an older set than the facts it would contradict.

// partialInventory serves one set as GitHub does when the token cannot read
// some repository: a null node, the rest of the data, and an error.
type partialInventory struct{ fakeInventory }

func (*partialInventory) SearchPullRequests(context.Context, string) (*github.PRSearchResult, error) {
	return &github.PRSearchResult{PRs: []github.PRFacts{}, IssueCount: 1, Incomplete: true},
		&github.GraphQLErrors{Errors: []github.GraphQLError{{Type: "FORBIDDEN", Message: "Resource not accessible by integration"}}}
}

// TestInventoryIncompleteSetIsNotCoverage: a set GitHub served only partly
// is kept with its error, but says so, and covers nothing: merges stay
// unknown and a PR missing from it is not reported gone.
func TestInventoryIncompleteSetIsNotCoverage(t *testing.T) {
	a := inventoryAsm(t)
	seedInventory(t, a.since(), map[string][]github.PRFacts{"alice/open-authored": {}, "bob/closed-recent": {}})
	k := inventory.Key{Login: "alice", Set: inventory.ClosedRecent}
	if _, err := inventory.Refresh(context.Background(), &partialInventory{}, k, a.since()); err != nil {
		t.Fatalf("partial data is a successful fetch: %v", err)
	}
	s, _ := a.build()
	var part SourcePart
	for _, p := range s.Sources[SourceGitHubInventory].Parts {
		if p.Name == "alice/closed-recent" {
			part = p
		}
	}
	if part.State != Fresh || !part.Incomplete || !strings.Contains(part.Error, "Resource not accessible") {
		t.Fatalf("part %+v, want fresh, incomplete and the error", part)
	}
	if !slices.ContainsFunc(s.Diagnostics, func(d string) bool { return strings.Contains(d, "alice/closed-recent is incomplete") }) {
		t.Errorf("no diagnostic for the incomplete set: %v", s.Diagnostics)
	}
	if s.Counts.Merged != nil {
		t.Errorf("merged counts from an incomplete closed-recent set: %v", s.Counts.Merged)
	}
	if inventoryCoverage(s)["alice/closed-recent"] {
		t.Error("an incomplete set counts as coverage")
	}
}

// TestInventoryUnknownLoginIsExplained: until the authenticated login is
// known, the user's own PRs cannot be shown; the source says so, and why.
func TestInventoryUnknownLoginIsExplained(t *testing.T) {
	a := inventoryAsm(t)
	a.c.cfg.People = []Person{{Login: "bob"}}
	a.c.cfg.Identity = func() (string, string) { return "", "" }
	seedInventory(t, a.since(), map[string][]github.PRFacts{"bob/open-authored": {openPR(5, "bob", "x")}})

	s, jobs := a.build()
	st := s.Sources[SourceGitHubInventory]
	if !strings.Contains(st.Detail, "your GitHub login is not known yet") || !strings.Contains(st.Detail, "background") {
		t.Fatalf("detail %q", st.Detail)
	}
	if !slices.ContainsFunc(jobs, func(j Job) bool { return j.Kind == JobIdentity }) {
		t.Error("no identity lookup queued")
	}
	if s.Counts.Merged != nil {
		t.Errorf("merged counts without me: %v", s.Counts.Merged)
	}

	a.c.cfg.IdentityError = func() string { return "HTTP 502" }
	s, _ = a.build()
	if d := s.Sources[SourceGitHubInventory].Detail; !strings.Contains(d, "the last lookup failed: HTTP 502") {
		t.Fatalf("detail %q does not give the failure", d)
	}
	if !slices.ContainsFunc(s.Diagnostics, func(d string) bool { return strings.Contains(d, "HTTP 502") }) {
		t.Errorf("diagnostics %v", s.Diagnostics)
	}
}

// TestInventoryCarriesDroppedPRsWhenClosedRecentFailed: a PR that left the
// open-authored set stays, stale, while closed-recent has no data to say
// what became of it.
func TestInventoryCarriesDroppedPRsWhenClosedRecentFailed(t *testing.T) {
	a := inventoryAsm(t)
	open := inventory.Key{Login: "alice", Set: inventory.OpenAuthored}
	closed := inventory.Key{Login: "alice", Set: inventory.ClosedRecent}
	f := &fakeInventory{sets: map[string][]github.PRFacts{"alice/open-authored": {openPR(1, "alice", "a"), openPR(2, "alice", "b")}}}
	if _, err := inventory.Refresh(context.Background(), f, open, a.since()); err != nil {
		t.Fatal(err)
	}
	if _, err := inventory.Refresh(context.Background(), &fakeInventory{err: errors.New("HTTP 502")}, closed, a.since()); err == nil {
		t.Fatal("want the closed-recent failure")
	}
	time.Sleep(2 * time.Millisecond)
	f.sets["alice/open-authored"] = []github.PRFacts{openPR(1, "alice", "a")}
	if _, err := inventory.Refresh(context.Background(), f, open, a.since()); err != nil {
		t.Fatal(err)
	}
	a.ls.at = time.Now()
	s, _ := a.build()
	n := s.Node(prNodeID(asmSlug, 2))
	if n == nil || n.PR.Freshness != Stale {
		t.Fatalf("PR 2 left open-authored while closed-recent failed: %+v", n)
	}
}

// TestInventoryOlderRequestSetDoesNotOutvoteNewerFacts: a review-requested
// set fetched before the facts the PR carries cannot keep a request those
// facts no longer show, unless they still request a team.
func TestInventoryOlderRequestSetDoesNotOutvoteNewerFacts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		requests []github.ReviewRequestFact
		want     bool
	}{
		{"approved since", []github.ReviewRequestFact{}, false},
		{"a team still asked", []github.ReviewRequestFact{{Reviewer: teamPrefix + "web", Kind: ReviewerTeam}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := inventoryAsm(t)
			asked := openPR(30, "alice", "x")
			asked.ReviewRequests = []github.ReviewRequestFact{{Reviewer: "bob", Kind: ReviewerUser}}
			seedInventory(t, a.since(), map[string][]github.PRFacts{"bob/review-requested": {asked}})
			time.Sleep(2 * time.Millisecond)
			now := openPR(30, "alice", "x")
			now.ReviewRequests = tc.requests
			now.Reviews = []github.ReviewFact{{Author: "bob", State: ReviewApproved}}
			seedInventory(t, a.since(), map[string][]github.PRFacts{"alice/open-authored": {now}})
			a.ls.at = time.Now()
			s, _ := a.build()
			got := slices.Contains(s.Node(prNodeID(asmSlug, 30)).PR.Involvement, Involvement{Person: "bob", Role: RoleReviewRequested})
			if got != tc.want {
				t.Fatalf("bob review_requested = %v, want %v: %+v", got, tc.want, s.Node(prNodeID(asmSlug, 30)).PR.Involvement)
			}
		})
	}
}

// TestInventoryNewestSetSuppliesFacts: a PR in several sets takes its facts
// from the most recently fetched one, so a stale "open" never hides a merge.
func TestInventoryNewestSetSuppliesFacts(t *testing.T) {
	a := inventoryAsm(t)
	open := openPR(40, "alice", "x")
	seedInventory(t, a.since(), map[string][]github.PRFacts{"alice/open-authored": {open}})
	time.Sleep(2 * time.Millisecond)
	merged := openPR(40, "alice", "x")
	merged.State, merged.MergedAt, merged.ClosedAt = "merged", time.Now().Add(-time.Minute), time.Now().Add(-time.Minute)
	seedInventory(t, a.since(), map[string][]github.PRFacts{"alice/closed-recent": {merged}})
	a.ls.at = time.Now()
	s, _ := a.build()
	if p := s.Node(prNodeID(asmSlug, 40)).PR; p.State != "merged" || p.MergedAt.IsZero() {
		t.Fatalf("state %q merged_at %v, want the newer closed-recent facts", p.State, p.MergedAt)
	}
}

// TestInventoryRecordsClosedRecentCoverage: the closed-recent part names the
// start of the window its search covers, even when it found nothing, so a
// period before it reads as incompletely covered.
func TestInventoryRecordsClosedRecentCoverage(t *testing.T) {
	a := inventoryAsm(t)
	seedInventory(t, a.since(), map[string][]github.PRFacts{"alice/closed-recent": {}, "alice/open-authored": {}})
	s, _ := a.build()
	for _, p := range s.Sources[SourceGitHubInventory].Parts {
		switch p.Name {
		case "alice/closed-recent":
			if !p.CoverageFrom.Equal(a.since()) {
				t.Errorf("closed-recent coverage_from %v, want %v", p.CoverageFrom, a.since())
			}
		default:
			if !p.CoverageFrom.IsZero() {
				t.Errorf("%s has coverage_from %v; only a time-bounded search does", p.Name, p.CoverageFrom)
			}
		}
	}
}

// TestInventoryNewerBookmarkDropsRequest: when the bookmark's lookup is
// newer and lists reviewers without the person or a team, their older
// review-requested set no longer makes the open PR theirs to review.
func TestInventoryNewerBookmarkDropsRequest(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reviewers []string
		want      bool
	}{
		{"nobody asked now", []string{}, false},
		{"a team asked now", []string{teamPrefix + "web"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := inventoryAsm(t)
			a.ws(asmMain, "TEAM-5-a")
			old := openPR(12, "alice", "TEAM-5-a")
			old.ReviewRequests = []github.ReviewRequestFact{{Reviewer: "bob", Kind: ReviewerUser}}
			seedInventory(t, a.since(), map[string][]github.PRFacts{"bob/review-requested": {old}})
			if err := prcache.Write(asmOrigin, asmMain, "TEAM-5-a", []models.PRRef{{Number: 12, State: "OPEN", URL: "u",
				RequestedReviewers: tc.reviewers, UpdatedAt: old.UpdatedAt.Add(time.Hour)}}); err != nil {
				t.Fatal(err)
			}
			s, _ := a.build()
			p := s.Node(prNodeID(asmSlug, 12)).PR
			if got := slices.Contains(p.Involvement, Involvement{Person: "bob", Role: RoleReviewRequested}); got != tc.want {
				t.Fatalf("bob review_requested = %v, want %v: %+v", got, tc.want, p.Involvement)
			}
		})
	}
}

// TestRefresherRenewsStaleIdentity: an inventory job also renews a login
// cached longer ago than inventory.IdentityTTL, since collections only ask
// for one while none is known; a failed renewal keeps the old login and
// does not fail the job.
func TestRefresherRenewsStaleIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := inventory.Dir()
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * inventory.IdentityTTL)
	writeIdentity := func() {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"login": "alice", "fetched_at": old, "last_attempt_at": old})
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "identity.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	job := Job{Kind: JobInventory, Inventory: inventory.Key{Login: "alice", Set: inventory.OpenAuthored}}

	writeIdentity()
	r := NewRefresher(Fetchers{Inventory: &fakeInventory{login: "carol"}}, RefresherOptions{IgnoreLeases: true})
	<-r.Submit([]Job{job})
	r.Close()
	if login, st := inventory.ReadIdentity(inventory.IdentityTTL, time.Now()); login != "carol" || st != inventory.Fresh {
		t.Fatalf("identity %q (%v), want the renewed login", login, st)
	}

	writeIdentity()
	r = NewRefresher(Fetchers{Inventory: &fakeInventory{}}, RefresherOptions{IgnoreLeases: true}) // Viewer fails
	res := <-r.Submit([]Job{job})
	r.Close()
	if len(res.Failed) > 0 {
		t.Fatalf("a failed identity renewal failed the inventory job: %v", res.Failed)
	}
	if login, _ := inventory.ReadIdentity(inventory.IdentityTTL, time.Now()); login != "alice" {
		t.Fatalf("identity %q, want the old login kept", login)
	}
	if inventory.IdentityError() == "" {
		t.Error("the failed renewal is not recorded")
	}
}
