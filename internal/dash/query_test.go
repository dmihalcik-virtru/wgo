package dash

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// updateFields rewrites testdata/fields.json, the registry the JS tests load,
// and the derived facts stored in the analysis fixture:
//
//	go test ./internal/dash -run 'TestFieldRegistryGolden|TestAnalysisFixtureFacts' -update
var updateFields = flag.Bool("update", false, "rewrite testdata/fields.json and the analysis fixture's facts")

// queryVectors is testdata/query_vectors.json, shared with live.js's tests.
type queryVectors struct {
	Snapshot json.RawMessage   `json:"snapshot"`
	Fields   []Field           `json:"fields"`
	Planned  map[string]string `json:"planned"`
	Cases    []struct {
		Name   string         `json:"name"`
		Query  string         `json:"query"`
		Base   bool           `json:"base"`
		Env    map[string]any `json:"env"`
		Preset string         `json:"preset"`
		Expect *struct {
			Matches []string `json:"matches"`
			Rows    []string `json:"rows"`
			Visible any      `json:"visible"`
		} `json:"expect"`
		Warnings []string `json:"warnings"`
		Error    string   `json:"error"`
		Pos      int      `json:"pos"`
		Fixed    *string  `json:"fixed"`
	} `json:"cases"`
}

func loadVectors(t testing.TB) (queryVectors, *Registry) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "query_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v queryVectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	planned := maps.Clone(Fields().planned)
	maps.Copy(planned, v.Planned)
	reg, err := NewRegistry(append(Fields().List(), v.Fields...), planned)
	if err != nil {
		t.Fatalf("vector fields: %v", err)
	}
	return v, reg
}

