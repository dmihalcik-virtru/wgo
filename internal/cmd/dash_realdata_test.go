package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/dash"
	"github.com/virtru/wgo/internal/github"
)

// realDataPRs is how many open PRs the fake GitHub serves alice: each names
// a Jira ticket in its title and a GitHub issue that does not exist.
const realDataPRs = 120

// fakeGitHubAPI serves the GraphQL and REST calls wgo dash --refresh makes,
// through http.DefaultTransport, without a network.
type fakeGitHubAPI struct {
	mu       sync.Mutex
	attempts map[string]int
	// issueDelay is how long each REST issue lookup takes.
	issueDelay time.Duration
	// noCut serves every page whole; noIssues leaves the issue references
	// out of the PRs' bodies.
	noCut, noIssues bool

	issues, notFound atomic.Int32
}

func (g *fakeGitHubAPI) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

func (g *fakeGitHubAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/graphql" {
		g.serveIssue(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.Unmarshal(body, &req)
	q, _ := req.Variables["q"].(string)
	if q == "" { // query { viewer { login } }
		_, _ = io.WriteString(w, `{"data": {"viewer": {"login": "alice"}}}`)
		return
	}
	page := 1
	if after, _ := req.Variables["after"].(string); after != "" {
		page, _ = strconv.Atoi(after)
		page++
	}
	g.mu.Lock()
	if g.attempts == nil {
		g.attempts = map[string]int{}
	}
	key := q + "#" + strconv.Itoa(page)
	g.attempts[key]++
	attempt := g.attempts[key]
	g.mu.Unlock()
	n := 0
	if strings.Contains(q, "is:open") && strings.Contains(q, "author:alice") {
		n = realDataPRs
	}
	lo, hi := (page-1)*github.PRSearchPageSize, min(page*github.PRSearchPageSize, n)
	nodes := []any{}
	for i := lo + 1; i <= hi; i++ {
		nodes = append(nodes, map[string]any{
			"number": i, "title": fmt.Sprintf("TEAM-%d: change %d", i, i), "url": fmt.Sprintf("https://github.com/acme/api/pull/%d", i),
			"state": "OPEN", "author": map[string]any{"login": "alice"}, "headRefName": fmt.Sprintf("change-%d", i), "baseRefName": "main",
			// gh-N names an issue that does not exist; a bare #N is no ticket.
			"body":                    g.body(i),
			"repository":              map[string]any{"nameWithOwner": "acme/api", "isArchived": false},
			"closingIssuesReferences": map[string]any{"nodes": []any{}},
		})
	}
	data, _ := json.Marshal(map[string]any{"data": map[string]any{"search": map[string]any{
		"issueCount": n,
		"pageInfo":   map[string]any{"hasNextPage": hi < n, "endCursor": strconv.Itoa(page)},
		"nodes":      nodes,
	}}})
	if !g.noCut && page == 3 && attempt == 1 {
		data = data[:len(data)/2] // a body cut off on the way, as seen once for real
	}
	_, _ = w.Write(data)
}

// body is PR i's description: a gh-N reference to an issue that does not
// exist and a bare #N, which is no ticket.
func (g *fakeGitHubAPI) body(i int) string {
	if g.noIssues {
		return "No issue."
	}
	return fmt.Sprintf("Follow-up of gh-%d; see #%d.", 1000+i, 2000+i)
}

func (g *fakeGitHubAPI) serveIssue(w http.ResponseWriter, r *http.Request) {
	g.issues.Add(1)
	time.Sleep(g.issueDelay)
	if !strings.HasPrefix(r.URL.Path, "/repos/acme/api/issues/") {
		http.Error(w, `{"message": "unexpected request"}`, http.StatusTeapot)
		return
	}
	g.notFound.Add(1)
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `{"message": "Not Found", "documentation_url": "https://docs.github.com/rest"}`)
}

