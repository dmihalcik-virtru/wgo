package dash

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/inventory"
	"github.com/virtru/wgo/internal/prcache"
	"github.com/virtru/wgo/models"
)

func TestCompareEffortsOrdersGroups(t *testing.T) {
	e := func(id, group, label string) *Node {
		return &Node{ID: id, Kind: KindEffort, Label: label, Effort: &EffortInfo{Group: group}}
	}
	list := []*Node{
		e("effort:ungrouped", GroupUngrouped, "Ungrouped"),
		e("effort:sprint:7", GroupSprint, "Sprint 7"),
		e("effort:repo:acme/web", GroupRepo, "acme/web"),
		e("effort:repo:acme/api", GroupRepo, "acme/api"),
		e("effort:theme", GroupTheme, "zeta"),
		e("effort:odd", "mystery", "alpha"),
		e("effort:ticket-team-1", GroupTicket, "TEAM-1"),
		e("effort:b", GroupEffort, "beta"),
		e("effort:a", GroupEffort, "Alpha"),
		{ID: "effort:bare", Kind: KindEffort, Label: "bare"},
	}
	slices.SortFunc(list, CompareEfforts)
	var got []string
	for _, n := range list {
		got = append(got, n.ID)
	}
	want := []string{"effort:a", "effort:b", "effort:ticket-team-1", "effort:odd", "effort:bare", "effort:theme",
		"effort:repo:acme/api", "effort:repo:acme/web", "effort:sprint:7", "effort:ungrouped"}
	if !slices.Equal(got, want) {
		t.Fatalf("order %v\nwant  %v", got, want)
	}
}

