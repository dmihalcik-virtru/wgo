package effort

import (
	"fmt"
	"sort"
	"strings"

	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/spec"
	"github.com/virtru/wgo/internal/store"
)

// WorkspaceInfo represents a workspace for attribution.
type WorkspaceInfo struct {
	Path         string // Absolute path to workspace
	MainRepoPath string // Path to main clone (for worktrees)
	Bookmark     string // Current bookmark (if any)
}

// MergedEffort represents an effort from either state or plan (or both).
type MergedEffort struct {
	ID          string
	Name        string
	Description string
	Branches    []string
	Source      string // "state", "plan", or "both"
}

// AttributionResult contains the attribution results for all workspaces.
type AttributionResult struct {
	// Efforts are the merged efforts attribution ran against, keyed by ID.
	Efforts map[string]MergedEffort
	// Grouped maps effort ID (or "ticket-<id>" for ticket groups) to
	// workspaces in that group.
	Grouped map[string][]WorkspaceInfo
	// Ungrouped contains workspaces that couldn't be attributed
	Ungrouped []WorkspaceInfo
	// Conflicts contains workspaces claimed by multiple efforts
	Conflicts []ConflictInfo
	// Diagnostics contains any warnings or errors during attribution
	Diagnostics []string
}

// ConflictInfo describes a workspace claimed by multiple efforts.
type ConflictInfo struct {
	Workspace WorkspaceInfo
	ClaimedBy []string // Effort IDs
}

// MergeEfforts merges state efforts with parsed plan efforts by ID.
// Plan entries win over state for the same ID. Both state-only and plan-only
// efforts are included. Returns merged efforts and diagnostics for efforts
// whose names collide under different IDs.
func MergeEfforts(stateEfforts map[string]store.Effort, planEfforts map[string]plan.EffortEntry) (map[string]MergedEffort, []string) {
	merged := make(map[string]MergedEffort)
	var diagnostics []string

	for id, effort := range stateEfforts {
		merged[id] = MergedEffort{
			ID:          id,
			Name:        effort.Name,
			Description: effort.Description,
			Branches:    effort.Branches,
			Source:      "state",
		}
	}

	for id, effort := range planEfforts {
		source := "plan"
		if _, exists := merged[id]; exists {
			source = "both"
		}
		merged[id] = MergedEffort{
			ID:          id,
			Name:        effort.Name,
			Description: effort.Description,
			Branches:    effort.Branches,
			Source:      source,
		}
	}

	byName := map[string][]string{}
	for id, e := range merged {
		key := strings.ToLower(strings.TrimSpace(e.Name))
		byName[key] = append(byName[key], id)
	}
	for _, ids := range byName {
		if len(ids) > 1 {
			sort.Strings(ids)
			diagnostics = append(diagnostics, fmt.Sprintf(
				"effort %q is stored under %d IDs (%s); they are grouped separately",
				merged[ids[0]].Name, len(ids), strings.Join(ids, ", ")))
		}
	}
	sort.Strings(diagnostics)

	return merged, diagnostics
}

// AttributeWorkspaces attributes workspaces to efforts using the following logic:
// 1. If themeIDOverride provides a ThemeID for the workspace, use that
// 2. Otherwise, resolve: workspace -> main clone -> current bookmark -> effort
// 3. Normalize legacy "repo:bookmark" to discovered main clone + exact bookmark
// 4. Never match a bookmark name across repos
// 5. Fallback to parsed ticket ID group, then Ungrouped
// 6. Multiple claimants = conflict in Ungrouped
// 7. No-bookmark workspaces stay in Ungrouped
//
// The result is deterministic for the same inputs.
func AttributeWorkspaces(
	workspaces []WorkspaceInfo,
	mainClones []MainCloneInfo,
	efforts map[string]MergedEffort,
	themeIDOverride func(wsPath string) string,
) *AttributionResult {
	result := &AttributionResult{
		Efforts: efforts,
		Grouped: make(map[string][]WorkspaceInfo),
	}

	ids := make([]string, 0, len(efforts))
	for id := range efforts {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	// Reverse index: normalized bookmark -> effort IDs (each at most once).
	bookmarkToEfforts := make(map[string][]string)
	for _, effortID := range ids {
		for _, branch := range efforts[effortID].Branches {
			normalized, diag := normalizeBranchRef(branch, mainClones)
			if diag != "" {
				result.Diagnostics = append(result.Diagnostics,
					fmt.Sprintf("effort %q: %s", efforts[effortID].Name, diag))
			}
			if normalized == "" {
				continue
			}
			claimants := bookmarkToEfforts[normalized]
			if len(claimants) == 0 || claimants[len(claimants)-1] != effortID {
				bookmarkToEfforts[normalized] = append(claimants, effortID)
			}
		}
	}

	for _, ws := range workspaces {
		if themeIDOverride != nil {
			if themeID := themeIDOverride(ws.Path); themeID != "" {
				result.Grouped[themeID] = append(result.Grouped[themeID], ws)
				continue
			}
		}

		if ws.Bookmark == "" {
			result.Ungrouped = append(result.Ungrouped, ws)
			result.Diagnostics = append(result.Diagnostics,
				"workspace "+ws.Path+" has no bookmark")
			continue
		}

		repoPath := ws.MainRepoPath
		if repoPath == "" {
			repoPath = ws.Path
		}
		claimants := bookmarkToEfforts[NormalizeBookmarkRef(repoPath, ws.Bookmark)]

		switch len(claimants) {
		case 0:
			if ticket := spec.ParseTicketFromBranch(ws.Bookmark); ticket != "" {
				key := "ticket-" + strings.ToLower(ticket)
				result.Grouped[key] = append(result.Grouped[key], ws)
				continue
			}
			result.Ungrouped = append(result.Ungrouped, ws)
		case 1:
			result.Grouped[claimants[0]] = append(result.Grouped[claimants[0]], ws)
		default:
			result.Conflicts = append(result.Conflicts, ConflictInfo{
				Workspace: ws,
				ClaimedBy: claimants,
			})
			result.Ungrouped = append(result.Ungrouped, ws)
			result.Diagnostics = append(result.Diagnostics,
				"workspace "+ws.Path+" claimed by multiple efforts: "+strings.Join(claimants, ", "))
		}
	}

	return result
}

// normalizeBranchRef normalizes a branch reference to absolute repo path +
// bookmark. An absolute "/path:bookmark" is returned as-is; any other
// "repo:bookmark" is resolved against mainClones with MatchRepo. Zero or
// several matches return ("", diagnostic) so the reference never matches.
func normalizeBranchRef(ref string, mainClones []MainCloneInfo) (string, string) {
	if strings.HasPrefix(ref, "/") {
		return ref, ""
	}
	clone, bookmark, err := ResolveRef(ref, mainClones)
	if err != nil {
		return "", err.Error()
	}
	return NormalizeBookmarkRef(clone.Path, bookmark), ""
}
