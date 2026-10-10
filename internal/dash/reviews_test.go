package dash

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/review"
)

// writeReviewRun writes a minimal year-in-review run under runsDir. Its PR
// #75 and ticket WGO-12 are the same entities as liveFixture's; its PR #9
// has the same title as live PR #76 but a different URL.
func writeReviewRun(t *testing.T, runsDir, label string, mod time.Time) string {
	t.Helper()
	dir := filepath.Join(runsDir, label)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := strings.Join([]string{
		`{"kind":"pr_authored","url":"https://github.com/virtru/wgo/pull/75","repo":"virtru/wgo","number":75,"title":"feat(dash): snapshot API","state":"merged","created":"2026-02-03T00:00:00Z","tickets":["WGO-12"]}`,
		`{"kind":"pr_authored","url":"https://github.com/virtru/wgo/pull/9","repo":"virtru/wgo","number":9,"title":"feat(dash): live explorer","state":"merged","created":"2026-03-03T00:00:00Z","tickets":[]}`,
		`{"kind":"jira","key":"WGO-12","summary":"Retry manifest fetches","url":"https://example.atlassian.net/browse/WGO-12","status":"Done","created":"2026-01-15T00:00:00Z"}`,
		`{"kind":"jira","key":"WGO-99","summary":"gh-70 dash","url":"https://example.atlassian.net/browse/WGO-99","status":"Done","created":"2026-01-15T00:00:00Z"}`,
		``,
	}, "\n")
	for name, body := range map[string]string{"ledger.jsonl": ledger, "coverage.json": `{"me":"dana"}`} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(dir, name), mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(dir, mod, mod); err != nil {
		t.Fatal(err)
	}
	return dir
}

func scannedIndex(t *testing.T, runsDir string) *ReviewIndex {
	t.Helper()
	ix := NewReviewIndex(runsDir)
	if err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	return ix
}

func newReviewServer(t *testing.T, src ViewSource, ix *ReviewIndex) *http.Client {
	t.Helper()
	srv := newServerOpts(t, HandlerOptions{Source: src, Reviews: ix})
	c := *srv.Client()
	c.Transport = rebase{srv.URL, c.Transport}
	return &c
}

// rebase lets tests write paths ("/review/x/") without the server URL.
type rebase struct {
	base string
	rt   http.RoundTripper
}

func (r rebase) RoundTrip(req *http.Request) (*http.Response, error) {
	u, _ := url.Parse(r.base)
	req.URL.Scheme, req.URL.Host, req.Host = u.Scheme, u.Host, u.Host
	return r.rt.RoundTrip(req)
}

func fetch(t *testing.T, c *http.Client, path string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://dash"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	resp.Body.Close()
	return resp, b.String()
}

