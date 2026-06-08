package stack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/store"
)

// GitOps is the subset of git operations the restack algorithm needs.
// Defined here so tests can mock without satisfying the full git.Client surface.
type GitOps interface {
	Fetch(repoPath string) error
	IsClean(worktreePath string) (bool, []string, error)
	ListWorktrees(repoPath string) ([]git.WorktreeInfo, error)
	ResolveRef(repoPath, ref string) (string, error)
	Rebase(worktreePath, ontoRef string) error
	RebaseOnto(worktreePath, newBase, upstream string) error
	Merge(worktreePath, ref string, noFF bool) error
	PushForceWithLease(repoPath string, refs []git.ForceLeaseRef) error
	HasActiveRebase(worktreePath string) (bool, error)
	RebaseContinue(worktreePath string) error
}

// GitHubOps is the subset of GitHub operations the restack algorithm needs.
type GitHubOps interface {
	Available() bool
	GetPRStatus(repoPath, branch string) (*github.PRInfo, error)
	GetPRBody(repoPath string, prNumber int) (string, error)
	UpdatePRBody(repoPath string, prNumber int, body string) error
	UpdatePRBase(repoPath string, prNumber int, baseBranch string) error
}

// Checkpoint is the on-disk state of an in-flight restack. Persisted so a
// conflict in the middle of the walk can be resolved manually and then
// resumed with `wgo stack restack --continue`.
type Checkpoint struct {
	StackID      string            `json:"stack_id"`
	StartedAt    time.Time         `json:"started_at"`
	TopoOrder    []string          `json:"topo_order"`    // annotation keys, dependency order
	CurrentIndex int               `json:"current_index"` // index into TopoOrder of the node to rebase next
	Leases       map[string]string `json:"leases"`        // node key -> remote OID captured before any rewrite
	// PreRebaseTips maps every node key (in TopoOrder plus startFrom) to its
	// local branch OID captured before the restack run began. Used as the
	// --onto upstream so each child only replays its own commits.
	PreRebaseTips map[string]string `json:"pre_rebase_tips,omitempty"`
	// MergedParents is the set of annotation keys whose GitHub PRs were already
	// merged when this restack began. For these, the rebase target is DefaultBase.
	MergedParents map[string]bool `json:"merged_parents,omitempty"`
	// ClosedNodes is the set of annotation keys whose GitHub PRs were closed
	// without merging. These nodes are skipped from rebasing; their children
	// rebase onto the nearest open ancestor (or DefaultBase if none found).
	ClosedNodes map[string]bool `json:"closed_nodes,omitempty"`
	// DefaultBase is the resolved remote default-branch ref (e.g. "origin/main")
	// used as the rebase target for nodes whose parent has merged or been closed.
	DefaultBase string `json:"default_base,omitempty"`
}

// CheckpointPath returns where the checkpoint for a stack lives on disk.
// Lives under ~/.wgo/cache so it sits alongside other transient state.
func CheckpointPath(wgoBaseDir, stackID string) string {
	return filepath.Join(wgoBaseDir, "cache", "restack-"+stackID+".json")
}

