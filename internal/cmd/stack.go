package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/virtru/wgo/internal/git"
	gh "github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/stack"
	"github.com/virtru/wgo/internal/store"
)

var stackCmd = &cobra.Command{
	Use:   "stack",
	Short: "Manage stacked / DAG-shaped pull requests across worktrees",
	Long: `wgo stack tracks parent/child relationships between branches and keeps
downstream PRs rebased on their parents across multiple worktrees.

The graph lives in ~/.wgo/state.json (Annotation.Parents and Annotation.StackID)
and is mirrored into each PR body as a <!-- wgo-stack:<id> --> block.`,
}

func init() {
	rootCmd.AddCommand(stackCmd)
	stackCmd.AddCommand(stackNewCmd, stackPushCmd, stackRestackCmd, stackSyncCmd, stackStatusCmd, stackRmCmd)
}

// ---- stack new -----------------------------------------------------------

var stackNewName string

var stackNewCmd = &cobra.Command{
	Use:          "new <name>",
	Short:        "Register the current branch as a stack root",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(_ *cobra.Command, args []string) error {
		stackNewName = args[0]
		return runStackNew()
	},
}

func runStackNew() error {
	repoPath, branch, err := currentRepoAndBranch()
	if err != nil {
		return err
	}
	s, state, err := loadStateForStack()
	if err != nil {
		return err
	}

	// Reuse an existing stack with the same name if one exists, so `new` is idempotent.
	var id string
	for sid, st := range state.Stacks {
		if st.Name == stackNewName {
			id = sid
			break
		}
	}
	if id == "" {
		id = newStackID()
	}

	rootRef := defaultBranchRef(repoPath)
	state.AddStack(store.Stack{ID: id, Name: stackNewName, RootRef: rootRef})

	if state.GetAnnotation(repoPath, branch) == nil {
		state.AddAnnotation(repoPath, branch, "")
	}
	state.SetStackID(repoPath, branch, id)

	if err := s.SaveState(state); err != nil {
		return err
	}
	fmt.Printf("stack %q (%s) rooted at %s:%s on %s\n", stackNewName, id, filepath.Base(repoPath), branch, rootRef)
	return nil
}

// ---- stack push ----------------------------------------------------------

var (
	stackPushParents []string
	stackPushDraft   bool
)

var stackPushCmd = &cobra.Command{
	Use:          "push <branch>",
	Short:        "Create a new branch (worktree) on top of one or more stack parents",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(_ *cobra.Command, args []string) error {
		return runStackPush(args[0])
	},
}

func init() {
	stackPushCmd.Flags().StringSliceVar(&stackPushParents, "on", nil,
		"Parent branch(es) to base on (may be repeated for merge nodes); defaults to the current branch")
	stackPushCmd.Flags().BoolVar(&stackPushDraft, "draft", false,
		"Open a draft PR with --base set to the first parent (requires gh)")
}

func runStackPush(branch string) error {
	repoPath, currentBranch, err := currentRepoAndBranch()
	if err != nil {
		return err
	}
	s, state, err := loadStateForStack()
	if err != nil {
		return err
	}

	parents := stackPushParents
	if len(parents) == 0 {
		parents = []string{currentBranch}
	}

	// Resolve current annotation to inherit stack id from any parent.
	stackID := ""
	for _, p := range parents {
		if ann := state.GetAnnotation(repoPath, p); ann != nil && ann.StackID != "" {
			stackID = ann.StackID
			break
		}
	}
	if stackID == "" {
		return fmt.Errorf("no parent is part of a stack; run `wgo stack new <name>` first on one of: %s", strings.Join(parents, ", "))
	}

	childKey := store.AnnotationKey(repoPath, branch)
	var parentKeys []string
	for _, p := range parents {
		parentKeys = append(parentKeys, store.AnnotationKey(repoPath, p))
	}
	for _, pk := range parentKeys {
		if stack.WouldCreateCycle(state, stackID, childKey, pk) {
			return fmt.Errorf("adding %s as parent of %s would create a cycle", pk, childKey)
		}
	}

	g := git.New("")
	wtPath, err := worktreePathFor(repoPath, branch)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(wtPath); statErr == nil {
		return fmt.Errorf("worktree already exists at %s", wtPath)
	}
	startPoint := parents[0]
	if err := g.WorktreeAdd(repoPath, wtPath, branch, true, startPoint); err != nil {
		return fmt.Errorf("worktree add: %w", err)
	}
	if err := g.Push(wtPath, branch); err != nil {
		// Roll back the worktree if push fails, so the local state stays consistent.
		_ = g.RemoveWorktree(repoPath, wtPath, true)
		return fmt.Errorf("push: %w", err)
	}

	state.AddAnnotation(repoPath, branch, "")
	state.SetParents(repoPath, branch, parentKeys)
	state.SetStackID(repoPath, branch, stackID)
	if err := s.SaveState(state); err != nil {
		return err
	}

	if stackPushDraft {
		if err := createDraftPR(wtPath, branch, parents[0]); err != nil {
			fmt.Fprintf(os.Stderr, "warning: --draft PR creation failed: %v\n", err)
		}
	}
	fmt.Println(wtPath)
	return nil
}

