package dash

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/review"
)

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

var bootRe = regexp.MustCompile(`<script id="wgo-boot" type="application/json">(.*?)</script>`)

func bootOf(t *testing.T, body string) map[string]any {
	t.Helper()
	m := bootRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no bootstrap element in page")
	}
	var boot map[string]any
	if err := json.Unmarshal([]byte(m[1]), &boot); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return boot
}

// checkCSP asserts the policy allows exactly the page's inline scripts and
// styles and nothing that would let injected markup run.
func checkCSP(t *testing.T, csp, body string) {
	t.Helper()
	for _, bad := range []string{"unsafe-inline", "unsafe-eval", "*", "http:", "https:"} {
		if strings.Contains(csp, bad) {
			t.Errorf("CSP contains %q: %s", bad, csp)
		}
	}
	// Exact directive set: a new or loosened directive fails. Script and
	// style sources must be sha256 hashes only (or 'none').
	fixed := map[string]string{
		"default-src":     "'none'",
		"connect-src":     "'self'",
		"img-src":         "'self' data:",
		"base-uri":        "'none'",
		"form-action":     "'none'",
		"frame-ancestors": "'none'",
	}
	got := map[string]string{}
	for _, d := range strings.Split(csp, ";") {
		name, val, _ := strings.Cut(strings.TrimSpace(d), " ")
		if _, dup := got[name]; dup {
			t.Errorf("CSP repeats directive %q: %s", name, csp)
		}
		got[name] = val
	}
	if len(got) != len(fixed)+2 {
		t.Errorf("CSP directives = %v, want exactly %d", got, len(fixed)+2)
	}
	for name, want := range fixed {
		if got[name] != want {
			t.Errorf("CSP %s = %q, want %q: %s", name, got[name], want, csp)
		}
	}
	for _, name := range []string{"script-src", "style-src"} {
		for _, src := range strings.Fields(got[name]) {
			if !(strings.HasPrefix(src, "'sha256-") && strings.HasSuffix(src, "'")) {
				t.Errorf("CSP %s has non-hash source %q: %s", name, src, csp)
			}
		}
		if got[name] == "" {
			t.Errorf("CSP lacks %s: %s", name, csp)
		}
	}
	n := 0
	for _, re := range []*regexp.Regexp{inlineScript, inlineStyle} {
		for _, m := range re.FindAllStringSubmatch(body, -1) {
			sum := sha256.Sum256([]byte(m[1]))
			if h := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"; !strings.Contains(csp, h) {
				t.Errorf("CSP lacks the hash of an inline element (%.40q)", m[1])
			}
			n++
		}
	}
	if n < 3 {
		t.Errorf("found only %d inline scripts/styles", n)
	}
	if strings.Count(csp, "'sha256-") != n {
		t.Errorf("CSP has %d hashes for %d inline elements", strings.Count(csp, "'sha256-"), n)
	}
	// Executable scripts carry no attributes, so every hashed element is
	// accounted for; any other <script ...> must be inert JSON.
	for _, m := range regexp.MustCompile(`<script[^>]+>`).FindAllString(body, -1) {
		if !strings.Contains(m, `type="application/json"`) {
			t.Errorf("unexpected script element %s", m)
		}
	}
}

func TestLivePageServesExplorerWithBootAndCSP(t *testing.T) {
	srv := newServer(t, fixtureDash(t))
	resp, body := get(t, srv.URL+"/")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("page: %d %v", resp.StatusCode, resp.Header)
	}
	for _, want := range []string{"<title>wgo dash — live work</title>", "<h1>wgo dash</h1>", `<div id="app">`, "window.WGOLive", "window.d3force", "/api/snapshot"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// The bootstrap precedes the app so the app can read it as it starts.
	if strings.Index(body, `id="wgo-boot"`) > strings.Index(body, "window.WGOLive") {
		t.Error("bootstrap comes after the live app")
	}
	boot := bootOf(t, body)
	if boot["mode"] != "live" || boot["api"] != "/api/snapshot" || boot["poll_ms"] != float64(5000) || boot["focus"] != nil {
		t.Errorf("boot = %v", boot)
	}
	checkCSP(t, resp.Header.Get("Content-Security-Policy"), body)

	resp, _ = get(t, srv.URL+"/nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("/nope: %d", resp.StatusCode)
	}
}

func TestLivePageRendersBeforeAnySnapshot(t *testing.T) {
	srv := newServer(t, emptySource{})
	resp, body := get(t, srv.URL+"/")
	if resp.StatusCode != 200 || !strings.Contains(body, "Loading the snapshot") {
		t.Fatalf("page before snapshot: %d", resp.StatusCode)
	}
}

