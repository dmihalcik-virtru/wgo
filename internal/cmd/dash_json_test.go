package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/virtru/wgo/internal/dash"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/inventory"
	"github.com/virtru/wgo/internal/jjtest"
)

// dashJSONOut is the part of `wgo dash --json` the test checks.
type dashJSONOut struct {
	Status     string `json:"status"`
	Generation uint64 `json:"generation"`
	Snapshot   struct {
		Schema  int `json:"schema"`
		Days    int `json:"days"`
		Sources map[string]struct {
			State string `json:"state"`
			Fresh int    `json:"fresh"`
		} `json:"sources"`
		Nodes []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"nodes"`
		Counts struct {
			Days []string `json:"days"`
		} `json:"counts"`
	} `json:"snapshot"`
}

func setDashFlags(t *testing.T, days int) {
	t.Helper()
	oj, od, or := dashJSON, dashDays, dashRefresh
	t.Cleanup(func() { dashJSON, dashDays, dashRefresh = oj, od, or })
	dashJSON, dashDays, dashRefresh = true, days, false
}

func runDashJSON(t *testing.T) dashJSONOut {
	t.Helper()
	var buf bytes.Buffer
	if err := runDash(context.Background(), &buf, noDashFlags); err != nil {
		t.Fatalf("runDash: %v\n%s", err, buf.String())
	}
	var out dashJSONOut
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	return out
}

func TestDashJSONEndToEnd(t *testing.T) {
	jjtest.RequireJJ(t)
	jjtest.SetIdentity(t)
	mains := t.TempDir()
	repo := filepath.Join(mains, "acme", "wgo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	mustJJ(t, repo, "git", "init", "--colocate")
	jjtest.Commit(t, repo, "feat: first", map[string]string{"a.txt": "a"})
	jjtest.Bookmark(t, repo, "gh-70-e2e", "@-")
	goldenHome(t, "[discovery]\nbase_dirs = [\""+mains+"\"]\nscan_depth = 3\n")
	setDashFlags(t, 3)

	first := runDashJSON(t)
	if first.Status != "ready" || first.Snapshot.Schema != 1 || first.Snapshot.Days != 3 || len(first.Snapshot.Counts.Days) != 3 {
		t.Fatalf("unexpected snapshot header: %+v", first)
	}
	kinds := map[string]int{}
	for _, n := range first.Snapshot.Nodes {
		kinds[n.Kind]++
	}
	if kinds[string(dash.KindWorkspace)] < 1 || kinds[string(dash.KindBookmark)] < 1 {
		t.Fatalf("node kinds = %v, want a workspace and a bookmark", kinds)
	}
	for _, src := range []string{dash.SourceGitHubPRs, dash.SourceGitHubIssues, dash.SourceJira} {
		if s, ok := first.Snapshot.Sources[src]; ok && s.Fresh > 0 {
			t.Fatalf("%s has %d fresh items with an empty cache and no --refresh", src, s.Fresh)
		}
	}

	second := runDashJSON(t)
	if second.Generation <= first.Generation {
		t.Fatalf("generation %d then %d, want an increase", first.Generation, second.Generation)
	}
}

func TestDashJSONRejectsZeroDays(t *testing.T) {
	goldenHome(t, "")
	setDashFlags(t, 0)
	if err := runDash(context.Background(), &bytes.Buffer{}, noDashFlags); err == nil {
		t.Fatal("--days 0 must be an error")
	}
}

// countingTransport fails and counts every request.
type countingTransport struct{ calls atomic.Int32 }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return nil, errors.New("network access in a test")
}

// inventoryJSONOut is the part of wgo dash --json the inventory tests check.
type inventoryJSONOut struct {
	Snapshot struct {
		Sources map[string]dash.SourceStatus `json:"sources"`
		People  []dash.Person                `json:"people"`
	} `json:"snapshot"`
}

func runDashJSONInto(t *testing.T, out any) error {
	t.Helper()
	var buf bytes.Buffer
	err := runDash(context.Background(), &buf, noDashFlags)
	if jerr := json.Unmarshal(buf.Bytes(), out); jerr != nil {
		t.Fatalf("decode: %v (runDash: %v)\n%s", jerr, err, buf.String())
	}
	return err
}

// TestDashJSONWithoutGitHubCredentials: with no GITHUB_TOKEN and no gh,
// wgo dash --json --refresh still prints a snapshot, github_inventory is
// unknown with the no-credentials reason, and no request is attempted.
func TestDashJSONWithoutGitHubCredentials(t *testing.T) {
	goldenHome(t, "[[dash.people]]\ngithub = \"alice\"\n")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	ct := &countingTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = ct
	t.Cleanup(func() { http.DefaultTransport = old })
	setDashFlags(t, 3)
	dashRefresh = true

	var out inventoryJSONOut
	if err := runDashJSONInto(t, &out); err != nil {
		t.Fatalf("runDash: %v", err)
	}
	st := out.Snapshot.Sources[dash.SourceGitHubInventory]
	if st.State != dash.Unknown || st.Detail != github.ErrNoAuth.Error() || len(st.Parts) != 3 {
		t.Fatalf("github_inventory %+v, want unknown with the no-credentials reason", st)
	}
	if n := ct.calls.Load(); n != 0 {
		t.Fatalf("%d requests attempted without credentials", n)
	}
}

type fakeViewer struct{ login string }

func (fakeViewer) SearchPullRequests(context.Context, string) (*github.PRSearchResult, error) {
	return nil, errors.New("not used")
}

func (f fakeViewer) Viewer(context.Context) (string, error) { return f.login, nil }

// TestDashJSONReadsCachedIdentity: the login the background lookup cached
// makes the authenticated user "me", followed even with no people listed.
func TestDashJSONReadsCachedIdentity(t *testing.T) {
	goldenHome(t, "")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "")
	setDashFlags(t, 3)
	if _, err := inventory.RefreshIdentity(context.Background(), fakeViewer{login: "alice"}); err != nil {
		t.Fatal(err)
	}
	var out inventoryJSONOut
	if err := runDashJSONInto(t, &out); err != nil {
		t.Fatalf("runDash: %v", err)
	}
	want := []dash.Person{{Login: "alice", Jira: dash.JiraCurrentUser, Me: true}}
	if !slices.Equal(out.Snapshot.People, want) {
		t.Fatalf("people %+v, want %+v", out.Snapshot.People, want)
	}
	if st := out.Snapshot.Sources[dash.SourceGitHubInventory]; len(st.Parts) != 3 || st.Parts[0].Name != "alice/open-authored" {
		t.Fatalf("github_inventory parts %+v", st.Parts)
	}
}
