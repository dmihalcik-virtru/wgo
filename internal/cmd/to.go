package cmd

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/git"
	gh "github.com/virtru/wgo/internal/github"
)

var toCmd = &cobra.Command{
	Use:   "to <github-url|owner/repo[@branch]>",
	Short: "Jump to a local checkout of a GitHub PR, branch, or issue",
	Long: `Given a GitHub URL or short owner/repo form, resolves it to a local worktree path.

Supports PR URLs, branch URLs, and issue URLs. Also accepts short forms:
  owner/repo          → local checkout of that repo
  owner/repo@branch   → specific branch/worktree

If no local checkout exists, clones the repo and creates a worktree automatically.

Output goes to stdout so you can use it with cd:
  cd $(wgo to https://github.com/owner/repo/pull/42)
  cd $(wgo to owner/repo@my-branch)`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: toCompletions,
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

// isGitHubURL reports whether the argument looks like a full URL.
func isGitHubURL(s string) bool {
	return strings.Contains(s, "://") || strings.HasPrefix(s, "git@")
}

// toCompletions provides shell completions for `wgo to`.
// It discovers all local repos, extracts owner/repo from remotes, and returns
// completions sorted by recency (most recently committed first).
func toCompletions(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	if err := config.Init(); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg := config.Get()

	disc := discovery.New(cfg.Discovery.BaseDirs, cfg.Discovery.ScanDepth, cfg.Discovery.ExcludePatterns)
	repos, err := disc.DiscoverAll()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	gitClient := git.New("")

	type candidate struct {
		completion string
		score      float64
	}

	var repoPrefix, branchPrefix string
	hasAt := false
	if rp, bp, ok := strings.Cut(toComplete, "@"); ok {
		hasAt = true
		repoPrefix = rp
		branchPrefix = bp
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	var candidates []candidate
	seen := make(map[string]bool) // deduplicate by completion key

	now := time.Now()

	for _, r := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()

			remoteURLs, err := gitClient.RemoteURLs(r.Path)
			if err != nil || len(remoteURLs) == 0 {
				return
			}

			// Extract owner/repo from remote URL
			ownerRepo := extractOwnerRepo(remoteURLs[0])
			if ownerRepo == "" {
				return
			}

			// Apply prefix filter
			if toComplete != "" {
				if hasAt {
					if ownerRepo != repoPrefix {
						return
					}
				} else {
					if !strings.HasPrefix(ownerRepo, toComplete) {
						return
					}
				}
			}

			// Score by last commit recency
			var score float64
			commit, err := gitClient.LastCommit(r.Path)
			if err == nil {
				age := now.Sub(commit.Date)
				ageDays := age.Hours() / 24
				switch {
				case ageDays < 1:
					score += 30
				case ageDays < 7:
					score += 15
				case ageDays < 30:
					score += 5
				}
			}

			// Bonus for uncommitted changes
			status, err := gitClient.Status(r.Path)
			if err == nil && (status.Modified > 0 || status.Staged > 0 || status.Untracked > 0) {
				score += 40
			}

			mu.Lock()
			defer mu.Unlock()

			// Base completion: owner/repo (only when not filtering by branch)
			if !hasAt {
				branch, _ := gitClient.CurrentBranch(r.Path)
				desc := r.Path
				if branch != "" {
					desc = fmt.Sprintf("%s (%s)", r.Path, branch)
				}
				key := ownerRepo
				if !seen[key] {
					seen[key] = true
					candidates = append(candidates, candidate{ownerRepo + "\t" + desc, score})
				}
			}

			// Also add worktree-specific completions for non-main branches
			worktrees, wtErr := gitClient.ListWorktrees(r.Path)
			if wtErr == nil {
				for _, wt := range worktrees {
					if wt.Branch == "" || wt.IsMain {
						continue
					}
					// Apply branch prefix filter if present
					if hasAt && !strings.HasPrefix(wt.Branch, branchPrefix) {
						continue
					}

					key := ownerRepo + "@" + wt.Branch
					if seen[key] {
						continue
					}
					seen[key] = true

					// Worktree score: slightly decay non-main by recency
					wtScore := score * math.Exp(-0.1)
					wtCommit, err := gitClient.LastCommit(wt.Path)
					if err == nil {
						ageDays := now.Sub(wtCommit.Date).Hours() / 24
						switch {
						case ageDays < 1:
							wtScore = 30
						case ageDays < 7:
							wtScore = 15
						case ageDays < 30:
							wtScore = 5
						}
					}
					candidates = append(candidates, candidate{key + "\t" + wt.Path, wtScore})
				}
			}
		}()
	}

	wg.Wait()

	// Sort by score descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	completions := make([]string, len(candidates))
	for i, c := range candidates {
		completions[i] = c.completion
	}
	return completions, cobra.ShellCompDirectiveNoFileComp
}