// withEnv returns snap with each top-level key of env replaced.
func withEnv(t testing.TB, snap json.RawMessage, env map[string]any) []byte {
	t.Helper()
	if len(env) == 0 {
		return snap
	}
	var m map[string]any
	if err := json.Unmarshal(snap, &m); err != nil {
		t.Fatal(err)
	}
	for k, v := range env {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestQueryVectors runs the shared vectors through the Go evaluator. The
// same file runs under node through live_logic.js (TestLiveJSLogic).
func TestQueryVectors(t *testing.T) {
	v, vecReg := loadVectors(t)
	if len(v.Cases) < 50 {
		t.Fatalf("only %d vector cases", len(v.Cases))
	}
	for _, c := range v.Cases {
		t.Run(c.Name, func(t *testing.T) {
			reg := vecReg
			if c.Base {
				reg = Fields()
			}
			if c.Preset != "" {
				f, ok := BuiltinFilter(c.Preset)
				if !ok || f.Expr != c.Query {
					t.Fatalf("preset %s is %q, the case says %q", c.Preset, f.Expr, c.Query)
				}
			}
			q, err := ParseQuery(c.Query, reg)
			if c.Error != "" {
				qe, ok := err.(*QueryError)
				if !ok {
					t.Fatalf("want error %q, got %v", c.Error, err)
				}
				if !strings.Contains(qe.Msg, c.Error) || qe.Pos != c.Pos {
					t.Fatalf("error = %q at %d, want %q at %d", qe.Msg, qe.Pos, c.Error, c.Pos)
				}
				if want := ""; c.Fixed != nil {
					want = *c.Fixed
					if qe.Fixed != want {
						t.Fatalf("fixed = %q, want %q", qe.Fixed, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			ix, err := newQueryIndex(withEnv(t, v.Snapshot, c.Env), reg)
			if err != nil {
				t.Fatal(err)
			}
			res := ix.evaluate(q)
			got, rows := slices.Sorted(slices.Values(res.Matches)), slices.Sorted(slices.Values(res.Rows))
			if !slices.Equal(got, c.Expect.Matches) {
				t.Errorf("matches = %q\nwant      %q", got, c.Expect.Matches)
			}
			if !slices.Equal(rows, c.Expect.Rows) {
				t.Errorf("rows = %q\nwant   %q", rows, c.Expect.Rows)
			}
			switch want := c.Expect.Visible.(type) {
			case nil:
			case string:
				if want != "all" || res.Filtered() {
					t.Errorf("visible: want %v, query filters = %v", want, res.Filtered())
				}
			case []any:
				var vis []string
				for _, id := range ix.ids {
					if res.Filtered() && res.Visible(id) {
						vis = append(vis, id)
					}
				}
				slices.Sort(vis)
				var w []string
				for _, x := range want {
					w = append(w, x.(string))
				}
				if !slices.Equal(vis, w) {
					t.Errorf("visible = %q\nwant      %q", vis, w)
				}
			}
			if !slices.Equal(res.Warnings, c.Warnings) {
				t.Errorf("warnings = %q, want %q", res.Warnings, c.Warnings)
			}
		})
	}
}

// TestQueryVectorSnapshotValidates keeps the vectors' snapshot a valid
// dash snapshot, so wgo dash --fixture can serve it.
func TestQueryVectorSnapshotValidates(t *testing.T) {
	v, _ := loadVectors(t)
	var s Snapshot
	if err := json.Unmarshal(v.Snapshot, &s); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestFieldRegistryGolden pins testdata/fields.json, which live_logic.js
// loads as the page's registry (the page gets it from its bootstrap).
func TestFieldRegistryGolden(t *testing.T) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", " ")
	if err := enc.Encode(Fields().JSON()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "fields.json")
	if *updateFields {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v; run go test ./internal/dash -run TestFieldRegistryGolden -update", err)
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Fatalf("%s is out of date with the registry; run go test ./internal/dash -run TestFieldRegistryGolden -update", path)
	}
}

// TestQuerySpecExamplesParse parses every query in spec/gh-100.md, with the
// catalogue fields the vectors register standing in for gh-97/98/99.
func TestQuerySpecExamplesParse(t *testing.T) {
	_, reg := loadVectors(t)
	examples := []string{
		"move_people:team:web", "mergeable:mergeable,unknown", "activity:2026-10-14;prstate:open",
		"repo:acme/api;status_category:done", "when:2026-10-01..2026-10-07", "merged:2026-10-01..2026-10-07",
		"check_failing:vendor-hsm-tests", "effort:repo:acme/api", "prstate:draft", "mismatch:code_merged_ticket_open",
		"status_category:in_progress", "!move:author", "age:2w..", "kind:pr", "text:\"In Review\"",
		"sprint:42", "updated_ago:<=30d", "points:>3", "created:>=today", "resolved:..+1w",
	}
	for _, f := range builtinFilters {
		examples = append(examples, f.Expr)
	}
	for _, ex := range examples {
		if _, err := ParseQuery(ex, reg); err != nil {
			t.Errorf("%s: %v", ex, err)
		}
	}
	if _, err := ParseQuery("prstate:open;status_category:done", reg); err == nil {
		t.Error("prstate:open;status_category:done must be an incompatible-fields error")
	}
}

// TestBuiltinFiltersAreVisible: with gh-99's fields registered, every
// built-in runs; one whose fields are not registered yet would be hidden and
// say which spec brings them.
func TestBuiltinFiltersAreVisible(t *testing.T) {
	merged := MergeFilters(nil, Fields())
	if len(merged) != len(builtinFilters) {
		t.Fatalf("got %d filters", len(merged))
	}
	for _, f := range merged {
		if f.Hidden {
			t.Errorf("%s hidden: %s", f.Name, f.Reason)
		}
	}
	// Without gh-99's fields, as before it landed.
	var fs []Field
	planned := map[string]string{}
	for _, f := range Fields().List() {
		if slices.ContainsFunc(f.Paths, func(p string) bool { return strings.HasPrefix(p, "pr.facts.") || strings.HasPrefix(p, "ticket.facts.") }) {
			planned[f.Name] = "gh-99"
			continue
		}
		fs = append(fs, f)
	}
	before := mustRegistry(fs, planned)
	merged = MergeFilters(nil, before)
	visible := []string{"review-inbox", "shipped", "close-candidates", "sprint"}
	for _, f := range merged {
		switch {
		case slices.Contains(visible, f.Name):
			if f.Hidden {
				t.Errorf("%s hidden: %s", f.Name, f.Reason)
			}
		case !f.Hidden || !strings.Contains(f.Reason, "not registered yet") || !strings.Contains(f.Reason, "arrives with gh-99"):
			t.Errorf("%s: hidden=%v reason=%q", f.Name, f.Hidden, f.Reason)
		}
	}
	if f, _ := LookupFilter(merged, "push-reviewers"); f.Reason != "needs fields not registered yet: move, ci (arrives with gh-99)" {
		t.Errorf("push-reviewers reason = %q", f.Reason)
	}
	if len(planned) != 11 {
		t.Errorf("gh-99 registers %d fields, want 11: %v", len(planned), planned)
	}
}

func TestConfigFiltersOverrideAndAppend(t *testing.T) {
	merged := MergeFilters(map[string]string{"shipped": "kind:pr;prstate:merged", "stale-api": "repo:acme/api;!activity:*", "broken": "prstat:open"}, Fields())
	shipped, _ := LookupFilter(merged, "shipped")
	if shipped.Builtin || !shipped.Overrides || shipped.Hidden || shipped.Expr != "kind:pr;prstate:merged" {
		t.Errorf("shipped = %+v", shipped)
	}
	names := []string{}
	for _, f := range merged[len(builtinFilters):] {
		names = append(names, f.Name)
	}
	if !slices.Equal(names, []string{"broken", "stale-api"}) {
		t.Errorf("configured filters = %v", names)
	}
	broken, _ := LookupFilter(merged, "broken")
	if !broken.Hidden || !strings.Contains(broken.Reason, "[dash.filters] broken in ~/.wgo/config.toml does not parse: unknown field \"prstat\"") {
		t.Errorf("broken = %+v", broken)
	}
	if f, _ := LookupFilter(merged, "stale-api"); f.Hidden {
		t.Errorf("stale-api hidden: %s", f.Reason)
	}
}

func TestQueryEnvReadsPeopleTolerantly(t *testing.T) {
	env := queryEnv("2026-10-20T15:00:00-04:00", json.RawMessage(`[{"login":"alice","me":true,"name":"Alice"},{"github":"bob"},{"name":"no login"},7]`))
	if env.Me != "alice" || len(env.People) != 2 || env.People[1].Login != "bob" || env.Now.IsZero() {
		t.Errorf("env = %+v", env)
	}
	if e := queryEnv("not a time", json.RawMessage(`{"login":"x"}`)); e.Me != "" || len(e.People) != 0 || !e.Now.IsZero() {
		t.Errorf("malformed env = %+v", e)
	}
}

func TestQueryIncompatibleFieldsWithoutAPairwiseConflict(t *testing.T) {
	reg := mustRegistry([]Field{
		{Name: "a", Type: FieldEnum, Kinds: []NodeKind{KindPR, KindTicket}, Paths: []string{"a"}},
		{Name: "b", Type: FieldEnum, Kinds: []NodeKind{KindTicket, KindAgent}, Paths: []string{"b"}},
		{Name: "c", Type: FieldEnum, Kinds: []NodeKind{KindPR, KindAgent}, Paths: []string{"c"}},
	}, nil)
	_, err := ParseQuery("a:x;b:y;c:z", reg)
	want := "fields a, b and c apply to no common node kind (a: pr, ticket; b: ticket, agent; c: pr, agent) at column 9; split them into separate queries"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %s", err, want)
	}
	if qe := err.(*QueryError); qe.Fixed != "c:z" {
		t.Errorf("fixed = %q, want the newest clause alone", qe.Fixed)
	}
}

// TestNewRegistryRejectsRegistrationTraps: mistakes a data spec could make
// when it registers a field fail loudly at start-up, not as baffling query
// errors later.
func TestNewRegistryRejectsRegistrationTraps(t *testing.T) {
	for _, tc := range []struct {
		f    Field
		want string
	}{
		{Field{Name: "priority", Type: FieldEnum, Kinds: []NodeKind{KindTicket}, Paths: []string{"ticket.priority"}, Values: []string{"High", "Low"}}, `value "High" must be lowercase`},
		{Field{Name: "x", Type: FieldEnum, Kinds: []NodeKind{"tikcet"}, Paths: []string{"ticket.x"}}, `unknown node kind "tikcet"; kinds are effort, workspace, bookmark, pr, ticket, agent`},
		{Field{Name: "x", Type: FieldSet, Kinds: []NodeKind{KindPR}, Paths: []string{"pr.x"}, Derive: DeriveLinkedRepos, DeriveKinds: []NodeKind{"tickets"}}, `unknown node kind "tickets"`},
		{Field{Name: "x", Type: FieldEnum, Kinds: []NodeKind{KindPR}, Paths: []string{"pr.x"}, Map: map[string]string{"APPROVED": "approved"}}, `map key "APPROVED" must be lowercase`},
		{Field{Name: "x", Type: FieldNumber, Kinds: []NodeKind{KindPR}, Paths: []string{"pr.x"}, Scale: "log10"}, `scale "log10" is not linear or log`},
		{Field{Name: "Bad", Type: FieldText, Kinds: []NodeKind{KindPR}, Paths: []string{"pr.x"}}, "names are lowercase"},
		{Field{Name: "x", Type: "fuzzy", Kinds: []NodeKind{KindPR}, Paths: []string{"pr.x"}}, `unknown type "fuzzy"`},
		{Field{Name: "x", Type: FieldText, Kinds: []NodeKind{KindPR}}, "no paths and no derive"},
		{Field{Name: "x", Type: FieldText, Kinds: []NodeKind{KindPR}, Paths: []string{"pr[state"}}, "unclosed ["},
	} {
		if _, err := Fields().With(tc.f); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.f, err, tc.want)
		}
	}
	// Data values may have any case: they are lowercased when indexed.
	reg, err := Fields().With(Field{Name: "priority", Type: FieldEnum, Kinds: []NodeKind{KindTicket}, Paths: []string{"ticket.priority"}, Values: []string{"high", "low"}})
	if err != nil {
		t.Fatal(err)
	}
	q, err := ParseQuery("priority:High", reg)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := newQueryIndex([]byte(`{"generated_at":"2026-10-20T15:00:00-04:00","nodes":[{"id":"t","kind":"ticket","ticket":{"priority":"High"}}]}`), reg)
	if err != nil {
		t.Fatal(err)
	}
	if res := ix.evaluate(q); len(res.Matches) != 1 {
		t.Errorf("priority:High against data High: %v", res.Matches)
	}
}

func TestMergeConfigFiltersToleratesBadEntries(t *testing.T) {
	merged := MergeConfigFilters(map[string]any{
		"release": map[string]any{"v2": "kind:pr;prstate:open"}, // "release.v2" = … in TOML
		"count":   int64(5),
		"shipped": []any{"x"},
		"fine":    "kind:ticket",
	}, Fields())
	byName := map[string]SavedFilter{}
	for _, f := range merged {
		byName[f.Name] = f
	}
	if f := byName["release.v2"]; f.Hidden || f.Expr != "kind:pr;prstate:open" {
		t.Errorf("dotted name: %+v", f)
	}
	if f := byName["count"]; !f.Hidden || !strings.Contains(f.Reason, "[dash.filters] count in ~/.wgo/config.toml is 5, not a query string") {
		t.Errorf("non-string: %+v", f)
	}
	if f := byName["shipped"]; f.Builtin || !f.Overrides || !f.Hidden || !strings.Contains(f.Reason, "not a query string") {
		t.Errorf("a bad entry overriding a built-in hides it: %+v", f)
	}
	if f := byName["fine"]; f.Hidden {
		t.Errorf("fine: %+v", f)
	}
}

// genSnapshot builds an invented snapshot of about n nodes: efforts with a
// workspace, bookmark, two PRs, a ticket and an agent each, and two more PRs
// per effort found only by the inventory, which their repository's group
// effort tracks (about 22 per repository), so the timings cover the efforts
// list's tracked PRs too.
func genSnapshot(n int) []byte {
	now := time.Date(2026, 10, 20, 15, 0, 0, 0, time.FixedZone("", -4*3600))
	var days []string
	for i := 13; i >= 0; i-- {
		days = append(days, now.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	s := &Snapshot{Schema: SchemaVersion, GeneratedAt: now, Days: 14, Sources: map[string]SourceStatus{}, Timezone: "America/New_York",
		Counts: Counts{Days: days, Activity: map[string][]int{}}}
	states := []string{"open", "open", "merged", "closed"}
	checks := []string{"success", "failure", "pending", ""}
	for r := range 20 {
		key := repoEffortKey(fmt.Sprintf("acme/repo-%d", r))
		s.Nodes = append(s.Nodes, Node{ID: effortNodeID(key), Kind: KindEffort, Label: fmt.Sprintf("acme/repo-%d", r), Effort: &EffortInfo{Key: key, Group: GroupRepo}})
	}
	for e := 0; len(s.Nodes) < n; e++ {
		eid := effortNodeID(fmt.Sprintf("effort-%d", e))
		ws := fmt.Sprintf("ws-%016x", e)
		bm := fmt.Sprintf("bm-%016x", e)
		tk := jiraTicketID(fmt.Sprintf("TEAM-%d", e))
		repo := fmt.Sprintf("acme/repo-%d", e%20)
		s.Nodes = append(s.Nodes,
			Node{ID: eid, Kind: KindEffort, Label: fmt.Sprintf("Effort %d", e), Effort: &EffortInfo{Key: fmt.Sprintf("effort-%d", e), Group: GroupEffort}},
			Node{ID: ws, Kind: KindWorkspace, Label: fmt.Sprintf("repo/ws-%d", e), Workspace: &WorkspaceInfo{Slug: "ws", Repo: "repo", RepoSlug: repo, EffortID: eid, LastActivity: now.Add(-time.Duration(e) * time.Hour)}},
			Node{ID: bm, Kind: KindBookmark, Label: fmt.Sprintf("team-%d-work", e), Bookmark: &BookmarkInfo{Name: "b", PRLookup: Fresh, PRCount: 2}},
			Node{ID: tk, Kind: KindTicket, Label: fmt.Sprintf("TEAM-%d", e), Ticket: &TicketInfo{Key: "k", System: "jira", Status: "In Progress", Freshness: Fresh}},
			Node{ID: agentNodeID(fmt.Sprintf("a-%d", e)), Kind: KindAgent, Label: "claude", Agent: &AgentInfo{Liveness: LivenessActive, EffortID: eid, WorkspaceID: ws}},
		)
		s.Edges = append(s.Edges, Edge{eid, ws, EdgeContains}, Edge{ws, bm, EdgeOn}, Edge{eid, agentNodeID(fmt.Sprintf("a-%d", e)), EdgeRuns})
		for p := 0; p < 2; p++ {
			id := prNodeID(repo, e*2+p)
			s.Nodes = append(s.Nodes, Node{ID: id, Kind: KindPR, Label: fmt.Sprintf("%s#%d", repo, e*2+p), PR: &PRInfo{Number: e*2 + p, Repo: repo,
				Title: fmt.Sprintf("Change %d", e*2+p), State: states[(e+p)%4], IsDraft: (e+p)%5 == 0, Checks: checks[(e*3+p)%4], ReviewersKnown: true,
				UpdatedAt: now.Add(-time.Duration(e*7+p) * time.Hour), Freshness: Fresh, BookmarkID: bm}})
			s.Edges = append(s.Edges, Edge{bm, id, EdgePR}, Edge{id, tk, EdgeTicket})
		}
		for p := 0; p < 2; p++ {
			num := 100000 + e*2 + p
			id := prNodeID(repo, num)
			s.Nodes = append(s.Nodes, Node{ID: id, Kind: KindPR, Label: fmt.Sprintf("%s#%d", repo, num), PR: &PRInfo{Number: num, Repo: repo,
				Title: fmt.Sprintf("Change %d", num), State: states[(e+p+1)%4], Checks: checks[(e+p)%4], ReviewersKnown: true,
				UpdatedAt: now.Add(-time.Duration(e*5+p) * time.Hour), Freshness: Fresh, Sources: []string{PRSourceInventory}}})
			s.Edges = append(s.Edges, Edge{effortNodeID(repoEffortKey(repo)), id, EdgeTracks}, Edge{id, tk, EdgeTicket})
		}
		act := make([]int, 14)
		act[e%14] = 1 + e%3
		s.Counts.Activity[eid] = act
	}
	b, _ := json.Marshal(s)
	return b
}

// TestQueryPerformance logs (does not assert) how long indexing and a
// 12-clause query take over 2,000 nodes (spec/gh-100.md, requirement 28).
func TestQueryPerformance(t *testing.T) {
	raw := genSnapshot(2000)
	t0 := time.Now()
	ix, err := newQueryIndex(raw, Fields())
	if err != nil {
		t.Fatal(err)
	}
	indexed := time.Since(t0)
	q, err := ParseQuery(perfQuery, Fields())
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Clauses) != 12 {
		t.Fatalf("perf query has %d clauses", len(q.Clauses))
	}
	const runs = 50
	t1 := time.Now()
	var res *QueryResult
	for range runs {
		res = ix.evaluate(q)
	}
	per := time.Since(t1) / runs
	t.Logf("query over %d nodes: index %v once, 12 clauses %v per evaluation (%d matches, %d rows)", ix.total, indexed, per, len(res.Matches), len(res.Rows))
	if len(res.Matches) == 0 {
		t.Error("the perf query matches nothing; it should exercise every clause")
	}
}

// perfQuery is the 12-clause query both evaluators time; live_logic.js has
// the same string.
const perfQuery = "kind:pr;prstate:open,draft,merged;!checks:pending;freshness:fresh,stale;repo:*;effort:*;activity:*;!text:zzz;review:none,approved;!prstate:closed;text:change;!repo:acme/repo-19"

// TestBuiltinFiltersOnTheAnalysisFixture runs the built-ins that gh-97 and
// gh-98's fields make visible over the sanitized analysis fixture, as
// wgo dash --fixture would (alice is me; generated 2026-10-30 16:00 UTC).
func TestBuiltinFiltersOnTheAnalysisFixture(t *testing.T) {
	v, err := LoadFixture(analysisFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, expr string }{
		// A team-only request (#104, team:web) reaches alice through her
		// review_requested involvement, beside the direct requests.
		{"review-inbox", "pr:acme/api#104 pr:acme/api#105 pr:acme/web#202"},
		{"shipped", "pr:acme/api#107 pr:acme/infra#303"},
		{"close-candidates", "pr:acme/api#105"},
		// TEAM-108 is in the future Sprint 13 only.
		{"sprint", "ticket:jira:TEAM-101 ticket:jira:TEAM-102 ticket:jira:TEAM-103 ticket:jira:TEAM-104 ticket:jira:TEAM-105 ticket:jira:TEAM-106"},
	} {
		f, _ := BuiltinFilter(tc.name)
		res, err := v.Query(f.Expr)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := strings.Join(slices.Sorted(slices.Values(res.Rows)), " "); got != tc.expr {
			t.Errorf("%s rows = %s\nwant %s", tc.name, got, tc.expr)
		}
	}
	// A merged PR counts in the week it merged, not the week it was last
	// updated: #108 merged on 2026-10-19 and #205 on 2026-10-22, and both
	// were updated the week after.
	for _, tc := range []struct {
		expr string
		in   bool
	}{
		{"kind:pr;when:2026-10-19..2026-10-25", true},
		{"kind:pr;when:2026-10-26..2026-11-01", false},
	} {
		res, err := v.Query(tc.expr)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"pr:acme/api#108", "pr:acme/web#205"} {
			if slices.Contains(res.Rows, id) != tc.in {
				t.Errorf("%s: %s in rows = %v, want %v", tc.expr, id, !tc.in, tc.in)
			}
		}
	}
	// A ticket's repository comes from the PRs and bookmarks that lead to
	// it; a ticket field and a PR field together are an error.
	res, err := v.Query("repo:acme/api;status_category:done")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Rows, " "); got != "ticket:jira:TEAM-102" {
		t.Errorf("done tickets in acme/api = %s", got)
	}
	if _, err := v.Query("prstate:open;status_category:done"); err == nil || !strings.Contains(err.Error(), "apply to no common node kind") {
		t.Errorf("prstate with status_category: %v", err)
	}
}

// TestFixtureTeamRequestIsMyMoveAndInMyInbox is gh-100's acceptance
// criterion on the analysis fixture: a team-only review request for the
// authenticated person (alice, on acme/api#104) matches both my-move and
// review-inbox, and the other gh-99 built-ins find their PRs and tickets.
func TestFixtureTeamRequestIsMyMoveAndInMyInbox(t *testing.T) {
	v, err := LoadFixture(analysisFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	run := func(name string) []string {
		t.Helper()
		f, ok := BuiltinFilter(name)
		if !ok {
			t.Fatalf("no built-in %s", name)
		}
		res, err := v.Query(f.Expr)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res.Rows
	}
	for _, name := range []string{"my-move", "review-inbox"} {
		if rows := run(name); !slices.Contains(rows, "pr:acme/api#104") {
			t.Errorf("%s rows %v lack the team-only request acme/api#104", name, rows)
		}
	}
	for name, want := range map[string][]string{
		"my-move":           {"pr:acme/api#103", "pr:acme/api#104", "pr:acme/infra#301", "pr:acme/web#202"},
		"waiting-reviewers": {"pr:acme/api#101", "pr:acme/api#102", "pr:acme/api#104", "pr:acme/web#202"},
		"push-reviewers":    {"pr:acme/api#101", "pr:acme/api#102", "pr:acme/api#104", "pr:acme/web#201", "pr:acme/web#207"},
		"nobody":            {"pr:acme/web#201", "pr:acme/web#207"},
		"jira-out-of-sync":  {"ticket:jira:TEAM-102", "ticket:jira:TEAM-103", "ticket:jira:TEAM-105", "ticket:jira:TEAM-106", "ticket:jira:TEAM-107"},
	} {
		if rows := run(name); !slices.Equal(slices.Sorted(slices.Values(rows)), want) {
			t.Errorf("%s rows %v, want %v", name, rows, want)
		}
	}
}