func TestLiveAppIssuesOnlyGets(t *testing.T) {
	// Slice 2 is read-only: no POST, no tokens, no launch actions.
	live, err := review.RenderPage(review.Page{Mode: review.ModeLive, Boot: liveBoot{Mode: "live"}})
	if err != nil {
		t.Fatal(err)
	}
	app := string(live)
	app = app[strings.Index(app, "/* wgo dash live explorer"):]
	for _, bad := range []string{"method:", "POST", "XMLHttpRequest", "token", "eval(", "new Function", ".innerHTML", "document.write"} {
		if strings.Contains(app, bad) {
			t.Errorf("live app contains %q", bad)
		}
	}
}

// TestLiveDataContract pins the /api/snapshot fields the live page reads.
// If a field is renamed in the model, the page would silently show it as
// missing; this fails instead.
func TestLiveDataContract(t *testing.T) {
	srv := newServer(t, fixtureDash(t))
	resp, body := get(t, srv.URL+"/api/snapshot")
	if resp.StatusCode != 200 {
		t.Fatalf("%d", resp.StatusCode)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"status", "generation", "age_seconds", "loaded_from_disk", "snapshot", "delta"} {
		if _, ok := p[k]; !ok {
			t.Errorf("payload lacks %q", k)
		}
	}
	if p["status"] != "ready" {
		t.Errorf("status = %v", p["status"])
	}
	snap := p["snapshot"].(map[string]any)
	for _, k := range []string{"nodes", "edges", "counts", "sources", "diagnostics"} {
		if _, ok := snap[k]; !ok {
			t.Errorf("snapshot lacks %q", k)
		}
	}
	counts := snap["counts"].(map[string]any)
	for _, k := range []string{"days", "activity", "pr_states", "agents"} {
		if _, ok := counts[k]; !ok {
			t.Errorf("counts lacks %q", k)
		}
	}
	if d := p["delta"].(map[string]any); d["status"] != "no_previous_look" {
		t.Errorf("delta = %v", d)
	}

	// Every per-kind field the page reads must exist in the served JSON and
	// be referenced by the page, so the two cannot drift apart unnoticed.
	fields := map[string][]string{
		"effort":    {"group", "description", "source", "conflicts", "workspace_id", "claimed_by"},
		"workspace": {"bookmark", "stack", "position", "size", "bookmarks", "description", "last_activity", "annotation", "error", "is_main", "repo_slug", "path", "change_id", "changes"},
		"bookmark":  {"pr_lookup", "pr_count", "pr_fetched_at"},
		"pr":        {"state", "is_draft", "checks", "review_decision", "requested_reviewers", "reviewers_known", "freshness", "url", "title", "updated_at"},
		"ticket":    {"status", "freshness", "url", "title", "system"},
		"agent":     {"tool", "status", "liveness", "branch", "start_time", "last_activity", "conflict", "conflicts_with", "session_id"},
	}
	live, _ := review.RenderPage(review.Page{Mode: review.ModeLive, Boot: liveBoot{Mode: "live"}})
	for kind, keys := range fields {
		var found bool
		for _, n := range snap["nodes"].([]any) {
			node := n.(map[string]any)
			if node["kind"] != kind {
				continue
			}
			found = true
			raw, _ := json.Marshal(node)
			for _, k := range keys {
				if !strings.Contains(string(raw), `"`+k+`"`) {
					continue // omitempty and unset on this node
				}
				if !strings.Contains(string(live), "."+k) && !strings.Contains(string(live), `"`+k+`"`) {
					t.Errorf("%s.%s is served but the live page never reads it", kind, k)
				}
			}
		}
		if !found {
			t.Errorf("fixture has no %s node", kind)
		}
	}
	// The fixture exercises the states the page must label distinctly.
	for _, want := range []string{`"pr_lookup":"unknown"`, `"pr_lookup":"stale"`, `"liveness":"uncertain"`, `"conflict":true`, `"group":"ungrouped"`, `"freshness":"unknown"`} {
		if !strings.Contains(body, want) {
			t.Errorf("fixture snapshot lacks %s", want)
		}
	}
}

func TestLiveFixtureAgeAdvances(t *testing.T) {
	d := fixtureDash(t)
	v := d.Current()
	var a, b strings.Builder
	_ = v.WriteJSON(&a, time.Now())
	_ = v.WriteJSON(&b, time.Now().Add(10*time.Second))
	if a.String() == b.String() {
		t.Error("age_seconds does not advance between reads")
	}
}

func TestLivePageRenderFailureIsLogged(t *testing.T) {
	var logged []string
	s := &server{
		opts: HandlerOptions{Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }},
		err:  errors.New("bootstrap marker missing"),
	}
	rec := httptest.NewRecorder()
	s.serveLive(rec, liveBoot{}, http.StatusOK)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "bootstrap marker missing") {
		t.Fatalf("logged %q, want the render error", logged)
	}
}
