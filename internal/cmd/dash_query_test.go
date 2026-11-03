package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/dash"
)

// vectorFixturePath writes the shared query vectors' snapshot to a file for
// --fixture.
func vectorFixturePath(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "dash", "testdata", "query_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, v.Snapshot, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// saveQueryFlags resets every wgo dash flag runDash reads when the test ends,
// and starts the test from the defaults.
func saveQueryFlags(t *testing.T) {
	t.Helper()
	saveDashFlags(t)
	oq, op, om, oc, og, ol, ov, os2, of, on := dashQuery, dashPreset, dashMD, dashCols, dashGroup, dashLayout, dashView, dashSprint, dashFixture, dashNoOpen
	t.Cleanup(func() {
		dashQuery, dashPreset, dashMD, dashCols, dashGroup, dashLayout, dashView, dashSprint, dashFixture, dashNoOpen = oq, op, om, oc, og, ol, ov, os2, of, on
	})
	dashJSON, dashDays, dashPort, dashRefresh = false, dash.DefaultDays, DefaultDashPort, false
	dashQuery, dashPreset, dashMD, dashCols, dashGroup, dashLayout, dashView, dashSprint, dashFixture, dashNoOpen = "", "", false, "", "", "", "", "", "", false
}

// passedFlags reports the named flags as passed on the command line.
func passedFlags(names ...string) func(string) bool {
	return func(n string) bool {
		for _, x := range names {
			if x == n {
				return true
			}
		}
		return false
	}
}

func runDashOut(t *testing.T, flags func(string) bool) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := runDash(context.Background(), &buf, flags)
	return buf.String(), err
}

// TestDashMDMatchesExtractRoute: wgo dash --md prints byte for byte what GET
// /api/extract serves for the same snapshot and arguments.
func TestDashMDMatchesExtractRoute(t *testing.T) {
	saveQueryFlags(t)
	goldenHome(t, "[dash.filters]\napi-work = \"repo:acme/api\"\n")
	fixture := vectorFixturePath(t)
	v, err := dash.LoadFixture(fixture)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(nil)
	h, err := dash.NewHandler(dash.HandlerOptions{Source: dash.StaticSource{V: v}, Host: srv.Listener.Addr().String(),
		Filters: dash.MergeFilters(map[string]string{"api-work": "repo:acme/api"}, dash.Fields())})
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = h
	srv.Start()
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		query, cols, group, layout string
		preset                     string
	}{
		{query: "kind:pr;prstate:open,draft", cols: "checks,review,repo"},
		{query: "repo:acme/api", cols: "kind,freshness", group: "repo", layout: "table"},
		{preset: "api-work", group: "none"},
		{},
	} {
		dashFixture, dashMD = fixture, true
		dashQuery, dashPreset, dashCols, dashGroup, dashLayout = tc.query, tc.preset, tc.cols, tc.group, tc.layout
		var passed []string
		if tc.query != "" {
			passed = append(passed, "query")
		}
		for name, val := range map[string]string{"cols": tc.cols, "group": tc.group, "layout": tc.layout} {
			if val != "" {
				passed = append(passed, name)
			}
		}
		md, err := runDashOut(t, passedFlags(passed...))
		if err != nil {
			t.Fatalf("%+v: %v", tc, err)
		}
		q := tc.query
		if tc.preset != "" {
			q = "repo:acme/api"
		}
		p := url.Values{}
		for k, val := range map[string]string{"q": q, "cols": tc.cols, "group": tc.group, "layout": tc.layout} {
			if val != "" {
				p.Set(k, val)
			}
		}
		resp, err := http.Get(srv.URL + dash.ExtractPath + "?" + p.Encode())
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != md {
			t.Errorf("%+v: wgo dash --md and /api/extract differ:\n%s\n---\n%s", tc, md, body)
		}
		if tc.preset != "" && !strings.HasPrefix(md, "## api-work\n") {
			t.Errorf("a preset's name heads the extract:\n%s", md)
		}
	}
}

