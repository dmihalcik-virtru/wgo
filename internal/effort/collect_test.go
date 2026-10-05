package effort

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/jjtest"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

// TestCollect_JJ runs Collect over real jj repos: two clones carrying the
// same bookmark name, a plan effort claiming one of them, a state-only
// legacy effort claiming the other, and a workspace with no bookmark. It
// also proves Collect leaves the plan and state files untouched.
func TestCollect_JJ(t *testing.T) {
	repo1, jjc := jjtest.NewRepo(t)
	repo2, _ := jjtest.NewRepo(t)
	jjtest.Bookmark(t, repo1, "feature", "@")
	jjtest.Bookmark(t, repo2, "feature", "@")
	bare := jjtest.NewWorkspace(t, repo1, "nobm") // @ sits on root: no bookmark

	dir := t.TempDir()
	s := store.NewWithDir(dir)
	st := &store.State{Efforts: map[string]store.Effort{
		"legacy": {Name: "Legacy", Branches: []string{filepath.Base(repo2) + ":feature"}},
	}}
	if err := s.SaveState(st); err != nil {
		t.Fatal(err)
	}
	planText := "# Plan\n\n## Efforts\n\n### Plan Effort\n\n- " + repo1 + ":feature\n\nstray words\n"
	if err := s.SavePlan(planText); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, dir)

	repos := []discovery.DiscoveredRepo{
		{Path: repo2, Name: filepath.Base(repo2)},
		{Path: bare, Name: "nobm-ws", IsWorktree: true, MainRepoPath: repo1},
		{Path: repo1, Name: filepath.Base(repo1)},
		{Path: repo1, Name: filepath.Base(repo1)}, // duplicates are collapsed
	}
	res, err := Collect(jjc, repos, s, nil)
	if err != nil {
		t.Fatal(err)
	}

	planID := plan.GenerateEffortID("Plan Effort")
	if got := res.Grouped[planID]; len(got) != 1 || got[0].Path != filepath.Clean(repo1) {
		t.Errorf("plan effort should group only repo1, got %+v", got)
	}
	if got := res.Grouped["legacy"]; len(got) != 1 || got[0].Path != filepath.Clean(repo2) {
		t.Errorf("legacy effort should group only repo2, got %+v", got)
	}
	if len(res.Ungrouped) != 1 || res.Ungrouped[0].Path != filepath.Clean(bare) || res.Ungrouped[0].Bookmark != "" {
		t.Errorf("bookmark-less workspace should be ungrouped, got %+v", res.Ungrouped)
	}
	if len(res.Conflicts) != 0 {
		t.Errorf("unexpected conflicts: %+v", res.Conflicts)
	}
	if !strings.Contains(strings.Join(res.Diagnostics, "\n"), "stray") {
		t.Errorf("plan diagnostics should be surfaced, got %q", res.Diagnostics)
	}
	if _, ok := res.Efforts["legacy"]; !ok {
		t.Errorf("merged efforts should include the state-only effort")
	}

	again, err := Collect(jjc, repos, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(again.Diagnostics, "\n") != strings.Join(res.Diagnostics, "\n") {
		t.Errorf("Collect is not deterministic: %q vs %q", again.Diagnostics, res.Diagnostics)
	}
	if after := snapshot(t, dir); !equalSnapshots(before, after) {
		t.Errorf("Collect modified the store directory")
	}
}

func snapshot(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		info, _ := e.Info()
		out[e.Name()] = append(b, []byte(info.ModTime().String())...)
	}
	return out
}

func equalSnapshots(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !bytes.Equal(v, b[k]) {
			return false
		}
	}
	return true
}
