package dash

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sprintView loads the analysis fixture, lets mutate change it, and returns
// it as a View, as wgo dash --fixture would serve it.
func sprintView(t testing.TB, mutate func(*Snapshot)) *View {
	t.Helper()
	s := loadAnalysisFixture(t)
	if mutate != nil {
		mutate(s)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	v, err := newViewJSON(s, raw, computeDelta(nil, s), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestSprintExtractGolden: the sprint summary of the analysis fixture, for
// the whole sprint and under a query, matches its golden file (rewrite them
// with go test ./internal/dash -run TestSprintExtractGolden -update).
func TestSprintExtractGolden(t *testing.T) {
	v, err := LoadFixture(analysisFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	filters := MergeFilters(nil, Fields())
	for _, tc := range []struct {
		file string
		args ExtractArgs
	}{
		// No sprint: the default, Sprint 12, which has the most tickets
		// for alice and bob.
		{"sprint_extract.golden.md", ExtractArgs{View: "sprint"}},
		{"sprint_extract_query.golden.md", ExtractArgs{View: "sprint", Sprint: "1234", Query: "status_category:in_progress,done"}},
	} {
		md, err := Extract(v, Fields(), filters, tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		path := filepath.Join("testdata", tc.file)
		if *updateFields {
			if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if md != string(want) {
			t.Errorf("%s differs; got:\n%s\nrun go test ./internal/dash -run TestSprintExtractGolden -update after checking it", tc.file, md)
		}
	}
}

// TestSprintExtractShowsTheSprint pins what the acceptance criteria rely on
// in the summary: goal, dates and elapsed time, points by category with
// unestimated and unknown points apart, every mismatch of the sprint with
// links, and the provenance footer.
func TestSprintExtractShowsTheSprint(t *testing.T) {
	v, err := LoadFixture(analysisFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	md, err := Extract(v, Fields(), nil, ExtractArgs{View: "sprint"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Sprint 12\n",
		"Sprint 1234 · board 42 · 2026-10-20 to 2026-11-03 · day 11 of 14\n",
		"> Ship token refresh and rate limits\n",
		"- Done: 3 points · 1 ticket\n- In Progress: 15 points · 3 tickets\n- To Do: 0 points · 2 tickets\n",
		// TEAM-104 was read without points; TEAM-106's points were never read.
		"Not in the points: 1 unestimated (TEAM-104) · 1 with points unknown (TEAM-106)\n",
		"**code merged ticket open** (1)\n\n- [TEAM-103](https://example.atlassian.net/browse/TEAM-103)",
		"**ticket done code open** (1)\n\n- [TEAM-102](https://example.atlassian.net/browse/TEAM-102) — Rate-limit the token endpoints: ticket Done; PR acme/api#102 open · PRs: [acme/api#102](https://github.com/acme/api/pull/102) open\n",
		"**in progress no code** (1)\n\n- [TEAM-105]",
		"**unassigned in sprint** (1)\n\n- [TEAM-106]",
		"· sprint 1234 on board 42, fetched 2026-10-30 15:52 +00:00 · the query shows 6 of 6 tickets\n",
		"Query: none (the whole sprint)\n",
		"jira_sprints fresh (1 fresh, oldest fetch 2026-10-30 15:52 +00:00)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("summary lacks %q:\n%s", want, md)
		}
	}
	// The parallel sprint has no goal and the fixture's idle mismatch.
	md, err = Extract(v, Fields(), MergeFilters(nil, Fields()), ExtractArgs{View: "sprint", Sprint: "sprint:1240", Query: "kind:ticket;mismatch:*"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Web Sprint 4\n", "day 4 of 14\n\nNo sprint goal.\n", "**in progress idle** (1)\n\n- [TEAM-107]",
		"The query shows 1 of 1 ticket: Done 0 points · 0 tickets; In Progress 5 points · 1 ticket; To Do 0 points · 0 tickets.\n",
		"Query: `kind:ticket;mismatch:*` (saved filter jira-out-of-sync)\n"} {
		if !strings.Contains(md, want) {
			t.Errorf("Web Sprint 4 lacks %q:\n%s", want, md)
		}
	}
}

// TestSprintExtractOrdersMismatchCodes: known codes in their order, then
// codes from a newer wgo by name, never in map order.
func TestSprintExtractOrdersMismatchCodes(t *testing.T) {
	v := sprintView(t, func(s *Snapshot) {
		for i := range s.Nodes {
			if n := &s.Nodes[i]; n.ID == "ticket:jira:TEAM-105" {
				n.Ticket.Facts.Mismatches = append(n.Ticket.Facts.Mismatches, Reason{Code: "zz_later", Evidence: "z"}, Reason{Code: "aa_first", Evidence: "a"}, Reason{Code: "mm_middle", Evidence: "m"})
			}
		}
	})
	for range 5 {
		md, err := Extract(v, Fields(), nil, ExtractArgs{View: "sprint"})
		if err != nil {
			t.Fatal(err)
		}
		at := func(s string) int { return strings.Index(md, s) }
		if !(at("**unassigned in sprint**") < at("**aa first**") && at("**aa first**") < at("**mm middle**") && at("**mm middle**") < at("**zz later**")) {
			t.Fatalf("mismatch codes out of order:\n%s", md)
		}
	}
}

// TestSprintExtractUnderAQuery: points stay those of the whole sprint, the
// share the query shows is stated, and only mismatches it shows are listed.
func TestSprintExtractUnderAQuery(t *testing.T) {
	v, err := LoadFixture(analysisFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	// A PR clause shows the tickets its PRs link to: #102 links TEAM-102.
	md, err := Extract(v, Fields(), nil, ExtractArgs{View: "sprint", Query: "ticket:TEAM-102;prstate:open"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"- Done: 3 points · 1 ticket\n",
		"The query shows 1 of 6 tickets: Done 3 points · 1 ticket; In Progress 0 points · 0 tickets; To Do 0 points · 0 tickets.\n",
		"**ticket done code open** (1)",
		"3 more tickets have mismatches the query does not show.\n",
		"the query shows 1 of 6 tickets\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("summary under ticket:TEAM-102 lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "TEAM-105") {
		t.Errorf("a mismatch outside the query is listed:\n%s", md)
	}
	md, err = Extract(v, Fields(), nil, ExtractArgs{View: "sprint", Query: "assignee:Bob"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "None on the tickets the query shows.\n") {
		t.Errorf("no mismatch under the query:\n%s", md)
	}
}

// TestSprintExtractExplainsMissingSprints: no [jira] board, sprint data
// that is unknown or failed, and a board without an active sprint each
// render an explanation, not an empty summary or an error.
func TestSprintExtractExplainsMissingSprints(t *testing.T) {
	noSprints := func(s *Snapshot) {
		var nodes []Node
		for _, n := range s.Nodes {
			if n.Kind != KindSprint {
				nodes = append(nodes, n)
			}
		}
		var edges []Edge
		for _, e := range s.Edges {
			if e.Kind != EdgeInSprint {
				edges = append(edges, e)
			}
		}
		s.Nodes, s.Edges = nodes, edges
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Snapshot)
		want   []string
		config bool
	}{
		{"no board", func(s *Snapshot) {
			noSprints(s)
			s.Sources[SourceJiraSprints] = SourceStatus{State: Unknown, Detail: "no [jira] board is configured; set board in the [jira] section of ~/.wgo/config.toml to show its sprints"}
		}, []string{"## Sprint summary unavailable: No \\[jira\\] board is configured\n"}, true},
		{"no source", func(s *Snapshot) {
			noSprints(s)
			delete(s.Sources, SourceJiraSprints)
		}, []string{"## Sprint summary unavailable: No sprint data\n", "does not report the jira_sprints source"}, true},
		{"not cached", func(s *Snapshot) {
			noSprints(s)
			s.Sources[SourceJiraSprints] = SourceStatus{State: Unknown, Unknown: 1, Detail: "board 42's sprints are not cached yet; wgo dash fetches them in the background with acli (wgo dash --json --refresh fetches them now)",
				Parts: []SourcePart{{Name: "board 42", State: Unknown}}}
		}, []string{"## Sprint summary unavailable: Board 42's sprints are not known yet\n", "Board 42's sprints are not cached yet; wgo dash fetches them in the background with acli (wgo dash --json --refresh fetches them now).\n"}, false},
		{"failed", func(s *Snapshot) {
			noSprints(s)
			s.Sources[SourceJiraSprints] = SourceStatus{State: Error, Error: 1, Detail: "board 42: the sprint lookup failed; check acli is installed and authenticated (acli jira auth status)",
				Parts: []SourcePart{{Name: "board 42", State: Error, Error: "acli: not logged in"}}}
		}, []string{"## Sprint summary unavailable: Board 42's sprints could not be read\n", "The lookup failed: acli: not logged in.\n", "check acli is installed"}, false},
		{"no active sprint", func(s *Snapshot) {
			var nodes []Node
			for _, n := range s.Nodes {
				if n.Sprint == nil || n.Sprint.State != SprintActive {
					nodes = append(nodes, n)
				}
			}
			s.Nodes = nodes
			var edges []Edge
			for _, e := range s.Edges {
				if e.Target != "sprint:1234" && e.Target != "sprint:1240" {
					edges = append(edges, e)
				}
			}
			s.Edges = edges
		}, []string{"## Sprint summary unavailable: Board 42 has no active sprint\n", "Future: Sprint 13 (1235) from 2026-11-03.\n"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := sprintView(t, tc.mutate)
			for _, sprint := range []string{"", "1234"} {
				md, err := Extract(v, Fields(), nil, ExtractArgs{View: "sprint", Sprint: sprint, Query: "kind:ticket"})
				if err != nil {
					t.Fatalf("sprint %q: %v", sprint, err)
				}
				for _, want := range append(tc.want, "· no active sprint\n", "Query: `kind:ticket`\n", "Sources: ") {
					if !strings.Contains(md, want) {
						t.Errorf("sprint %q: lacks %q:\n%s", sprint, want, md)
					}
				}
				if got := strings.Contains(md, "```toml\n[jira]\nboard = <id>\n```\n"); got != tc.config {
					t.Errorf("sprint %q: shows how to set [jira] board = %v, want %v:\n%s", sprint, got, tc.config, md)
				}
				if strings.Contains(md, "### Points") {
					t.Errorf("an explanation renders an empty summary:\n%s", md)
				}
			}
		})
	}
	// A failed ticket search on a sprint that has data is a note on it.
	v := sprintView(t, func(s *Snapshot) {
		st := s.Sources[SourceJiraSprints]
		st.Parts = append(st.Parts, SourcePart{Name: "sprint 1234", State: Stale, Error: "acli: HTTP 502"})
		s.Sources[SourceJiraSprints] = st
		for i := range s.Nodes {
			if s.Nodes[i].ID == "sprint:1234" {
				s.Nodes[i].Sprint.Truncated = true
			}
		}
	})
	md, err := Extract(v, Fields(), nil, ExtractArgs{View: "sprint"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Note: Jira found more tickets in this sprint than wgo reads, so some are missing.\n", "Note: This sprint's last ticket search failed (acli: HTTP 502); its tickets are from an earlier search.\n"} {
		if !strings.Contains(md, want) {
			t.Errorf("lacks %q:\n%s", want, md)
		}
	}
}

func TestSprintExtractArgErrors(t *testing.T) {
	v, err := LoadFixture(analysisFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args ExtractArgs
		want string
	}{
		{ExtractArgs{View: "sprint", Sprint: "999"}, "no sprint 999 in this snapshot; the active sprints are 1234 (Sprint 12), 1240 (Web Sprint 4)"},
		{ExtractArgs{View: "sprint", Sprint: "1235"}, "sprint 1235 (Sprint 13) is future, and the sprint view summarizes active sprints only; the active sprints are 1234 (Sprint 12), 1240 (Web Sprint 4)"},
		{ExtractArgs{View: "sprint", Sprint: "twelve"}, `sprint "twelve" is not a sprint ID; give the number Jira shows for it, such as 1234`},
		{ExtractArgs{View: "sprint", Sprint: "-3"}, "is not a sprint ID"},
		{ExtractArgs{Sprint: "1234"}, "a sprint only applies to the sprint view; ask for view sprint (--view sprint) to summarize sprint 1234"},
		{ExtractArgs{View: "sprint", Cols: []string{"points"}}, "cols only applies to the rows view; the sprint view's summary has fixed sections"},
		{ExtractArgs{View: "sprint", Group: "repo"}, "group only applies to the rows view"},
		{ExtractArgs{View: "sprint", Layout: "table"}, "layout only applies to the rows view"},
		{ExtractArgs{View: "sprint", Sort: []string{"points"}}, "sort only applies to the rows view"},
		{ExtractArgs{View: "kanban"}, `unknown view "kanban"; the available views are rows and sprint`},
	} {
		_, err := Extract(v, Fields(), nil, tc.args)
		if !errors.Is(err, ErrExtractArgs) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
	if _, err := Extract(v, Fields(), nil, ExtractArgs{View: "sprint", Query: "status_categry:done"}); err == nil || !strings.Contains(err.Error(), "did you mean status_category?") {
		t.Errorf("a query error: %v", err)
	}
}

// TestSprintExtractRouteMatchesExtract: GET /api/extract?view=sprint serves
// exactly what Extract renders, for summaries, explanations and errors.
func TestSprintExtractRouteMatchesExtract(t *testing.T) {
	filters := MergeFilters(nil, Fields())
	for _, v := range []*View{
		sprintView(t, nil),
		sprintView(t, func(s *Snapshot) {
			s.Sources[SourceJiraSprints] = SourceStatus{State: Unknown}
			var nodes []Node
			for _, n := range s.Nodes {
				if n.Kind != KindSprint {
					nodes = append(nodes, n)
				}
			}
			s.Nodes = nodes
		}),
	} {
		srv := newServerOpts(t, HandlerOptions{Source: StaticSource{V: v}, Filters: filters})
		for _, args := range []ExtractArgs{
			{View: "sprint"},
			{View: "sprint", Sprint: "1240"},
			{View: "sprint", Sprint: "1234", Query: "kind:ticket;mismatch:*"},
			{View: "sprint", Sprint: "1234", Query: "text:\"rate limit\""},
		} {
			want, err := Extract(v, Fields(), filters, args)
			if err != nil {
				t.Fatal(err)
			}
			p := url.Values{"view": {"sprint"}}
			if args.Sprint != "" {
				p.Set("sprint", args.Sprint)
			}
			if args.Query != "" {
				p.Set("q", args.Query)
			}
			resp, body := get(t, srv.URL+ExtractPath+"?"+p.Encode())
			if resp.StatusCode != 200 || body != want {
				t.Errorf("%+v: %d, route and Extract differ:\n%s\n---\n%s", args, resp.StatusCode, body, want)
			}
		}
	}
	srv := newServerOpts(t, HandlerOptions{Source: StaticSource{V: sprintView(t, nil)}})
	resp, body := get(t, srv.URL+ExtractPath+"?view=sprint&sprint=999")
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "no sprint 999 in this snapshot; the active sprints are 1234 (Sprint 12), 1240 (Web Sprint 4)") {
		t.Errorf("unknown sprint: %d %q", resp.StatusCode, body)
	}
}

func TestDefaultSprint(t *testing.T) {
	s := loadAnalysisFixture(t)
	active := activeSprints(s)
	if len(active) != 2 || active[0].id() != 1234 || active[1].id() != 1240 {
		t.Fatalf("active sprints: %+v", active)
	}
	// Sprint 12: five of its six tickets are alice's or bob's (TEAM-106 is
	// unassigned); Web Sprint 4's one ticket is carol's.
	if a, b := active[0], active[1]; len(a.tickets) != 6 || a.people != 5 || a.unknownPeople != 0 || len(b.tickets) != 1 || b.people != 0 || b.unknownPeople != 0 {
		t.Errorf("tickets and people: %d/%d/%d, %d/%d/%d", len(a.tickets), a.people, a.unknownPeople, len(b.tickets), b.people, b.unknownPeople)
	}
	if d := defaultSprint(active); d.id() != 1234 {
		t.Errorf("default = %d", d.id())
	}
	mk := func(id, tickets, people, unknown int) *sprintSlice {
		return &sprintSlice{node: &Node{Sprint: &SprintInfo{ID: id}}, tickets: make([]*Node, tickets), people: people, unknownPeople: unknown}
	}
	for _, tc := range []struct {
		list []*sprintSlice
		want int
	}{
		{[]*sprintSlice{mk(1, 9, 1, 0), mk(2, 3, 2, 0)}, 2}, // people first
		{[]*sprintSlice{mk(1, 3, 2, 0), mk(2, 4, 2, 0)}, 2}, // then tickets
		{[]*sprintSlice{mk(1, 4, 2, 0), mk(2, 4, 2, 0)}, 1}, // then the first
		{[]*sprintSlice{mk(1, 0, 0, 0), mk(2, 0, 0, 0)}, 1},
		// Unknown for some sprint: the most tickets.
		{[]*sprintSlice{mk(1, 3, 3, 0), mk(2, 4, 0, 4)}, 2},
		{[]*sprintSlice{mk(1, 4, 3, 1), mk(2, 4, 0, 0)}, 1},
	} {
		if d := defaultSprint(tc.list); d.id() != tc.want {
			t.Errorf("default of %+v = %d, want %d", tc.list, d.id(), tc.want)
		}
	}
}

// TestForPeople: whose ticket it is comes from account IDs or the sprint's
// search, and is unknown, never "no", when neither can tell.
// live_logic.js runs the same cases against S.forPeople.
func TestForPeople(t *testing.T) {
	acct := []Person{{Login: "alice", Jira: "acct-alice", Me: true}, {Login: "bob", Jira: "acct-bob"}}
	placeholder := []Person{{Login: "alice", Jira: JiraCurrentUser, Me: true}, {Login: "bob", Jira: "acct-bob"}}
	noJira := []Person{{Login: "alice", Jira: JiraCurrentUser, Me: true}, {Login: "bob"}}
	searched := &SprintInfo{ID: 7, Searched: true}
	for _, tc := range []struct {
		name   string
		t      TicketInfo
		sp     *SprintInfo
		people []Person
		want   tri
	}{
		{"account ID", TicketInfo{AssigneeID: "ACCT-BOB"}, &SprintInfo{ID: 7}, placeholder, triTrue},
		{"found by the search", TicketInfo{AssigneeID: "acct-x", SprintSearch: []int{7}}, &SprintInfo{ID: 7}, placeholder, triTrue},
		{"found by another sprint's search", TicketInfo{AssigneeID: "acct-x", SprintSearch: []int{8}}, &SprintInfo{ID: 7}, placeholder, triUnknown},
		{"unassigned", TicketInfo{Freshness: Fresh}, &SprintInfo{ID: 7}, placeholder, triFalse},
		{"assignee unknown", TicketInfo{Freshness: Unknown}, &SprintInfo{ID: 7}, placeholder, triUnknown},
		{"compared with every account ID", TicketInfo{AssigneeID: "acct-carol"}, &SprintInfo{ID: 7}, acct, triFalse},
		{"currentUser() cannot be compared", TicketInfo{AssigneeID: "acct-carol"}, &SprintInfo{ID: 7}, placeholder, triUnknown},
		{"an email cannot be compared", TicketInfo{AssigneeID: "acct-carol"}, &SprintInfo{ID: 7}, []Person{{Login: "alice", Jira: "alice@example.com"}}, triUnknown},
		{"display names are not trusted", TicketInfo{Assignee: "Alice", Freshness: Fresh}, &SprintInfo{ID: 7}, []Person{{Login: "alice", Jira: JiraCurrentUser, Name: "Alice"}}, triUnknown},
		{"the complete search missed it", TicketInfo{AssigneeID: "acct-carol"}, searched, placeholder, triFalse},
		{"a truncated search", TicketInfo{AssigneeID: "acct-carol"}, &SprintInfo{ID: 7, Searched: true, Truncated: true}, placeholder, triUnknown},
		{"a search that skipped someone", TicketInfo{AssigneeID: "acct-carol"}, searched, noJira, triUnknown},
		{"nobody followed", TicketInfo{AssigneeID: "acct-carol"}, searched, nil, triUnknown},
	} {
		if got := forPeople(&tc.t, tc.sp, tc.people); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	// Without account IDs or the search, the default is the sprint with
	// the most tickets, not a sprint with zero "of the people".
	s := loadAnalysisFixture(t)
	s.People = placeholder
	for i := range s.Nodes {
		if n := &s.Nodes[i]; n.Ticket != nil {
			n.Ticket.SprintSearch = nil
		} else if n.Sprint != nil {
			n.Sprint.Searched = false
		}
	}
	active := activeSprints(s)
	if a, b := active[0], active[1]; a.people != 1 || a.unknownPeople != 4 || b.people != 0 || b.unknownPeople != 1 {
		t.Errorf("with currentUser(): %d/%d, %d/%d", a.people, a.unknownPeople, b.people, b.unknownPeople)
	}
	if d := defaultSprint(active); d.id() != 1234 {
		t.Errorf("default = %d", d.id())
	}
}

// TestSprintDay is "day N of M" in the snapshot's timezone. live_logic.js
// checks the page's L.sprintDay against the same cases.
func TestSprintDay(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database")
	}
	ts := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	for _, tc := range []struct {
		start, end, now string
		loc             *time.Location
		want            string
	}{
		{"2026-10-20T08:00:00Z", "2026-11-03T08:00:00Z", "2026-10-30T16:00:00Z", time.UTC, "day 11 of 14"},
		{"2026-10-20T08:00:00Z", "2026-11-03T08:00:00Z", "2026-10-20T08:00:00Z", time.UTC, "day 1 of 14"},
		{"2026-10-20T08:00:00Z", "2026-11-03T08:00:00Z", "2026-11-03T07:00:00Z", time.UTC, "day 14 of 14"},
		{"2026-10-20T08:00:00Z", "2026-11-03T08:00:00Z", "2026-11-04T09:00:00Z", time.UTC, "day 14 of 14, past its end"},
		{"2026-10-20T08:00:00Z", "2026-11-03T08:00:00Z", "2026-10-18T09:00:00Z", time.UTC, "starts in 2 days"},
		{"2026-10-20T08:00:00Z", "2026-11-03T08:00:00Z", "2026-10-20T07:00:00Z", time.UTC, "starts today"},
		// Three weeks, Monday 10:00 in New York (14:00 UTC); on Thursday of
		// week one at 09:00 there (13:00 UTC) it is day 4, though less than
		// three days have passed.
		{"2026-10-05T14:00:00Z", "2026-10-26T14:00:00Z", "2026-10-08T13:00:00Z", ny, "day 4 of 21"},
		// 23:30 in New York is the next day in UTC.
		{"2026-10-05T14:00:00Z", "2026-10-26T14:00:00Z", "2026-10-09T03:30:00Z", ny, "day 4 of 21"},
		{"2026-10-05T14:00:00Z", "2026-10-26T14:00:00Z", "2026-10-09T03:30:00Z", time.UTC, "day 5 of 21"},
		// Across the end of daylight saving time (Nov 1 in New York).
		{"2026-10-26T13:00:00Z", "2026-11-09T14:00:00Z", "2026-11-08T15:00:00Z", ny, "day 14 of 14"},
	} {
		if got := sprintDayText(ts(tc.start), ts(tc.end), ts(tc.now), tc.loc); got != tc.want {
			t.Errorf("%s..%s at %s in %s = %q, want %q", tc.start, tc.end, tc.now, tc.loc, got, tc.want)
		}
	}
	if got := sprintDayText(time.Time{}, ts("2026-11-03T08:00:00Z"), ts("2026-10-30T16:00:00Z"), time.UTC); got != "" {
		t.Errorf("unknown start = %q", got)
	}
}

func TestCountSprint(t *testing.T) {
	s := loadAnalysisFixture(t)
	p := countSprint(activeSprints(s)[0].tickets)
	want := []sprintPart{{StatusDone, "Done", 3, 1}, {StatusInProgress, "In Progress", 15, 3}, {StatusToDo, "To Do", 0, 2}}
	for i, w := range want {
		if p.parts[i] != w {
			t.Errorf("part %d = %+v, want %+v", i, p.parts[i], w)
		}
	}
	if len(p.unestimated) != 1 || p.unestimated[0].Label != "TEAM-104" || len(p.unknownPoints) != 1 || p.unknownPoints[0].Label != "TEAM-106" || len(p.unknownCategory) != 0 {
		t.Errorf("apart: unestimated %v, unknown %v, category %v", p.unestimated, p.unknownPoints, p.unknownCategory)
	}
	if got := compareKeys("TEAM-9", "TEAM-10"); got >= 0 {
		t.Errorf("TEAM-9 sorts after TEAM-10")
	}
	// Two decimals, as the page's S.num (live_logic.js checks the same).
	for f, want := range map[float64]string{1: "1 point", 2.5: "2.5 points", 0: "0 points", 0.1 + 0.2: "0.3 points", 1.0 / 3: "0.33 points", 1e-9: "0 points", 13: "13 points"} {
		if got := pointsText(f); got != want {
			t.Errorf("pointsText(%v) = %q, want %q", f, got, want)
		}
	}
	tenth, fifth := 0.1, 0.2
	q := countSprint([]*Node{{Ticket: &TicketInfo{StatusCategory: StatusDone, StoryPoints: &tenth, StoryPointsKnown: true}}, {Ticket: &TicketInfo{StatusCategory: StatusDone, StoryPoints: &fifth, StoryPointsKnown: true}}})
	if got := pointsText(q.parts[0].points); got != "0.3 points" {
		t.Errorf("a sum of fractions: %q", got)
	}
}