func createDraftPR(wtPath, branch, base string) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh CLI not installed")
	}
	cmd := exec.Command("gh", "pr", "create", "--draft", "--fill",
		"--head", branch, "--base", base)
	cmd.Dir = wtPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ---- stack restack -------------------------------------------------------

var stackRestackContinue bool

var stackRestackCmd = &cobra.Command{
	Use:          "restack [<branch>]",
	Short:        "Rebase every descendant of <branch> in topological order",
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE: func(_ *cobra.Command, args []string) error {
		var startBranch string
		if len(args) == 1 {
			startBranch = args[0]
		}
		return runStackRestack(startBranch, stackRestackContinue)
	},
}

func init() {
	stackRestackCmd.Flags().BoolVar(&stackRestackContinue, "continue", false,
		"Resume from the saved checkpoint after resolving a conflict")
}

func runStackRestack(startBranch string, cont bool) error {
	s, state, err := loadStateForStack()
	if err != nil {
		return err
	}

	repoPath, currentBranch, err := currentRepoAndBranch()
	if err != nil && !cont {
		return err
	}

	var stackID, startFrom string
	if cont {
		// Find the in-flight stack by scanning the cache dir.
		stackID, err = inflightStackID(s.BaseDir())
		if err != nil {
			return err
		}
	} else {
		if startBranch == "" {
			startBranch = currentBranch
		}
		ann := state.GetAnnotation(repoPath, startBranch)
		if ann == nil || ann.StackID == "" {
			return fmt.Errorf("%s is not part of a managed stack", startBranch)
		}
		stackID = ann.StackID
		startFrom = store.AnnotationKey(repoPath, startBranch)
	}

	res, err := stack.Restack(git.New(""), gh.NewClient(), state, stack.Options{
		WgoBaseDir: s.BaseDir(),
		StackID:    stackID,
		StartFrom:  startFrom,
		Continue:   cont,
	})
	if err != nil {
		return err
	}

	for _, node := range res.Completed {
		fmt.Printf("rebased %s\n", node)
	}
	if len(res.RebaseConflicts) > 0 {
		for _, c := range res.RebaseConflicts {
			fmt.Fprintf(os.Stderr, "halted at %s (%s): %v\n", c.Node, c.Operation, c.Err)
			if len(c.DirtyPaths) > 0 {
				fmt.Fprintf(os.Stderr, "  dirty paths in %s:\n", c.WorktreePath)
				for _, p := range c.DirtyPaths {
					fmt.Fprintf(os.Stderr, "    %s\n", p)
				}
			}
			if c.ResumeCommand != "" {
				fmt.Fprintf(os.Stderr, "  resume: %s\n", c.ResumeCommand)
			}
		}
		return fmt.Errorf("restack halted with %d conflict(s)", len(res.RebaseConflicts))
	}
	for _, ref := range res.PushedRefs {
		fmt.Printf("pushed %s (lease=%s)\n", ref.Branch, shortOID(ref.ExpectedOID))
	}
	return nil
}

// inflightStackID finds the single in-flight restack checkpoint, if any.
func inflightStackID(baseDir string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(baseDir, "cache"))
	if err != nil {
		return "", fmt.Errorf("no in-flight restack found: %w", err)
	}
	var ids []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "restack-") && strings.HasSuffix(name, ".json") {
			ids = append(ids, strings.TrimSuffix(strings.TrimPrefix(name, "restack-"), ".json"))
		}
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("no in-flight restack to resume")
	case 1:
		return ids[0], nil
	default:
		return "", fmt.Errorf("multiple in-flight restacks (%v); specify which with `wgo stack restack` from inside a worktree of the target stack", ids)
	}
}

