package effort

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

// Source is the read-only slice of wgo storage Collect needs. Taking this
// instead of a store.Store guarantees Collect cannot write the plan or state.
type Source interface {
	LoadState() (*store.State, error)
	LoadPlan() (string, error)
}

// Collect gathers the discovered workspaces, their main clones and current
// nearest bookmarks, merges state and plan efforts (plan wins) and attributes every
// workspace. The diagnostics include plan-parse, merge and attribution
// warnings. It is read-only: it never writes the plan file or state.
//
// A workspace's bookmark is jj.NearestBookmark: the nearest ancestor
// bookmark, the first of them when several tie, so workspaces on a commit with
// several bookmarks are attributed by whichever jj lists first.
//
// themeID, when non-nil, returns an explicit theme for a workspace path
// (gh-72 agent ThemeIDs); it wins over bookmark membership.
func Collect(jjc jj.Client, repos []discovery.DiscoveredRepo, src Source, themeID func(wsPath string) string) (*AttributionResult, error) {
	state, err := src.LoadState()
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	content, err := src.LoadPlan()
	if err != nil {
		return nil, fmt.Errorf("load plan: %w", err)
	}
	p, err := plan.Parse(content)
	if err != nil {
		return nil, fmt.Errorf("parse plan: %w", err)
	}

	var diags []string
	diags = append(diags, p.Diagnostics...)

	clones, cloneDiags := MainClonesWithDiagnostics(jjc, repos)
	diags = append(diags, cloneDiags...)

	sorted := append([]discovery.DiscoveredRepo(nil), repos...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	var workspaces []WorkspaceInfo
	seen := map[string]bool{}
	for _, r := range sorted {
		path := filepath.Clean(r.Path)
		if seen[path] {
			continue
		}
		seen[path] = true
		ws := WorkspaceInfo{Path: path, MainRepoPath: filepath.Clean(ResolveMainClone(r))}
		if jjc == nil {
			ws.BookmarkErr = true
			diags = append(diags, fmt.Sprintf("workspace %s: no jj client to read its bookmark", path))
		} else if bm, err := jjc.NearestBookmark(path); err != nil {
			ws.BookmarkErr = true
			diags = append(diags, fmt.Sprintf("workspace %s: could not read its bookmark: %v", path, err))
		} else {
			ws.Bookmark = bm
		}
		workspaces = append(workspaces, ws)
	}

	merged, mergeDiags := MergeEfforts(state.Efforts, p.Efforts)
	diags = append(diags, mergeDiags...)

	result := AttributeWorkspaces(workspaces, clones, merged, themeID)
	result.Diagnostics = append(diags, result.Diagnostics...)
	return result, nil
}
