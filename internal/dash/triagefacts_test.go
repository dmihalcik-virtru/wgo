package dash

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

// triageSnap holds PR and ticket nodes, with a ticket edge from each PR to
// each of its ticket keys that has a node.
type triageSnap struct{ s Snapshot }

func (ts *triageSnap) pr(p *PRInfo) *triageSnap {
	id := prNodeID(p.Repo, p.Number)
	ts.s.Nodes = append(ts.s.Nodes, Node{ID: id, Kind: KindPR, PR: p})
	for _, k := range p.TicketKeys {
		ts.s.Edges = append(ts.s.Edges, Edge{Source: id, Target: jiraTicketID(k), Kind: EdgeTicket})
	}
	return ts
}

func (ts *triageSnap) ticket(t *TicketInfo) *triageSnap {
	ts.s.Nodes = append(ts.s.Nodes, Node{ID: jiraTicketID(t.Key), Kind: KindTicket, Ticket: t})
	return ts
}

func (ts *triageSnap) facts(id string) *PRFacts {
	return ComputeFacts(&ts.s, factPeople, DefaultFactThresholds(), factsNow).PRs[id]
}

func TestHumanActivityIgnoresBotsAndTheAuthor(t *testing.T) {
	p := readyPR()
	p.Reviews = []Review{
		{Author: "sonarqubecloud", State: ReviewCommented, SubmittedAt: day(29, 9)},
		{Author: "dependabot[bot]", State: ReviewCommented, SubmittedAt: day(29, 8)},
		{Author: "alice", State: ReviewCommented, SubmittedAt: day(28, 9)},
		{Author: "bob", State: ReviewChangesRequested, SubmittedAt: day(25, 9)},
	}
	f := (&triageSnap{}).pr(p).facts("pr:acme/api#41")
	if !f.HumanActivity.Equal(day(25, 9)) {
		t.Errorf("human_activity = %v, want Oct 25 (bob's review)", f.HumanActivity)
	}
	if len(f.HumanReviews) != 1 || f.HumanReviews[0].Author != "bob" {
		t.Errorf("human_reviews = %+v", f.HumanReviews)
	}

	// Only bots: known, and none.
	p.Reviews = p.Reviews[:3]
	b, _ := json.Marshal((&triageSnap{}).pr(p).facts("pr:acme/api#41"))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if got, ok := m["human_reviews"].([]any); !ok || len(got) != 0 {
		t.Errorf("bots only: human_reviews = %v, want []", m["human_reviews"])
	}
	if _, ok := m["human_activity"]; ok {
		t.Errorf("bots only: human_activity = %v, want omitted", m["human_activity"])
	}

	// Reviews not fetched: unknown, with the input named.
	p.Reviews = nil
	f = (&triageSnap{}).pr(p).facts("pr:acme/api#41")
	if f.HumanReviews != nil || f.ApprovalDismissed != nil || !slices.Contains(f.Uncertain, "reviews") {
		t.Errorf("no reviews: human_reviews=%v approval_dismissed=%v uncertain=%v", f.HumanReviews, f.ApprovalDismissed, f.Uncertain)
	}
}

func TestBotLoginsAreConfigurable(t *testing.T) {
	bots, err := CompileBotLogins([]string{"^x-test"})
	if err != nil {
		t.Fatal(err)
	}
	p := readyPR()
	p.Reviews = []Review{{Author: "X-Test-Runner", State: ReviewCommented, SubmittedAt: day(29, 9)}, {Author: "sonarqubecloud", State: ReviewCommented, SubmittedAt: day(28, 9)}}
	ts := (&triageSnap{}).pr(p)
	f := ComputeFacts(&ts.s, factPeople, FactThresholds{BotLogins: bots}, factsNow).PRs["pr:acme/api#41"]
	if len(f.HumanReviews) != 1 || f.HumanReviews[0].Author != "sonarqubecloud" {
		t.Errorf("configured patterns replace the defaults: human_reviews = %+v", f.HumanReviews)
	}
	if _, err := CompileBotLogins([]string{"("}); err == nil {
		t.Error("a bad pattern is an error")
	}
}

