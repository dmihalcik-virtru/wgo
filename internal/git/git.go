// Package git provides Git operations for wgo.
package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/virtru/wgo/models"
)

// WorktreeInfo contains information about a git worktree.
type WorktreeInfo struct {
	Path       string
	Branch     string
	CommitHash string
	IsMain     bool
	IsBare     bool
}

// Client is the interface for Git operations.
type Client interface {
	IsRepo(path string) (bool, error)
	CurrentBranch(repoPath string) (string, error)
	Status(repoPath string) (models.GitStatus, error)
	AheadBehind(repoPath, branch string) (ahead int, behind int, err error)
	LastCommit(repoPath string) (models.CommitInfo, error)
	RepoName(repoPath string) (string, error)
	RemoteURL(repoPath string) (string, error)
	RecentCommitCount(repoPath string, since time.Time) (int, error)
	DiffStat(repoPath string) (models.DiffStat, error)
	ListWorktrees(repoPath string) ([]WorktreeInfo, error)
	Clone(url, destPath string) error
	WorktreeAdd(repoPath, wtPath, branch string, create bool, startPoint string) error
	Fetch(repoPath string) error
	FetchPRRef(repoPath string, prNumber int, localBranch string) error
	DefaultBranch(repoPath string) (string, error)
	BranchExists(repoPath, branch string) (bool, error)
	RemoveWorktree(repoPath, wtPath string, force bool) error
	DeleteBranch(repoPath, branch string, force bool) error
	PruneWorktrees(repoPath string) error
	ListLocalBranches(repoPath string) ([]string, error)
	IsBranchMerged(repoPath, branch, base string) (bool, error)
}

// CLIClient is a Git client implementation using the git CLI.
type CLIClient struct {
	workDir string
}

// New creates a new CLIClient.
func New(workDir string) *CLIClient {
	return &CLIClient{
		workDir: workDir,
	}
}

// NewFromCwd creates a new CLIClient using the current working directory.
func NewFromCwd() (*CLIClient, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current directory: %w", err)
	}
	return New(cwd), nil
}

// IsRepo checks if a path is a git repository.
func (c *CLIClient) IsRepo(path string) (bool, error) {
	output, err := c.runInPath(path, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false, nil // Not a repo
	}
	return strings.TrimSpace(output) == "true", nil
}

// CurrentBranch returns the current branch name.
func (c *CLIClient) CurrentBranch(repoPath string) (string, error) {
	output, err := c.runInPath(repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}
	return strings.TrimSpace(output), nil
}

// Status returns the git status.
func (c *CLIClient) Status(repoPath string) (models.GitStatus, error) {
	output, err := c.runInPath(repoPath, "status", "--porcelain")
	if err != nil {
		return models.GitStatus{}, fmt.Errorf("failed to get status: %w", err)
	}

	status := models.GitStatus{}
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}

		if len(line) < 3 {
			continue
		}

		staged := string(line[0])
		unstaged := string(line[1])

		switch staged {
		case "A":
			status.Added++
			status.Staged++
		case "M":
			status.Modified++
			status.Staged++
		case "D":
			status.Deleted++
			status.Staged++
		}

		switch unstaged {
		case "M":
			status.Modified++
		case "D":
			status.Deleted++
		case "?":
			status.Untracked++
		}

		if strings.HasPrefix(unstaged, "U") || strings.HasPrefix(staged, "U") {
			status.Conflicts++
		}
	}

	return status, nil
}

// AheadBehind returns how many commits ahead/behind the remote tracking branch.
func (c *CLIClient) AheadBehind(repoPath, branch string) (ahead int, behind int, err error) {
	// Get tracking branch
	trackingBranch, err := c.runInPath(repoPath, "rev-parse", "--abbrev-ref", branch+"@{u}")
	if err != nil {
		// No tracking branch
		return 0, 0, nil
	}

	trackingBranch = strings.TrimSpace(trackingBranch)
	if trackingBranch == "" {
		return 0, 0, nil
	}

	// Count commits ahead and behind
	aheadOutput, err := c.runInPath(repoPath, "rev-list", "--count", branch+".."+trackingBranch)
	if err != nil {
		return 0, 0, nil
	}

	behindOutput, err := c.runInPath(repoPath, "rev-list", "--count", trackingBranch+".."+branch)
	if err != nil {
		return 0, 0, nil
	}

	fmt.Sscanf(strings.TrimSpace(aheadOutput), "%d", &behind) // ahead is commits in tracking not in branch
	fmt.Sscanf(strings.TrimSpace(behindOutput), "%d", &ahead)  // behind is commits in branch not in tracking

	return ahead, behind, nil
}

