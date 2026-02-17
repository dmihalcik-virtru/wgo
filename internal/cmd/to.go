package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/git"
	gh "github.com/virtru/wgo/internal/github"
)

var toCmd = &cobra.Command{
	Use:   "to <github-url>",
	Short: "Jump to a local checkout of a GitHub PR, branch, or issue",
	Long: `Given a GitHub URL, resolves it to a local worktree path.

Supports PR URLs, branch URLs, and issue URLs. If no local checkout
exists, clones the repo and creates a worktree automatically.

Output goes to stdout so you can use it with cd:
  cd $(wgo to https://github.com/owner/repo/pull/42)`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTo(args[0])
	},
	SilenceUsage: true,
}

func init() {
	rootCmd.AddCommand(toCmd)
}

// log prints to stderr so stdout stays clean for cd $(...).
func logTo(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func runTo(rawURL string) error {
	// 1. Parse URL
	parsed, err := gh.ParseGitHubURL(rawURL)
	if err != nil {
		return err
	}

	// 2. Resolve branch name
	branch, err := resolveBranch(parsed)
	if err != nil {
		return err
	}

	logTo("resolved branch: %s", branch)

	// 3. Load config for discovery
	if err := config.Init(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg := config.Get()

	gitClient := git.New("")

	// 4. Search for existing checkout
	existing, err := findExistingCheckout(gitClient, cfg, parsed.Owner, parsed.Repo, branch)
	if err == nil && existing != "" {
		logTo("found existing checkout")
		fmt.Println(existing)
		return nil
	}

	// 5. Find or clone the repo
	repoPath, err := findOrCloneRepo(gitClient, cfg, parsed.Owner, parsed.Repo)
	if err != nil {
		return err
	}

	// 6. Fetch latest (best-effort)
	logTo("fetching latest...")
	_ = gitClient.Fetch(repoPath)

	// 7. Create worktree
	wtPath, err := createWorktree(gitClient, repoPath, cfg, parsed, branch)
	if err != nil {
		return err
	}

	fmt.Println(wtPath)
	return nil
}

func resolveBranch(parsed *gh.ParsedURL) (string, error) {
	switch parsed.Type {
	case gh.URLTypePR:
		num, _ := strconv.Atoi(parsed.Identifier)
		logTo("looking up PR #%d...", num)
		branch, err := gh.PRBranch(parsed.Owner, parsed.Repo, num)
		if err != nil {
			return "", fmt.Errorf("failed to resolve PR branch: %w", err)
		}
		return branch, nil

	case gh.URLTypeBranch:
		if parsed.Identifier == "" {
			return "main", nil // default branch placeholder
		}
		return parsed.Identifier, nil

	case gh.URLTypeIssue:
		num, _ := strconv.Atoi(parsed.Identifier)
		logTo("looking up issue #%d...", num)
		title, err := gh.IssueTitle(parsed.Owner, parsed.Repo, num)
		if err != nil {
			return "", fmt.Errorf("failed to resolve issue title: %w", err)
		}
		return gh.IssueBranchName(num, title), nil

	default:
		return "", fmt.Errorf("unsupported URL type")
	}
}

// findExistingCheckout searches discovered repos for one that matches the
// owner/repo and has the branch checked out.
func findExistingCheckout(gitClient *git.CLIClient, cfg *config.Config, owner, repo, branch string) (string, error) {
	disc := discovery.New(cfg.Discovery.BaseDirs, cfg.Discovery.ScanDepth, cfg.Discovery.ExcludePatterns)
	repos, err := disc.DiscoverAll()
	if err != nil {
		return "", err
	}

	for _, r := range repos {
		if !matchesRemote(gitClient, r.Path, owner, repo) {
			continue
		}

		// Check current branch
		cur, err := gitClient.CurrentBranch(r.Path)
		if err == nil && cur == branch {
			return r.Path, nil
		}

		// Check worktrees
		worktrees, err := gitClient.ListWorktrees(r.Path)
		if err != nil {
			continue
		}
		for _, wt := range worktrees {
			if wt.Branch == branch {
				return wt.Path, nil
			}
		}
	}

	return "", fmt.Errorf("not found")
}

// matchesRemote checks if any of a repo's remotes match the given owner/repo.
// This handles fork setups where origin is a fork and upstream is the canonical repo.
func matchesRemote(gitClient *git.CLIClient, repoPath, owner, repo string) bool {
	target := owner + "/" + repo
	remoteURLs, err := gitClient.RemoteURLs(repoPath)
	if err != nil {
		return false
	}
	for _, remoteURL := range remoteURLs {
		// Handle HTTPS (github.com/owner/repo.git) and SSH (git@github.com:owner/repo.git)
		remoteURL = strings.TrimSuffix(remoteURL, ".git")
		if strings.HasSuffix(remoteURL, target) {
			return true
		}
	}
	return false
}

// findOrCloneRepo locates an existing clone or creates one.
func findOrCloneRepo(gitClient *git.CLIClient, cfg *config.Config, owner, repo string) (string, error) {
	// Search existing repos
	disc := discovery.New(cfg.Discovery.BaseDirs, cfg.Discovery.ScanDepth, cfg.Discovery.ExcludePatterns)
	repos, err := disc.DiscoverAll()
	if err == nil {
		for _, r := range repos {
			if matchesRemote(gitClient, r.Path, owner, repo) {
				logTo("using existing clone: %s", r.Path)
				return r.Path, nil
			}
		}
	}

	// Clone into first base dir
	if len(cfg.Discovery.BaseDirs) == 0 {
		return "", fmt.Errorf("no base_dirs configured; cannot clone")
	}

	baseDir := cfg.Discovery.BaseDirs[0]
	destPath := filepath.Join(baseDir, owner, repo)

	// Check if destPath already exists as a repo (not found by discovery
	// due to path structure, e.g. missing owner directory level)
	if _, err := os.Stat(destPath); err == nil {
		isRepo, _ := gitClient.IsRepo(destPath)
		if isRepo {
			logTo("using existing repo at: %s", destPath)
			return destPath, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	cloneURL := gh.RepoCloneURL(owner, repo)
	logTo("cloning %s...", cloneURL)
	if err := gitClient.Clone(cloneURL, destPath); err != nil {
		return "", fmt.Errorf("clone failed: %w", err)
	}

	return destPath, nil
}

// createWorktree creates a new worktree for the given branch.
func createWorktree(gitClient *git.CLIClient, repoPath string, cfg *config.Config, parsed *gh.ParsedURL, branch string) (string, error) {
	baseDir := cfg.Discovery.BaseDirs[0]
	sanitized := gh.SanitizeBranch(branch)
	wtPath := filepath.Join(baseDir, sanitized, parsed.Repo)

	// Check if path already exists (e.g. from a previous run)
	if info, err := os.Stat(wtPath); err == nil && info.IsDir() {
		logTo("worktree path already exists")
		return wtPath, nil
	}

	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return "", fmt.Errorf("failed to create worktree parent: %w", err)
	}

	switch parsed.Type {
	case gh.URLTypeIssue:
		// New branch off default branch
		defaultBranch, err := gitClient.DefaultBranch(repoPath)
		if err != nil {
			defaultBranch = "main"
		}
		logTo("creating worktree with new branch %s from origin/%s...", branch, defaultBranch)
		if err := gitClient.WorktreeAdd(repoPath, wtPath, branch, true, "origin/"+defaultBranch); err != nil {
			return "", fmt.Errorf("worktree add failed: %w", err)
		}

	case gh.URLTypePR, gh.URLTypeBranch:
		// Check if branch exists on remote
		exists, _ := gitClient.BranchExists(repoPath, branch)
		if !exists && parsed.Type == gh.URLTypePR {
			// Branch may be from a fork; fetch the PR ref directly
			num, _ := strconv.Atoi(parsed.Identifier)
			logTo("branch not on origin, fetching PR #%d ref...", num)
			if err := gitClient.FetchPRRef(repoPath, num, branch); err != nil {
				return "", fmt.Errorf("branch %q not found and PR #%d fetch failed: %w", branch, num, err)
			}
			exists = true
		}
		if !exists {
			return "", fmt.Errorf("branch %q not found locally or on origin", branch)
		}
		logTo("creating worktree for branch %s...", branch)
		if err := gitClient.WorktreeAdd(repoPath, wtPath, branch, false, ""); err != nil {
			return "", fmt.Errorf("worktree add failed: %w", err)
		}

	default:
		return "", fmt.Errorf("unsupported URL type for worktree creation")
	}

	return wtPath, nil
}