// TestPRFactsOmittedWhenUnknown pins the "unknown is omitted, never zero"
// contract: a bookmark-only PR encodes none of the inventory facts, while a
// reported zero and a reported empty list are kept.
func TestPRFactsOmittedWhenUnknown(t *testing.T) {
	b, err := json.Marshal(PRInfo{Number: 1, State: "open", Freshness: Fresh, BookmarkID: "bm-1", Sources: []string{PRSourceBookmark}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"author", "created_at", "merged_at", "closed_at", "ready_at", "additions", "deletions", "changed_files",
		"mergeable", "base_ref", "head_ref", "review_requests", "reviews", "check_details", "checks_truncated", "ticket_keys", "involvement"} {
		if strings.Contains(string(b), `"`+key+`"`) {
			t.Errorf("unknown %s is encoded: %s", key, b)
		}
	}
	zero := 0
	b, err = json.Marshal(PRInfo{Additions: &zero, Reviews: []Review{}, ReviewRequests: []ReviewRequest{}, CheckDetails: []CheckDetail{},
		TicketKeys: []string{}, Involvement: []Involvement{}, Freshness: Fresh, Sources: []string{PRSourceInventory}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"additions":0`, `"reviews":[]`, `"review_requests":[]`, `"check_details":[]`, `"ticket_keys":[]`, `"involvement":[]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("reported %s is lost: %s", want, b)
		}
	}
	if strings.Contains(string(b), "bookmark_id") {
		t.Errorf("an inventory-only PR omits bookmark_id: %s", b)
	}
	b, err = json.Marshal(TicketInfo{Key: "TEAM-1", System: "jira", Freshness: Fresh})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"key":"TEAM-1","system":"jira","freshness":"fresh"}`; string(b) != want {
		t.Errorf("a ticket without Jira facts encodes %s, want %s", b, want)
	}
}

// TestAssembleAnalysisWiring covers what assemble does for the analysis
// model itself: bookmark PRs say where they came from, repo: and sprint:
// keys make group efforts, the people list joins config with the cached
// identity, and merged counts are present for every effort.
func TestAssembleAnalysisWiring(t *testing.T) {
	a := newAsm(t)
	a.c.cfg.People = []Person{{Login: "bob", Name: "Bob"}}
	a.c.cfg.Pair = Person{Login: "carol"}
	a.c.cfg.Identity = func() (string, string) { return "alice", "" }
	w := a.ws("/w/wt/feature", "feature")
	if err := prcache.Write(asmOrigin, asmMain, "feature", []models.PRRef{{Number: 4, State: "OPEN"}}); err != nil {
		t.Fatal(err)
	}
	a.session("s-repo", "active", nil, "", "repo:acme/widgets")
	a.session("s-sprint", "active", nil, "", "sprint:7")
	// Merges are known only once everyone's closed-recent set is.
	seedInventory(t, inventory.ClosedSince(a.ls.at, a.ls.days), map[string][]github.PRFacts{"alice/closed-recent": {}, "bob/closed-recent": {}})
	s, _ := a.build()

	pr := s.Node(prNodeID(asmSlug, 4))
	if pr == nil || !slices.Equal(pr.PR.Sources, []string{PRSourceBookmark}) || pr.PR.BookmarkID != bookmarkID(asmMain, w.bookmark) {
		t.Fatalf("bookmark PR: %+v", pr)
	}
	for id, want := range map[string][2]string{
		effortNodeID(repoEffortKey("Acme/Widgets")): {GroupRepo, "acme/widgets"},
		effortNodeID(sprintEffortKey(7)):            {GroupSprint, "Sprint 7"},
	} {
		n := s.Node(id)
		if n == nil || n.Effort.Group != want[0] || n.Label != want[1] {
			t.Errorf("effort %s: %+v, want group %s label %q", id, n, want[0], want[1])
		}
		if len(s.Counts.Merged[id]) != s.Days {
			t.Errorf("effort %s has no merged series: %v", id, s.Counts.Merged)
		}
	}
	want := []Person{{Login: "alice", Jira: JiraCurrentUser, Me: true}, {Login: "bob", Name: "Bob"}}
	if !slices.Equal(s.People, want) {
		t.Errorf("people %+v, want %+v", s.People, want)
	}

	// Without a cached identity nobody is me, and with nobody configured
	// either, the pair alone is followed.
	a.c.cfg.People, a.c.cfg.Identity = nil, nil
	s, _ = a.build()
	if want := []Person{{Login: "carol"}}; !slices.Equal(s.People, want) {
		t.Errorf("people %+v, want %+v", s.People, want)
	}
}

// TestComputeCountsTrackedAndMerged covers the counts gh-97 adds: a PR
// attached by a tracks edge counts under that effort, an inventory PR on a
// bookmark whose lookup is unknown counts as a PR rather than unknown, and
// merged PRs are counted per day of merge inside the window only. An effort
// whose merged series is unknown has none rather than zeros: it holds a
// bookmark of unknown state, or a merged PR of unknown merge time that may
// have merged in the window.
func TestComputeCountsTrackedAndMerged(t *testing.T) {
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, asmZone)
	mergedAt := func(days int) time.Time { return at.AddDate(0, 0, -days) }
	s := &Snapshot{
		Days: 3,
		Nodes: []Node{
			{ID: "effort:e", Kind: KindEffort, Effort: &EffortInfo{Key: "e", Group: GroupEffort}},
			{ID: "effort:repo:acme/api", Kind: KindEffort, Effort: &EffortInfo{Key: "repo:acme/api", Group: GroupRepo}},
			{ID: "bm-1", Kind: KindBookmark, Bookmark: &BookmarkInfo{PRLookup: Unknown}},
			{ID: "bm-2", Kind: KindBookmark, Bookmark: &BookmarkInfo{PRLookup: Unknown}},
			{ID: "pr:1", Kind: KindPR, PR: &PRInfo{State: "open", BookmarkID: "bm-1"}},
			{ID: "pr:2", Kind: KindPR, PR: &PRInfo{State: "merged", MergedAt: mergedAt(0)}},
			{ID: "pr:3", Kind: KindPR, PR: &PRInfo{State: "merged", MergedAt: mergedAt(2)}},
			{ID: "pr:4", Kind: KindPR, PR: &PRInfo{State: "merged", MergedAt: mergedAt(3)}}, // before the window
			// Merge time unknown, but updated (so merged) before the window.
			{ID: "pr:5", Kind: KindPR, PR: &PRInfo{State: "merged", UpdatedAt: mergedAt(4)}},
			{ID: "pr:6", Kind: KindPR, PR: &PRInfo{State: "open", IsDraft: true}},
			{ID: "effort:f", Kind: KindEffort, Effort: &EffortInfo{Key: "f", Group: GroupEffort}},
			{ID: "effort:g", Kind: KindEffort, Effort: &EffortInfo{Key: "g", Group: GroupEffort}},
			// Merge time unknown and updated in the window: it may have merged in it.
			{ID: "pr:7", Kind: KindPR, PR: &PRInfo{State: "merged", UpdatedAt: mergedAt(1)}},
			// Merge time and update time unknown.
			{ID: "pr:8", Kind: KindPR, PR: &PRInfo{State: "merged"}},
		},
		Edges: []Edge{
			{"bm-1", "pr:1", EdgePR},
			{"effort:e", "pr:2", EdgeTracks},
			{"effort:repo:acme/api", "pr:2", EdgeTracks},
			{"effort:repo:acme/api", "pr:3", EdgeTracks},
			{"effort:repo:acme/api", "pr:4", EdgeTracks},
			{"effort:repo:acme/api", "pr:5", EdgeTracks},
			{"effort:repo:acme/api", "pr:6", EdgeTracks},
			{"effort:f", "pr:3", EdgeTracks},
			{"effort:f", "pr:7", EdgeTracks},
			{"effort:g", "pr:8", EdgeTracks},
		},
	}
	ls := &localState{at: at, days: 3}
	c := computeCounts(s, ls, map[string]map[string]bool{"effort:e": {"bm-1": true, "bm-2": true}})
	wantStates := map[string]map[string]int{
		"effort:e":             {"open": 1, "unknown": 1, "merged": 1},
		"effort:repo:acme/api": {"merged": 4, "draft": 1},
		"effort:f":             {"merged": 2},
		"effort:g":             {"merged": 1},
	}
	if !reflect.DeepEqual(c.PRStates, wantStates) {
		t.Errorf("pr_states %v, want %v", c.PRStates, wantStates)
	}
	// effort:e has a bookmark of unknown state; f and g a merged PR that
	// may have merged in the window at an unknown time.
	wantMerged := map[string][]int{"effort:repo:acme/api": {1, 0, 1}}
	if !reflect.DeepEqual(c.Merged, wantMerged) {
		t.Errorf("merged %v, want %v", c.Merged, wantMerged)
	}
}
