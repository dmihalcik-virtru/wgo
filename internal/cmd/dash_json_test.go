package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/virtru/wgo/internal/dash"
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
	if err := runDash(context.Background(), &buf); err != nil {
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
	if err := runDash(context.Background(), &bytes.Buffer{}); err == nil {
		t.Fatal("--days 0 must be an error")
	}
}
