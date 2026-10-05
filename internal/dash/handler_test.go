package dash

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

// largeDash publishes a snapshot with 100 workspaces and 300 PR, ticket and
// agent nodes.
func largeDash(t testing.TB) *Dash {
	t.Helper()
	d, err := Open(Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := &Snapshot{Schema: SchemaVersion, GeneratedAt: time.Now(), Days: DefaultDays, Sources: map[string]SourceStatus{}}
	for i := 0; i < 100; i++ {
		ws := WorkspaceID("/src/repo", fmt.Sprintf("/src/ws-%d", i))
		var changes []string
		for c := 0; c < 20; c++ {
			changes = append(changes, fmt.Sprintf("%032d", i*100+c))
		}
		bm := fmt.Sprintf("bm-%d", i)
		s.Nodes = append(s.Nodes,
			Node{ID: ws, Kind: KindWorkspace, Label: fmt.Sprintf("repo/ws-%d", i), Workspace: &WorkspaceInfo{
				Slug: fmt.Sprintf("ws-%d", i), Repo: "repo", Path: fmt.Sprintf("/src/ws-%d", i), Bookmark: bm, Changes: changes,
				EffortID: UngroupedID, Stack: &StackPosition{Position: 1, Size: 1, Bookmarks: []string{bm}}}},
			Node{ID: bm, Kind: KindBookmark, Label: bm, Bookmark: &BookmarkInfo{Name: bm, PRLookup: Fresh, PRCount: 1}},
			Node{ID: prNodeID("acme/repo", i), Kind: KindPR, Label: fmt.Sprintf("#%d", i), PR: &PRInfo{
				Number: i, Repo: "acme/repo", State: "open", Title: strings.Repeat("t", 60), ReviewersKnown: true,
				RequestedReviewers: []string{"a", "b"}, Freshness: Fresh, BookmarkID: bm}},
			Node{ID: jiraTicketID(fmt.Sprintf("WGO-%d", i)), Kind: KindTicket, Label: "t", Ticket: &TicketInfo{Key: "k", System: "jira", Status: "In Progress", Freshness: Fresh}},
			Node{ID: agentNodeID(fmt.Sprintf("claude-%012d", i)), Kind: KindAgent, Label: "claude", Agent: &AgentInfo{SessionID: "x", Tool: "claude", Liveness: "active", WorkspaceID: ws, EffortID: UngroupedID}},
		)
		s.Edges = append(s.Edges, Edge{UngroupedID, ws, EdgeContains}, Edge{ws, bm, EdgeOn})
	}
	if err := d.Publish(s); err != nil {
		t.Fatal(err)
	}
	return d
}

// newServer starts src behind Handler bound to the server's own address.
func newServer(t testing.TB, src ViewSource) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	srv.Config.Handler = Handler(src, srv.Listener.Addr().String())
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func TestSnapshotHandlerP95(t *testing.T) {
	d := largeDash(t)
	srv := newServer(t, d)
	client := srv.Client()
	const n = 200
	var lat []time.Duration
	for i := 0; i < n; i++ {
		start := time.Now()
		resp, err := client.Get(srv.URL + "/api/snapshot")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %v", resp.StatusCode, err)
		}
		lat = append(lat, time.Since(start))
		if i == 0 {
			var out struct {
				Status     string    `json:"status"`
				Generation uint64    `json:"generation"`
				Snapshot   *Snapshot `json:"snapshot"`
				Delta      *Delta    `json:"delta"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			if out.Status != "ready" || out.Generation != 1 || len(out.Snapshot.Nodes) != 500 || out.Delta.Status != DeltaNoPreviousLook {
				t.Fatalf("body: status=%s gen=%d nodes=%d", out.Status, out.Generation, len(out.Snapshot.Nodes))
			}
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p95 := lat[n*95/100-1]
	t.Logf("GET /api/snapshot over %d requests: p50=%v p95=%v max=%v", n, lat[n/2], p95, lat[n-1])
	if p95 >= 100*time.Millisecond {
		t.Fatalf("p95 = %v, want < 100ms", p95)
	}
}

func BenchmarkSnapshotHandler(b *testing.B) {
	d := largeDash(b)
	srv := newServer(b, d)
	client := srv.Client()
	for b.Loop() {
		resp, err := client.Get(srv.URL + "/api/snapshot")
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

type emptySource struct{}

func (emptySource) Current() *View { return nil }

func TestHandlerEmptyStateAndPage(t *testing.T) {
	srv := newServer(t, emptySource{})
	resp, err := srv.Client().Get(srv.URL + "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"loading"`) {
		t.Fatalf("empty state: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %v", resp.Header)
	}
	resp, err = srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Security-Policy") != "default-src 'none'" {
		t.Fatalf("page: %d %v", resp.StatusCode, resp.Header)
	}
	resp, err = srv.Client().Post(srv.URL+"/api/snapshot", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", resp.StatusCode)
	}
}

func TestHandlerRejectsForeignHost(t *testing.T) {
	srv := newServer(t, largeDash(t))
	_, port, _ := strings.Cut(srv.Listener.Addr().String(), ":")
	for _, host := range []string{"evil.example", "evil.example:" + port, "localhost:" + port, "127.0.0.1", "127.0.0.1:1", "[::1]:" + port} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/snapshot", nil)
		req.Host = host
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || strings.Contains(string(body), "snapshot") {
			t.Fatalf("Host %q: status %d", host, resp.StatusCode)
		}
	}
}