func TestDashJSONQueryAddsMatchesAndLimitsTheSnapshot(t *testing.T) {
	saveQueryFlags(t)
	goldenHome(t, "")
	dashFixture, dashJSON = vectorFixturePath(t), true

	plain, err := runDashOut(t, noDashFlags)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, `"query"`) {
		t.Error("--json without --query must be unchanged")
	}
	dashQuery = "repo:acme/api;kind:ticket"
	out, err := runDashOut(t, passedFlags("query"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Snapshot struct {
			Nodes []struct{ ID string } `json:"nodes"`
		} `json:"snapshot"`
		Query struct {
			Expr    string   `json:"expr"`
			Matches []string `json:"matches"`
			Rows    []string `json:"rows"`
			Filters []struct {
				Name, Reason string
				Hidden       bool
			} `json:"filters"`
		} `json:"query"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Query.Expr != dashQuery || len(got.Query.Matches) != 9 || len(got.Query.Rows) != 2 {
		t.Fatalf("query = %+v", got.Query)
	}
	if n := len(got.Snapshot.Nodes); n == 0 || n >= 24 {
		t.Errorf("snapshot has %d nodes; it should keep only the visible set", n)
	}
	if len(got.Query.Filters) == 0 || !got.Query.Filters[0].Hidden || got.Query.Filters[0].Reason == "" {
		t.Errorf("--json reports why built-in filters are hidden: %+v", got.Query.Filters)
	}
}

func TestDashQueryErrorsSuggestACommand(t *testing.T) {
	saveQueryFlags(t)
	goldenHome(t, "")
	dashFixture, dashMD, dashQuery = vectorFixturePath(t), true, "kind:pr;prstat:open"
	_, err := runDashOut(t, passedFlags("query"))
	var qe *dashQueryError
	if !errors.As(err, &qe) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"unknown field \"prstat\" at column 9; did you mean prstate?", "  kind:pr;prstat:open\n          ^", "Try: wgo dash --md --query 'kind:pr;prstate:open'"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%s", want, err)
		}
	}
	dashQuery = "prstate:1..3"
	if _, err := runDashOut(t, passedFlags("query")); err == nil || !strings.Contains(err.Error(), "Edit the query at column 9 as above and rerun: wgo dash --md --query 'prstate:1..3'") {
		t.Errorf("unfixable error = %v", err)
	}
	dashMD, dashJSON, dashQuery = false, true, "it's:x"
	if _, err := runDashOut(t, passedFlags("query")); err == nil || !strings.Contains(err.Error(), `wgo dash --json --query 'it'\''s:x'`) {
		t.Errorf("shell quoting: %v", err)
	}
}