// ---- stack sync ----------------------------------------------------------

var stackSyncCmd = &cobra.Command{
	Use:          "sync",
	Short:        "Refresh PR base targets and marker blocks without rebasing",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runStackSync()
	},
}

func runStackSync() error {
	_, state, err := loadStateForStack()
	if err != nil {
		return err
	}
	repoPath, branch, err := currentRepoAndBranch()
	if err != nil {
		return err
	}
	ann := state.GetAnnotation(repoPath, branch)
	if ann == nil || ann.StackID == "" {
		return fmt.Errorf("%s is not part of a managed stack", branch)
	}
	graph, err := stack.Build(state, ann.StackID)
	if err != nil {
		return err
	}
	ghClient := gh.NewClient()
	if !ghClient.Available() {
		return fmt.Errorf("gh CLI required for sync")
	}

	// Detect merged parents and retarget child PR bases to origin/<default>.
	defaultBase := defaultBranchRef(repoPath)
	// Strip "origin/" so the gh API accepts a branch name.
	defaultBase = strings.TrimPrefix(defaultBase, "origin/")

	for node := range graph.Parents {
		nodeRepo, nodeBranch, err := keyParts(node)
		if err != nil {
			continue
		}
		pr, err := ghClient.GetPRStatus(nodeRepo, nodeBranch)
		if err != nil || pr == nil {
			continue
		}
		// Check each parent: if its PR has merged, drop it from Parents and (if it was the head parent) retarget the base.
		var remaining []string
		retarget := false
		for _, pk := range graph.Parents[node] {
			pRepo, pBranch, err := keyParts(pk)
			if err != nil {
				continue
			}
			ppr, _ := ghClient.GetPRStatus(pRepo, pBranch)
			if ppr != nil && ppr.IsMerged() {
				retarget = true
				continue
			}
			remaining = append(remaining, pk)
		}
		if retarget {
			state.SetParents(nodeRepo, nodeBranch, remaining)
			if len(remaining) == 0 {
				if err := ghClient.UpdatePRBase(nodeRepo, pr.Number, defaultBase); err != nil {
					fmt.Fprintf(os.Stderr, "retarget #%d: %v\n", pr.Number, err)
				}
			}
		}
	}
	// Rebuild the graph after pruning merged parents, then refresh marker blocks.
	graph, _ = stack.Build(state, ann.StackID)
	if err := refreshMarkers(ghClient, graph); err != nil {
		return err
	}

	s, _ := store.New()
	return s.SaveState(state)
}

