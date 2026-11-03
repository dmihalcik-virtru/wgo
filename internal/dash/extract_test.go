package dash

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// vectorFixture writes the shared vectors' snapshot to a file and loads it
// as wgo dash --fixture does.
func vectorFixture(t testing.TB) (*View, string) {
	t.Helper()
	v, _ := loadVectors(t)
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, v.Snapshot, 0o600); err != nil {
		t.Fatal(err)
	}
	view, err := LoadFixture(path)
	if err != nil {
		t.Fatal(err)
	}
	return view, path
}

func TestExtractRowsBullets(t *testing.T) {
	v, _ := vectorFixture(t)
	md, err := Extract(v, Fields(), MergeFilters(nil, Fields()), ExtractArgs{Query: "kind:pr;prstate:open,draft", Cols: []string{"checks", "review", "repo"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "## kind:pr;prstate:open,draft\n\n" +
		"### API widgets\n\n" +
		"- [acme/api#41](https://github.com/acme/api/pull/41) — Add widget endpoints · checks: failure · review: none · repo: acme/api\n" +
		"- [acme/api#42](https://github.com/acme/api/pull/42) — Widget pagination (WIP) · checks: pending · review: none · repo: acme/api\n\n" +
		"### Ungrouped\n\n" +
		"- [acme/web#12](https://github.com/acme/web/pull/12) — Theme tokens for the web app · checks: success · review: none · repo: acme/web\n\n" +
		"---\n\n" +
		"Snapshot 2026-10-20 15:00 -04:00 · generation 7 · 3 of 26 nodes\n\n" +
		"Query: `kind:pr;prstate:open,draft`\n\n" +
		"Sources: agents fresh (2 fresh) · github_issues unknown (1 unknown) · github_prs stale (3 fresh, 2 stale, oldest fetch 2026-10-20 09:00 -04:00) · jira stale (2 fresh, 1 stale) · jira_sprints fresh (2 fresh) · jj fresh (4 fresh, 1 unavailable)\n"
	if md != want {
		t.Fatalf("extract:\n%s\nwant:\n%s", md, want)
	}
}

func TestExtractUnknownsGroupsAndTable(t *testing.T) {
	v, _ := vectorFixture(t)
	filters := MergeFilters(map[string]string{"web-prs": "kind:pr;repo:acme/web"}, Fields())
	md, err := Extract(v, Fields(), filters, ExtractArgs{Query: "kind:pr;repo:acme/web", Cols: []string{"checks", "effort"}, Group: "repo", Layout: "table"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## web-prs\n", // a saved filter's name is the heading
		"### acme/web\n\n| Item | Title | checks | effort |\n| --- | --- | --- | --- |\n",
		"| [acme/web#9](https://github.com/acme/web/pull/9) | Old spike | unknown | ungrouped |\n", // unknown, never blank
		"Query: `kind:pr;repo:acme/web`",
		"2 of 26 nodes",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("extract lacks %q:\n%s", want, md)
		}
	}
	md, err = Extract(v, Fields(), filters, ExtractArgs{Query: "repo:unknown", Cols: []string{"repo", "checks"}, Group: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "### Repository unknown\n\n- notes · repo: unknown\n- [TEAM-99]") {
		t.Errorf("unknown repo section:\n%s", md)
	}
	if strings.Contains(md, "checks:") {
		t.Errorf("a column that does not apply to the row is left out:\n%s", md)
	}
	md, err = Extract(v, Fields(), filters, ExtractArgs{Group: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(md, "##") || strings.Contains(md, "###") || !strings.Contains(md, "Query: none (every node)") || !strings.Contains(md, "26 of 26 nodes") {
		t.Errorf("empty query, no groups:\n%s", md)
	}
	md, err = Extract(v, Fields(), filters, ExtractArgs{Query: "text:nothing-matches-this"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "No rows match.\n") {
		t.Errorf("no rows:\n%s", md)
	}
}

func TestExtractNoEffortSectionAndEscaping(t *testing.T) {
	v, _ := vectorFixture(t)
	// A future sprint none of whose tickets are followed is in no effort.
	// Every ticket hangs off a PR, a bookmark or a group effort that tracks
	// it, so none is; a sprint group is a section of its own.
	md, err := Extract(v, Fields(), nil, ExtractArgs{Query: "kind:sprint;!effort:*"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "### No effort\n\n- Sprint 13\n") {
		t.Errorf("no-effort section:\n%s", md)
	}
	md, err = Extract(v, Fields(), nil, ExtractArgs{Query: "kind:ticket;!effort:*"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "No rows match.\n") {
		t.Errorf("no-effort tickets:\n%s", md)
	}
	md, err = Extract(v, Fields(), nil, ExtractArgs{Query: "kind:ticket;effort:sprint:1234"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "### Sprint 12\n\n- [TEAM-99](https://example.atlassian.net/browse/TEAM-99) — Widget docs\n") {
		t.Errorf("sprint group section:\n%s", md)
	}
	for in, want := range map[string]string{
		"a_b *c* [d](e) <f> | `g`": "a_b \\*c\\* \\[d\\](e) \\<f\\> \\| \\`g\\`",
		"#1 _x_\nnext":             "\\#1 \\_x\\_ next",
	} {
		if got := mdText(in); got != want {
			t.Errorf("mdText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := mdCode("a`b"); got != "``a`b``" {
		t.Errorf("mdCode = %q", got)
	}
}

func TestExtractArgErrors(t *testing.T) {
	v, _ := vectorFixture(t)
	for _, tc := range []struct {
		args ExtractArgs
		want string
	}{
		{ExtractArgs{View: "sprint"}, "the sprint view is not available yet: it arrives with gh-105"},
		{ExtractArgs{Sprint: "42"}, "a sprint is chosen for the sprint view, which is not available yet"},
		{ExtractArgs{View: "kanban"}, `unknown view "kanban"`},
		{ExtractArgs{Group: "team"}, `unknown group "team"; use effort, repo or none`},
		{ExtractArgs{Layout: "csv"}, `unknown layout "csv"`},
		{ExtractArgs{Cols: []string{"prstat"}}, `unknown column "prstat"; did you mean prstate?`},
		{ExtractArgs{Cols: []string{"move"}}, "column move is not available yet: it arrives with gh-99"},
	} {
		_, err := Extract(v, Fields(), nil, tc.args)
		if !errors.Is(err, ErrExtractArgs) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
	noRepo := mustRegistry([]Field{{Name: "kind", Type: FieldEnum, Kinds: queryKinds, Paths: []string{"kind"}}}, nil)
	if _, err := Extract(v, noRepo, nil, ExtractArgs{Group: "repo"}); !errors.Is(err, ErrExtractArgs) || !strings.Contains(err.Error(), "group repo needs the repo field") {
		t.Errorf("group repo without a repo field: %v", err)
	}
	_, err := Extract(v, Fields(), nil, ExtractArgs{Query: "prstat:open"})
	var qe *QueryError
	if !errors.As(err, &qe) || qe.Pos != 1 || qe.Fixed != "prstate:open" {
		t.Errorf("query error = %v", err)
	}
}

// TestExtractRouteMatchesExtract: GET /api/extract returns exactly what
// Extract (and so wgo dash --md) renders for the same arguments.
func TestExtractRouteMatchesExtract(t *testing.T) {
	v, _ := vectorFixture(t)
	filters := MergeFilters(map[string]string{"api": "repo:acme/api"}, Fields())
	srv := newServerOpts(t, HandlerOptions{Source: StaticSource{V: v}, Filters: filters})
	for _, args := range []ExtractArgs{
		{},
		{Query: "repo:acme/api", Cols: []string{"kind", "freshness"}, Group: "repo", Layout: "table"},
		{Query: "activity:-6d;prstate:open", Cols: []string{"activity"}, Group: "none"},
		{Query: "text:\"API WIDGETS\"", Layout: "bullets"},
	} {
		want, err := Extract(v, Fields(), filters, args)
		if err != nil {
			t.Fatal(err)
		}
		p := url.Values{}
		for k, val := range map[string]string{"q": args.Query, "cols": strings.Join(args.Cols, ","), "group": args.Group, "layout": args.Layout} {
			if val != "" {
				p.Set(k, val)
			}
		}
		resp, body := get(t, srv.URL+ExtractPath+"?"+p.Encode())
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/markdown; charset=utf-8" {
			t.Fatalf("%v: %d %s", args, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if body != want {
			t.Errorf("%+v: route and Extract differ:\n%s\n---\n%s", args, body, want)
		}
	}
	for path, want := range map[string]string{
		"?view=sprint&sprint=42": "the sprint view is not available yet",
		"?q=prstat:open":         `unknown field "prstat" at column 1; did you mean prstate?`,
		"?cols=nope":             `unknown column "nope"`,
	} {
		resp, body := get(t, srv.URL+ExtractPath+path)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, want) || strings.Contains(body, "extract:") {
			t.Errorf("%s: %d %q, want 400 with %q", path, resp.StatusCode, body, want)
		}
	}
	empty := newServer(t, emptySource{})
	if resp, _ := get(t, empty.URL+ExtractPath); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("no snapshot: %d, want 503", resp.StatusCode)
	}
}

func TestLoadFixture(t *testing.T) {
	v, path := vectorFixture(t)
	if v.Snapshot.Generation != 7 || v.Loaded || len(v.Snapshot.Nodes) != 26 {
		t.Fatalf("fixture view: gen %d loaded %v nodes %d", v.Snapshot.Generation, v.Loaded, len(v.Snapshot.Nodes))
	}
	if env := v.Env(); env.Me != "alice" || len(env.People) != 2 {
		t.Errorf("people from the fixture JSON: %+v", env)
	}
	// The fixture is served as written: fields this build does not model
	// (people, a PR's merged_at) still reach the page.
	var buf bytes.Buffer
	if err := v.WriteJSON(&buf, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"people":[`) || !strings.Contains(buf.String(), `"merged_at":"2026-10-07T16:00:00-04:00"`) {
		t.Error("fixture JSON is not served as written")
	}
	// A whole /api/snapshot body loads too.
	body := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(body, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if w, err := LoadFixture(body); err != nil || w.Snapshot.Generation != 7 {
		t.Errorf("API body fixture: %v", err)
	}
	raw, _ := os.ReadFile(path)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["schema"] = 9
	raw, _ = json.Marshal(m)
	bad := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(bad, raw, 0o600)
	if _, err := LoadFixture(bad); err == nil || !strings.Contains(err.Error(), "not a valid dash snapshot") {
		t.Errorf("invalid fixture: %v", err)
	}
	if _, err := LoadFixture(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("missing fixture loaded")
	}
}

func TestWriteQueryJSONLimitsToTheVisibleSet(t *testing.T) {
	v, _ := vectorFixture(t)
	res, err := v.Query("prstate:draft")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := v.WriteQueryJSON(&buf, time.Now(), res, MergeFilters(nil, Fields())); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Status   string `json:"status"`
		Snapshot struct {
			Nodes  []struct{ ID string }
			Edges  []struct{ Source, Target string }
			People []map[string]any `json:"people"`
			Counts map[string]any   `json:"counts"`
		} `json:"snapshot"`
		Query struct {
			Expr    string        `json:"expr"`
			Matches []string      `json:"matches"`
			Rows    []string      `json:"rows"`
			Filters []SavedFilter `json:"filters"`
		} `json:"query"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "ready" || out.Query.Expr != "prstate:draft" || len(out.Query.Matches) != 1 || out.Query.Matches[0] != "pr:acme/api#42" {
		t.Fatalf("query = %+v", out.Query)
	}
	if len(out.Snapshot.Nodes) != 6 || len(out.Snapshot.Edges) != 6 || len(out.Snapshot.People) != 2 || out.Snapshot.Counts == nil {
		t.Errorf("limited snapshot: %d nodes, %d edges, people %v", len(out.Snapshot.Nodes), len(out.Snapshot.Edges), out.Snapshot.People)
	}
	if f := out.Query.Filters; len(f) != len(builtinFilters) || !f[0].Hidden || !strings.Contains(f[0].Reason, "gh-99") {
		t.Errorf("filters report why they are hidden: %+v", f)
	}
	// A query with only kind clauses does not filter: the snapshot is whole.
	all, _ := v.Query("kind:pr")
	buf.Reset()
	_ = v.WriteQueryJSON(&buf, time.Now(), all, nil)
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil || len(out.Snapshot.Nodes) != 26 || len(out.Query.Rows) != 5 {
		t.Errorf("kind-only query: %d nodes, %d rows, %v", len(out.Snapshot.Nodes), len(out.Query.Rows), err)
	}
}

func TestLiveBootCarriesRegistryFiltersAndPeople(t *testing.T) {
	v, _ := vectorFixture(t)
	filters := MergeFilters(map[string]string{"mine": "repo:acme/api"}, Fields())
	srv := newServerOpts(t, HandlerOptions{Source: StaticSource{V: v}, Filters: filters})
	_, body := get(t, srv.URL+"/")
	boot := bootOf(t, body)
	if boot["me"] != "alice" || boot["extract"] != ExtractPath {
		t.Errorf("boot me/extract = %v / %v", boot["me"], boot["extract"])
	}
	if ppl, _ := boot["people"].([]any); len(ppl) != 2 {
		t.Errorf("boot people = %v", boot["people"])
	}
	fields, _ := boot["fields"].(map[string]any)
	if fl, _ := fields["fields"].([]any); len(fl) != len(Fields().List()) {
		t.Errorf("boot fields = %v", fields)
	}
	if fs, _ := boot["filters"].([]any); len(fs) != len(builtinFilters)+1 {
		t.Errorf("boot filters = %v", boot["filters"])
	}
	// Before any snapshot there are no people to name.
	srv = newServer(t, emptySource{})
	_, body = get(t, srv.URL+"/")
	if boot := bootOf(t, body); boot["me"] != nil || boot["fields"] == nil {
		t.Errorf("boot before a snapshot = %v", boot)
	}
}

// TestLivePageInlinesAddonsAfterLiveJS: an addon script is inlined after
// live.js, hashed into the CSP, and registers before the page starts.
func TestLivePageInlinesAddonsAfterLiveJS(t *testing.T) {
	probe := "(function () { window.WGOLive.registerView({ id: \"probe\", label: \"Probe\", question: \"Do addon views appear?\", render: function (ctx) { ctx.container.textContent = \"probe view\"; } }); })();"
	srv := newServerOpts(t, HandlerOptions{Source: fixtureDash(t), Addons: []string{"web/live-probe.js"},
		AddonFS: fstest.MapFS{"web/live-probe.js": {Data: []byte(probe)}}})
	resp, body := get(t, srv.URL+"/")
	if resp.StatusCode != 200 {
		t.Fatalf("page: %d", resp.StatusCode)
	}
	live, add := strings.Index(body, "window.WGOLive = L"), strings.Index(body, `id: "probe"`)
	if live < 0 || add < 0 || add < live {
		t.Fatalf("addon at %d, live.js at %d: the addon must follow live.js", add, live)
	}
	checkCSP(t, resp.Header.Get("Content-Security-Policy"), body)
	var logged []string
	if _, err := NewHandler(HandlerOptions{Source: fixtureDash(t), Host: "x", Addons: []string{"web/missing.js"},
		Logf: func(f string, a ...any) { logged = append(logged, f) }}); err != nil || len(logged) != 1 {
		t.Fatalf("a missing addon is a logged render failure (served as a 500), got %v, logged %q", err, logged)
	}
}

func TestExtractRouteIsGETOnly(t *testing.T) {
	v, _ := vectorFixture(t)
	srv := newServer(t, StaticSource{V: v})
	resp, err := http.Post(srv.URL+ExtractPath, "text/plain", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST %s = %d", ExtractPath, resp.StatusCode)
	}
}