func TestApprovalDismissed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision string
		state    string
		author   string
		want     bool
	}{
		{"stale approval", reviewRequired, ReviewApproved, "bob", true},
		{"dismissed approval", reviewRequired, ReviewDismissed, "bob", true},
		{"still approved", ReviewApproved, ReviewApproved, "bob", false},
		{"bot approval", reviewRequired, ReviewApproved, "github-actions", false},
		{"no approval", reviewRequired, ReviewCommented, "bob", false},
	} {
		p := readyPR()
		p.ReviewDecision = tc.decision
		p.Reviews = []Review{{Author: tc.author, State: tc.state, SubmittedAt: day(25, 9)}}
		f := (&triageSnap{}).pr(p).facts("pr:acme/api#41")
		if f.ApprovalDismissed == nil || *f.ApprovalDismissed != tc.want {
			t.Errorf("%s: approval_dismissed = %v, want %v", tc.name, f.ApprovalDismissed, tc.want)
		}
	}
}

func TestStackLinks(t *testing.T) {
	base, mid, top := readyPR(), readyPR(), readyPR()
	base.Number, base.HeadRef, base.BaseRef = 1, "feat-a", "main"
	mid.Number, mid.HeadRef, mid.BaseRef = 2, "feat-b", "feat-a"
	top.Number, top.HeadRef, top.BaseRef = 3, "feat-c", "feat-b"
	merged := readyPR()
	merged.Number, merged.State, merged.HeadRef, merged.BaseRef = 4, "merged", "feat-d", "feat-a"
	other := readyPR()
	other.Number, other.Repo, other.HeadRef, other.BaseRef = 5, "acme/web", "feat-e", "feat-a"
	ts := (&triageSnap{}).pr(base).pr(mid).pr(top).pr(merged).pr(other)
	facts := ComputeFacts(&ts.s, factPeople, DefaultFactThresholds(), factsNow).PRs
	for id, want := range map[string][2][]string{
		"pr:acme/api#1": {{}, {"pr:acme/api#2"}},
		"pr:acme/api#2": {{"pr:acme/api#1"}, {"pr:acme/api#3"}},
		"pr:acme/api#3": {{"pr:acme/api#2"}, {}},
	} {
		f := facts[id]
		if !reflect.DeepEqual(f.StackedOn, want[0]) || !reflect.DeepEqual(f.StackChildren, want[1]) {
			t.Errorf("%s: stacked_on %v, stack_children %v; want %v, %v", id, f.StackedOn, f.StackChildren, want[0], want[1])
		}
	}
	if f := facts["pr:acme/api#4"]; f.StackedOn != nil || f.StackChildren != nil {
		t.Errorf("a merged PR has no stack links: %v %v", f.StackedOn, f.StackChildren)
	}

	// A head in a fork or on the default branch is no stack's base: a
	// fork's main and a main → release PR would otherwise be "stacked on"
	// by every PR based on main.
	feature, fork, sync := readyPR(), readyPR(), readyPR()
	feature.Number, feature.HeadRef, feature.BaseRef, feature.DefaultBranch = 10, "feat-x", "main", "main"
	fork.Number, fork.HeadRef, fork.BaseRef, fork.Author, fork.CrossRepository = 11, "feat-x", "main", "bob", true
	sync.Number, sync.HeadRef, sync.BaseRef = 12, "main", "release/1.x"
	ts = (&triageSnap{}).pr(feature).pr(fork).pr(sync)
	facts = ComputeFacts(&ts.s, factPeople, DefaultFactThresholds(), factsNow).PRs
	for _, id := range []string{"pr:acme/api#10", "pr:acme/api#11", "pr:acme/api#12"} {
		if f := facts[id]; len(f.StackedOn) != 0 || len(f.StackChildren) != 0 {
			t.Errorf("%s: stacked_on %v, stack_children %v; want none", id, f.StackedOn, f.StackChildren)
		}
	}

	// Without refs the links are unknown.
	noRefs := readyPR()
	noRefs.BaseRef = ""
	f := (&triageSnap{}).pr(noRefs).facts("pr:acme/api#41")
	if f.StackedOn != nil || f.StackChildren != nil || !slices.Contains(f.Uncertain, "base_ref") {
		t.Errorf("no refs: %v %v %v", f.StackedOn, f.StackChildren, f.Uncertain)
	}
}

