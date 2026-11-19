package dash

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// triageFixture is alice's open PRs, one per default bucket plus the
// awkward cases, with facts computed and alice's inventory set fresh.
func triageFixture(t *testing.T, edit func(*triageSnap)) *View {
	t.Helper()
	pr := func(n int, mod func(*PRInfo)) *PRInfo {
		p := readyPR()
		p.Number, p.URL, p.Title = n, "https://github.com/acme/api/pull/"+strconv.Itoa(n), "PR "+strconv.Itoa(n)
		p.HeadRef = "b" + strconv.Itoa(n)
		p.TicketKeys = []string{}
		if mod != nil {
			mod(p)
		}
		return p
	}
	old, stale := day(1, 9).AddDate(-1, 0, 0), day(1, 9).AddDate(0, 0, -10)
	ticket := func(key, cat string, sprint string) *TicketInfo {
		ti := &TicketInfo{Key: key, System: "jira", Freshness: Fresh, Status: map[string]string{StatusToDo: "Backlog", StatusInProgress: "In Progress", StatusDone: "Done"}[cat],
			StatusCategory: cat, URL: "https://jira.example/browse/" + key, Sprints: []SprintRef{}}
		if sprint != "" {
			ti.Sprints = []SprintRef{{ID: 1, State: sprint}}
		}
		return ti
	}
	ts := (&triageSnap{}).
		pr(pr(1, func(p *PRInfo) { p.ReviewDecision = ReviewApproved })).
		pr(pr(2, func(p *PRInfo) {
			p.Reviews = []Review{{Author: "bob", State: ReviewApproved, SubmittedAt: day(20, 9)}}
		})).
		pr(pr(3, func(p *PRInfo) { p.IsDraft, p.UpdatedAt, p.TicketKeys = true, old, []string{"TEAM-3"} })).
		pr(pr(4, func(p *PRInfo) { p.IsDraft, p.TicketKeys = true, []string{"TEAM-4"} })).
		pr(pr(5, func(p *PRInfo) { p.IsDraft, p.UpdatedAt, p.CreatedAt = true, old, old })).
		pr(pr(6, func(p *PRInfo) { p.UpdatedAt = stale })).
		pr(pr(7, func(p *PRInfo) { p.IsDraft, p.TicketKeys = true, []string{"TEAM-7"} })).
		pr(pr(8, func(p *PRInfo) { p.IsDraft = true })).
		pr(pr(9, func(p *PRInfo) { p.IsDraft, p.UpdatedAt, p.TicketKeys = true, stale, []string{"TEAM-9"} })).
		pr(pr(10, func(p *PRInfo) { p.IsDraft, p.UpdatedAt, p.TicketKeys = true, stale, []string{"TEAM-10"} })).
		// Someone else's PR, and one of alice's that merged.
		pr(pr(11, func(p *PRInfo) { p.Author = "bob" })).
		pr(pr(12, func(p *PRInfo) { p.State = "merged" })).
		ticket(ticket("TEAM-3", StatusDone, "")).
		ticket(ticket("TEAM-4", StatusToDo, SprintActive)).
		ticket(ticket("TEAM-7", StatusToDo, "")).
		ticket(ticket("TEAM-9", StatusInProgress, SprintClosed)).
		ticket(&TicketInfo{Key: "TEAM-10", System: "jira", Freshness: Error})
	if edit != nil {
		edit(ts)
	}
	s := &ts.s
	s.GeneratedAt = factsNow
	s.People = []Person{{Login: "alice", Me: true}, {Login: "bob"}}
	s.Sources = map[string]SourceStatus{
		SourceGitHubInventory: {State: Fresh, Parts: []SourcePart{{Name: "alice/open-authored", State: Fresh, FetchedAt: factsNow}}},
		SourceJira:            {State: Fresh},
	}
	ComputeFacts(s, s.People, DefaultFactThresholds(), factsNow).apply(s)
	v, err := newView(s, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func defaultRules(t *testing.T) *TriageRules {
	t.Helper()
	rules, err := CompileTriageRules(DefaultTriageRules(30), Fields())
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

// buckets maps each bucket to its PR numbers.
func buckets(tr *Triage) map[string][]int {
	out := map[string][]int{}
	for _, b := range tr.Buckets {
		out[b.Name] = []int{}
		for _, it := range b.Items {
			n, _ := strconv.Atoi(it.Ref[strings.IndexByte(it.Ref, '#')+1:])
			out[b.Name] = append(out[b.Name], n)
		}
	}
	return out
}

func TestDefaultTriageBuckets(t *testing.T) {
	tr, err := BuildTriage(triageFixture(t, nil), defaultRules(t), "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]int{
		"land":       {1, 2},
		"keep":       {4},
		"close?":     {3, 5},
		"draft?":     {6},
		"fix ticket": {7},
		"active":     {8},
		"parked":     {9, 10},
	}
	got := buckets(tr)
	// Sorted by ticket, then ID: compare as sets.
	for k := range got {
		slices.Sort(got[k])
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buckets\n got %v\nwant %v", got, want)
	}
	if tr.Total != 10 || tr.Me != "alice" {
		t.Errorf("total %d, me %q", tr.Total, tr.Me)
	}
	names := []string{}
	for _, b := range tr.Buckets {
		names = append(names, b.Name)
	}
	if !reflect.DeepEqual(names, []string{"land", "keep", "close?", "draft?", "fix ticket", "active", "parked"}) {
		t.Errorf("bucket order %v", names)
	}
}

func TestUnknownTicketDoesNotMatchAndIsNamed(t *testing.T) {
	tr, err := BuildTriage(triageFixture(t, nil), defaultRules(t), "")
	if err != nil {
		t.Fatal(err)
	}
	var item TriageItem
	for _, b := range tr.Buckets {
		for _, it := range b.Items {
			if it.Ref == "acme/api#10" {
				item = it
			}
		}
	}
	if !reflect.DeepEqual(item.Unknown, []string{"ticket_sprint_state", "ticket_category"}) {
		t.Errorf("#10 unknown = %v", item.Unknown)
	}
	if !slices.ContainsFunc(tr.Incomplete, func(s string) bool { return strings.Contains(s, "TEAM-10") }) {
		t.Errorf("incomplete %v should name TEAM-10", tr.Incomplete)
	}
}

func TestTriageStackGuard(t *testing.T) {
	v := triageFixture(t, func(ts *triageSnap) {
		child := readyPR()
		child.Number, child.URL, child.HeadRef, child.BaseRef, child.IsDraft = 13, "https://github.com/acme/api/pull/13", "b13", "b3", true
		ts.pr(child)
	})
	tr, err := BuildTriage(v, defaultRules(t), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range tr.Buckets[2].Items {
		if it.Ref == "acme/api#3" {
			if len(it.StackChildren) != 1 || it.StackChildren[0].Number != 13 || it.StackChildren[0].URL == "" {
				t.Errorf("#3 stack children %+v", it.StackChildren)
			}
			return
		}
	}
	t.Errorf("#3 is not in %s", tr.Buckets[2].Name)
}

func TestConfiguredTriageRules(t *testing.T) {
	rules, err := CompileTriageRules([]TriageRule{
		{Bucket: "drafts", Match: "prstate:draft"},
		{Bucket: "rest", Note: "everything else"},
	}, Fields())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := BuildTriage(triageFixture(t, nil), rules, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := buckets(tr); len(got) != 2 || len(got["drafts"]) != 7 || len(got["rest"]) != 3 {
		t.Errorf("configured buckets %v", got)
	}

	for _, tc := range []struct {
		rules []TriageRule
		want  string
	}{
		{[]TriageRule{{Bucket: "x", Match: "prstat:open"}, {Bucket: "rest"}}, `rule 1 (bucket "x")`},
		{[]TriageRule{{Bucket: "y", Match: "status_category:done"}, {Bucket: "x"}}, "status_category does not apply to PRs"},
		{[]TriageRule{{Match: "stale:true"}}, "rule 1 has no bucket"},
		{[]TriageRule{{Bucket: "  "}}, "rule 1 has no bucket"},
		{[]TriageRule{{Bucket: "all"}, {Bucket: "drafts", Match: "prstate:draft"}}, `rule 1 (bucket "all") has no match`},
		{[]TriageRule{{Bucket: "drafts", Match: "prstate:draft"}}, "is the last rule but has a match"},
		{[]TriageRule{{Bucket: "[wip]"}}, "cannot round-trip"},
		{[]TriageRule{{Bucket: "later (maybe)"}}, "cannot round-trip"},
		{[]TriageRule{{Bucket: "New since Monday"}}, "cannot round-trip"},
		{[]TriageRule{{Bucket: " padded"}}, "cannot round-trip"},
		{[]TriageRule{}, "is empty"},
	} {
		if _, err := CompileTriageRules(tc.rules, Fields()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err %v, want %q", tc.rules, err, tc.want)
		}
	}
}

func TestCompareTriageItemsByNumber(t *testing.T) {
	items := []TriageItem{
		{ID: "pr:acme/web#2", Ref: "acme/web#2"},
		{ID: "pr:acme/api#100", Ref: "acme/api#100"},
		{ID: "pr:acme/api#99", Ref: "acme/api#99"},
		{ID: "pr:acme/api#7", Ref: "acme/api#7", Tickets: []TriageTicket{{Key: "TEAM-1"}}},
	}
	slices.SortFunc(items, compareTriageItems)
	var got []string
	for _, it := range items {
		got = append(got, it.Ref)
	}
	if want := []string{"acme/api#7", "acme/api#99", "acme/api#100", "acme/web#2"}; !slices.Equal(got, want) {
		t.Errorf("order %v, want %v", got, want)
	}
}

func TestBuildTriageNeedsCompiledRules(t *testing.T) {
	v := triageFixture(t, nil)
	for _, rs := range []*TriageRules{nil, {}} {
		if _, err := BuildTriage(v, rs, ""); err == nil {
			t.Errorf("BuildTriage(%v) built a triage with no rules", rs)
		}
	}
}

// TestTriageBucketNotesJoinWhole: a note that is part of another bucket
// note is still shown.
func TestTriageBucketNotesJoinWhole(t *testing.T) {
	rules, err := CompileTriageRules([]TriageRule{
		{Bucket: "a", Note: "Ping a reviewer, then wait.", Match: "prstate:draft"},
		{Bucket: "a", Note: "Ping a reviewer", Match: "stale:true"},
		{Bucket: "rest"},
	}, Fields())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := BuildTriage(triageFixture(t, nil), rules, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := tr.Buckets[0].Note; got != "Ping a reviewer, then wait. Ping a reviewer" {
		t.Errorf("note %q", got)
	}
}

func TestTriageFilterAndIncompleteInventory(t *testing.T) {
	v := triageFixture(t, nil)
	tr, err := BuildTriage(v, defaultRules(t), "prstate:draft")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Total != 7 {
		t.Errorf("filtered total %d, want 7", tr.Total)
	}
	if _, err := BuildTriage(v, defaultRules(t), "prstat:x"); err == nil {
		t.Error("a bad filter is an error")
	}

	v.Snapshot.Sources[SourceGitHubInventory] = SourceStatus{State: Error, Parts: []SourcePart{{Name: "alice/open-authored", State: Fresh, Truncated: true, FetchedAt: factsNow.Add(-time.Hour)}}}
	v2, _ := newView(v.Snapshot, nil, false, nil)
	tr, err = BuildTriage(v2, defaultRules(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Incomplete) == 0 || !strings.Contains(tr.Incomplete[0], "alice/open-authored: cut short") {
		t.Errorf("incomplete %v", tr.Incomplete)
	}
}
