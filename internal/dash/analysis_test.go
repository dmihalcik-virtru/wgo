package dash

import (
	"encoding/json"
	"errors"
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
// identity, and merged counts are present once every person's closed-recent
// set is known and absent without the authenticated user.
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
	// Without me, my own merges were never searched: no merged count is
	// known, and none is reported as zero.
	if s.Counts.Merged != nil {
		t.Errorf("merged counts without me: %v", s.Counts.Merged)
	}
}

// TestComputeCountsTrackedAndMerged covers the counts gh-97 adds: a PR
// attached by a tracks edge counts under that effort; an inventory PR on a
// bookmark whose lookup is unknown counts as a PR without hiding that
// unknown lookup; and merged PRs are counted per day of merge inside the
// window only, for efforts whose merged count is knowable. A merged PR of
// unknown merge time leaves its effort's series unknown only if it may have
// merged in the window: its last update is unknown or not before it.
func TestComputeCountsTrackedAndMerged(t *testing.T) {
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, asmZone)
	mergedAt := func(days int) time.Time { return at.AddDate(0, 0, -days) }
	s := &Snapshot{
		Days: 3,
		Sources: map[string]SourceStatus{SourceGitHubInventory: {State: Fresh, Parts: []SourcePart{
			{Name: "alice/closed-recent", State: Fresh},
		}}},
		People: []Person{{Login: "Alice", Me: true}},
		Nodes: []Node{
			{ID: "effort:e", Kind: KindEffort, Effort: &EffortInfo{Key: "e", Group: GroupEffort}},
			{ID: "effort:repo:acme/api", Kind: KindEffort, Effort: &EffortInfo{Key: "repo:acme/api", Group: GroupRepo}},
			{ID: "effort:repo:acme/web", Kind: KindEffort, Effort: &EffortInfo{Key: "repo:acme/web", Group: GroupRepo}},
			{ID: "bm-1", Kind: KindBookmark, Bookmark: &BookmarkInfo{PRLookup: Unknown}},
			{ID: "bm-2", Kind: KindBookmark, Bookmark: &BookmarkInfo{PRLookup: Fresh}},
			{ID: "pr:1", Kind: KindPR, PR: &PRInfo{State: "open", BookmarkID: "bm-1"}},
			{ID: "pr:2", Kind: KindPR, PR: &PRInfo{State: "merged", MergedAt: mergedAt(0)}},
			{ID: "pr:3", Kind: KindPR, PR: &PRInfo{State: "merged", MergedAt: mergedAt(2)}},
			{ID: "pr:4", Kind: KindPR, PR: &PRInfo{State: "merged", MergedAt: mergedAt(3)}}, // before the window
			{ID: "pr:5", Kind: KindPR, PR: &PRInfo{State: "merged"}},                        // merge time unknown
			{ID: "pr:6", Kind: KindPR, PR: &PRInfo{State: "open", IsDraft: true}},
			// Merge time unknown and updated in the window: it may have merged in it.
			{ID: "pr:7", Kind: KindPR, PR: &PRInfo{State: "merged", UpdatedAt: mergedAt(1)}},
			// Merge time unknown, but updated (so merged) before the window.
			{ID: "pr:9", Kind: KindPR, PR: &PRInfo{State: "merged", UpdatedAt: mergedAt(4)}},
			{ID: "effort:f", Kind: KindEffort, Effort: &EffortInfo{Key: "f", Group: GroupEffort}},
		},
		Edges: []Edge{
			{"bm-1", "pr:1", EdgePR},
			{"effort:e", "pr:2", EdgeTracks},
			{"effort:repo:acme/api", "pr:2", EdgeTracks},
			{"effort:repo:acme/api", "pr:3", EdgeTracks},
			{"effort:repo:acme/api", "pr:4", EdgeTracks},
			{"effort:repo:acme/api", "pr:6", EdgeTracks},
			{"effort:repo:acme/api", "pr:9", EdgeTracks},
			{"effort:repo:acme/web", "pr:5", EdgeTracks},
			{"effort:f", "pr:3", EdgeTracks},
			{"effort:f", "pr:7", EdgeTracks},
		},
	}
	ls := &localState{at: at, days: 3}
	bookmarks := map[string]map[string]bool{"effort:e": {"bm-1": true, "bm-2": true}}
	c := computeCounts(s, ls, bookmarks)
	wantStates := map[string]map[string]int{
		"effort:e":             {"open": 1, "unknown": 1, "none": 1, "merged": 1},
		"effort:repo:acme/api": {"merged": 4, "draft": 1},
		"effort:repo:acme/web": {"merged": 1},
		"effort:f":             {"merged": 2},
	}
	if !reflect.DeepEqual(c.PRStates, wantStates) {
		t.Errorf("pr_states %v, want %v", c.PRStates, wantStates)
	}
	// effort:e has a bookmark whose PRs are unknown, and acme/web and f a
	// merge at an unknown time that may fall in the window: none has a
	// series. acme/api's unknown merge time predates the window.
	wantMerged := map[string][]int{"effort:repo:acme/api": {1, 0, 1}}
	if !reflect.DeepEqual(c.Merged, wantMerged) {
		t.Errorf("merged %v, want %v", c.Merged, wantMerged)
	}

	// Without the authenticated user, or without every person's
	// closed-recent set fresh and complete, no merged count is known at all.
	for name, mutate := range map[string]func(s *Snapshot){
		"truncated": func(s *Snapshot) { s.Sources[SourceGitHubInventory].Parts[0].Truncated = true },
		"unknown":   func(s *Snapshot) { s.Sources[SourceGitHubInventory].Parts[0].State = Unknown },
		"stale":     func(s *Snapshot) { s.Sources[SourceGitHubInventory].Parts[0].State = Stale },
		"no me":     func(s *Snapshot) { s.People[0].Me = false },
		"failed duplicate": func(s *Snapshot) {
			st := s.Sources[SourceGitHubInventory]
			st.Parts = append(st.Parts, SourcePart{Name: "ALICE/closed-recent", State: Error})
			s.Sources[SourceGitHubInventory] = st
		},
		"uncovered person": func(s *Snapshot) { s.People = append(s.People, Person{Login: "bob"}) },
		"no people":        func(s *Snapshot) { s.People = nil },
		"no inventory":     func(s *Snapshot) { s.Sources = nil },
	} {
		cp := *s
		cp.Sources = map[string]SourceStatus{SourceGitHubInventory: {State: Fresh, Parts: slices.Clone(s.Sources[SourceGitHubInventory].Parts)}}
		cp.People = slices.Clone(s.People)
		mutate(&cp)
		if got := computeCounts(&cp, ls, bookmarks).Merged; got != nil {
			t.Errorf("%s: merged %v, want none", name, got)
		}
	}
}

