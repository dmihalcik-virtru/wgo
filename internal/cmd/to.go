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
	gh "github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
)

var toOnParent string

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
	toCmd.Flags().StringVar(&toOnParent, "on", "",
		"For new branches: base the new worktree on this in-flight branch (records it as the stack parent). Ignored when the target branch already exists.")
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

	jjc := jj.NewCLI()

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

			remotes, err := jjc.RemoteURLs(r.Path)
			if err != nil || len(remotes) == 0 {
				return
			}
			// Prefer origin, fall back to any.
			remoteURL := remotes["origin"]
			if remoteURL == "" {
				for _, u := range remotes {
					remoteURL = u
					break
				}
			}
			ownerRepo := extractOwnerRepo(remoteURL)
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

			// Score by last commit recency on the workspace's @-.
			var score float64
			if entries, err := jjc.Log(r.Path, "@-"); err == nil && len(entries) > 0 {
				age := now.Sub(entries[0].AuthorTimestamp)
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
			if status, err := jjc.Status(r.Path); err == nil && !status.Clean {
				score += 40
			}

			mu.Lock()
			defer mu.Unlock()

			// Base completion: owner/repo (only when not filtering by branch)
			if !hasAt {
				branch := currentBookmark(jjc, r.Path)
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

			// Also add workspace-specific completions for non-default workspaces.
			workspaces, wsErr := jjc.ListWorkspaces(r.Path)
			if wsErr == nil {
				for _, ws := range workspaces {
					if ws.Name == "default" {
						continue
					}
					wsBookmark := currentBookmark(jjc, ws.Path)
					if wsBookmark == "" {
						continue
					}
					if hasAt && !strings.HasPrefix(wsBookmark, branchPrefix) {
						continue
					}

					key := ownerRepo + "@" + wsBookmark
					if seen[key] {
						continue
					}
					seen[key] = true

					// Workspace score: slightly decay by recency
					wsScore := score * math.Exp(-0.1)
					if entries, err := jjc.Log(ws.Path, "@-"); err == nil && len(entries) > 0 {
						ageDays := now.Sub(entries[0].AuthorTimestamp).Hours() / 24
						switch {
						case ageDays < 1:
							wsScore = 30
						case ageDays < 7:
							wsScore = 15
						case ageDays < 30:
							wsScore = 5
						}
					}
					candidates = append(candidates, candidate{key + "\t" + ws.Path, wsScore})
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

	jjc := jj.NewCLI()

	// 4. Search for existing checkout
	existing, err := findExistingCheckout(jjc, cfg, parsed.Owner, parsed.Repo, branch)
	if err == nil && existing != "" {
		logTo("found existing checkout")
		fmt.Println(existing)
		return nil
	}

	// 5. Find or clone the repo
	repoPath, err := findOrCloneRepo(jjc, cfg, parsed.Owner, parsed.Repo)
	if err != nil {
		return err
	}

	// 6. Fetch latest (best-effort)
	logTo("fetching latest...")
	_ = jjc.GitFetch(repoPath, "", nil)

	// 7. Create workspace
	wtPath, err := createWorktree(jjc, repoPath, cfg, parsed, branch)
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
	jjc := jj.NewCLI()

	if branch != "" {
		existing, err := findExistingCheckout(jjc, cfg, owner, repo, branch)
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
		if matchesRemote(jjc, r.Path, owner, repo) {
			logTo("found existing checkout")
			fmt.Println(r.Path)
			return nil
		}
	}

	// Not found locally; clone it
	rawURL := fmt.Sprintf("https://github.com/%s/%s", owner, repo)
	return runTo(rawURL)
}

// currentBookmark returns the first local bookmark on the workspace's @
// change, or "" if there is none. Used as the jj-side equivalent of git's
// "current branch" concept.
func currentBookmark(jjc jj.Client, workspacePath string) string {
	ch, err := jjc.CurrentChange(workspacePath)
	if err != nil {
		return ""
	}
	if len(ch.Bookmarks) > 0 {
		return ch.Bookmarks[0]
	}
	return ""
}

// findExistingCheckout searches discovered repos for one whose origin
// matches owner/repo and has a workspace whose @ carries the named bookmark.
func findExistingCheckout(jjc jj.Client, cfg *config.Config, owner, repo, branch string) (string, error) {
	disc := discovery.New(cfg.Discovery.BaseDirs, cfg.Discovery.ScanDepth, cfg.Discovery.ExcludePatterns)
	repos, err := disc.DiscoverAll()
	if err != nil {
		return "", err
	}

	for _, r := range repos {
		if !matchesRemote(jjc, r.Path, owner, repo) {
			continue
		}

		// Check the workspace at r.Path itself first.
		if currentBookmark(jjc, r.Path) == branch {
			return r.Path, nil
		}

		// Then check sibling workspaces.
		workspaces, err := jjc.ListWorkspaces(r.Path)
		if err != nil {
			continue
		}
		for _, ws := range workspaces {
			if currentBookmark(jjc, ws.Path) == branch {
				return ws.Path, nil
			}
		}
	}

	return "", fmt.Errorf("not found")
}

// matchesRemote checks if any of a repo's remotes match the given owner/repo.
// This handles fork setups where origin is a fork and upstream is the canonical repo.
func matchesRemote(jjc jj.Client, repoPath, owner, repo string) bool {
	target := owner + "/" + repo
	remotes, err := jjc.RemoteURLs(repoPath)
	if err != nil {
		return false
	}
	for _, remoteURL := range remotes {
		// Handle HTTPS (github.com/owner/repo.git) and SSH (git@github.com:owner/repo.git)
		remoteURL = strings.TrimSuffix(remoteURL, ".git")
		if strings.HasSuffix(remoteURL, target) {
			return true
		}
	}
	return false
}

// findOrCloneRepo locates an existing clone or creates one.
func findOrCloneRepo(jjc jj.Client, cfg *config.Config, owner, repo string) (string, error) {
	// Search existing repos
	disc := discovery.New(cfg.Discovery.BaseDirs, cfg.Discovery.ScanDepth, cfg.Discovery.ExcludePatterns)
	repos, err := disc.DiscoverAll()
	if err == nil {
		for _, r := range repos {
			if matchesRemote(jjc, r.Path, owner, repo) {
				mainPath := r.Path
				if r.IsWorktree && r.MainRepoPath != "" {
					mainPath = r.MainRepoPath
				}
				logTo("using existing clone: %s", mainPath)
				return mainPath, nil
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
		if jjc.IsRepo(destPath) {
			logTo("using existing repo at: %s", destPath)
			return destPath, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	cloneURL := gh.RepoCloneURL(owner, repo)
	logTo("cloning %s...", cloneURL)
	if err := jjc.GitClone(cloneURL, destPath); err != nil {
		return "", fmt.Errorf("clone failed: %w", err)
	}

	return destPath, nil
}

// createWorktree creates a new jj workspace for the given branch.
//
// Layout per gh-21:
//   - URLTypeIssue / URLTypeBranch: <worktrees_dir>/<sanitized-branch>/<repo>
//   - URLTypePR:                    <worktrees_dir>/pr-<N>-<sanitized-headRef>/<owner>/<repo>
//
// The PR layout encodes both the PR number and the head ref so two PRs that
// share a branch name can coexist on disk.
func createWorktree(jjc jj.Client, repoPath string, cfg *config.Config, parsed *gh.ParsedURL, branch string) (string, error) {
	var wtPath string
	if parsed.Type == gh.URLTypePR {
		wtPath = filepath.Join(cfg.Worktree.WorktreesDir,
			"pr-"+parsed.Identifier+"-"+gh.SanitizeBranch(branch),
			parsed.Owner, parsed.Repo)
	} else {
		wtPath = filepath.Join(cfg.Worktree.WorktreesDir, gh.SanitizeBranch(branch), parsed.Repo)
	}

	// Check if path already exists (e.g. from a previous run).
	if info, err := os.Stat(wtPath); err == nil && info.IsDir() {
		if currentBookmark(jjc, wtPath) != branch {
			logTo("workspace exists at %s, moving @ to %s...", wtPath, branch)
			if err := jjc.EditChange(wtPath, branch); err != nil {
				logTo("warning: could not move to %s: %v", branch, err)
			}
		} else {
			logTo("workspace path already exists")
		}
		return wtPath, nil
	}

	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		return "", fmt.Errorf("failed to create workspace parent: %w", err)
	}

	switch parsed.Type {
	case gh.URLTypeIssue:
		startPoint := ""
		if toOnParent != "" {
			if !bookmarkExists(jjc, repoPath, toOnParent) {
				return "", fmt.Errorf("--on parent %q not found locally or on origin", toOnParent)
			}
			startPoint = toOnParent
			logTo("creating workspace with new bookmark %s on top of %s...", branch, toOnParent)
		} else {
			defaultBranch, err := defaultBranchFor(jjc, repoPath)
			if err != nil {
				defaultBranch = "main"
			}
			startPoint = "origin/" + defaultBranch
			logTo("creating workspace with new bookmark %s from %s...", branch, startPoint)
		}
		if err := jjc.WorkspaceAdd(repoPath, branch, wtPath, startPoint); err != nil {
			return "", fmt.Errorf("workspace add failed: %w", err)
		}
		if err := jjc.BookmarkCreate(repoPath, branch, startPoint); err != nil {
			return "", fmt.Errorf("create bookmark %s: %w", branch, err)
		}
		if toOnParent != "" {
			if err := recordStackParent(repoPath, branch, toOnParent); err != nil {
				logTo("error: %v", err)
				logTo("workspace created but stack parent not recorded")
			}
		}

	case gh.URLTypePR:
		num, _ := strconv.Atoi(parsed.Identifier)
		logTo("looking up PR #%d head ref...", num)
		info, err := gh.GetPRHeadRef(parsed.Owner+"/"+parsed.Repo, num)
		if err != nil {
			return "", err
		}
		baseSlug := parsed.Owner + "/" + parsed.Repo
		bookmark := fmt.Sprintf("pr-%d-%s", num, gh.SanitizeBranch(info.Ref))

		if info.RepoSlug == "" || info.RepoSlug == baseSlug {
			// Same-repo PR: fetch from origin.
			logTo("fetching origin/%s...", info.Ref)
			if err := jjc.GitFetch(repoPath, "origin", []string{info.Ref}); err != nil {
				return "", fmt.Errorf("fetch %s from origin: %w", info.Ref, err)
			}
		} else {
			// Fork PR: add the fork as a temporary remote, fetch, then
			// leave the remote in place (it's harmless and lets the user
			// re-fetch without re-adding).
			forkRemote := "pr-" + parsed.Identifier + "-fork"
			forkURL := "https://github.com/" + info.RepoSlug + ".git"
			logTo("PR is from fork %s; adding remote %s...", info.RepoSlug, forkRemote)
			if err := jjc.GitRemoteAdd(repoPath, forkRemote, forkURL); err != nil {
				// Remote already exists is non-fatal; jj returns it as
				// stderr text. Try to fetch anyway.
				logTo("warning: add remote %s: %v", forkRemote, err)
			}
			if err := jjc.GitFetch(repoPath, forkRemote, []string{info.Ref}); err != nil {
				return "", fmt.Errorf("fetch %s from %s: %w", info.Ref, forkRemote, err)
			}
		}

		// Pin the bookmark to the exact head OID so subsequent advancement
		// on the remote does not surprise the local workspace.
		if err := jjc.BookmarkCreate(repoPath, bookmark, info.OID); err != nil {
			return "", fmt.Errorf("create bookmark %s at %s: %w", bookmark, info.OID, err)
		}
		logTo("creating workspace at bookmark %s...", bookmark)
		if err := jjc.WorkspaceAdd(repoPath, bookmark, wtPath, bookmark); err != nil {
			return "", fmt.Errorf("workspace add failed: %w", err)
		}

	case gh.URLTypeBranch:
		if !bookmarkExists(jjc, repoPath, branch) {
			return "", fmt.Errorf("branch %q not found locally or on origin", branch)
		}
		logTo("creating workspace for branch %s...", branch)
		if err := jjc.WorkspaceAdd(repoPath, branch, wtPath, branch); err != nil {
			return "", fmt.Errorf("workspace add failed: %w", err)
		}

	default:
		return "", fmt.Errorf("unsupported URL type for workspace creation")
	}

	return wtPath, nil
}

// bookmarkExists returns true when a bookmark of name exists locally or on
// any remote of repo.
func bookmarkExists(jjc jj.Client, repo, name string) bool {
	bms, err := jjc.BookmarkList(repo, jj.BookmarkListOpts{AllRemotes: true, Names: []string{name}})
	if err != nil {
		return false
	}
	for _, b := range bms {
		if b.Name == name {
			return true
		}
	}
	// Also accept a remote-tracking ref written as "origin/name".
	bms, err = jjc.BookmarkList(repo, jj.BookmarkListOpts{AllRemotes: true})
	if err != nil {
		return false
	}
	for _, b := range bms {
		if b.Name == name {
			return true
		}
	}
	return false
}

// recordStackParent used to persist a parent link in state.json so the new
// worktree participated in restack/sync. After the jj migration this is
// expressed in the jj DAG directly — `jj workspace add -r <parent-bookmark>`
// produces a child commit whose parent is the parent bookmark's tip — so
// the wgo annotation no longer carries Parents/StackID. This function is
// now a no-op kept to preserve --on's call site; the parent linkage lives
// in jj.
func recordStackParent(_, _, _ string) error {
	return nil
}