// extractOwnerRepo extracts "owner/repo" from a GitHub remote URL.
func extractOwnerRepo(remoteURL string) string {
	remoteURL = strings.TrimSuffix(remoteURL, ".git")
	// SSH: git@github.com:owner/repo
	if strings.HasPrefix(remoteURL, "git@") {
		if _, after, ok := strings.Cut(remoteURL, ":"); ok {
			return after
		}
	}
	// HTTPS: https://github.com/owner/repo
	if _, after, ok := strings.Cut(remoteURL, "github.com/"); ok {
		return after
	}
	return ""
}

func runTo(rawURL string) error {
	// Short-form: owner/repo or owner/repo@branch
	if !isGitHubURL(rawURL) {
		return runToLocal(rawURL)
	}

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
			logTo("no branch specified, querying GitHub for default branch...")
			branch, err := gh.RepoDefaultBranch(parsed.Owner, parsed.Repo)
			if err != nil {
				return "", fmt.Errorf("could not determine default branch: %w", err)
			}
			return branch, nil
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

// runToLocal handles short-form args like "owner/repo" or "owner/repo@branch".
func runToLocal(short string) error {
	owner, repo, branch := "", "", ""

	if before, after, ok := strings.Cut(short, "@"); ok {
		branch = after
		short = before
	}

	parts := strings.SplitN(short, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid short form %q: expected owner/repo[@branch]", short)
	}
	owner, repo = parts[0], parts[1]

	if err := config.Init(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg := config.Get()
	gitClient := git.New("")

	if branch != "" {
		existing, err := findExistingCheckout(gitClient, cfg, owner, repo, branch)
		if err == nil && existing != "" {
			logTo("found existing checkout")
			fmt.Println(existing)
			return nil
		}
		// Fall back to constructing a GitHub URL
		rawURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", owner, repo, branch)
		return runTo(rawURL)
	}

	// No branch: find any checkout of owner/repo
	disc := discovery.New(cfg.Discovery.BaseDirs, cfg.Discovery.ScanDepth, cfg.Discovery.ExcludePatterns)
	repos, err := disc.DiscoverAll()
	if err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	for _, r := range repos {
		if matchesRemote(gitClient, r.Path, owner, repo) {
			logTo("found existing checkout")
			fmt.Println(r.Path)
			return nil
		}
	}

	// Not found locally; clone it
	rawURL := fmt.Sprintf("https://github.com/%s/%s", owner, repo)
	return runTo(rawURL)
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

	// Clone into mains_dir
	if cfg.Worktree.MainsDir == "" {
		return "", fmt.Errorf("worktree.mains_dir not configured; cannot clone")
	}

	destPath := filepath.Join(cfg.Worktree.MainsDir, owner, repo)

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
	sanitized := gh.SanitizeBranch(branch)
	wtPath := filepath.Join(cfg.Worktree.WorktreesDir, sanitized, parsed.Repo)

	// Check if path already exists (e.g. from a previous run)
	if info, err := os.Stat(wtPath); err == nil && info.IsDir() {
		cur, err := gitClient.CurrentBranch(wtPath)
		if err == nil && cur != branch {
			logTo("worktree exists on branch %q, switching to %q...", cur, branch)
			if err := gitClient.Checkout(wtPath, branch); err != nil {
				logTo("warning: could not switch branch: %v", err)
			}
		} else {
			logTo("worktree path already exists")
		}
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