func TestValidRunLabel(t *testing.T) {
	for _, ok := range []string{"2026-test", "2025_H2", "run.v2", "a"} {
		if !ValidRunLabel(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", ".hidden", "a/b", "../x", "a\\b", "a..b", "a\x00b", "a\nb", "/etc", strings.Repeat("x", 129)} {
		if ValidRunLabel(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestReviewRoutesServeKnownRunsByteCompatible(t *testing.T) {
	runs := t.TempDir()
	dir := writeReviewRun(t, runs, "2026-test", time.Now().Add(-time.Hour))
	c := newReviewServer(t, fixtureDash(t), scannedIndex(t, runs))

	resp, body := fetch(t, c, "/review/")
	if resp.StatusCode != 200 || !strings.Contains(body, `href="/review/2026-test/"`) || !strings.Contains(body, "<h1>wgo dash</h1>") {
		t.Fatalf("/review/: %d %s", resp.StatusCode, body)
	}

	run, err := review.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	g := review.Build(run)
	var want bytes.Buffer
	enc := json.NewEncoder(&want)
	enc.SetIndent("", " ")
	if err := enc.Encode(g); err != nil {
		t.Fatal(err)
	}
	resp, body = fetch(t, c, "/review/2026-test/graph.json")
	if resp.StatusCode != 200 || body != want.String() {
		t.Fatalf("graph.json differs from `wgo review graph --json` (%d)", resp.StatusCode)
	}

	resp, body = fetch(t, c, "/review/2026-test/")
	if resp.StatusCode != 200 {
		t.Fatalf("page: %d", resp.StatusCode)
	}
	checkCSP(t, resp.Header.Get("Content-Security-Policy"), body)
	boot := bootOf(t, body)
	if boot["served"] != true || boot["run"] != "2026-test" || boot["lookup"] != "/lookup" {
		t.Errorf("boot = %v", boot)
	}
	// The served page is the offline export plus the bootstrap and the
	// served-only add-on, inserted before </body>; nothing else changes.
	offline, err := review.Render(g)
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(body, `<script id="wgo-boot"`)
	if i < 0 || body[:i] != string(offline[:i]) || !strings.HasSuffix(body, string(offline[i:])) {
		t.Error("served page is not the offline page plus additions before </body>")
	}
	if strings.Contains(string(offline), "wgo-forward") || !strings.Contains(body, "wgo-forward") {
		t.Error("forward links must be served-only")
	}

	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, _ = fetch(t, c, "/review/2026-test")
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/review/2026-test/" {
		t.Errorf("redirect: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestReviewRoutesRejectTraversal(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, "runs")
	writeReviewRun(t, runs, "2026-test", time.Now())
	writeReviewRun(t, runs, ".hidden", time.Now())            // a run with an invalid label
	outside := writeReviewRun(t, root, "outside", time.Now()) // a run outside the runs dir
	if err := os.Symlink(outside, filepath.Join(runs, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	ix := scannedIndex(t, runs)
	if got := len(ix.state.Load().runs); got != 1 {
		t.Fatalf("indexed %d runs, want only 2026-test", got)
	}
	c := newReviewServer(t, fixtureDash(t), ix)
	for _, p := range []string{
		"/review/../etc/passwd",
		"/review/../../secret.txt",
		"/review/%2e%2e/secret.txt",
		"/review/%2e%2e/%2e%2e/etc/passwd",
		"/review/..%2F..%2Fetc%2Fpasswd/",
		"/review/..%2F..%2Fsecret.txt/graph.json",
		"/review/..%2Fruns%2F2026-test/graph.json",
		"/review/2026-test%2F..%2F..%2Fsecret.txt/",
		"/review/..%5C..%5Csecret.txt/",
		"/review/.hidden/",
		"/review/.hidden/graph.json",
		"/review/%2ehidden/",
		"/review/a%00b/",
		"/review/%00/graph.json",
		"/review/linked/",
		"/review/linked/graph.json",
		"/review/outside/",
		"/review/2026-test/ledger.jsonl",
		"/review/2026-test/coverage.json",
		"/review/2026-test/../../secret.txt",
		"/review/%252e%252e/secret.txt",
		"/review/%252e%252e/%252e%252e/etc/passwd",
		"/review/%252e%252e%252f%252e%252e%252fsecret.txt/",
		"/review/%252e%252e%252fruns%252f2026-test/graph.json",
		"/review/2026-test/%252e%252e/%252e%252e/secret.txt",
		"/review/" + url.PathEscape(filepath.Join(root, "outside")) + "/",
		"/review/" + url.PathEscape(outside) + "/graph.json",
	} {
		resp, body := fetch(t, c, p)
		if resp.StatusCode == 200 || strings.Contains(body, "TOPSECRET") || strings.Contains(body, "root:") || strings.Contains(body, "pr_authored") {
			t.Errorf("%s: %d served %.80q", p, resp.StatusCode, body)
		}
	}
}

func TestReviewIndexRescansChangesAndRemovals(t *testing.T) {
	runs := t.TempDir()
	writeReviewRun(t, runs, "old", time.Now().Add(-2*time.Hour))
	ix := scannedIndex(t, runs)
	v1 := ix.state.Load().version
	if err := ix.Scan(); err != nil || ix.state.Load().version != v1 {
		t.Fatalf("unchanged rescan bumped the version (%v)", err)
	}
	writeReviewRun(t, runs, "new", time.Now())
	if err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	st := ix.state.Load()
	if st.version == v1 || len(st.runs) != 2 || st.runs[0].label != "new" {
		t.Fatalf("after adding a run: version %d runs %d first %q", st.version, len(st.runs), st.runs[0].label)
	}
	if err := os.RemoveAll(filepath.Join(runs, "new")); err != nil {
		t.Fatal(err)
	}
	if err := ix.Scan(); err != nil || ix.run("new") != nil {
		t.Fatal("removed run still served")
	}
	if err := os.WriteFile(filepath.Join(runs, "broken"), nil, 0o644); err != nil { // a file, not a run
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runs, "empty"), 0o755); err != nil { // no ledger
		t.Fatal(err)
	}
	if err := ix.Scan(); err != nil || len(ix.Errors()) != 1 || ix.run("empty") != nil {
		t.Fatalf("errors = %v", ix.Errors())
	}
	if err := NewReviewIndex(filepath.Join(runs, "missing")).Scan(); err != nil {
		t.Fatalf("missing runs dir: %v", err)
	}
}

func TestReviewRunErrorsAreSurfaced(t *testing.T) {
	runs := t.TempDir()
	writeReviewRun(t, runs, "2026-test", time.Now())
	corrupt := writeReviewRun(t, runs, "corrupt", time.Now())
	if err := os.WriteFile(filepath.Join(corrupt, "coverage.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	ix := scannedIndex(t, runs)
	errs := ix.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0], "review run corrupt") {
		t.Fatalf("errors = %v, want one for the corrupt run", errs)
	}
	if ix.run("corrupt") != nil || ix.run("2026-test") == nil {
		t.Fatal("the corrupt run was served or the good run was dropped")
	}
	c := newReviewServer(t, fixtureDash(t), ix)
	resp, body := fetch(t, c, "/api/review-links")
	var l ReviewLinks
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &l) != nil || len(l.Errors) != 1 || !strings.Contains(l.Errors[0], "corrupt") {
		t.Fatalf("links payload does not carry the scan error: %d %s", resp.StatusCode, body)
	}
	if l.Efforts[effortNodeID("gh-70-dash")].Run != "2026-test" {
		t.Errorf("the good run's links were lost: %s", body)
	}

	// Fixing the run clears the error, and the cached payload follows.
	if err := os.WriteFile(filepath.Join(corrupt, "coverage.json"), []byte(`{"me":"dana"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(corrupt, "coverage.json"), later, later); err != nil {
		t.Fatal(err)
	}
	if err := ix.Scan(); err != nil || len(ix.Errors()) != 0 {
		t.Fatalf("after the fix: %v %v", err, ix.Errors())
	}
	if _, body := fetch(t, c, "/api/review-links"); !strings.Contains(body, `"errors":[]`) {
		t.Errorf("links payload kept a stale error: %s", body)
	}

	// An unreadable runs directory is returned and recorded; loaded runs stay.
	bad := filepath.Join(t.TempDir(), "runs")
	writeReviewRun(t, bad, "2026-test", time.Now())
	ix2 := scannedIndex(t, bad)
	if err := os.RemoveAll(bad); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, nil, 0o644); err != nil { // a file where the directory was
		t.Fatal(err)
	}
	if err := ix2.Scan(); err == nil || len(ix2.Errors()) != 1 || ix2.run("2026-test") == nil {
		t.Fatalf("unreadable runs dir: err %v errors %v", err, ix2.Errors())
	}
	if _, body := fetch(t, newReviewServer(t, fixtureDash(t), ix2), "/api/review-links"); !strings.Contains(body, "review runs in") {
		t.Errorf("links payload lacks the runs-dir error: %s", body)
	}
}

func TestComputeLinksMatchesExactIdentityOnly(t *testing.T) {
	runs := t.TempDir()
	writeReviewRun(t, runs, "older", time.Now().Add(-48*time.Hour))
	writeReviewRun(t, runs, "newer", time.Now().Add(-time.Hour))
	ix := scannedIndex(t, runs)
	s := liveFixture(time.Now())
	links := computeLinks(s, ix.state.Load().runs)

	pr75, pr76 := prNodeID("virtru/wgo", 75), prNodeID("virtru/wgo", 76)
	got, ok := links.Entities[pr75]
	if !ok || got.Run != "newer" || got.URL != "/review/newer/#focus="+url.PathEscape("pr:https://github.com/virtru/wgo/pull/75") {
		t.Errorf("pr #75 link = %+v", got)
	}
	// #76 has the same title as the review run's #9, and the effort's label
	// equals the summary of WGO-99, but neither is the same entity.
	if l, ok := links.Entities[pr76]; ok {
		t.Errorf("similar title produced a link: %+v", l)
	}
	if l := links.Entities[jiraTicketID("WGO-12")]; l.Run != "newer" || l.Matches[0].ReviewNode != "ticket:WGO-12" {
		t.Errorf("ticket link = %+v", l)
	}
	for id := range links.Entities {
		if id != pr75 && id != jiraTicketID("WGO-12") {
			t.Errorf("unexpected entity link for %s", id)
		}
	}
	e1 := links.Efforts[effortNodeID("gh-70-dash")]
	if e1.Run != "newer" || len(e1.Matches) != 1 || e1.Matches[0].Entity != pr75 {
		t.Errorf("effort link = %+v", e1)
	}
	if e2 := links.Efforts[effortNodeID("ticket:WGO-12")]; e2.Run != "newer" || !strings.Contains(e2.URL, "focus=") {
		t.Errorf("ticket effort link = %+v", e2)
	}
	if _, ok := links.Efforts[UngroupedID]; ok {
		t.Error("Ungrouped linked without a match")
	}

	// A PR that only matches by title (no URL) never links.
	s2 := liveFixture(time.Now())
	for i := range s2.Nodes {
		if s2.Nodes[i].PR != nil {
			s2.Nodes[i].PR.URL = ""
		}
		if s2.Nodes[i].Ticket != nil {
			s2.Nodes[i].Ticket.Key, s2.Nodes[i].Ticket.URL = "", ""
		}
	}
	if l := computeLinks(s2, ix.state.Load().runs); len(l.Entities)+len(l.Efforts) != 0 {
		t.Errorf("links without identities: %+v", l)
	}
}

func TestReviewLinksAPI(t *testing.T) {
	runs := t.TempDir()
	writeReviewRun(t, runs, "2026-test", time.Now())
	c := newReviewServer(t, fixtureDash(t), scannedIndex(t, runs))
	_, body := fetch(t, c, "/")
	if bootOf(t, body)["links"] != "/api/review-links" {
		t.Error("live boot lacks the links endpoint")
	}
	resp, body := fetch(t, c, "/api/review-links")
	var l ReviewLinks
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &l) != nil || l.Efforts[effortNodeID("gh-70-dash")].Run != "2026-test" {
		t.Fatalf("links: %d %s", resp.StatusCode, body)
	}
	c2 := newReviewServer(t, emptySource{}, scannedIndex(t, runs))
	if resp, body := fetch(t, c2, "/api/review-links"); resp.StatusCode != 200 || !strings.Contains(body, `"efforts":{}`) {
		t.Fatalf("links before a snapshot: %d %s", resp.StatusCode, body)
	}
}

func TestLookupFocusesActiveEffortOrSaysNotActive(t *testing.T) {
	c := newReviewServer(t, fixtureDash(t), NewReviewIndex(t.TempDir()))
	for entity, focus := range map[string]string{
		"pr:https://github.com/virtru/wgo/pull/75": prNodeID("virtru/wgo", 75),
		"ticket:WGO-12": jiraTicketID("WGO-12"),
		"ticket:https://github.com/virtru/wgo/issues/70": githubTicketID("virtru/wgo", 70),
		"effort:gh-70-dash": "effort:gh-70-dash",
		"agent:claude-3":    "agent:claude-3",
	} {
		resp, body := fetch(t, c, "/lookup?entity="+url.QueryEscape(entity))
		if resp.StatusCode != 200 {
			t.Errorf("%s: %d", entity, resp.StatusCode)
			continue
		}
		boot := bootOf(t, body)
		if boot["focus"] != focus || boot["entity"] != entity {
			t.Errorf("%s: boot %v", entity, boot)
		}
		if !strings.Contains(body, "<title>wgo dash — live work</title>") || !strings.Contains(body, "<h1>wgo dash</h1>") {
			t.Errorf("%s: page lacks the wgo title or heading", entity)
		}
		checkCSP(t, resp.Header.Get("Content-Security-Policy"), body)
	}

	for _, entity := range []string{"pr:https://github.com/virtru/wgo/pull/999", "ticket:WGO-404", "person:alice", "feat(dash): live explorer", "pr:<script>alert(1)</script>"} {
		resp, body := fetch(t, c, "/lookup?entity="+url.QueryEscape(entity))
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "<title>wgo dash — not active</title>") ||
			!strings.Contains(body, "<h1>wgo dash</h1>") || !strings.Contains(body, "is not active") || strings.Contains(body, "<script") {
			t.Errorf("%s: %d %s", entity, resp.StatusCode, body)
		}
		if resp.Header.Get("Content-Security-Policy") != staticCSP || staticCSP != "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'" {
			t.Errorf("%s: CSP %q", entity, resp.Header.Get("Content-Security-Policy"))
		}
	}
	if resp, _ := fetch(t, c, "/lookup"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no entity: %d", resp.StatusCode)
	}
	if resp, _ := fetch(t, c, "/lookup?entity=a%00b"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("control character: %d", resp.StatusCode)
	}
	c2 := newReviewServer(t, emptySource{}, nil)
	if resp, body := fetch(t, c2, "/lookup?entity=effort:x"); resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "<h1>wgo dash</h1>") {
		t.Errorf("before a snapshot: %d", resp.StatusCode)
	}
	if resp, _ := fetch(t, c2, "/review/"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("/review/ without an index: %d", resp.StatusCode)
	}
}

func TestMutatingMethodsAreRejectedOnEveryRoute(t *testing.T) {
	runs := t.TempDir()
	writeReviewRun(t, runs, "2026-test", time.Now())
	c := newReviewServer(t, fixtureDash(t), scannedIndex(t, runs))
	for _, path := range []string{"/", "/api/snapshot", "/api/review-links", "/lookup?entity=effort:gh-70-dash", "/review/", "/review/2026-test/", "/review/2026-test/graph.json"} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			req, err := http.NewRequest(method, "http://dash"+path, strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 405 or 404", method, path, resp.StatusCode)
			}
		}
	}
}
