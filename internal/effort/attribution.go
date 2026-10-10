package effort

import (
	"fmt"
	"slices"
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
	Bookmark     string // Nearest bookmark (if any)
	// BookmarkErr is set when reading the bookmark failed, so attribution does
	// not also report the workspace as having none.
	BookmarkErr bool
}

// EffortSource says where a merged effort came from.
type EffortSource string

// Effort sources.
const (
	SourceState EffortSource = "state"
	SourcePlan  EffortSource = "plan"
	SourceBoth  EffortSource = "both"
)

// MergedEffort represents an effort from either state or plan (or both).
type MergedEffort struct {
	ID          string
	Name        string
	Description string
	Branches    []string
	Source      EffortSource
	// StateBranches are the raw state references of an effort present in both
	// state and the plan (Branches then holds the plan's). Attribution falls
	// back to them when a plan reference became ambiguous after a second
	// clone with the same directory name was discovered.
	StateBranches []string
}

// AttributionResult contains the attribution results for all workspaces.
type AttributionResult struct {
	// Efforts are the merged efforts attribution ran against, keyed by ID.
	Efforts map[string]MergedEffort
	// Grouped maps a group key to its workspaces. The key is an effort ID, a
	// lowercased "ticket-<ticket>" for a ticket group, or a ThemeID returned
	// by the override. Only effort IDs are keys of Efforts.
	Grouped map[string][]WorkspaceInfo
	// Ungrouped contains workspaces that couldn't be attributed, including
	// those listed in Conflicts.
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
// A plan entry replaces the whole state entry for the same ID, Branches
// included; branches only state knows are not unioned in (they stay in
// StateBranches). State-only and plan-only efforts are included. Returns the
// merged efforts and diagnostics for efforts whose names collide under
// different IDs and for efforts whose state and plan branches disagree.
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
		m := MergedEffort{
			ID:          id,
			Name:        effort.Name,
			Description: effort.Description,
			Branches:    slices.Clone(effort.Branches),
			Source:      SourcePlan,
		}
		if prev, exists := merged[id]; exists {
			m.Source = SourceBoth
			m.StateBranches = prev.Branches
			if onlyState, onlyPlan := setDiff(prev.Branches, m.Branches), setDiff(m.Branches, prev.Branches); len(onlyState)+len(onlyPlan) > 0 {
				diagnostics = append(diagnostics, fmt.Sprintf(
					"effort %q: state and plan disagree: only in state %v, only in plan %v; the plan wins",
					m.Name, onlyState, onlyPlan))
			}
		}
		merged[id] = m
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

// setDiff returns the elements of a that are not in b, in a's order.
func setDiff(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
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
				if alt, ok := resolveViaState(branch, efforts[effortID].StateBranches, mainClones); ok {
					normalized, diag = alt, ""
				}
			}
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

	unknownThemes := map[string]bool{}
	for _, ws := range workspaces {
		if themeIDOverride != nil {
			if themeID := themeIDOverride(ws.Path); themeID != "" {
				if _, ok := efforts[themeID]; !ok && !unknownThemes[themeID] {
					unknownThemes[themeID] = true
					result.Diagnostics = append(result.Diagnostics,
						fmt.Sprintf("theme %q matches no effort; workspace %s is grouped under it anyway", themeID, ws.Path))
				}
				result.Grouped[themeID] = append(result.Grouped[themeID], ws)
				continue
			}
		}

		if ws.Bookmark == "" {
			result.Ungrouped = append(result.Ungrouped, ws)
			if !ws.BookmarkErr {
				result.Diagnostics = append(result.Diagnostics,
					"workspace "+ws.Path+" has no bookmark")
			}
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
// bookmark. Every reference, absolute "/path:bookmark" included, is resolved
// against mainClones with ResolveRef. Zero or several matches return
// ("", diagnostic) so the reference never matches.
func normalizeBranchRef(ref string, mainClones []MainCloneInfo) (string, string) {
	clone, bookmark, err := ResolveRef(ref, mainClones)
	if err != nil {
		return "", err.Error()
	}
	return NormalizeBookmarkRef(clone.Path, bookmark), ""
}

// resolveViaState retries a plan reference that did not resolve (typically
// because a second clone with the same directory name made it ambiguous)
// against the effort's state references. A state reference is used when it
// names the same bookmark and resolves to exactly one of the clones the plan
// reference matched.
func resolveViaState(planRef string, stateRefs []string, mainClones []MainCloneInfo) (string, bool) {
	repoRef, bookmark, ok := splitRef(planRef)
	if !ok {
		return "", false
	}
	candidates := MatchRepo(repoRef, mainClones)
	if len(candidates) < 2 {
		return "", false
	}
	var found string
	for _, sr := range stateRefs {
		clone, bm, err := ResolveRef(sr, mainClones)
		if err != nil || bm != bookmark {
			continue
		}
		if !slices.ContainsFunc(candidates, func(c MainCloneInfo) bool { return c.Path == clone.Path }) {
			continue
		}
		n := NormalizeBookmarkRef(clone.Path, bm)
		if found != "" && found != n {
			return "", false
		}
		found = n
	}
	return found, found != ""
}
