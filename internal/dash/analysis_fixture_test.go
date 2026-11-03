package dash

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// analysisFixturePath is the sanitized snapshot the analysis epic (gh-96)
// builds on: invented people, repositories, PRs, tickets and sprints that
// exercise every PR, ticket and sprint fact. Later specs extend it.
const analysisFixturePath = "testdata/analysis_fixture.json"

// loadAnalysisFixture decodes the fixture strictly: a misspelt field fails.
func loadAnalysisFixture(t testing.TB) *Snapshot {
	t.Helper()
	data, err := os.ReadFile(analysisFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var s Snapshot
	if err := dec.Decode(&s); err != nil {
		t.Fatalf("%s: %v", analysisFixturePath, err)
	}
	return &s
}

// scaleAnalysisFixture returns a snapshot of at least minNodes nodes made of
// whole copies of base, for performance checks (gh-96: filter changes
// re-render in under 100 ms at 2,000 nodes). Copy 0 is base; copy k >= 1
// appends "~k" to every node ID and every reference to one (edge endpoints,
// effort_id, bookmark_id, workspace_id, conflict workspace IDs, counts keys),
// to effort keys, and to agent session IDs and conflicts_with, so agent IDs
// stay "agent:<session>". Everything else (labels, people, PR numbers,
// ticket keys, sprint IDs, times, sources) is shared: the copies are
// disjoint graphs with identical inputs. A JS test can mirror this from
// analysis_fixture.json in a few lines. The derived facts (gh-99) are then
// recomputed over the whole: each copy's PRs count toward their
// repository's check stats, which can make a check systemic that was not.
func scaleAnalysisFixture(t testing.TB, base *Snapshot, minNodes int) *Snapshot {
	t.Helper()
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	copies := max(1, (minNodes+len(base.Nodes)-1)/len(base.Nodes))
	out := *base
	out.Nodes, out.Edges = nil, nil
	out.Counts = Counts{Days: base.Counts.Days, Activity: map[string][]int{}, PRStates: map[string]map[string]int{},
		Agents: map[string]map[string]int{}, Merged: map[string][]int{}}
	for k := range copies {
		var c Snapshot
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		suffix := ""
		if k > 0 {
			suffix = fmt.Sprintf("~%d", k)
		}
		ref := func(id string) string {
			if id == "" {
				return ""
			}
			return id + suffix
		}
		for _, n := range c.Nodes {
			n.ID = ref(n.ID)
			switch {
			case n.Effort != nil:
				n.Effort.Key += suffix
				for i := range n.Effort.Conflicts {
					n.Effort.Conflicts[i].WorkspaceID = ref(n.Effort.Conflicts[i].WorkspaceID)
				}
			case n.Workspace != nil:
				n.Workspace.EffortID = ref(n.Workspace.EffortID)
			case n.PR != nil:
				n.PR.BookmarkID = ref(n.PR.BookmarkID)
			case n.Agent != nil:
				n.Agent.SessionID += suffix
				n.Agent.EffortID = ref(n.Agent.EffortID)
				n.Agent.WorkspaceID = ref(n.Agent.WorkspaceID)
				for i := range n.Agent.ConflictsWith {
					n.Agent.ConflictsWith[i] += suffix
				}
			}
			out.Nodes = append(out.Nodes, n)
		}
		for _, e := range c.Edges {
			out.Edges = append(out.Edges, Edge{Source: ref(e.Source), Target: ref(e.Target), Kind: e.Kind})
		}
		for id, v := range c.Counts.Activity {
			out.Counts.Activity[ref(id)] = v
		}
		for id, v := range c.Counts.PRStates {
			out.Counts.PRStates[ref(id)] = v
		}
		for id, v := range c.Counts.Agents {
			out.Counts.Agents[ref(id)] = v
		}
		for id, v := range c.Counts.Merged {
			out.Counts.Merged[ref(id)] = v
		}
	}
	ComputeFacts(&out, out.People, DefaultFactThresholds(), out.GeneratedAt).apply(&out)
	return &out
}

func TestAnalysisFixtureValidates(t *testing.T) {
	if err := loadAnalysisFixture(t).Validate(); err != nil {
		t.Fatalf("analysis fixture invalid: %v", err)
	}
}

// TestAnalysisFixtureRoundTrips checks the JSON contract: re-encoding the
// decoded fixture gives the same document, so every field name matches and
// "reported, none" ([]) and zero counts (additions: 0) survive as such.
func TestAnalysisFixtureRoundTrips(t *testing.T) {
	data, err := os.ReadFile(analysisFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(loadAnalysisFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(again, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("the fixture does not survive a decode and encode:\n got %s", again)
	}
}

// effortBookmarksOf derives assemble's effort -> bookmarks map from the
// contains and on edges.
func effortBookmarksOf(s *Snapshot) map[string]map[string]bool {
	wsEffort := map[string]string{}
	for _, e := range s.Edges {
		if e.Kind == EdgeContains {
			wsEffort[e.Target] = e.Source
		}
	}
	out := map[string]map[string]bool{}
	for _, e := range s.Edges {
		if eff := wsEffort[e.Source]; e.Kind == EdgeOn && eff != "" {
			if out[eff] == nil {
				out[eff] = map[string]bool{}
			}
			out[eff][e.Target] = true
		}
	}
	return out
}

// checkCountsMatch recomputes the snapshot's PR-state, merged and agent
// counts from its nodes and edges and compares them with what it carries.
func checkCountsMatch(t *testing.T, s *Snapshot) {
	t.Helper()
	ls := &localState{at: s.GeneratedAt, days: s.Days}
	got := computeCounts(s, ls, effortBookmarksOf(s))
	if !slices.Equal(got.Days, s.Counts.Days) {
		t.Errorf("days: computed %v, fixture %v", got.Days, s.Counts.Days)
	}
	if !reflect.DeepEqual(got.PRStates, s.Counts.PRStates) {
		t.Errorf("pr_states: computed %v, fixture %v", got.PRStates, s.Counts.PRStates)
	}
	if !reflect.DeepEqual(got.Merged, s.Counts.Merged) {
		t.Errorf("merged: computed %v, fixture %v", got.Merged, s.Counts.Merged)
	}
	if !reflect.DeepEqual(got.Agents, s.Counts.Agents) {
		t.Errorf("agents: computed %v, fixture %v", got.Agents, s.Counts.Agents)
	}
}

func TestAnalysisFixtureCountsMatchItsNodes(t *testing.T) {
	checkCountsMatch(t, loadAnalysisFixture(t))
}

// TestAnalysisFixtureCoversEveryFact fails when a PR, ticket or sprint fact
// (or a field of their parts, a source part or a person) appears nowhere in
// the fixture, so a spec that adds a field must also exercise it here.
func TestAnalysisFixtureCoversEveryFact(t *testing.T) {
	data, err := os.ReadFile(analysisFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Nodes      []map[string]json.RawMessage `json:"nodes"`
		Sources    map[string]map[string]any    `json:"sources"`
		People     []map[string]any             `json:"people"`
		CheckStats map[string]any               `json:"check_stats"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	seen := map[string]map[string]bool{} // type name -> JSON keys present
	note := func(typ string, obj map[string]any) {
		if seen[typ] == nil {
			seen[typ] = map[string]bool{}
		}
		for k := range obj {
			seen[typ][k] = true
		}
	}
	each := func(v any, typ string) {
		list, _ := v.([]any)
		for _, x := range list {
			if m, ok := x.(map[string]any); ok {
				note(typ, m)
			}
		}
	}
	for _, n := range doc.Nodes {
		for kind, typ := range map[string]string{"pr": "PRInfo", "ticket": "TicketInfo", "sprint": "SprintInfo"} {
			raw, ok := n[kind]
			if !ok {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			note(typ, m)
			each(m["review_requests"], "ReviewRequest")
			each(m["reviews"], "Review")
			each(m["check_details"], "CheckDetail")
			each(m["involvement"], "Involvement")
			each(m["sprints"], "SprintRef")
			if p, ok := m["parent"].(map[string]any); ok {
				note("TicketParent", p)
			}
			if f, ok := m["facts"].(map[string]any); ok {
				note(map[string]string{"pr": "PRFacts", "ticket": "TicketFacts"}[kind], f)
				each(f["reasons"], "Reason")
				each(f["mismatches"], "Reason")
				each(f["linked_prs"], "LinkedPR")
			}
		}
	}
	for _, st := range doc.Sources {
		each(st["parts"], "SourcePart")
	}
	for _, p := range doc.People {
		note("Person", p)
	}
	for _, list := range doc.CheckStats {
		each(list, "CheckStat")
	}

	// notExercised lists fields the fixture deliberately leaves out.
	notExercised := map[string]string{
		"TicketInfo.error":             "failed lookups are liveFixture's",
		"SprintInfo.truncated":         "set by the sprint ticket search, which arrives with gh-98's sprint fetch",
		"SprintInfo.ticket_scope":      "set by the sprint ticket search, which arrives with gh-98's sprint fetch",
		"SprintInfo.ticket_lookup":     "set by the sprint ticket search, which arrives with gh-98's sprint fetch",
		"SprintInfo.ticket_fetched_at": "set by the sprint ticket search, which arrives with gh-98's sprint fetch",
		"SprintInfo.ticket_count":      "set by the sprint ticket search, which arrives with gh-98's sprint fetch",
	}
	for _, v := range []any{PRInfo{}, TicketInfo{}, SprintInfo{}, ReviewRequest{}, Review{}, CheckDetail{},
		Involvement{}, SprintRef{}, TicketParent{}, SourcePart{}, Person{},
		PRFacts{}, Reason{}, TicketFacts{}, LinkedPR{}, CheckStat{}} {
		rt := reflect.TypeOf(v)
		for f := range rt.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			key := rt.Name() + "." + name
			if _, skip := notExercised[key]; skip {
				continue
			}
			if !seen[rt.Name()][name] {
				t.Errorf("the fixture never sets %s; add a case to %s", key, analysisFixturePath)
			}
		}
	}
}

// TestAnalysisFixtureScenarios pins the cases later specs' acceptance
// criteria rely on (gh-97 to gh-105).
func TestAnalysisFixtureScenarios(t *testing.T) {
	s := loadAnalysisFixture(t)
	now := s.GeneratedAt
	byID := map[string]*Node{}
	var prs []*Node
	for i := range s.Nodes {
		n := &s.Nodes[i]
		byID[n.ID] = n
		if n.PR != nil {
			prs = append(prs, n)
		}
	}
	into := func(id string, kind EdgeKind) []string {
		var out []string
		for _, e := range s.Edges {
			if e.Target == id && e.Kind == kind {
				out = append(out, e.Source)
			}
		}
		return out
	}
	from := func(id string, kind EdgeKind) []string {
		var out []string
		for _, e := range s.Edges {
			if e.Source == id && e.Kind == kind {
				out = append(out, e.Target)
			}
		}
		return out
	}
	open := func(p *PRInfo) bool { return p.State == "open" }
	failing := func(p *PRInfo) []string {
		var out []string
		for _, c := range p.CheckDetails {
			if c.State == CheckFailure {
				out = append(out, c.Name)
			}
		}
		return out
	}
	involves := func(p *PRInfo, person, role string) bool {
		return slices.Contains(p.Involvement, Involvement{Person: person, Role: role})
	}
	group := func(effortID string) string {
		if n := byID[effortID]; n != nil && n.Effort != nil {
			return n.Effort.Group
		}
		return ""
	}
	// some reports whether any PR satisfies ok.
	some := func(what string, ok func(n *Node) bool) {
		t.Helper()
		if !slices.ContainsFunc(prs, ok) {
			t.Errorf("no PR is %s", what)
		}
	}

	t.Run("people", func(t *testing.T) {
		want := []Person{{Login: "alice", Jira: "acct-alice", Name: "Alice", Me: true}, {Login: "bob", Jira: "acct-bob", Name: "Bob"}}
		if !reflect.DeepEqual(s.People, want) {
			t.Errorf("people %+v, want %+v", s.People, want)
		}
		repos := map[string]bool{}
		for _, n := range prs {
			repos[n.PR.Repo] = true
		}
		if want := []string{"acme/api", "acme/infra", "acme/web"}; !slices.Equal(slices.Sorted(maps.Keys(repos)), want) {
			t.Errorf("repos %v, want %v", slices.Sorted(maps.Keys(repos)), want)
		}
	})

	t.Run("inventory sets", func(t *testing.T) {
		some("an open draft authored by alice", func(n *Node) bool { return open(n.PR) && n.PR.IsDraft && n.PR.Author == "alice" })
		some("an open ready PR authored by alice", func(n *Node) bool { return open(n.PR) && !n.PR.IsDraft && n.PR.Author == "alice" })
		some("found only through review-requested, by carol", func(n *Node) bool {
			return n.PR.Author == "carol" && !slices.ContainsFunc(n.PR.Involvement, func(i Involvement) bool { return i.Role == RoleAuthor })
		})
		some("in several sets", func(n *Node) bool {
			return involves(n.PR, "alice", RoleAuthor) && involves(n.PR, "bob", RoleReviewRequested)
		})
		recent := now.AddDate(0, 0, -28)
		some("merged within closed-recent", func(n *Node) bool { return n.PR.State == "merged" && n.PR.MergedAt.After(recent) })
		some("closed unmerged within closed-recent", func(n *Node) bool {
			return n.PR.State == "closed" && n.PR.MergedAt.IsZero() && n.PR.ClosedAt.After(recent)
		})
		some("merged, then updated in a later week", func(n *Node) bool {
			if n.PR.MergedAt.IsZero() {
				return false
			}
			my, mw := n.PR.MergedAt.ISOWeek()
			uy, uw := n.PR.UpdatedAt.ISOWeek()
			return uy > my || (uy == my && uw > mw)
		})
		some("merged 10 to 20 days before generated_at", func(n *Node) bool {
			m := n.PR.MergedAt
			return !m.IsZero() && !m.Before(now.AddDate(0, 0, -20)) && !m.After(now.AddDate(0, 0, -10))
		})
		some("merged within the last 7 days", func(n *Node) bool { return !n.PR.MergedAt.IsZero() && n.PR.MergedAt.After(now.AddDate(0, 0, -7)) })
		some("known only through a bookmark, with no inventory facts", func(n *Node) bool {
			return slices.Equal(n.PR.Sources, []string{PRSourceBookmark}) && n.PR.Author == "" && n.PR.Reviews == nil && n.PR.Additions == nil
		})
		some("reporting zero additions", func(n *Node) bool { return n.PR.Additions != nil && *n.PR.Additions == 0 })
	})

	t.Run("reviews", func(t *testing.T) {
		some("a team-only request for alice, recorded by involvement", func(n *Node) bool {
			p := n.PR
			return open(p) && len(p.ReviewRequests) > 0 && !slices.ContainsFunc(p.ReviewRequests, func(r ReviewRequest) bool { return r.Kind != ReviewerTeam }) &&
				involves(p, "alice", RoleReviewRequested) && !slices.Contains(p.RequestedReviewers, "alice")
		})
		some("ready with no reviewers", func(n *Node) bool {
			p := n.PR
			return open(p) && !p.IsDraft && p.ReviewersKnown && p.ReviewRequests != nil && len(p.ReviewRequests) == 0 && len(p.Reviews) == 0
		})
		some("changes requested, then re-requested", func(n *Node) bool {
			for _, r := range n.PR.Reviews {
				for _, q := range n.PR.ReviewRequests {
					if r.State == ReviewChangesRequested && q.Reviewer == r.Author && q.RequestedAt.After(r.SubmittedAt) {
						return true
					}
				}
			}
			return false
		})
		some("approved and unmerged", func(n *Node) bool { return open(n.PR) && n.PR.ReviewDecision == ReviewApproved })
		some("a review request with no known time", func(n *Node) bool {
			return slices.ContainsFunc(n.PR.ReviewRequests, func(r ReviewRequest) bool { return r.RequestedAt.IsZero() })
		})
		some("conflicting", func(n *Node) bool { return n.PR.Mergeable == MergeConflicting })
		some("of unknown mergeability", func(n *Node) bool { return n.PR.Mergeable == MergeUnknown })
	})

	t.Run("checks", func(t *testing.T) {
		runs := map[string][2]int{} // repo -> {failures, runs} of vendor-hsm-tests on open PRs
		for _, n := range prs {
			if !open(n.PR) {
				continue
			}
			for _, c := range n.PR.CheckDetails {
				if c.Name == "vendor-hsm-tests" {
					r := runs[n.PR.Repo]
					r[1]++
					if c.State == CheckFailure {
						r[0]++
					}
					runs[n.PR.Repo] = r
				}
			}
		}
		if runs["acme/api"] != [2]int{5, 6} || runs["acme/web"][0] != 1 {
			t.Errorf("vendor-hsm-tests failures/runs on open PRs: %v, want 5/6 in acme/api and 1 failure in acme/web", runs)
		}
		some("an acme/api PR failing only vendor-hsm-tests", func(n *Node) bool {
			return n.PR.Repo == "acme/api" && open(n.PR) && slices.Equal(failing(n.PR), []string{"vendor-hsm-tests"})
		})
		some("failing a check of its own", func(n *Node) bool {
			return open(n.PR) && slices.ContainsFunc(failing(n.PR), func(c string) bool { return c != "vendor-hsm-tests" })
		})
		some("truncated checks", func(n *Node) bool { return n.PR.ChecksTruncated })
	})

	t.Run("attachment", func(t *testing.T) {
		some("found through a bookmark and the inventory", func(n *Node) bool {
			return slices.Equal(n.PR.Sources, []string{PRSourceBookmark, PRSourceInventory}) && n.PR.BookmarkID != ""
		})
		some("inventory-only, attached to a workspace bookmark (case 1)", func(n *Node) bool {
			return slices.Equal(n.PR.Sources, []string{PRSourceInventory}) && n.PR.BookmarkID != "" && slices.Contains(into(n.ID, EdgePR), n.PR.BookmarkID)
		})
		tracked := func(g string) func(n *Node) bool {
			return func(n *Node) bool {
				return n.PR.BookmarkID == "" && slices.ContainsFunc(into(n.ID, EdgeTracks), func(e string) bool { return group(e) == g })
			}
		}
		some("tracked by a ticket-<id> effort (case 2)", tracked(GroupTicket))
		some("tracked by a workspace's effort through its ticket (case 3)", tracked(GroupEffort))
		some("tracked by a repo: group (case 4)", tracked(GroupRepo))
	})

	t.Run("sprints", func(t *testing.T) {
		var active, future *SprintInfo
		for _, n := range s.Nodes {
			if n.Sprint == nil {
				continue
			}
			switch n.Sprint.State {
			case SprintActive:
				active = n.Sprint
			case SprintFuture:
				future = n.Sprint
			}
		}
		if active == nil || active.Goal == "" || active.Start.IsZero() || active.End.IsZero() || active.Board != 42 || future == nil {
			t.Fatalf("want an active sprint with goal, dates and board 42, and a future sprint; got %+v and %+v", active, future)
		}
		activeID := sprintNodeID(active.ID)
		type tk struct {
			id string
			t  *TicketInfo
		}
		var inSprint []tk
		cats := map[string]bool{}
		for _, n := range s.Nodes {
			if n.Ticket == nil {
				continue
			}
			cats[n.Ticket.StatusCategory] = true
			if slices.Contains(from(n.ID, EdgeInSprint), activeID) {
				inSprint = append(inSprint, tk{n.ID, n.Ticket})
			}
		}
		for _, c := range []string{StatusToDo, StatusInProgress, StatusDone} {
			if !cats[c] {
				t.Errorf("no ticket in status category %s", c)
			}
		}
		linked := func(id string) []*PRInfo {
			var out []*PRInfo
			for _, src := range into(id, EdgeTicket) {
				if n := byID[src]; n != nil && n.PR != nil {
					out = append(out, n.PR)
				}
			}
			return out
		}
		want := func(what string, ok func(x tk) bool) {
			t.Helper()
			if !slices.ContainsFunc(inSprint, ok) {
				t.Errorf("no ticket in the active sprint is %s", what)
			}
		}
		// gh-105 counts unestimated tickets apart from those whose
		// estimate is unknown.
		want("unestimated", func(x tk) bool { return x.t.StoryPointsKnown && x.t.StoryPoints == nil })
		want("estimated", func(x tk) bool { return x.t.StoryPointsKnown && x.t.StoryPoints != nil })
		want("of unknown estimate", func(x tk) bool { return !x.t.StoryPointsKnown && x.t.StoryPoints == nil })
		want("without a PR, tracked by the sprint group", func(x tk) bool {
			return len(linked(x.id)) == 0 && slices.ContainsFunc(into(x.id, EdgeTracks), func(e string) bool { return group(e) == GroupSprint })
		})
		want("done with an open PR", func(x tk) bool {
			return x.t.StatusCategory == StatusDone && slices.ContainsFunc(linked(x.id), open)
		})
		want("in progress with only a merged PR", func(x tk) bool {
			l := linked(x.id)
			return x.t.StatusCategory == StatusInProgress && len(l) == 1 && l[0].State == "merged"
		})
		want("in progress with no PR or workspace", func(x tk) bool {
			return x.t.StatusCategory == StatusInProgress && len(linked(x.id)) == 0 && len(into(x.id, EdgeTicket)) == 0
		})
		want("unassigned", func(x tk) bool { return x.t.Assignee == "" && x.t.AssigneeID == "" })
		want("carried over from a closed sprint", func(x tk) bool {
			return slices.ContainsFunc(x.t.Sprints, func(r SprintRef) bool { return r.State == SprintClosed })
		})
	})

	t.Run("sources", func(t *testing.T) {
		for _, src := range []string{SourceGitHubInventory, SourceJiraSprints} {
			st, ok := s.Sources[src]
			if !ok || len(st.Parts) == 0 || st.OldestFetch.IsZero() {
				t.Errorf("source %s: %+v, want parts and an oldest fetch", src, st)
			}
		}
		if st := s.Sources[SourceGitHubInventory]; st.State != Stale || !slices.ContainsFunc(st.Parts, func(p SourcePart) bool { return p.State == Stale && p.Error != "" }) {
			t.Errorf("github_inventory should be stale with a failed part: %+v", st)
		}
		if sp := s.Node("sprint:1234").Sprint; sp.TicketScope != TicketScopeConfiguredPeopleAndUnassigned || sp.TicketLookup != Fresh || sp.TicketCount == nil {
			t.Errorf("the active sprint should record its ticket search's scope, freshness and count: %+v", sp)
		}
	})
}

func TestScaledAnalysisFixture(t *testing.T) {
	base := loadAnalysisFixture(t)
	s := scaleAnalysisFixture(t, base, 2000)
	if n := len(s.Nodes); n < 2000 || n >= 2000+len(base.Nodes) {
		t.Fatalf("scaled to %d nodes, want 2,000 to %d", n, 2000+len(base.Nodes)-1)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("scaled fixture invalid: %v", err)
	}
	checkCountsMatch(t, s)
	for _, n := range s.Nodes {
		if (n.PR != nil && n.PR.Facts == nil) || (n.Ticket != nil && n.Ticket.Facts == nil) {
			t.Fatalf("scaled %s has no facts", n.ID)
		}
	}
	if st := s.CheckStats["acme/api"]; len(st) == 0 || st[len(st)-1].Runs != 6*(len(s.Nodes)/len(base.Nodes)) {
		t.Errorf("scaled check stats were not recomputed: %+v", st)
	}
	t.Logf("scaled fixture: %d nodes, %d edges", len(s.Nodes), len(s.Edges))

	// WGO_DASH_SCALED_FIXTURE=<path> writes the scaled snapshot for tests
	// and measurements outside Go, such as the page's JS under node.
	if path := os.Getenv("WGO_DASH_SCALED_FIXTURE"); path != "" {
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Clean(path), data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(data))
	}
}