func TestDashQueryFlagCombinations(t *testing.T) {
	saveQueryFlags(t)
	goldenHome(t, "[dash.filters]\nweb = \"repo:acme/web\"\nbroken = \"prstat:x\"\n")
	fixture := vectorFixturePath(t)
	for _, tc := range []struct {
		name  string
		set   func()
		flags []string
		want  string
	}{
		{"both query and preset", func() { dashQuery, dashPreset = "kind:pr", "web" }, []string{"query"}, "give --query or --preset, not both"},
		{"cols without --md", func() { dashJSON, dashCols = true, "repo" }, []string{"cols"}, "--cols only applies with --md"},
		{"json and md", func() { dashJSON, dashMD = true, true }, nil, "--json and --md are alternatives"},
		{"sprint view", func() { dashMD, dashView = true, "sprint" }, []string{"view"}, "the sprint view is not available yet: it arrives with gh-105"},
		{"sprint id", func() { dashMD, dashSprint = true, "42" }, []string{"sprint"}, "not available yet"},
		{"unknown preset", func() { dashMD, dashPreset = true, "nope" }, nil, `no saved filter named "nope" (available: web)`},
		{"hidden built-in", func() { dashMD, dashPreset = true, "my-move" }, nil, "saved filter my-move cannot run: needs fields not registered yet: move_people (arrives with gh-99)"},
		{"broken config filter", func() { dashMD, dashPreset = true, "broken" }, nil, "[dash.filters] broken in ~/.wgo/config.toml does not parse"},
		{"bad fixture", func() { dashJSON, dashFixture = true, filepath.Join(t.TempDir(), "none.json") }, nil, "wgo dash --fixture:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saveQueryFlags(t)
			dashFixture = fixture
			tc.set()
			_, err := runDashOut(t, passedFlags(tc.flags...))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	// A configured filter runs as a preset and overrides nothing it should not.
	saveQueryFlags(t)
	dashFixture, dashMD, dashPreset = fixture, true, "web"
	md, err := runDashOut(t, noDashFlags)
	if err != nil || !strings.HasPrefix(md, "## web\n") || !strings.Contains(md, "Query: `repo:acme/web`") {
		t.Fatalf("preset web: %v\n%s", err, md)
	}
}

// TestDashFiltersConfigNeverBreaksCommands: a dotted filter name and a
// non-string value in [dash.filters] decode, and only that filter is hidden.
func TestDashFiltersConfigNeverBreaksCommands(t *testing.T) {
	saveQueryFlags(t)
	goldenHome(t, "[dash.filters]\n\"release.v2\" = \"kind:pr;prstate:open\"\ncount = 5\n")
	fixture := vectorFixturePath(t)
	dashFixture, dashMD, dashPreset = fixture, true, "release.v2"
	md, err := runDashOut(t, noDashFlags)
	if err != nil || !strings.HasPrefix(md, "## release.v2\n") {
		t.Fatalf("dotted preset: %v\n%s", err, md)
	}
	dashPreset = "count"
	if _, err := runDashOut(t, noDashFlags); err == nil || !strings.Contains(err.Error(), "saved filter count cannot run: [dash.filters] count in ~/.wgo/config.toml is 5, not a query string") {
		t.Fatalf("non-string preset: %v", err)
	}
}

// TestDashQueryAloneServesAtTheQuery: wgo dash --query EXPR serves (here a
// fixture, read-only) and opens the browser at #q=EXPR.
func TestDashQueryAloneServesAtTheQuery(t *testing.T) {
	saveQueryFlags(t)
	goldenHome(t, "")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opened := make(chan string, 1)
	old := openInBrowser
	openInBrowser = func(u string) error { opened <- u; return nil }
	t.Cleanup(func() { openInBrowser = old })
	dashFixture, dashPort, dashQuery = vectorFixturePath(t), port, "kind:pr;text:\"a&b=c d\""
	done := make(chan error, 1)
	var out syncBuffer
	go func() { done <- runDash(ctx, &out, passedFlags("query", "port")) }()
	var u string
	select {
	case u = <-opened:
	case err := <-done:
		t.Fatalf("runDash returned early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the browser was not opened")
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port) + "/"
	if want := base + "#q=kind:pr;text:%22a%26b%3Dc%20d%22"; u != want {
		t.Errorf("opened %s, want %s", u, want)
	}
	// The fixture is served read-only: no actions, and /api/extract works.
	resp, err := http.Get(base + "api/extract?q=kind:pr")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "5 of 24 nodes") {
		t.Errorf("extract from the served fixture: %d %s", resp.StatusCode, body)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "read-only, a fixed snapshot") {
		t.Errorf("output = %q", out.String())
	}
}

// TestDashMDCollectsAndPrints: wgo dash --md (and --md --refresh) without
// --fixture collects a snapshot and prints Markdown; it never serves. The
// port is held busy, so an attempt to listen would fail the command.
func TestDashMDCollectsAndPrints(t *testing.T) {
	saveQueryFlags(t)
	goldenHome(t, "[discovery]\nbase_dirs = [\""+t.TempDir()+"\"]\n")
	// No GitHub token, gh or acli: --refresh has no lookup that could fail
	// (otherwise it would look up the GitHub identity, gh-97).
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	dashPort = ln.Addr().(*net.TCPAddr).Port
	for _, refresh := range []bool{false, true} {
		dashMD, dashRefresh, dashQuery = true, refresh, "kind:workspace"
		done := make(chan struct{})
		var out string
		go func() {
			defer close(done)
			out, err = runDashOut(t, passedFlags("query", "port"))
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatalf("--md (refresh=%v) did not return: it is serving", refresh)
		}
		if err != nil {
			t.Fatalf("--md (refresh=%v): %v", refresh, err)
		}
		if !strings.HasPrefix(out, "## kind:workspace\n") || !strings.Contains(out, "Query: `kind:workspace`") || strings.Contains(out, "serving") {
			t.Errorf("--md (refresh=%v) printed:\n%s", refresh, out)
		}
	}
}

func TestHashEscapeKeepsQueriesReadable(t *testing.T) {
	if got := hashEscape(`kind:pr;!text:"a b",c/d@e&f=g#h%`); got != "kind:pr;!text:%22a%20b%22,c/d@e%26f%3Dg%23h%25" {
		t.Errorf("hashEscape = %s", got)
	}
}