// TestComputeCountsMergedUnknowns covers the rest of the merged-count rules:
// a merge counts on its local day, a PR reached twice in one effort counts
// once, and an effort with a PR of unknown state or a workspace jj could not
// read has no series.
func TestComputeCountsMergedUnknowns(t *testing.T) {
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, asmZone) // UTC-7
	effort := func(id string) Node {
		return Node{ID: id, Kind: KindEffort, Effort: &EffortInfo{Key: id, Group: GroupEffort}}
	}
	s := &Snapshot{
		Days: 3,
		Sources: map[string]SourceStatus{SourceGitHubInventory: {State: Fresh, Parts: []SourcePart{
			{Name: "alice/closed-recent", State: Fresh},
		}}},
		People: []Person{{Login: "alice", Me: true}},
		Nodes: []Node{
			effort("effort:tz"), effort("effort:dup"), effort("effort:state"), effort("effort:broken"),
			{ID: "ws-broken", Kind: KindWorkspace, Workspace: &WorkspaceInfo{EffortID: "effort:broken", Error: "jj timed out"}},
			{ID: "bm-dup", Kind: KindBookmark, Bookmark: &BookmarkInfo{PRLookup: Fresh, PRCount: 1}},
			// 03:00 UTC on the 10th is the evening of the 9th locally.
			{ID: "pr:tz", Kind: KindPR, PR: &PRInfo{State: "merged", MergedAt: time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)}},
			{ID: "pr:dup", Kind: KindPR, PR: &PRInfo{State: "merged", MergedAt: at, BookmarkID: "bm-dup"}},
			{ID: "pr:state", Kind: KindPR, PR: &PRInfo{}},
		},
		Edges: []Edge{
			{"effort:tz", "pr:tz", EdgeTracks},
			{"bm-dup", "pr:dup", EdgePR},
			{"effort:dup", "pr:dup", EdgeTracks},
			{"effort:state", "pr:state", EdgeTracks},
		},
	}
	ls := &localState{at: at, days: 3, workspaces: []*wsData{{id: "ws-broken", err: errors.New("jj timed out")}}}
	c := computeCounts(s, ls, map[string]map[string]bool{"effort:dup": {"bm-dup": true}})
	wantStates := map[string]map[string]int{
		"effort:tz": {"merged": 1}, "effort:dup": {"merged": 1}, "effort:state": {"unknown": 1}, "effort:broken": {},
	}
	if !reflect.DeepEqual(c.PRStates, wantStates) {
		t.Errorf("pr_states %v, want %v", c.PRStates, wantStates)
	}
	wantMerged := map[string][]int{"effort:tz": {0, 1, 0}, "effort:dup": {0, 0, 1}}
	if !reflect.DeepEqual(c.Merged, wantMerged) {
		t.Errorf("merged %v, want %v", c.Merged, wantMerged)
	}
}

