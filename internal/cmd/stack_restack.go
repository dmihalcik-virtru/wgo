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
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return fmt.Errorf("create worktree dir: %w", err)
	}
	if err := g.WorktreeAdd(repoPath, wtPath, branch, false, ""); err != nil {
		return fmt.Errorf("create missing worktree at %s: %w", wtPath, err)
	}
	return nil
}