// LastCommit returns information about the most recent commit.
func (c *CLIClient) LastCommit(repoPath string) (models.CommitInfo, error) {
	output, err := c.runInPath(repoPath, "log", "-1", "--pretty=format:%H|%s|%an|%ai")
	if err != nil {
		return models.CommitInfo{}, fmt.Errorf("failed to get last commit: %w", err)
	}

	parts := strings.Split(strings.TrimSpace(output), "|")
	if len(parts) < 4 {
		return models.CommitInfo{}, fmt.Errorf("unexpected git log format")
	}

	date, _ := time.Parse("2006-01-02 15:04:05 -0700", parts[3])

	return models.CommitInfo{
		Hash:    parts[0],
		Message: parts[1],
		Author:  parts[2],
		Date:    date,
	}, nil
}

// RepoName returns the repository name (directory name of root).
func (c *CLIClient) RepoName(repoPath string) (string, error) {
	rootDir, err := c.getRootDir(repoPath)
	if err != nil {
		return "", err
	}
	return filepath.Base(rootDir), nil
}

// RemoteURL returns the remote origin URL.
func (c *CLIClient) RemoteURL(repoPath string) (string, error) {
	output, err := c.runInPath(repoPath, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("failed to get remote URL: %w", err)
	}
	return strings.TrimSpace(output), nil
}

// RemoteURLs returns fetch URLs for all configured remotes.
func (c *CLIClient) RemoteURLs(repoPath string) ([]string, error) {
	output, err := c.runInPath(repoPath, "remote")
	if err != nil {
		return nil, fmt.Errorf("failed to list remotes: %w", err)
	}
	var urls []string
	for _, name := range strings.Split(strings.TrimSpace(output), "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		u, err := c.runInPath(repoPath, "remote", "get-url", name)
		if err == nil {
			urls = append(urls, strings.TrimSpace(u))
		}
	}
	return urls, nil
}

// RecentCommitCount returns the number of commits since a given time.
func (c *CLIClient) RecentCommitCount(repoPath string, since time.Time) (int, error) {
	sinceStr := since.Format(time.RFC3339)
	output, err := c.runInPath(repoPath, "rev-list", "--count", "--since="+sinceStr, "HEAD")
	if err != nil {
		return 0, nil // No commits or other issue, return 0
	}
	var count int
	fmt.Sscanf(strings.TrimSpace(output), "%d", &count)
	return count, nil
}

// DiffStat returns line-level diff statistics for uncommitted changes.
func (c *CLIClient) DiffStat(repoPath string) (models.DiffStat, error) {
	var stat models.DiffStat

	// Unstaged changes
	unstaged, err := c.runInPath(repoPath, "diff", "--numstat")
	if err == nil {
		parseDiffNumstat(unstaged, &stat)
	}

	// Staged changes
	staged, err := c.runInPath(repoPath, "diff", "--cached", "--numstat")
	if err == nil {
		parseDiffNumstat(staged, &stat)
	}

	return stat, nil
}

// ListWorktrees returns all worktrees for the repository at repoPath.
func (c *CLIClient) ListWorktrees(repoPath string) ([]WorktreeInfo, error) {
	output, err := c.runInPath(repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	var worktrees []WorktreeInfo
	var current WorktreeInfo
	isFirst := true

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(line, "worktree "):
			if !isFirst {
				worktrees = append(worktrees, current)
			}
			isFirst = false
			current = WorktreeInfo{Path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "HEAD "):
			current.CommitHash = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			branch := strings.TrimPrefix(line, "branch ")
			current.Branch = strings.TrimPrefix(branch, "refs/heads/")
		case line == "bare":
			current.IsBare = true
		case line == "":
			// blank line separates entries; handled by next "worktree " prefix
		}
	}

	// Append the last entry
	if !isFirst {
		worktrees = append(worktrees, current)
	}

	// Mark first non-bare entry as main
	if len(worktrees) > 0 && !worktrees[0].IsBare {
		worktrees[0].IsMain = true
	}

	// Filter out bare entries
	filtered := worktrees[:0]
	for _, wt := range worktrees {
		if !wt.IsBare {
			filtered = append(filtered, wt)
		}
	}

	return filtered, nil
}

// Clone clones a repository to the given destination path.
func (c *CLIClient) Clone(url, destPath string) error {
	cmd := exec.Command("git", "clone", url, destPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone: %s", stderr.String())
	}
	return nil
}

// WorktreeAdd adds a new worktree. If create is true, creates a new branch.
func (c *CLIClient) WorktreeAdd(repoPath, wtPath, branch string, create bool, startPoint string) error {
	args := []string{"worktree", "add"}
	if create {
		args = append(args, "-b", branch, wtPath)
		if startPoint != "" {
			args = append(args, startPoint)
		}
	} else {
		args = append(args, wtPath, branch)
	}
	_, err := c.runInPath(repoPath, args...)
	return err
}

// Fetch runs git fetch --all --prune in the given repo.
func (c *CLIClient) Fetch(repoPath string) error {
	_, err := c.runInPath(repoPath, "fetch", "--all", "--prune")
	return err
}

