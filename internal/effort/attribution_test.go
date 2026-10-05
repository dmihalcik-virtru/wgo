package effort

import (
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

func TestAttributeWorkspaces_SimpleMatch(t *testing.T) {
	workspaces := []WorkspaceInfo{
		{Path: "/repo1", MainRepoPath: "/repo1", Bookmark: "feature-1"},
		{Path: "/repo2", MainRepoPath: "/repo2", Bookmark: "feature-2"},
	}

	mainClones := []MainCloneInfo{
		{Path: "/repo1", Name: "repo1"},
		{Path: "/repo2", Name: "repo2"},
	}

	efforts := map[string]MergedEffort{
		"effort-1": {
			Name:     "Effort 1",
			Branches: []string{"/repo1:feature-1"},
		},
		"effort-2": {
			Name:     "Effort 2",
			Branches: []string{"/repo2:feature-2"},
		},
	}

	result := AttributeWorkspaces(workspaces, mainClones, efforts, nil)

	if len(result.Grouped["effort-1"]) != 1 {
		t.Errorf("expected 1 workspace in effort-1, got %d", len(result.Grouped["effort-1"]))
	}

	if len(result.Grouped["effort-2"]) != 1 {
		t.Errorf("expected 1 workspace in effort-2, got %d", len(result.Grouped["effort-2"]))
	}

	if len(result.Ungrouped) != 0 {
		t.Errorf("expected 0 ungrouped workspaces, got %d", len(result.Ungrouped))
	}
}

func TestAttributeWorkspaces_NoBookmark(t *testing.T) {
	workspaces := []WorkspaceInfo{
		{Path: "/repo1", MainRepoPath: "/repo1", Bookmark: ""},
	}

	mainClones := []MainCloneInfo{}
	efforts := map[string]MergedEffort{}

	result := AttributeWorkspaces(workspaces, mainClones, efforts, nil)

	if len(result.Ungrouped) != 1 {
		t.Errorf("expected 1 ungrouped workspace, got %d", len(result.Ungrouped))
	}

	if len(result.Diagnostics) != 1 {
		t.Errorf("expected 1 diagnostic, got %d", len(result.Diagnostics))
	}
}

func TestAttributeWorkspaces_TicketFallback(t *testing.T) {
	workspaces := []WorkspaceInfo{
		{Path: "/repo1", MainRepoPath: "/repo1", Bookmark: "WGO-123-feature"},
		{Path: "/repo2", MainRepoPath: "/repo2", Bookmark: "gh-45-bugfix"},
	}

	mainClones := []MainCloneInfo{}
	efforts := map[string]MergedEffort{}

	result := AttributeWorkspaces(workspaces, mainClones, efforts, nil)

	// Should create ticket groups
	if len(result.Grouped["ticket-wgo-123"]) != 1 {
		t.Errorf("expected 1 workspace in ticket-wgo-123, got %d", len(result.Grouped["ticket-wgo-123"]))
	}

	if len(result.Grouped["ticket-gh-45"]) != 1 {
		t.Errorf("expected 1 workspace in ticket-gh-45, got %d", len(result.Grouped["ticket-gh-45"]))
	}

	if len(result.Ungrouped) != 0 {
		t.Errorf("expected 0 ungrouped workspaces, got %d", len(result.Ungrouped))
	}
}

func TestAttributeWorkspaces_Conflict(t *testing.T) {
	workspaces := []WorkspaceInfo{
		{Path: "/repo1", MainRepoPath: "/repo1", Bookmark: "shared-branch"},
	}

	mainClones := []MainCloneInfo{
		{Path: "/repo1", Name: "repo1"},
	}

	efforts := map[string]MergedEffort{
		"effort-1": {
			Name:     "Effort 1",
			Branches: []string{"/repo1:shared-branch"},
		},
		"effort-2": {
			Name:     "Effort 2",
			Branches: []string{"/repo1:shared-branch"},
		},
	}

	result := AttributeWorkspaces(workspaces, mainClones, efforts, nil)

	if len(result.Conflicts) != 1 {
		t.Errorf("expected 1 conflict, got %d", len(result.Conflicts))
	}

	if len(result.Ungrouped) != 1 {
		t.Errorf("expected 1 ungrouped workspace (conflict), got %d", len(result.Ungrouped))
	}

	if len(result.Diagnostics) != 1 {
		t.Errorf("expected 1 diagnostic, got %d", len(result.Diagnostics))
	}
}

func TestAttributeWorkspaces_ThemeOverride(t *testing.T) {
	workspaces := []WorkspaceInfo{
		{Path: "/repo1", MainRepoPath: "/repo1", Bookmark: "feature-1"},
	}

	mainClones := []MainCloneInfo{
		{Path: "/repo1", Name: "repo1"},
	}

	efforts := map[string]MergedEffort{
		"effort-1": {
			Name:     "Effort 1",
			Branches: []string{"/repo1:feature-1"},
		},
	}

	// Override should win over bookmark match
	override := func(wsPath string) string {
		if wsPath == "/repo1" {
			return "custom-theme"
		}
		return ""
	}

	result := AttributeWorkspaces(workspaces, mainClones, efforts, override)

	if len(result.Grouped["custom-theme"]) != 1 {
		t.Errorf("expected 1 workspace in custom-theme, got %d", len(result.Grouped["custom-theme"]))
	}

	if len(result.Grouped["effort-1"]) != 0 {
		t.Errorf("expected 0 workspaces in effort-1 (overridden), got %d", len(result.Grouped["effort-1"]))
	}
}

func TestAttributeWorkspaces_NoCrossRepoMatch(t *testing.T) {
	// Same bookmark name in different repos should not match
	workspaces := []WorkspaceInfo{
		{Path: "/repo1", MainRepoPath: "/repo1", Bookmark: "main"},
		{Path: "/repo2", MainRepoPath: "/repo2", Bookmark: "main"},
	}

	mainClones := []MainCloneInfo{
		{Path: "/repo1", Name: "repo1"},
		{Path: "/repo2", Name: "repo2"},
	}

	efforts := map[string]MergedEffort{
		"effort-1": {
			Name:     "Effort 1",
			Branches: []string{"/repo1:main"}, // Only repo1
		},
	}

	result := AttributeWorkspaces(workspaces, mainClones, efforts, nil)

	if len(result.Grouped["effort-1"]) != 1 {
		t.Errorf("expected 1 workspace in effort-1, got %d", len(result.Grouped["effort-1"]))
	}

	// repo2:main should be ungrouped (not matched across repos)
	if len(result.Ungrouped) != 1 {
		t.Errorf("expected 1 ungrouped workspace, got %d", len(result.Ungrouped))
	}
}

func TestAttributeWorkspaces_LegacyNormalization(t *testing.T) {
	// Test legacy "repo:bookmark" resolution
	workspaces := []WorkspaceInfo{
		{Path: "/home/user/repos/owner/myrepo", MainRepoPath: "/home/user/repos/owner/myrepo", Bookmark: "feature-1"},
	}

	mainClones := []MainCloneInfo{
		{Path: "/home/user/repos/owner/myrepo", Name: "myrepo", Owner: "owner", Repo: "myrepo"},
	}

	efforts := map[string]MergedEffort{
		"effort-1": {
			Name:     "Effort 1",
			Branches: []string{"myrepo:feature-1"}, // Legacy format - directory name
		},
		"effort-2": {
			Name:     "Effort 2",
			Branches: []string{"owner/myrepo:feature-1"}, // Legacy format - owner/repo
		},
	}

	result := AttributeWorkspaces(workspaces, mainClones, efforts, nil)

	// Both efforts should match (conflict)
	if len(result.Conflicts) != 1 {
		t.Errorf("expected 1 conflict (both legacy formats match), got %d", len(result.Conflicts))
	}
}

func TestNormalizeBranchRef_Legacy(t *testing.T) {
	mainClones := []MainCloneInfo{
		{Path: "/home/repos/owner/wgo", Name: "wgo", Owner: "owner", Repo: "wgo"},
	}

	// Test directory name match
	normalized, diag := normalizeBranchRef("wgo:feature-1", mainClones)
	if diag != "" {
		t.Errorf("unexpected diagnostic: %s", diag)
	}
	if normalized != "/home/repos/owner/wgo:feature-1" {
		t.Errorf("expected /home/repos/owner/wgo:feature-1, got %s", normalized)
	}

	// Test owner/repo match
	normalized, diag = normalizeBranchRef("owner/wgo:feature-2", mainClones)
	if diag != "" {
		t.Errorf("unexpected diagnostic: %s", diag)
	}
	if normalized != "/home/repos/owner/wgo:feature-2" {
		t.Errorf("expected /home/repos/owner/wgo:feature-2, got %s", normalized)
	}

	// Test no match
	normalized, diag = normalizeBranchRef("nonexistent:feature", mainClones)
	if diag == "" {
		t.Error("expected diagnostic for non-matching repo")
	}
	if normalized != "" {
		t.Errorf("expected empty normalized ref for no match, got %s", normalized)
	}
}

func TestMergeEfforts(t *testing.T) {
	stateEfforts := map[string]store.Effort{
		"effort-1": {
			Name:        "State Effort 1",
			Description: "From state",
			Branches:    []string{"repo1:branch1"},
		},
		"effort-2": {
			Name:        "State Effort 2",
			Description: "State only",
			Branches:    []string{"repo2:branch2"},
		},
	}

	planEfforts := map[string]plan.EffortEntry{
		"effort-1": {
			ID:          "effort-1",
			Name:        "Plan Effort 1",
			Description: "From plan (should win)",
			Branches:    []string{"repo1:branch1-updated"},
		},
		"effort-3": {
			ID:          "effort-3",
			Name:        "Plan Effort 3",
			Description: "Plan only",
			Branches:    []string{"repo3:branch3"},
		},
	}

	merged, diags := MergeEfforts(stateEfforts, planEfforts)

	if len(diags) != 1 || !strings.Contains(diags[0], "state and plan disagree: only in state [repo1:branch1], only in plan [repo1:branch1-updated]") {
		t.Errorf("expected one divergence diagnostic, got: %v", diags)
	}

	if len(merged) != 3 {
		t.Fatalf("expected 3 merged efforts, got %d", len(merged))
	}

	// effort-1: plan wins
	if merged["effort-1"].Name != "Plan Effort 1" {
		t.Errorf("expected plan to win for effort-1 name")
	}
	if merged["effort-1"].Source != SourceBoth {
		t.Errorf("expected source 'both' for effort-1, got %s", merged["effort-1"].Source)
	}

	// effort-2: state only
	if merged["effort-2"].Source != SourceState {
		t.Errorf("expected source 'state' for effort-2, got %s", merged["effort-2"].Source)
	}

	// effort-3: plan only
	if merged["effort-3"].Source != SourcePlan {
		t.Errorf("expected source 'plan' for effort-3, got %s", merged["effort-3"].Source)
	}
}

func TestAttributeWorkspaces_SameEffortListedTwiceIsNotAConflict(t *testing.T) {
	clones := []MainCloneInfo{{Path: "/m/a/wgo", Name: "wgo", Owner: "a", Repo: "wgo"}}
	ws := []WorkspaceInfo{{Path: "/w/x", MainRepoPath: "/m/a/wgo", Bookmark: "feat"}}
	efforts := map[string]MergedEffort{
		"e": {Name: "E", Branches: []string{"wgo:feat", "/m/a/wgo:feat"}},
	}
	r := AttributeWorkspaces(ws, clones, efforts, nil)
	if len(r.Conflicts) != 0 || len(r.Grouped["e"]) != 1 {
		t.Errorf("expected one grouped workspace and no conflict, got %+v", r)
	}
}

func TestAttributeWorkspaces_Deterministic(t *testing.T) {
	clones := []MainCloneInfo{{Path: "/m/a/wgo", Name: "wgo", Owner: "a", Repo: "wgo"}}
	ws := []WorkspaceInfo{{Path: "/w/x", MainRepoPath: "/m/a/wgo", Bookmark: "feat"}}
	efforts := map[string]MergedEffort{}
	for _, id := range []string{"e5", "e1", "e3", "e2", "e4"} {
		efforts[id] = MergedEffort{Name: id, Branches: []string{"wgo:feat", "missing:x"}}
	}
	first := AttributeWorkspaces(ws, clones, efforts, nil)
	for i := 0; i < 20; i++ {
		got := AttributeWorkspaces(ws, clones, efforts, nil)
		if strings.Join(got.Diagnostics, "|") != strings.Join(first.Diagnostics, "|") ||
			strings.Join(got.Conflicts[0].ClaimedBy, ",") != "e1,e2,e3,e4,e5" {
			t.Fatalf("nondeterministic attribution: %q vs %q", got.Diagnostics, first.Diagnostics)
		}
	}
}

func TestMergeEffortsReportsNameCollision(t *testing.T) {
	st := map[string]store.Effort{"old-id": {Name: "Feature"}}
	pl := map[string]plan.EffortEntry{plan.GenerateEffortID("Feature"): {Name: "Feature"}}
	_, diags := MergeEfforts(st, pl)
	if len(diags) != 1 || !strings.Contains(diags[0], `"Feature" is stored under 2 IDs`) {
		t.Errorf("expected a name-collision diagnostic, got %q", diags)
	}
}

func TestMergeEffortsClonesBranchesAndKeepsStateBranches(t *testing.T) {
	st := map[string]store.Effort{"e": {Name: "E", Branches: []string{"/m/a/wgo:x"}}}
	pl := map[string]plan.EffortEntry{"e": {Name: "E", Branches: []string{"wgo:x"}}}
	merged, _ := MergeEfforts(st, pl)
	merged["e"].Branches[0] = "mutated"
	if pl["e"].Branches[0] != "wgo:x" || st["e"].Branches[0] != "/m/a/wgo:x" {
		t.Error("MergeEfforts aliased its inputs")
	}
	if got := merged["e"].StateBranches; len(got) != 1 || got[0] != "/m/a/wgo:x" {
		t.Errorf("StateBranches = %q", got)
	}
}

func TestAttributeWorkspaces_AbsoluteRefMustNameADiscoveredClone(t *testing.T) {
	clones := []MainCloneInfo{{Path: "/m/a/wgo", Name: "wgo"}}
	ws := []WorkspaceInfo{{Path: "/w/x", MainRepoPath: "/m/a/wgo", Bookmark: "feat"}}
	efforts := map[string]MergedEffort{
		"clean":   {Name: "Clean", Branches: []string{"/m/a/wgo/:feat"}},
		"unknown": {Name: "Unknown", Branches: []string{"/elsewhere/wgo:feat"}},
	}
	r := AttributeWorkspaces(ws, clones, efforts, nil)
	if len(r.Grouped["clean"]) != 1 || len(r.Grouped["unknown"]) != 0 {
		t.Errorf("grouped = %+v", r.Grouped)
	}
	found := false
	for _, d := range r.Diagnostics {
		found = found || (strings.Contains(d, `effort "Unknown"`) && strings.Contains(d, "/elsewhere/wgo"))
	}
	if !found {
		t.Errorf("expected a diagnostic for the unknown absolute path, got %q", r.Diagnostics)
	}
}

func TestSplitRefSharesPlanDefinition(t *testing.T) {
	repo, bm, ok := splitRef("owner/repo:feat")
	if !ok || repo != "owner/repo" || bm != "feat" {
		t.Errorf("got %q %q %v", repo, bm, ok)
	}
	if _, _, ok := splitRef("repo:has space"); ok {
		t.Error("whitespace in a bookmark must be rejected, as the plan parser does")
	}
}

// A second clone with the same directory name makes the plan's short ref
// ambiguous; the state's exact path still attributes the workspace.
func TestAttributeWorkspaces_AmbiguousPlanRefFallsBackToState(t *testing.T) {
	clones := []MainCloneInfo{
		{Path: "/m/fork/wgo", Name: "wgo", Owner: "fork", Repo: "wgo"},
		{Path: "/m/up/wgo", Name: "wgo", Owner: "up", Repo: "wgo"},
	}
	ws := []WorkspaceInfo{{Path: "/w/x", MainRepoPath: "/m/fork/wgo", Bookmark: "feat"}}
	merged, _ := MergeEfforts(
		map[string]store.Effort{"e": {Name: "E", Branches: []string{"/m/fork/wgo:feat"}}},
		map[string]plan.EffortEntry{"e": {Name: "E", Branches: []string{"wgo:feat"}}},
	)
	r := AttributeWorkspaces(ws, clones, merged, nil)
	if len(r.Grouped["e"]) != 1 {
		t.Errorf("expected fallback to the state ref, got grouped=%v diags=%q", r.Grouped, r.Diagnostics)
	}

	// Without a usable state ref the ambiguity is reported and nothing matches.
	only := map[string]MergedEffort{"e": {Name: "E", Branches: []string{"wgo:feat"}}}
	r = AttributeWorkspaces(ws, clones, only, nil)
	if len(r.Grouped["e"]) != 0 || len(r.Diagnostics) == 0 {
		t.Errorf("expected an ambiguity diagnostic, got grouped=%v diags=%q", r.Grouped, r.Diagnostics)
	}
}

func TestAttributeWorkspaces_BookmarkErrSuppressesNoBookmarkDiagnostic(t *testing.T) {
	ws := []WorkspaceInfo{{Path: "/w/x", BookmarkErr: true}, {Path: "/w/y"}}
	r := AttributeWorkspaces(ws, nil, map[string]MergedEffort{}, nil)
	if len(r.Ungrouped) != 2 {
		t.Fatalf("ungrouped = %d", len(r.Ungrouped))
	}
	if len(r.Diagnostics) != 1 || !strings.Contains(r.Diagnostics[0], "/w/y has no bookmark") {
		t.Errorf("diagnostics = %q", r.Diagnostics)
	}
}

func TestAttributeWorkspaces_UnknownThemeIsReportedOnce(t *testing.T) {
	ws := []WorkspaceInfo{{Path: "/w/a"}, {Path: "/w/b"}, {Path: "/w/c"}}
	efforts := map[string]MergedEffort{"known": {Name: "Known"}}
	theme := func(p string) string {
		if p == "/w/c" {
			return "known"
		}
		return "ghost"
	}
	r := AttributeWorkspaces(ws, nil, efforts, theme)
	if len(r.Grouped["ghost"]) != 2 || len(r.Grouped["known"]) != 1 {
		t.Errorf("grouped = %v", r.Grouped)
	}
	if len(r.Diagnostics) != 1 || !strings.Contains(r.Diagnostics[0], `theme "ghost" matches no effort`) {
		t.Errorf("diagnostics = %q", r.Diagnostics)
	}
}