// fakeAcliOnPath puts an acli on PATH that behaves as acli 1.3 does with an
// expired login, taking delay per call, and logs each call to the returned
// file.
func fakeAcliOnPath(t *testing.T, delay string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
echo "$*" >> "` + log + `"
/bin/sleep ` + delay + `
case "$*" in
"jira auth status") echo "✗ Error: unauthorized: use 'acli jira auth login' to authenticate"; exit 1 ;;
"jira workitem view"*) echo "✗ Error: failed to fetch work item details"; exit 1 ;;
*) echo "✗ Error: unauthorized: use 'acli [product] auth login' to authenticate"; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "acli"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return log
}

// TestDashRefreshRealDataSituation replays what a real HOME showed: a PR
// inventory naming a hundred-odd Jira tickets, acli's login expired, the PRs'
// GitHub issue references all 404, and a GraphQL page cut off once. A waited
// refresh finishes quickly, says once that the Jira lookups were skipped,
// shows the issues as not found, and exits 0. With a board, its sprint list
// says "unauthorized" and pauses Jira lookups; without one, the first work
// item view fails without saying why and acli's auth status confirms it.
func TestDashRefreshRealDataSituation(t *testing.T) {
	for _, tc := range []struct {
		name, jira string
		// jobs is the Jira lookups skipped: every ticket, and the board.
		jobs int
		// maxViews and authStatus bound the acli calls.
		maxViews, authStatus int
	}{
		{name: "with a board", jira: "[jira]\nboard = 42\nsite = \"jira.example\"\n", jobs: realDataPRs + 1, maxViews: 0, authStatus: 0},
		{name: "without a board", jira: "[jira]\nsite = \"jira.example\"\n", jobs: realDataPRs, maxViews: 4, authStatus: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			goldenHome(t, "[[dash.people]]\ngithub = \"alice\"\n\n[dash]\nticket_prefixes = [\"TEAM\"]\n\n"+tc.jira)
			acliLog := fakeAcliOnPath(t, "0.2")
			t.Setenv("GITHUB_TOKEN", "test-token")
			gh := &fakeGitHubAPI{issueDelay: 5 * time.Millisecond}
			old := http.DefaultTransport
			http.DefaultTransport = gh
			t.Cleanup(func() { http.DefaultTransport = old })
			setDashFlags(t, 3)
			dashRefresh = true

			var buf bytes.Buffer
			start := time.Now()
			err := runDash(context.Background(), &buf, noDashFlags)
			took := time.Since(start)
			if err != nil {
				t.Fatalf("an expired acli login and 404s failed the refresh: %v", err)
			}
			// Most of it is the 1 s back-off before the cut-off page's retry.
			// Without the pause, the acli calls alone would take 6 s.
			if took > 8*time.Second {
				t.Fatalf("wgo dash --json --refresh took %v", took)
			}
			t.Logf("wgo dash --json --refresh took %v", took)

			var out struct {
				Diagnostics []string `json:"diagnostics"`
				Snapshot    struct {
					Sources map[string]dash.SourceStatus `json:"sources"`
					Nodes   []dash.Node                  `json:"nodes"`
				} `json:"snapshot"`
			}
			if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("acli is not authenticated: run `acli jira auth login`; %d Jira lookups skipped", tc.jobs)
			found := false
			for _, d := range out.Diagnostics {
				if strings.Contains(d, "failed") || strings.Contains(d, "not run") {
					t.Errorf("diagnostic %q: nothing failed or ran out of time", d)
				}
				found = found || d == want
			}
			if !found {
				t.Fatalf("diagnostics %q, want %q", out.Diagnostics, want)
			}
			if st := out.Snapshot.Sources[dash.SourceJira]; st.State != dash.Error || st.Detail != "acli is not authenticated: run `acli jira auth login`" {
				t.Fatalf("jira source %+v", st)
			}
			if st := out.Snapshot.Sources[dash.SourceGitHubIssues]; st.State != dash.Fresh || st.NotFound != realDataPRs || st.Fresh != 0 {
				t.Fatalf("github_issues source %+v, want %d not found", st, realDataPRs)
			}
			if st := out.Snapshot.Sources[dash.SourceGitHubInventory]; st.Fresh != 3 {
				t.Fatalf("github_inventory %+v: the cut-off page was not retried", st)
			}

			prs, notFound := 0, 0
			for _, n := range out.Snapshot.Nodes {
				switch {
				case n.PR != nil:
					prs++
				case n.Ticket != nil && n.Ticket.System == "github":
					if !n.Ticket.NotFound || n.Ticket.Status != dash.TicketNotFound || n.Ticket.Error != "" {
						t.Fatalf("issue ticket %+v, want not found", n.Ticket)
					}
					notFound++
				}
			}
			if prs != realDataPRs || notFound != realDataPRs {
				t.Fatalf("%d PRs and %d not-found issues, want %d of each (bare #N is no ticket)", prs, notFound, realDataPRs)
			}
			if n := gh.issues.Load(); n != realDataPRs {
				t.Fatalf("%d issue lookups, want %d", n, realDataPRs)
			}

			calls, _ := os.ReadFile(acliLog)
			if views := strings.Count(string(calls), "workitem view"); views > tc.maxViews || strings.Count(string(calls), "auth status") != tc.authStatus {
				t.Fatalf("acli calls:\n%s\nwant at most %d views and %d auth status", calls, tc.maxViews, tc.authStatus)
			}
		})
	}
}

