package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/stack"
	"github.com/virtru/wgo/internal/store"
)

func ensureRestackWorktrees(state *store.State, wgoBaseDir, stackID, startFrom string, cont bool) error {
	nodes, err := restackNodes(state, wgoBaseDir, stackID, startFrom, cont)
	if err != nil {
		return err
	}

	g := git.New("")
	for _, node := range nodes {
		repoPath, branch, err := splitAnnotationKey(node)
		if err != nil {
			return err
		}
		if err := ensureBranchWorktree(g, repoPath, branch); err != nil {
			return fmt.Errorf("%s: %w", node, err)
		}
	}
	return nil
}

func restackNodes(state *store.State, wgoBaseDir, stackID, startFrom string, cont bool) ([]string, error) {
	if cont {
		cp, err := stack.LoadCheckpoint(wgoBaseDir, stackID)
		if err != nil {
			return nil, err
		}
		if cp == nil {
			return nil, fmt.Errorf("no checkpoint for stack %s; nothing to resume", stackID)
		}
		return append([]string(nil), cp.TopoOrder...), nil
	}

	graph, err := stack.Build(state, stackID)
	if err != nil {
		return nil, err
	}
	return graph.AffectedDescendants(startFrom)
}

func ensureBranchWorktree(g *git.CLIClient, repoPath, branch string) error {
	worktrees, err := g.ListWorktrees(repoPath)
	if err != nil {
		return err
	}
	for _, wt := range worktrees {
		if wt.Branch == branch {
			return nil
		}
	}

	// Prune stale worktree administrative entries
	_ = g.PruneWorktrees(repoPath)

	exists, err := g.BranchExists(repoPath, branch)
	if err != nil {
		return fmt.Errorf("check branch %q: %w", branch, err)
	}
	if !exists {
		return fmt.Errorf("branch %q not found locally or on origin", branch)
	}

	wtPath, err := worktreePathFor(repoPath, branch)
	if err != nil {
		return err
	}

	// Handle pre-existing directory at target path
	if info, statErr := os.Stat(wtPath); statErr == nil && info.IsDir() {
		// Check if it's a valid worktree
		cur, branchErr := g.CurrentBranch(wtPath)

		if branchErr == nil {
			// Valid worktree exists
			if cur == branch {
				// Already on correct branch - idempotent
				return nil
			}
			// Worktree on wrong branch - switch it
			if checkoutErr := g.Checkout(wtPath, branch); checkoutErr != nil {
				return fmt.Errorf("worktree at %s is on branch %q, failed to switch to %q: %w",
					wtPath, cur, branch, checkoutErr)
			}
			return nil
		}

		// Directory exists but is not a valid worktree (stale)
		if removeErr := os.RemoveAll(wtPath); removeErr != nil {
			return fmt.Errorf("stale directory exists at %s and cannot be removed: %w\nManual cleanup: rm -rf %s",
				wtPath, removeErr, wtPath)
		}
		// Stale directory removed - proceed with creation below
	}

	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return fmt.Errorf("create worktree dir: %w", err)
	}
	if err := g.WorktreeAdd(repoPath, wtPath, branch, false, ""); err != nil {
		return fmt.Errorf("create missing worktree at %s: %w", wtPath, err)
	}
	return nil
}
