package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/virtru/wgo/internal/dash"
)

// TestDashMDSprintMatchesGoldenAndRoute: wgo dash --md --view sprint prints
// the sprint summary golden files of internal/dash, and byte for byte what
// GET /api/extract?view=sprint serves for the same snapshot, sprint and
// query (spec/gh-105.md).
func TestDashMDSprintMatchesGoldenAndRoute(t *testing.T) {
	saveQueryFlags(t)
	goldenHome(t, "")
	fixture := filepath.Join("..", "dash", "testdata", "analysis_fixture.json")
	v, err := dash.LoadFixture(fixture)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(nil)
	h, err := dash.NewHandler(dash.HandlerOptions{Source: dash.StaticSource{V: v}, Host: srv.Listener.Addr().String(),
		Filters: dash.MergeFilters(nil, dash.Fields())})
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = h
	srv.Start()
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		sprint, query, preset, golden string
	}{
		{golden: "sprint_extract.golden.md"},
		{sprint: "1234", query: "status_category:in_progress,done", golden: "sprint_extract_query.golden.md"},
		{sprint: "1240", query: "kind:ticket;mismatch:*"},
		{sprint: "1234", preset: "jira-out-of-sync"},
	} {
		saveQueryFlags(t)
		dashFixture, dashMD, dashView, dashSprint, dashQuery, dashPreset = fixture, true, "sprint", tc.sprint, tc.query, tc.preset
		passed := []string{"view"}
		if tc.sprint != "" {
			passed = append(passed, "sprint")
		}
		if tc.query != "" {
			passed = append(passed, "query")
		}
		md, err := runDashOut(t, passedFlags(passed...))
		if err != nil {
			t.Fatalf("%+v: %v", tc, err)
		}
		if tc.golden != "" {
			want, err := os.ReadFile(filepath.Join("..", "dash", "testdata", tc.golden))
			if err != nil {
				t.Fatal(err)
			}
			if md != string(want) {
				t.Errorf("%+v: the CLI differs from %s:\n%s", tc, tc.golden, md)
			}
		}
		q := tc.query
		if tc.preset != "" {
			f, _ := dash.BuiltinFilter(tc.preset)
			q = f.Expr
		}
		p := url.Values{"view": {"sprint"}}
		for k, val := range map[string]string{"sprint": tc.sprint, "q": q} {
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
		if resp.StatusCode != 200 || string(body) != md {
			t.Errorf("%+v: %d, CLI and route differ:\n%s\n---\n%s", tc, resp.StatusCode, md, body)
		}
	}
}