func TestLinkedTicketFacts(t *testing.T) {
	inSprint := &TicketInfo{Key: "TEAM-41", System: "jira", Freshness: Fresh, StatusCategory: StatusDone,
		Sprints: []SprintRef{{ID: 1, State: SprintClosed}, {ID: 2, State: SprintActive}}}
	f := (&triageSnap{}).pr(readyPR()).ticket(inSprint).facts("pr:acme/api#41")
	if !reflect.DeepEqual(f.TicketCategory, []string{StatusDone}) || !reflect.DeepEqual(f.TicketSprintState, []string{SprintClosed, SprintActive}) {
		t.Errorf("ticket_category %v, ticket_sprint_state %v", f.TicketCategory, f.TicketSprintState)
	}

	// No ticket: known and empty.
	none := readyPR()
	none.TicketKeys = []string{}
	f = (&triageSnap{}).pr(none).facts("pr:acme/api#41")
	if f.TicketCategory == nil || len(f.TicketCategory) != 0 || f.TicketSprintState == nil || len(f.TicketSprintState) != 0 {
		t.Errorf("no ticket: ticket_category %#v, ticket_sprint_state %#v", f.TicketCategory, f.TicketSprintState)
	}

	// A ticket not fetched: unknown, naming it.
	f = (&triageSnap{}).pr(readyPR()).ticket(&TicketInfo{Key: "TEAM-41", System: "jira", Freshness: Unknown}).facts("pr:acme/api#41")
	if f.TicketCategory != nil || f.TicketSprintState != nil || !slices.Contains(f.Uncertain, "ticket:TEAM-41") {
		t.Errorf("unfetched: ticket_category %v, ticket_sprint_state %v, uncertain %v", f.TicketCategory, f.TicketSprintState, f.Uncertain)
	}

	// A GitHub issue's category follows its state, and it has no sprints.
	p := readyPR()
	p.TicketKeys = []string{"gh-7"}
	ts := &triageSnap{}
	ts.s.Nodes = append(ts.s.Nodes,
		Node{ID: "pr:acme/api#41", Kind: KindPR, PR: p},
		Node{ID: "ticket:gh:acme/api#7", Kind: KindTicket, Ticket: &TicketInfo{Key: "gh-7", System: "github", Freshness: Fresh, Status: "CLOSED"}})
	ts.s.Edges = append(ts.s.Edges, Edge{Source: "pr:acme/api#41", Target: "ticket:gh:acme/api#7", Kind: EdgeTicket})
	f = ts.facts("pr:acme/api#41")
	if !reflect.DeepEqual(f.TicketCategory, []string{StatusDone}) || f.TicketSprintState == nil || len(f.TicketSprintState) != 0 {
		t.Errorf("github issue: ticket_category %v, ticket_sprint_state %v", f.TicketCategory, f.TicketSprintState)
	}
}

// TestTriageFieldsQuery runs the new query fields over a snapshot's JSON, so
// unknown and empty values reach the query language as the spec says.
func TestTriageFieldsQuery(t *testing.T) {
	noTicket := readyPR()
	noTicket.Number, noTicket.TicketKeys = 1, []string{}
	unfetched := readyPR()
	unfetched.Number, unfetched.TicketKeys = 2, []string{"TEAM-2"}
	botsOnly := readyPR()
	botsOnly.Number, botsOnly.TicketKeys = 3, []string{"TEAM-3"}
	botsOnly.Reviews = []Review{{Author: "codecov", State: ReviewCommented, SubmittedAt: day(1, 9)}}
	ts := (&triageSnap{}).pr(noTicket).pr(unfetched).pr(botsOnly).
		ticket(&TicketInfo{Key: "TEAM-2", System: "jira", Freshness: Unknown}).
		ticket(&TicketInfo{Key: "TEAM-3", System: "jira", Freshness: Fresh, StatusCategory: StatusToDo, Sprints: []SprintRef{}})
	ts.s.GeneratedAt = factsNow
	ComputeFacts(&ts.s, factPeople, DefaultFactThresholds(), factsNow).apply(&ts.s)
	raw, err := json.Marshal(ts.s)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := newQueryIndex(raw, Fields())
	if err != nil {
		t.Fatal(err)
	}
	for expr, want := range map[string][]string{
		"!ticket_category:*":         {"pr:acme/api#1"},
		"ticket_category:todo":       {"pr:acme/api#3"},
		"!human_ago:<=180;kind:pr":   {"pr:acme/api#1", "pr:acme/api#2", "pr:acme/api#3"},
		"human_reviewer:*":           {},
		"approval_dismissed:false":   {"pr:acme/api#1", "pr:acme/api#2", "pr:acme/api#3"},
		"ticket_sprint_state:active": {},
	} {
		q, err := ParseQuery(expr, Fields())
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if got := ix.evaluate(q).Rows; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: rows %v, want %v", expr, got, want)
		}
	}
}