// hangingAcliOnPath puts an acli on PATH that never answers, writing its
// process ID to the returned file first.
func hangingAcliOnPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	script := "#!/bin/sh\necho $$ >> \"" + pids + "\"\nexec /bin/sleep 600\n"
	if err := os.WriteFile(filepath.Join(dir, "acli"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return pids
}

// TestDashRefreshExitsOnTimeWithHangingAcli: the deadline bounds when wgo
// dash --json --refresh exits, not only when it prints: an acli that hangs
// is killed at the deadline, and the command returns at once after it.
func TestDashRefreshExitsOnTimeWithHangingAcli(t *testing.T) {
	for _, tc := range []struct{ name, jira string }{
		{name: "sprint list hangs", jira: "[jira]\nboard = 42\nsite = \"jira.example\"\n"},
		{name: "ticket views hang", jira: "[jira]\nsite = \"jira.example\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			goldenHome(t, "[[dash.people]]\ngithub = \"alice\"\n\n[dash]\nticket_prefixes = [\"TEAM\"]\n\n"+tc.jira)
			pids := hangingAcliOnPath(t)
			t.Setenv("GITHUB_TOKEN", "test-token")
			old := http.DefaultTransport
			http.DefaultTransport = &fakeGitHubAPI{noCut: true, noIssues: true}
			t.Cleanup(func() { http.DefaultTransport = old })
			setDashFlags(t, 3)
			dashRefresh = true
			ot := dashRefreshTimeout
			t.Cleanup(func() { dashRefreshTimeout = ot })
			dashRefreshTimeout = time.Second

			var buf bytes.Buffer
			start := time.Now()
			err := runDash(context.Background(), &buf, noDashFlags)
			took := time.Since(start)
			if err != nil {
				t.Fatalf("runDash: %v", err)
			}
			t.Logf("returned after %v with a 1s deadline and acli hanging", took)
			if took > 4*time.Second {
				t.Fatalf("returned after %v: the deadline did not bound the exit", took)
			}
			var out struct {
				Diagnostics []string `json:"diagnostics"`
			}
			if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, d := range out.Diagnostics {
				found = found || strings.HasPrefix(d, "not run: refresh deadline (1s) reached; ")
			}
			if !found {
				t.Fatalf("diagnostics %q", out.Diagnostics)
			}
			data, _ := os.ReadFile(pids)
			started := strings.Fields(string(data))
			if len(started) == 0 {
				t.Fatal("acli never ran")
			}
			for _, p := range started {
				pid, _ := strconv.Atoi(p)
				if err := syscall.Kill(pid, 0); err == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
					t.Errorf("acli (pid %d) still runs after wgo dash returned", pid)
				}
			}
		})
	}
}