// DefaultBranch returns the default branch name (e.g. main or master).
func (c *CLIClient) DefaultBranch(repoPath string) (string, error) {
	// Try symbolic-ref first
	output, err := c.runInPath(repoPath, "symbolic-ref", "refs/remotes/origin/HEAD", "--short")
	if err == nil {
		branch := strings.TrimSpace(output)
		// Strip "origin/" prefix
		branch = strings.TrimPrefix(branch, "origin/")
		if branch != "" {
			return branch, nil
		}
	}
	// Fallback: check if main or master exists
	for _, candidate := range []string{"main", "master"} {
		exists, err := c.BranchExists(repoPath, candidate)
		if err == nil && exists {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not determine default branch")
}

// FetchPRRef fetches a pull request head ref into a local branch.
// It tries the upstream remote first (for fork setups), then falls back to origin.
func (c *CLIClient) FetchPRRef(repoPath string, prNumber int, localBranch string) error {
	refspec := fmt.Sprintf("pull/%d/head:%s", prNumber, localBranch)
	// Try upstream first (common in fork setups where PRs live on the upstream repo)
	_, err := c.runInPath(repoPath, "fetch", "upstream", refspec)
	if err == nil {
		return nil
	}
	_, err = c.runInPath(repoPath, "fetch", "origin", refspec)
	return err
}

// BranchExists checks if a branch exists locally or on origin.
func (c *CLIClient) BranchExists(repoPath, branch string) (bool, error) {
	// Check local
	_, err := c.runInPath(repoPath, "rev-parse", "--verify", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	// Check remote
	_, err = c.runInPath(repoPath, "rev-parse", "--verify", "refs/remotes/origin/"+branch)
	if err == nil {
		return true, nil
	}
	return false, nil
}

// RemoveWorktree removes a worktree at the given path.
func (c *CLIClient) RemoveWorktree(repoPath, wtPath string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, wtPath)
	_, err := c.runInPath(repoPath, args...)
	return err
}

// DeleteBranch deletes a local branch. Use force=true for -D (force delete).
func (c *CLIClient) DeleteBranch(repoPath, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := c.runInPath(repoPath, "branch", flag, branch)
	return err
}

// PruneWorktrees runs git worktree prune to clean up stale worktree references.
func (c *CLIClient) PruneWorktrees(repoPath string) error {
	_, err := c.runInPath(repoPath, "worktree", "prune")
	return err
}

// ListLocalBranches returns a list of all local branches in the repository.
// Checkout switches the working directory at repoPath to the given branch.
func (c *CLIClient) Checkout(repoPath, branch string) error {
	_, err := c.runInPath(repoPath, "checkout", branch)
	return err
}

func (c *CLIClient) ListLocalBranches(repoPath string) ([]string, error) {
	output, err := c.runInPath(repoPath, "branch", "--format=%(refname:short)")
	if err != nil {
		return nil, fmt.Errorf("failed to list branches: %w", err)
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			branches = append(branches, line)
		}
	}
	return branches, nil
}

// IsBranchMerged reports whether branch has been fully merged into base.
func (c *CLIClient) IsBranchMerged(repoPath, branch, base string) (bool, error) {
	output, err := c.runInPath(repoPath, "branch", "--merged", base)
	if err != nil {
		return false, fmt.Errorf("failed to check merged branches: %w", err)
	}
	for _, line := range strings.Split(output, "\n") {
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "* "))
		if name == branch {
			return true, nil
		}
	}
	return false, nil
}

// parseDiffNumstat parses git diff --numstat output and adds to stat.
func parseDiffNumstat(output string, stat *models.DiffStat) {
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		// Binary files show "-" for insertions/deletions
		if fields[0] == "-" || fields[1] == "-" {
			stat.FilesChanged++
			continue
		}
		var ins, del int
		fmt.Sscanf(fields[0], "%d", &ins)
		fmt.Sscanf(fields[1], "%d", &del)
		stat.Insertions += ins
		stat.Deletions += del
		stat.FilesChanged++
	}
}

// RunInPathWithContext executes a git command in a specified path with context support.
func (c *CLIClient) RunInPathWithContext(ctx context.Context, path string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = path

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), ctx.Err())
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), stderr.String())
	}

	return stdout.String(), nil
}

// getRootDir returns the repository root directory.
func (c *CLIClient) getRootDir(repoPath string) (string, error) {
	output, err := c.runInPath(repoPath, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("failed to get repository root: %w", err)
	}
	return strings.TrimSpace(output), nil
}

// run executes a git command in the working directory.
func (c *CLIClient) run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if c.workDir != "" {
		cmd.Dir = c.workDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), stderr.String())
	}

	return stdout.String(), nil
}

// runInPath executes a git command in a specified path.
func (c *CLIClient) runInPath(path string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = path

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), stderr.String())
	}

	return stdout.String(), nil
}

// RunWithContext executes a git command with context support.
func (c *CLIClient) RunWithContext(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if c.workDir != "" {
		cmd.Dir = c.workDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), ctx.Err())
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), stderr.String())
	}

	return stdout.String(), nil
}