// SaveCheckpoint atomically writes the checkpoint to disk.
func SaveCheckpoint(wgoBaseDir string, cp *Checkpoint) error {
	path := CheckpointPath(wgoBaseDir, cp.StackID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create checkpoint dir: %w", err)
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	return os.Rename(tmp, path)
}

// LoadCheckpoint reads a checkpoint from disk; returns (nil, nil) when absent.
func LoadCheckpoint(wgoBaseDir, stackID string) (*Checkpoint, error) {
	data, err := os.ReadFile(CheckpointPath(wgoBaseDir, stackID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read checkpoint: %w", err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("parse checkpoint: %w", err)
	}
	return &cp, nil
}

// DeleteCheckpoint removes the checkpoint file. Idempotent.
func DeleteCheckpoint(wgoBaseDir, stackID string) error {
	err := os.Remove(CheckpointPath(wgoBaseDir, stackID))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Options configures a Restack call.
type Options struct {
	WgoBaseDir string // ~/.wgo, used for the checkpoint file
	StackID    string
	// StartFrom is the annotation key whose movement triggered the restack.
	// Only descendants of this node are rebased. Ignored on resume.
	StartFrom string
	// Continue resumes from a previously saved checkpoint instead of computing
	// a fresh topo order. The checkpoint must exist.
	Continue bool
	// DryRun computes the plan and prints it but performs no mutations.
	DryRun bool
	// DefaultBase is the remote default-branch ref (e.g. "origin/main") used
	// as the rebase target for nodes whose direct parent has merged to main.
	// Set by the caller via defaultBranchRef(repoPath). If empty, falls back
	// to "origin/main".
	DefaultBase string
}

// Result is what Restack returns to the caller for display.
type Result struct {
	StackID         string
	PlannedNodes    []string // nodes that would be / were rebased, in order
	Completed       []string // nodes successfully rebased and pushed
	PushedRefs      []git.ForceLeaseRef
	RebaseConflicts []ConflictReport // non-empty on conflict; restack halts and writes checkpoint
}

// ConflictReport surfaces enough info for the user to resolve a conflict.
type ConflictReport struct {
	Node          string // annotation key
	WorktreePath  string
	Operation     string // "rebase" or "merge"
	OntoOrRef     string
	Err           error
	DirtyPaths    []string // porcelain output, if available
	ResumeCommand string
}

// keyParts splits an annotation key into (repoPath, branch).
func keyParts(key string) (string, string, error) {
	// Annotation keys look like "/abs/repo/path:branch-name". Branch names
	// may contain slashes but not colons; split on the LAST colon so paths
	// with embedded colons (rare on macOS, possible elsewhere) survive.
	idx := strings.LastIndex(key, ":")
	if idx < 0 {
		return "", "", fmt.Errorf("malformed annotation key: %q", key)
	}
	return key[:idx], key[idx+1:], nil
}

// locateWorktree returns the worktree path for a given (repoPath, branch).
// Returns ("", nil) if no worktree currently has that branch checked out.
func locateWorktree(g GitOps, repoPath, branch string) (string, error) {
	wts, err := g.ListWorktrees(repoPath)
	if err != nil {
		return "", fmt.Errorf("list worktrees for %s: %w", repoPath, err)
	}
	for _, wt := range wts {
		if wt.Branch == branch {
			return wt.Path, nil
		}
	}
	return "", nil
}

// resolveParentTip returns the OID a child should be rebased onto.
// For an in-stack parent, that's the local tip of the parent's branch
// (which the algorithm has already rebased and pushed).
// For an external parent (not in the stack), we fetch and use the remote tip.
func resolveParentTip(g GitOps, state *store.State, stackID, parentKey string) (string, error) {
	repoPath, branch, err := keyParts(parentKey)
	if err != nil {
		return "", err
	}
	// In-stack parent: use the local branch tip (already updated by this run).
	if ann := state.GetAnnotation(repoPath, branch); ann != nil && ann.StackID == stackID {
		return g.ResolveRef(repoPath, branch)
	}
	// External parent: prefer the remote tracking ref if it exists, else local.
	if oid, err := g.ResolveRef(repoPath, "origin/"+branch); err == nil {
		return oid, nil
	}
	return g.ResolveRef(repoPath, branch)
}

// Restack rebases every descendant of opts.StartFrom in topological order,
// then pushes everything atomically per repo. Conflicts halt the walk and
// write a checkpoint; resume with opts.Continue=true.
func Restack(g GitOps, gh GitHubOps, state *store.State, opts Options) (*Result, error) {
	if opts.StackID == "" {
		return nil, fmt.Errorf("stack id required")
	}

	graph, err := Build(state, opts.StackID)
	if err != nil {
		return nil, err
	}

	cp, err := loadOrBuildCheckpoint(g, gh, graph, opts)
	if err != nil {
		return nil, err
	}

	res := &Result{
		StackID:      opts.StackID,
		PlannedNodes: append([]string(nil), cp.TopoOrder...),
	}

	if opts.DryRun {
		return res, nil
	}

	// Initial fetch (skip on resume — the user may have fetched manually,
	// and re-fetching could move parent tips out from under our captured leases).
	if !opts.Continue {
		if err := g.Fetch(rootRepoOf(graph)); err != nil {
			return res, fmt.Errorf("fetch: %w", err)
		}
	}

	for i := cp.CurrentIndex; i < len(cp.TopoOrder); i++ {
		cp.CurrentIndex = i
		node := cp.TopoOrder[i]
		if err := SaveCheckpoint(opts.WgoBaseDir, cp); err != nil {
			return res, err
		}

		conflict, err := rebaseOne(g, state, graph, node, cp)
		if err != nil {
			return res, err
		}
		if conflict != nil {
			res.RebaseConflicts = append(res.RebaseConflicts, *conflict)
			return res, nil // halt; checkpoint already saved
		}

		// Drop any merged parents from state and the in-memory graph so
		// subsequent nodes see the updated parent relationships.
		nodeRepo, nodeBranch, _ := keyParts(node)
		var remaining []string
		for _, pk := range graph.Parents[node] {
			if !cp.MergedParents[pk] {
				remaining = append(remaining, pk)
			}
		}
		if len(remaining) != len(graph.Parents[node]) {
			state.SetParents(nodeRepo, nodeBranch, remaining)
			graph.Parents[node] = remaining
			// Rebuild children index for this node (simplified: clear stale entries).
			for pk := range cp.MergedParents {
				children := graph.Children[pk]
				for i, c := range children {
					if c == node {
						graph.Children[pk] = append(children[:i], children[i+1:]...)
						break
					}
				}
			}
		}

		res.Completed = append(res.Completed, node)
	}

	// All nodes rebased. Build atomic push groups (one per repo) using the
	// leases captured before any local rewrite.
	pushGroups := buildPushGroups(cp)
	for repoPath, refs := range pushGroups {
		if err := g.PushForceWithLease(repoPath, refs); err != nil {
			return res, fmt.Errorf("push %s: %w", repoPath, err)
		}
		res.PushedRefs = append(res.PushedRefs, refs...)
	}

	// Rewrite closed-PR parents in the graph before PR sync. A closed parent is
	// replaced with its nearest open ancestor so syncPRs retargets child PR bases
	// to the correct branch rather than a closed/deleted one.
	if len(cp.ClosedNodes) > 0 {
		for node := range graph.Parents {
			var rewired []string
			changed := false
			for _, pk := range graph.Parents[node] {
				if !cp.ClosedNodes[pk] {
					rewired = append(rewired, pk)
					continue
				}
				changed = true
				// Walk up from the closed parent to the nearest open ancestor.
				cur := pk
				for {
					ancestors := graph.Parents[cur]
					if len(ancestors) == 0 {
						break // no open ancestor found; omit (defaultBase used by syncPRs)
					}
					gp := ancestors[0]
					if cp.MergedParents[gp] {
						break // merged root; omit
					}
					if cp.ClosedNodes[gp] {
						cur = gp
						continue
					}
					rewired = append(rewired, gp)
					break
				}
			}
			if changed {
				graph.Parents[node] = rewired
			}
		}
	}

	// Refresh PR bodies and retarget bases where needed.
	if gh != nil && gh.Available() {
		if err := syncPRs(gh, graph, cp.DefaultBase); err != nil {
			// PR sync failures are reported but do NOT halt execution: code is already
			// pushed, so the rebase succeeded. Added to RebaseConflicts for visibility,
			// but ResumeCommand/WorktreePath fields remain empty to signal this is
			// advisory only (no checkpoint saved, no manual resolution needed).
			res.RebaseConflicts = append(res.RebaseConflicts, ConflictReport{
				Operation: "pr-sync",
				Err:       err,
			})
		}
	}

	if err := DeleteCheckpoint(opts.WgoBaseDir, opts.StackID); err != nil {
		return res, fmt.Errorf("delete checkpoint: %w", err)
	}
	return res, nil
}

func loadOrBuildCheckpoint(g GitOps, gh GitHubOps, graph *Graph, opts Options) (*Checkpoint, error) {
	if opts.Continue {
		cp, err := LoadCheckpoint(opts.WgoBaseDir, opts.StackID)
		if err != nil {
			return nil, err
		}
		if cp == nil {
			return nil, fmt.Errorf("no checkpoint for stack %s; nothing to resume", opts.StackID)
		}
		return cp, nil
	}

	if opts.StartFrom == "" {
		return nil, fmt.Errorf("StartFrom is required when not resuming")
	}

	defaultBase := opts.DefaultBase
	if defaultBase == "" {
		defaultBase = "origin/main"
	}

	// Detect merged and closed PRs for every node in the stack. Merged nodes
	// cause children to rebase onto defaultBase. Closed nodes are excluded from
	// rebasing; their children rebase onto the nearest open ancestor.
	mergedParents := map[string]bool{}
	closedNodes := map[string]bool{}
	if gh != nil && gh.Available() {
		for node := range graph.Parents {
			nRepo, nBranch, err := keyParts(node)
			if err != nil {
				continue
			}
			pr, err := gh.GetPRStatus(nRepo, nBranch)
			if err != nil || pr == nil {
				continue
			}
			if pr.IsMerged() {
				mergedParents[node] = true
			} else if pr.IsClosed() {
				closedNodes[node] = true
			}
		}
	}

	descendants, err := graph.AffectedDescendants(opts.StartFrom)
	if err != nil {
		return nil, err
	}

	// If startFrom itself has a merged or closed parent it needs to be rebased
	// too — AffectedDescendants excludes the root, so prepend it explicitly.
	topoOrder := descendants
	for _, pk := range graph.Parents[opts.StartFrom] {
		if mergedParents[pk] || closedNodes[pk] {
			topoOrder = append([]string{opts.StartFrom}, descendants...)
			break
		}
	}

	// Exclude closed-PR nodes from the rebase order — their branches shouldn't
	// be rebased (the PR is done), and their worktrees may no longer exist.
	var openOrder []string
	for _, node := range topoOrder {
		if !closedNodes[node] {
			openOrder = append(openOrder, node)
		}
	}
	topoOrder = openOrder

	if len(topoOrder) == 0 {
		return &Checkpoint{
			StackID:       opts.StackID,
			StartedAt:     time.Now(),
			MergedParents: mergedParents,
			ClosedNodes:   closedNodes,
			DefaultBase:   defaultBase,
		}, nil
	}

	// Capture the pre-rebase local OID for every node we'll rebase PLUS every
	// parent referenced by those nodes (including startFrom and merged parents).
	// These become the --onto upstream for each child: "skip commits already on <old>".
	preRebaseTips := map[string]string{}
	captureLocalTip := func(key string) {
		if _, already := preRebaseTips[key]; already {
			return
		}
		repoPath, branch, err := keyParts(key)
		if err != nil {
			return
		}
		// Try local branch first; fall back to remote tracking ref.
		if oid, err := g.ResolveRef(repoPath, branch); err == nil {
			preRebaseTips[key] = oid
			return
		}
		if oid, err := g.ResolveRef(repoPath, "origin/"+branch); err == nil {
			preRebaseTips[key] = oid
		}
	}
	captureLocalTip(opts.StartFrom)
	for _, node := range topoOrder {
		captureLocalTip(node)
		for _, pk := range graph.Parents[node] {
			captureLocalTip(pk)
		}
	}

	// Capture remote OIDs for force-with-lease BEFORE any rewriting happens.
	leases := make(map[string]string, len(topoOrder))
	for _, node := range topoOrder {
		repoPath, branch, err := keyParts(node)
		if err != nil {
			return nil, err
		}
		if oid, err := g.ResolveRef(repoPath, "origin/"+branch); err == nil {
			leases[node] = oid
		}
		// Absent remote ref => empty lease; PushForceWithLease handles this.
	}

	return &Checkpoint{
		StackID:       opts.StackID,
		StartedAt:     time.Now(),
		TopoOrder:     topoOrder,
		CurrentIndex:  0,
		Leases:        leases,
		PreRebaseTips: preRebaseTips,
		MergedParents: mergedParents,
		ClosedNodes:   closedNodes,
		DefaultBase:   defaultBase,
	}, nil
}

// rebaseOne rebases (and possibly merges) a single node onto its parent's new
// tip using `git rebase --onto <newBase> <upstream>`. This form isolates each
// branch's own commits regardless of whether the parent was squash-merged,
// rebased, or simply force-pushed. Returns a non-nil conflict report when git
// left the worktree in a halt-state.
func rebaseOne(g GitOps, state *store.State, graph *Graph, node string, cp *Checkpoint) (*ConflictReport, error) {
	repoPath, branch, err := keyParts(node)
	if err != nil {
		return nil, err
	}

	wtPath, err := locateWorktree(g, repoPath, branch)
	if err != nil {
		return nil, err
	}
	if wtPath == "" {
		return nil, fmt.Errorf("%s: no worktree currently has branch %q checked out", node, branch)
	}

	clean, dirty, err := g.IsClean(wtPath)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", node, err)
	}
	if !clean {
		return &ConflictReport{
			Node:          node,
			WorktreePath:  wtPath,
			Operation:     "precheck",
			DirtyPaths:    dirty,
			Err:           fmt.Errorf("worktree is dirty"),
			ResumeCommand: fmt.Sprintf("wgo stack restack --continue %s", cp.StackID),
		}, nil
	}

	parents := graph.Parents[node]
	if len(parents) == 0 {
		// Genuine unparented root — nothing to rebase.
		return nil, nil
	}

	firstParent := parents[0]
	pRepo, pBranch, err := keyParts(firstParent)
	if err != nil {
		return nil, fmt.Errorf("%s: parse parent key %s: %w", node, firstParent, err)
	}

	// upstream is where this node's history currently starts — commits on top
	// of this OID belong to this node alone and should be replayed.
	upstream := cp.PreRebaseTips[firstParent]
	if upstream == "" {
		// Branch tip unavailable (e.g. deleted after PR close/merge).
		// Fall back to defaultBase so we replay all commits not yet in main.
		if cp.MergedParents[firstParent] || cp.ClosedNodes[firstParent] {
			upstream = cp.DefaultBase
		} else {
			return nil, fmt.Errorf("%s: no pre-rebase tip recorded for parent %s", node, firstParent)
		}
	}

	// newBase is where we want to land: defaultBase for merged/closed parents,
	// or the parent's current local tip (already rebased earlier in this run).
	var newBase string
	switch {
	case cp.MergedParents[firstParent]:
		newBase = cp.DefaultBase
	case cp.ClosedNodes[firstParent]:
		newBase, err = effectiveBase(g, firstParent, graph, cp)
		if err != nil {
			return nil, fmt.Errorf("%s: resolve effective base for closed parent %s: %w", node, firstParent, err)
		}
	default:
		newBase, err = g.ResolveRef(pRepo, pBranch)
		if err != nil {
			return nil, fmt.Errorf("%s: resolve parent tip %s: %w", node, firstParent, err)
		}
	}

	// Check if there's an active rebase from a previous halt.
	hasActiveRebase, err := g.HasActiveRebase(wtPath)
	if err != nil {
		return nil, fmt.Errorf("%s: check rebase state: %w", node, err)
	}

	if hasActiveRebase {
		if err := g.RebaseContinue(wtPath); err != nil {
			return &ConflictReport{
				Node:          node,
				WorktreePath:  wtPath,
				Operation:     "rebase-continue",
				OntoOrRef:     firstParent,
				Err:           err,
				ResumeCommand: fmt.Sprintf("wgo stack restack --continue %s", cp.StackID),
			}, nil
		}
	} else {
		if err := g.RebaseOnto(wtPath, newBase, upstream); err != nil {
			return &ConflictReport{
				Node:          node,
				WorktreePath:  wtPath,
				Operation:     "rebase-onto",
				OntoOrRef:     firstParent,
				Err:           err,
				ResumeCommand: fmt.Sprintf("wgo stack restack --continue %s", cp.StackID),
			}, nil
		}
	}

	// Multi-parent (merge node): merge each extra parent into the rebased branch.
	for _, extra := range parents[1:] {
		extraTip, err := resolveParentTip(g, state, cp.StackID, extra)
		if err != nil {
			return nil, fmt.Errorf("%s: resolve parent %s: %w", node, extra, err)
		}
		if err := g.Merge(wtPath, extraTip, true); err != nil {
			return &ConflictReport{
				Node:          node,
				WorktreePath:  wtPath,
				Operation:     "merge",
				OntoOrRef:     extra,
				Err:           err,
				ResumeCommand: fmt.Sprintf("wgo stack restack --continue %s", cp.StackID),
			}, nil
		}
	}

	return nil, nil
}

// buildPushGroups partitions the rebased nodes into per-repo push batches so
// each repo gets one atomic push. Branches without captured leases are still
// included; PushForceWithLease handles the empty-OID case.
func buildPushGroups(cp *Checkpoint) map[string][]git.ForceLeaseRef {
	out := make(map[string][]git.ForceLeaseRef)
	for _, node := range cp.TopoOrder {
		repoPath, branch, err := keyParts(node)
		if err != nil {
			continue
		}
		out[repoPath] = append(out[repoPath], git.ForceLeaseRef{
			Branch:      branch,
			ExpectedOID: cp.Leases[node],
		})
	}
	return out
}

// rootRepoOf picks any repo in the stack to use for the up-front fetch.
// Cross-repo stacks are out of scope for this spec, so a single fetch on
// one of the repos covers everything.
func rootRepoOf(graph *Graph) string {
	for node := range graph.Parents {
		if repoPath, _, err := keyParts(node); err == nil {
			return repoPath
		}
	}
	return ""
}

// effectiveBase returns the rebase target for a node whose direct parent has a
// closed PR. It walks up the graph, skipping merged and closed nodes, returning
// the nearest open ancestor's branch tip. Falls back to defaultBase when no open
// ancestor exists in the stack (all merged up to the root) or when a ref cannot
// be resolved.
func effectiveBase(g GitOps, closedParent string, graph *Graph, cp *Checkpoint) (string, error) {
	cur := closedParent
	for {
		grandparents := graph.Parents[cur]
		if len(grandparents) == 0 {
			return cp.DefaultBase, nil
		}
		gp := grandparents[0]
		if cp.MergedParents[gp] {
			return cp.DefaultBase, nil
		}
		if cp.ClosedNodes[gp] {
			cur = gp
			continue
		}
		gpRepo, gpBranch, err := keyParts(gp)
		if err != nil {
			return cp.DefaultBase, nil
		}
		tip, err := g.ResolveRef(gpRepo, gpBranch)
		if err != nil {
			return cp.DefaultBase, nil
		}
		return tip, nil
	}
}

// syncPRs walks every node in the stack, retargets PR bases to match the
// current graph (defaultBase for roots, first in-stack parent branch for
// non-roots), and rewrites marker blocks in PR bodies.
func syncPRs(gh GitHubOps, graph *Graph, defaultBase string) error {
	// Strip "origin/" prefix so gh pr edit accepts a plain branch name.
	plainDefaultBase := strings.TrimPrefix(defaultBase, "origin/")

	marker := buildMarker(gh, graph, "")
	for node := range graph.Parents {
		repoPath, branch, err := keyParts(node)
		if err != nil {
			continue
		}
		pr, err := gh.GetPRStatus(repoPath, branch)
		if err != nil || pr == nil {
			continue
		}
		if pr.IsMerged() || pr.IsClosed() {
			continue
		}

		// Compute the expected PR base from the current graph.
		expectedBase := plainDefaultBase
		for _, pk := range graph.Parents[node] {
			if _, inStack := graph.Parents[pk]; inStack {
				_, parentBranch, err := keyParts(pk)
				if err == nil {
					expectedBase = parentBranch
				}
				break
			}
		}
		if err := gh.UpdatePRBase(repoPath, pr.Number, expectedBase); err != nil {
			return fmt.Errorf("%s #%d base: %w", branch, pr.Number, err)
		}

		body, err := gh.GetPRBody(repoPath, pr.Number)
		if err != nil {
			return fmt.Errorf("%s #%d: %w", branch, pr.Number, err)
		}
		marker.Self = node
		updated := ApplyMarker(body, marker.Render())
		if updated != body {
			if err := gh.UpdatePRBody(repoPath, pr.Number, updated); err != nil {
				return fmt.Errorf("%s #%d body: %w", branch, pr.Number, err)
			}
		}
	}
	return nil
}

// buildMarker assembles a Marker for the given stack from current state.
// `self` may be left empty by the caller and set per-PR before each Render.
func buildMarker(gh GitHubOps, graph *Graph, self string) Marker {
	order, _ := graph.TopoSort()
	nodes := make([]MarkerNode, 0, len(order))
	for _, key := range order {
		repoPath, branch, err := keyParts(key)
		if err != nil {
			continue
		}
		mn := MarkerNode{
			Key:     key,
			Branch:  branch,
			Parents: append([]string(nil), graph.Parents[key]...),
		}
		if pr, err := gh.GetPRStatus(repoPath, branch); err == nil && pr != nil {
			mn.PRNumber = pr.Number
		}
		nodes = append(nodes, mn)
	}
	return Marker{StackID: graph.StackID, Self: self, Nodes: nodes}
}
