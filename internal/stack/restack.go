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
	Merge(worktreePath, ref string, noFF bool) error
	PushForceWithLease(repoPath string, refs []git.ForceLeaseRef) error
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

	cp, err := loadOrBuildCheckpoint(g, graph, opts)
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

		conflict, err := rebaseOne(g, state, graph, node, opts)
		if err != nil {
			return res, err
		}
		if conflict != nil {
			res.RebaseConflicts = append(res.RebaseConflicts, *conflict)
			return res, nil // halt; checkpoint already saved
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

	// Refresh PR bodies and retarget bases where needed.
	if gh != nil && gh.Available() {
		if err := syncPRs(gh, state, graph); err != nil {
			// PR sync failures are non-fatal: code is already pushed.
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

func loadOrBuildCheckpoint(g GitOps, graph *Graph, opts Options) (*Checkpoint, error) {
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
	descendants, err := graph.AffectedDescendants(opts.StartFrom)
	if err != nil {
		return nil, err
	}
	if len(descendants) == 0 {
		return &Checkpoint{StackID: opts.StackID, StartedAt: time.Now()}, nil
	}

	// Capture remote OIDs for the lease BEFORE any rewriting happens.
	leases := make(map[string]string, len(descendants))
	for _, node := range descendants {
		repoPath, branch, err := keyParts(node)
		if err != nil {
			return nil, err
		}
		if oid, err := g.ResolveRef(repoPath, "origin/"+branch); err == nil {
			leases[node] = oid
		}
		// Absent remote ref => empty lease; PushForceWithLease falls back to the
		// looser form, which is right for newly-created branches that haven't
		// been pushed yet.
	}

	return &Checkpoint{
		StackID:      opts.StackID,
		StartedAt:    time.Now(),
		TopoOrder:    descendants,
		CurrentIndex: 0,
		Leases:       leases,
	}, nil
}

// rebaseOne rebases (and possibly merges) a single node. Returns a non-nil
// conflict report when git left the worktree in a halt-state.
func rebaseOne(g GitOps, state *store.State, graph *Graph, node string, opts Options) (*ConflictReport, error) {
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
			ResumeCommand: fmt.Sprintf("wgo stack restack --continue %s", opts.StackID),
		}, nil
	}

	parents := graph.Parents[node]
	if len(parents) == 0 {
		// Root of the subgraph being restacked; nothing to do here unless
		// the user explicitly named a root, in which case caller passed it
		// as StartFrom (which is *excluded* from descendants). Skip safely.
		return nil, nil
	}

	// Single-parent: pure rebase.
	firstParentTip, err := resolveParentTip(g, state, opts.StackID, parents[0])
	if err != nil {
		return nil, fmt.Errorf("%s: resolve parent %s: %w", node, parents[0], err)
	}
	if err := g.Rebase(wtPath, firstParentTip); err != nil {
		return &ConflictReport{
			Node:          node,
			WorktreePath:  wtPath,
			Operation:     "rebase",
			OntoOrRef:     parents[0],
			Err:           err,
			ResumeCommand: fmt.Sprintf("wgo stack restack --continue %s", opts.StackID),
		}, nil
	}

	// Multi-parent (merge node): merge each extra parent into the rebased branch.
	for _, extra := range parents[1:] {
		extraTip, err := resolveParentTip(g, state, opts.StackID, extra)
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
				ResumeCommand: fmt.Sprintf("wgo stack restack --continue %s", opts.StackID),
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

// syncPRs walks every node in the stack and (a) retargets the PR base when
// the recorded parent has merged or differs from the live base, (b) rewrites
// the marker block in each PR body.
func syncPRs(gh GitHubOps, _ *store.State, graph *Graph) error {
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
