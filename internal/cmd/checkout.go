package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/git"
	gh "github.com/virtru/wgo/internal/github"
)

type checkoutInfo struct {
	RepoPath     string
	WorktreePath string
	Branch       string
}

func currentCheckout() (*checkoutInfo, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("could not determine working directory: %w", err)
	}

	g := git.New("")
	branch, err := g.CurrentBranch(wd)
	if err != nil {
		return nil, fmt.Errorf("could not determine current branch: %w", err)
	}
	repoPath, err := g.MainRepoPath(wd)
	if err != nil {
		return nil, fmt.Errorf("could not determine canonical repo path: %w", err)
	}

	return &checkoutInfo{
		RepoPath:     repoPath,
		WorktreePath: wd,
		Branch:       branch,
	}, nil
}

func canonicalRepoPath(path string) (string, error) {
	return git.New("").MainRepoPath(path)
}

func configuredWorktreePath(repoName, branch string) (string, error) {
	if err := config.Init(); err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	cfg := config.Get()
	if cfg.Worktree.WorktreesDir == "" {
		return "", fmt.Errorf("worktree.worktrees_dir not configured")
	}
	return filepath.Join(cfg.Worktree.WorktreesDir, gh.SanitizeBranch(branch), repoName), nil
}