// TestDeltaInventoryOnlyPRs covers "since last look" for PRs only the
// inventory found: such a PR is new only when, for one of its involvements,
// every inventory set that would hold it was complete at the baseline, and one that disappears while such a
// set is no longer complete is unknown rather than silently dropped.
func TestDeltaInventoryOnlyPRs(t *testing.T) {
	const id = "pr:acme/api#7"
	at := time.Unix(1_800_000_000, 0)
	// snap has the given inventory parts and, unless role is empty, an
	// inventory-only PR in which alice has that role.
	snap := func(role string, parts ...SourcePart) *Snapshot {
		s := &Snapshot{Schema: SchemaVersion, Sources: map[string]SourceStatus{SourceGitHubInventory: {Parts: parts}}}
		if role != "" {
			s.Nodes = []Node{{ID: id, Kind: KindPR, Label: "acme/api#7", PR: &PRInfo{Number: 7, State: "open", Freshness: Fresh,
				Involvement: []Involvement{{Person: "Alice", Role: role}}, Sources: []string{PRSourceInventory}}}}
		}
		return s
	}
	part := func(name string, f Freshness) SourcePart { return SourcePart{Name: name, State: f} }
	authored := []SourcePart{part("alice/open-authored", Fresh), part("alice/closed-recent", Stale)}
	// twoRoles is snap with alice as author and bob as requested reviewer.
	twoRoles := func(parts ...SourcePart) *Snapshot {
		s := snap(RoleAuthor, parts...)
		s.Nodes[0].PR.Involvement = append(s.Nodes[0].PR.Involvement, Involvement{Person: "bob", Role: RoleReviewRequested})
		return s
	}
	bobRequested := part("bob/review-requested", Fresh)

	for _, tc := range []struct {
		name string
		base *Snapshot
		now  *Snapshot
		want string // the PR's delta status, "" for no entry
	}{
		{"authored, both sets known", snap("", authored...), snap(RoleAuthor, authored...), ItemNew},
		{"authored, closed-recent unknown then", snap("", authored[0], part("alice/closed-recent", Unknown)), snap(RoleAuthor, authored...), ItemUnknown},
		{"authored, set truncated then", snap("", authored[0], SourcePart{Name: "alice/closed-recent", State: Fresh, Truncated: true}), snap(RoleAuthor, authored...), ItemUnknown},
		{"authored, another person's sets known", snap("", part("bob/open-authored", Fresh), part("bob/closed-recent", Fresh)), snap(RoleAuthor, authored...), ItemUnknown},
		{"review requested, set known", snap("", part("alice/review-requested", Fresh)), snap(RoleReviewRequested, authored...), ItemNew},
		{"reviewed is no inventory set", snap("", authored...), snap(RoleReviewed, authored...), ItemUnknown},
		{"no inventory then", &Snapshot{Schema: SchemaVersion}, snap(RoleAuthor, authored...), ItemUnknown},
		{"gone while its sets are known", snap(RoleAuthor, authored...), snap("", authored...), ""},
		{"gone while a set is unknown", snap(RoleAuthor, authored...), snap("", authored[0], part("alice/closed-recent", Error)), ItemUnknown},
		// With several involvements one fully known group suffices to call
		// the PR new, but any incomplete part makes its disappearance unknown.
		{"two roles, only the reviewer's set known then", snap("", bobRequested), twoRoles(bobRequested), ItemNew},
		{"two roles, neither known then", snap("", authored[0]), twoRoles(authored...), ItemUnknown},
		{"two roles, gone while the reviewer's set is unknown", twoRoles(append(authored, bobRequested)...),
			snap("", append(authored, part("bob/review-requested", Unknown))...), ItemUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := computeDelta(baselineFrom(tc.base, at), tc.now)
			var got string
			for _, p := range d.PRs {
				if p.ID == id {
					got = p.Status
				}
			}
			if got != tc.want {
				t.Fatalf("pr delta %q, want %q: %+v", got, tc.want, d.PRs)
			}
			if wantChanges := map[bool]int{true: 1}[tc.want == ItemNew]; d.Summary.PRStateChanges != wantChanges {
				t.Errorf("pr_state_changes %d, want %d", d.Summary.PRStateChanges, wantChanges)
			}
		})
	}
}

func TestSprintInfoTicketCoverageJSON(t *testing.T) {
	// An unknown ticket result must not serialize as a zero count.
	unknown, err := json.Marshal(SprintInfo{ID: 7, Name: "Sprint 7", State: SprintActive, Freshness: Fresh})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"ticket_count"`, `"ticket_lookup"`, `"truncated"`, `"ticket_scope"`} {
		if strings.Contains(string(unknown), key) {
			t.Fatalf("unknown ticket coverage serialized %s: %s", key, unknown)
		}
	}

	n := 0
	known, err := json.Marshal(SprintInfo{
		ID: 7, Name: "Sprint 7", State: SprintActive, Freshness: Fresh,
		TicketScope:  TicketScopeConfiguredPeopleAndUnassigned,
		TicketLookup: Stale,
		TicketCount:  &n,
		Truncated:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"ticket_scope":"configured_people_and_unassigned"`,
		`"ticket_lookup":"stale"`,
		`"ticket_count":0`,
		`"truncated":true`,
	} {
		if !strings.Contains(string(known), want) {
			t.Fatalf("known ticket coverage missing %s: %s", want, known)
		}
	}
}