func refreshMarkers(ghClient gh.Client, graph *stack.Graph) error {
	order, _ := graph.TopoSort()
	nodes := make([]stack.MarkerNode, 0, len(order))
	for _, key := range order {
		repoPath, branch, err := keyParts(key)
		if err != nil {
			continue
		}
		mn := stack.MarkerNode{Key: key, Branch: branch, Parents: append([]string(nil), graph.Parents[key]...)}
		if pr, err := ghClient.GetPRStatus(repoPath, branch); err == nil && pr != nil {
			mn.PRNumber = pr.Number
		}
		nodes = append(nodes, mn)
	}
	for _, key := range order {
		repoPath, branch, err := keyParts(key)
		if err != nil {
			continue
		}
		pr, err := ghClient.GetPRStatus(repoPath, branch)
		if err != nil || pr == nil {
			continue
		}
		body, err := ghClient.GetPRBody(repoPath, pr.Number)
		if err != nil {
			return err
		}
		m := stack.Marker{StackID: graph.StackID, Self: key, Nodes: nodes}
		updated := stack.ApplyMarker(body, m.Render())
		if updated != body {
			if err := ghClient.UpdatePRBody(repoPath, pr.Number, updated); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- stack status --------------------------------------------------------

var stackStatusCmd = &cobra.Command{
	Use:          "status [<stack-id>]",
	Short:        "Show the DAG of a stack with PR numbers and parents",
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE: func(_ *cobra.Command, args []string) error {
		id := ""
		if len(args) == 1 {
			id = args[0]
		}
		return runStackStatus(id)
	},
}

func runStackStatus(stackID string) error {
	_, state, err := loadStateForStack()
	if err != nil {
		return err
	}
	if stackID == "" {
		repoPath, branch, err := currentRepoAndBranch()
		if err != nil {
			return err
		}
		ann := state.GetAnnotation(repoPath, branch)
		if ann == nil || ann.StackID == "" {
			return fmt.Errorf("%s is not part of a managed stack; pass a stack id explicitly", branch)
		}
		stackID = ann.StackID
	}
	st := state.GetStack(stackID)
	if st == nil {
		return fmt.Errorf("unknown stack id %q", stackID)
	}
	graph, err := stack.Build(state, stackID)
	if err != nil {
		return err
	}
	order, _ := graph.TopoSort()

	fmt.Printf("stack %q (%s) on %s\n", st.Name, st.ID, st.RootRef)
	for _, key := range order {
		_, branch, _ := keyParts(key)
		parents := append([]string(nil), graph.Parents[key]...)
		sort.Strings(parents)
		switch len(parents) {
		case 0:
			fmt.Printf("  %s ← root\n", branch)
		default:
			pBranches := make([]string, 0, len(parents))
			for _, p := range parents {
				_, b, _ := keyParts(p)
				pBranches = append(pBranches, b)
			}
			fmt.Printf("  %s ↳ on %s\n", branch, strings.Join(pBranches, ", "))
		}
	}
	return nil
}

// ---- stack rm ------------------------------------------------------------

var stackRmCmd = &cobra.Command{
	Use:          "rm <branch>",
	Short:        "Remove a branch from its stack (refuses if it has unmerged children)",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(_ *cobra.Command, args []string) error {
		return runStackRm(args[0])
	},
}

func runStackRm(branch string) error {
	repoPath, _, err := currentRepoAndBranch()
	if err != nil {
		return err
	}
	s, state, err := loadStateForStack()
	if err != nil {
		return err
	}
	key := store.AnnotationKey(repoPath, branch)
	ann := state.GetAnnotation(repoPath, branch)
	if ann == nil || ann.StackID == "" {
		return fmt.Errorf("%s is not in a managed stack", branch)
	}
	graph, err := stack.Build(state, ann.StackID)
	if err != nil {
		return err
	}
	if children := graph.Children[key]; len(children) > 0 {
		return fmt.Errorf("%s has stack children %v; remove or retarget them first", branch, children)
	}
	state.SetStackID(repoPath, branch, "")
	state.SetParents(repoPath, branch, nil)
	return s.SaveState(state)
}

// ---- shared helpers ------------------------------------------------------

func currentRepoAndBranch() (string, string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", "", fmt.Errorf("not in a git repository")
	}
	repoPath := strings.TrimSpace(string(out))
	g := git.New("")
	branch, err := g.CurrentBranch(repoPath)
	if err != nil {
		return "", "", fmt.Errorf("could not determine current branch: %w", err)
	}
	return repoPath, branch, nil
}

func loadStateForStack() (*store.FileStore, *store.State, error) {
	s, err := store.New()
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	if err := s.EnsureDir(); err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	state, err := s.LoadState()
	if err != nil {
		return nil, nil, fmt.Errorf("load state: %w", err)
	}
	return s, state, nil
}

func defaultBranchRef(repoPath string) string {
	g := git.New("")
	if def, err := g.DefaultBranch(repoPath); err == nil && def != "" {
		return "origin/" + def
	}
	return "origin/main"
}

// worktreePathFor returns the conventional worktree path for a (repo, branch).
// Reuses the location pattern documented in spec/WGO-101 / used by `wgo to`:
// <worktrees_dir>/<sanitized-branch>/<repo-basename>.
// If config is unavailable, falls back to a sibling directory of the repo.
func worktreePathFor(repoPath, branch string) (string, error) {
	// Lean on the same helper used by `wgo to` / `wgo add`. Since those import
	// internal/config directly and we want to stay loosely coupled, we replicate
	// the simplest behavior: place new worktrees alongside the repo.
	parent := filepath.Dir(repoPath)
	safe := gh.SanitizeBranch(branch)
	return filepath.Join(parent, safe), nil
}

func newStackID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "stack"
	}
	return hex.EncodeToString(b[:])
}

func keyParts(key string) (string, string, error) {
	idx := strings.LastIndex(key, ":")
	if idx < 0 {
		return "", "", fmt.Errorf("malformed annotation key: %q", key)
	}
	return key[:idx], key[idx+1:], nil
}

func shortOID(oid string) string {
	if len(oid) > 7 {
		return oid[:7]
	}
	return oid
}
