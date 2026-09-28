package cmd

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
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
	"github.com/virtru/wgo/internal/stack"
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
		"Land the workspace on this node. For a PR checkout: an interior stack member (or any existing bookmark) to fork from. For a new issue branch: the in-flight parent to base on.")
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

	disc := discovery.FromConfig(cfg)
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

	// 2. Load config for discovery
	if err := config.Init(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg := config.Get()

	jjc := jj.NewCLI()

	// Branch and bare-repo URLs may name the trunk, in which case the mains
	// clone already *is* that checkout and a <worktrees_dir>/<trunk>/<repo>
	// workspace would be a redundant second copy of the same commit. PR and
	// issue URLs can never name the trunk, so they keep the flow below.
	if parsed.Type == gh.URLTypeBranch {
		return runToBranch(jjc, cfg, parsed)
	}

	// 3. Resolve branch name
	branch, err := resolveBranch(parsed)
	if err != nil {
		return err
	}

	logTo("resolved branch: %s", branch)

	// 4. Search for existing checkout
	existing, err := findExistingCheckout(jjc, cfg, parsed.Owner, parsed.Repo, workspaceBookmarkNames(parsed, branch))
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

// runToBranch resolves a branch or bare-repo GitHub URL.
//
// The step order differs from runTo's, and the difference is the point: the
// repo is located *first* so trunk can be identified from local jj state, and
// the trunk check runs *before* findExistingCheckout. That ordering matters
// because findExistingCheckout matches any workspace whose @ carries the named
// bookmark — including the redundant <worktrees_dir>/<trunk>/<repo> workspaces
// earlier versions of wgo created for trunk URLs. Checking trunk first is what
// stops those from being handed back.
//
// Hoisting findOrCloneRepo is safe: it and findExistingCheckout scan the same
// DiscoverAll() with the same matchesRemote predicate, so anything the latter
// would have found the former finds too, and no clone happens spuriously.
func runToBranch(jjc jj.Client, cfg *config.Config, parsed *gh.ParsedURL) error {
	repoPath, err := findOrCloneRepo(jjc, cfg, parsed.Owner, parsed.Repo)
	if err != nil {
		return err
	}

	localTrunk := localTrunkBookmark(jjc, repoPath)

	branch := parsed.Identifier
	if branch == "" {
		branch, err = resolveDefaultBranch(jjc, repoPath, parsed.Owner, parsed.Repo)
		if err != nil {
			return err
		}
	}
	logTo("resolved branch: %s", branch)

	isTrunk := isTrunkTarget(branch, localTrunk, func() (string, error) {
		return defaultBranchFor(jjc, repoPath)
	})
	if isTrunk {
		if toOnParent != "" {
			logTo("warning: --on is ignored for a trunk checkout")
		}
		logTo("fetching latest...")
		_ = jjc.GitFetch(repoPath, "", nil)
		return printMainsCheckout(jjc, cfg, repoPath, branch)
	}

	existing, err := findExistingCheckout(jjc, cfg, parsed.Owner, parsed.Repo, workspaceBookmarkNames(parsed, branch))
	if err == nil && existing != "" {
		logTo("found existing checkout")
		fmt.Println(existing)
		return nil
	}

	logTo("fetching latest...")
	_ = jjc.GitFetch(repoPath, "", nil)

	wtPath, err := createWorktree(jjc, repoPath, cfg, parsed, branch)
	if err != nil {
		return err
	}

	fmt.Println(wtPath)
	return nil
}

// resolveDefaultBranch answers "what is owner/repo's default branch" for a URL
// that named no ref.
//
// GitHub is the authority — a repo defaulting to "develop" while main@origin
// also exists would otherwise be misrouted — but when the API is unreachable
// the local jj trunk is a good enough answer, so a bare URL keeps working
// offline instead of hard-failing the way resolveBranch does.
func resolveDefaultBranch(jjc jj.Client, repoPath, owner, repo string) (string, error) {
	logTo("no branch specified, querying GitHub for default branch...")
	branch, err := gh.RepoDefaultBranch(owner, repo)
	if err == nil {
		return branch, nil
	}
	logTo("warning: could not reach GitHub (%v); falling back to local trunk", err)
	if local := localTrunkBookmark(jjc, repoPath); local != "" {
		return local, nil
	}
	return "", fmt.Errorf("could not determine default branch for %s/%s: GitHub said %w, "+
		"and %s has no local trunk bookmark; pass an explicit branch or run `jj -R %s git fetch`",
		owner, repo, err, repoPath, repoPath)
}

// printMainsCheckout prints repoPath — the repo's main workspace, which *is*
// the trunk checkout — on stdout.
//
// Two conditions are reported on stderr without being acted on: work stranded
// on the clone's @, and a leftover <worktrees_dir>/<trunk>/<repo> workspace
// from before trunk URLs routed here. Both are the user's to resolve; wgo does
// not modify their repos on a read-only lookup.
func printMainsCheckout(jjc jj.Client, cfg *config.Config, repoPath, branch string) error {
	// Preserved from createWorktree, which used to be the only caller: a legacy
	// main checkout gets colocated the first time wgo touches it.
	if enabled, err := jjc.EnsureColocated(repoPath); err != nil {
		logTo("warning: could not enable colocation for %s: %v", repoPath, err)
	} else if enabled {
		logTo("enabling colocation for %s...", repoPath)
	}

	// Reusing the park planner keeps `wgo to` and `wgo park` from ever
	// disagreeing about what counts as stranded work.
	if p, ok, err := planPark(jjc, cfg, repoPath, parkOpts{}); err == nil && ok {
		describeParkPlan(p, "warning: ")
		logTo("hint: run `wgo park %s` to move that work to its own workspace", repoPath)
	}

	if junk := redundantTrunkWorkspace(jjc, cfg, repoPath, branch); junk != "" {
		logTo("note: %s duplicates this checkout and is no longer used by `wgo to`", junk)
		logTo("      remove it with: jj -R %s workspace forget %s && rm -rf %s",
			repoPath, gh.SanitizeBranch(branch), junk)
	}

	fmt.Println(repoPath)
	return nil
}

// redundantTrunkWorkspace returns the path of a secondary workspace sitting at
// <worktrees_dir>/<sanitized-trunk>/<repo> — the layout pre-fix versions of
// `wgo to` produced for trunk URLs — or "" when there is none.
func redundantTrunkWorkspace(jjc jj.Client, cfg *config.Config, repoPath, trunk string) string {
	if cfg.Worktree.WorktreesDir == "" || trunk == "" {
		return ""
	}
	want := absResolved(filepath.Join(cfg.Worktree.WorktreesDir, gh.SanitizeBranch(trunk), filepath.Base(repoPath)))
	if want == "" {
		return ""
	}
	workspaces, err := jjc.ListWorkspaces(repoPath)
	if err != nil {
		return ""
	}
	for _, ws := range workspaces {
		if absResolved(ws.Path) == want {
			return ws.Path
		}
	}
	return ""
}

// resolveBranch maps a parsed URL to a branch name. URLTypeBranch is handled by
// runToBranch instead — it needs the local repo to identify trunk — so only the
// PR and issue arms are reachable from runTo.
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
		// Always delegate. Short-circuiting on findExistingCheckout here would
		// hand back a <worktrees_dir>/<trunk>/<repo> workspace for
		// `wgo to owner/repo@main`; runToBranch performs the same lookup after
		// deciding whether the branch is trunk.
		rawURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", owner, repo, branch)
		return runTo(rawURL)
	}

	// No branch: find any checkout of owner/repo
	disc := discovery.FromConfig(cfg)
	repos, err := disc.DiscoverAll()
	if err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	for _, r := range repos {
		if !matchesRemote(jjc, r.Path, owner, repo) {
			continue
		}
		// Discovery walk order is arbitrary, so the first match is often a
		// secondary workspace under worktrees_dir. "owner/repo" with no branch
		// means the repo itself, which is the main clone.
		mainPath := r.Path
		if r.IsWorktree && r.MainRepoPath != "" {
			mainPath = r.MainRepoPath
		}
		logTo("found existing checkout")
		return printMainsCheckout(jjc, cfg, mainPath, localTrunkBookmark(jjc, mainPath))
	}

	// Not found locally; clone it
	rawURL := fmt.Sprintf("https://github.com/%s/%s", owner, repo)
	return runTo(rawURL)
}

// currentBookmark returns the nearest local bookmark at or below the
// workspace's @, or "" if there is none. Used as the jj-side equivalent of
// git's "current branch" concept. jj's working copy is normally an empty
// change above the bookmark (which sits on @-), so this resolves through
// jj.NearestBookmark rather than inspecting @ alone.
func currentBookmark(jjc jj.Client, workspacePath string) string {
	bm, err := jjc.NearestBookmark(workspacePath)
	if err != nil {
		return ""
	}
	return bm
}

// workspaceBookmarkNames lists the local bookmark names a workspace for this
// target may be sitting on. A PR workspace lands on the head-ref bookmark when
// wgo could track it and on wgo's pin when it could not (protected ref,
// divergent local name, fork) — and which of those happened is not recoverable
// later, so every lookup that identifies a workspace by its bookmark has to
// accept both or it misses half the PR workspaces wgo creates.
//
// The result is in preference order, pin first: a local bookmark named after
// the head ref can also be the *divergent* one that forced the pin in the
// first place, and that one points somewhere else entirely.
func workspaceBookmarkNames(parsed *gh.ParsedURL, branch string) []string {
	var names []string
	if parsed.Type == gh.URLTypePR {
		if n, err := strconv.Atoi(parsed.Identifier); err == nil {
			names = append(names, prPinBookmark(n, branch))
		}
	}
	return append(names, branch)
}

// findExistingCheckout searches discovered repos for one whose origin
// matches owner/repo and has a workspace whose @ carries any of bookmarks.
func findExistingCheckout(jjc jj.Client, cfg *config.Config, owner, repo string, bookmarks []string) (string, error) {
	disc := discovery.FromConfig(cfg)
	repos, err := disc.DiscoverAll()
	if err != nil {
		return "", err
	}

	// currentBookmark yields "" for a workspace with no bookmark, which must
	// not match a caller that passed an empty name through.
	carries := func(path string) bool {
		cur := currentBookmark(jjc, path)
		return cur != "" && slices.Contains(bookmarks, cur)
	}

	for _, r := range repos {
		if !matchesRemote(jjc, r.Path, owner, repo) {
			continue
		}

		// Check the workspace at r.Path itself first.
		if carries(r.Path) {
			return r.Path, nil
		}

		// Then check sibling workspaces.
		workspaces, err := jjc.ListWorkspaces(r.Path)
		if err != nil {
			continue
		}
		for _, ws := range workspaces {
			if carries(ws.Path) {
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

// findOrCloneRepo locates an existing clone or creates one, falling back to an
// empty local repo when the clone fails.
func findOrCloneRepo(jjc jj.Client, cfg *config.Config, owner, repo string) (string, error) {
	return findOrCloneRepoOpt(jjc, cfg, owner, repo, true)
}

// findOrCloneRepoNoInit is findOrCloneRepo for callers that need a repository
// with real history behind it.
//
// `wgo to`, `add` and `join` want the empty-repo fallback: starting work
// against a GitHub repo that has no commits yet is a supported flow. A rig is
// the opposite. Every pin resolves through a tag revset, so an empty repo
// resolves nothing, and the planner cannot tell "this repo is unreachable"
// from "this tag was never fetched" — it reports the second and aborts the
// whole rig. The initialised repo also outlives the failure: `IsRepo(destPath)`
// short-circuits every later lookup for that slug, so the real clone never
// happens. Fail here instead, and let the caller record a skip.
func findOrCloneRepoNoInit(jjc jj.Client, cfg *config.Config, owner, repo string) (string, error) {
	return findOrCloneRepoOpt(jjc, cfg, owner, repo, false)
}

func findOrCloneRepoOpt(jjc jj.Client, cfg *config.Config, owner, repo string, initOnCloneFailure bool) (string, error) {
	// Search existing repos
	disc := discovery.FromConfig(cfg)
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
	_, statErr := os.Stat(destPath)
	preexisting := statErr == nil
	if preexisting {
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
		if !preexisting {
			// A failed clone can leave a partial destPath behind, which would
			// make `jj git init` fail — and would make the next lookup for this
			// slug find a broken repo. Only ever remove a path this call
			// created.
			_ = os.RemoveAll(destPath)
		}
		if !initOnCloneFailure {
			return "", fmt.Errorf("clone %s: %w", cloneURL, err)
		}
		// jj clones a commitless remote fine, so this is a fallback for the
		// cases it does not cover — non-GitHub remotes, older jj, a transient
		// failure. Reproduce what a successful clone of an empty repo yields:
		// a colocated repo with origin wired up.
		logTo("clone failed (%v); initialising an empty repo at %s", err, destPath)
		if initErr := jjc.GitInit(destPath, jj.InitOpts{}); initErr != nil {
			return "", fmt.Errorf("clone %s failed (%v) and `jj git init` fallback failed: %w; "+
				"try `jj git clone %s %s` manually", cloneURL, err, initErr, cloneURL, destPath)
		}
		if remErr := jjc.GitRemoteAdd(destPath, "origin", cloneURL); remErr != nil {
			return "", fmt.Errorf("init %s: add origin: %w", destPath, remErr)
		}
		if fetchErr := jjc.GitFetch(destPath, "origin", nil); fetchErr != nil {
			logTo("warning: fetch after init failed (remote may be empty): %v", fetchErr)
		}
	}

	return destPath, nil
}

// addWorkspace creates a workspace and removes what it created on failure.
//
// `jj workspace add` registers the workspace and creates its directory before
// it resolves the revset, so a failed add leaves a husk behind: a registered
// workspace whose directory holds nothing but .jj/. Left in place, the next
// `wgo to` stats that directory, takes the re-entry path, and has to fail on a
// workspace wgo itself abandoned. Callers reach this only after establishing
// that wtPath did not already exist, so removing it cannot take user work.
func addWorkspace(jjc jj.Client, repoPath, wtPath, name, revset string) error {
	// A taken name is itself one of the reasons `jj workspace add` fails, so
	// forgetting unconditionally would unregister the workspace that already
	// owned the name — turning a failed add into someone else's lost
	// workspace. Only forget a registration this call introduced.
	preexisting := workspaceRegistered(jjc, repoPath, name)

	err := jjc.WorkspaceAdd(repoPath, wtPath, jj.WorkspaceAddOpts{Name: name, Revset: revset})
	if err == nil {
		return nil
	}
	if !preexisting && workspaceRegistered(jjc, repoPath, name) {
		if ferr := jjc.WorkspaceForget(repoPath, name); ferr != nil {
			logTo("warning: could not forget partial workspace %s: %v", name, ferr)
		}
	}
	if rerr := os.RemoveAll(wtPath); rerr != nil {
		logTo("warning: could not remove partial workspace at %s: %v", wtPath, rerr)
	}
	return err
}

// workspaceRegistered reports whether repoPath has a workspace named name. A
// list failure answers true: the only caller uses this to decide whether
// forgetting the name is safe, and "I could not tell" must not authorize
// unregistering a workspace that may not be ours.
func workspaceRegistered(jjc jj.Client, repoPath, name string) bool {
	wss, err := jjc.ListWorkspaces(repoPath)
	if err != nil {
		return true
	}
	return slices.ContainsFunc(wss, func(w jj.Workspace) bool { return w.Name == name })
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
	if enabled, err := jjc.EnsureColocated(repoPath); err != nil {
		logTo("warning: could not enable colocation for %s: %v", repoPath, err)
	} else if enabled {
		logTo("enabling colocation for %s...", repoPath)
	}

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
		// A pinned PR workspace sits on pr-<N>-<slug>, never on the head ref —
		// the pin is taken precisely when the head ref could not be tracked, so
		// comparing against `branch` alone never matched and every re-run tried
		// to move @ onto a bookmark that by construction does not exist.
		want := workspaceBookmarkNames(parsed, branch)
		cur := currentBookmark(jjc, wtPath)
		if cur != "" && slices.Contains(want, cur) {
			logTo("workspace path already exists")
			return wtPath, nil
		}
		target, err := firstLocalBookmark(jjc, repoPath, want)
		if err != nil {
			return "", err
		}
		// From here on wgo cannot show that wtPath is a checkout of what was
		// asked for, and `wgo to` is consumed as `cd $(wgo to ...)` — printing
		// the path anyway walks the user into the wrong tree. Fail instead.
		if target == "" {
			return "", fmt.Errorf("workspace at %s exists but none of its bookmarks (%s) resolve locally, "+
				"so wgo cannot land it on %s; if it is an empty leftover from an interrupted run, "+
				"find it with `jj -R %s workspace list`, forget it, and `rm -rf %s`",
				wtPath, strings.Join(want, ", "), branch, repoPath, wtPath)
		}
		logTo("workspace exists at %s, moving @ to %s...", wtPath, target)
		if err := jjc.EditChange(wtPath, target); err != nil {
			// Not only unresolvable names land here: `jj edit` also refuses an
			// immutable target, which a protected ref is by design.
			return "", fmt.Errorf("workspace exists at %s but @ could not be moved to %s: %w", wtPath, target, err)
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
			exists, err := bookmarkExists(jjc, repoPath, toOnParent)
			if err != nil {
				return "", err
			}
			if !exists {
				return "", fmt.Errorf("--on parent %q not found locally or on origin", toOnParent)
			}
			// An existing parent implies the repo already has commits, so there
			// is no trunk to bootstrap.
			startPoint = toOnParent
			logTo("creating workspace with new bookmark %s on top of %s...", branch, toOnParent)
		} else {
			defaultBranch, err := defaultBranchFor(jjc, repoPath)
			if err != nil {
				defaultBranch = "main"
			}
			if err := ensureTrunk(jjc, repoPath, defaultBranch, parsed.Repo); err != nil {
				return "", fmt.Errorf("bootstrap %s: %w", defaultBranch, err)
			}
			startPoint = defaultBranch + "@origin"
			logTo("creating workspace with new bookmark %s from %s...", branch, startPoint)
		}
		if err := ensureWorkspaceAndBookmark(jjc, repoPath, branch, wtPath, startPoint, parsed.Repo); err != nil {
			return "", err
		}
		if toOnParent != "" {
			if err := recordStackParent(repoPath, branch, toOnParent); err != nil {
				logTo("error: %v", err)
				logTo("workspace created but stack parent not recorded")
			}
		}

	case gh.URLTypePR:
		num, _ := strconv.Atoi(parsed.Identifier)
		baseSlug := parsed.Owner + "/" + parsed.Repo

		// The stack is the unit of work: resolve the whole stack containing the
		// target PR and fetch every member. A lone PR resolves to a one-entry
		// stack, so the common single-PR case falls out of the same code path.
		logTo("resolving stack for PR #%d...", num)
		members, err := stack.ResolveStack(gh.NewClient(), repoPath, stack.PRRef{Number: num})
		if err != nil || len(members) == 0 {
			// Degrade to single-PR checkout when stack resolution is unavailable.
			if err != nil {
				logTo("stack resolution failed (%v); checking out PR #%d alone", err, num)
			}
			info, e := gh.GetPRHeadRef(baseSlug, num)
			if e != nil {
				return "", e
			}
			members = []stack.StackMember{{
				Branch: info.Ref, PRNumber: num, HeadSlug: info.RepoSlug, HeadOID: info.OID,
			}}
		}

		named := memberByPR(members, num)
		if named == nil { // defensive: seed should always be present
			named = &members[len(members)-1]
		}

		// (A) Fetch every head branch: batch same-repo refs into one fetch;
		// each fork head comes from its own remote.
		var sameRepoRefs []string
		forkRemote := map[int]string{}
		for i := range members {
			m := &members[i]
			if m.HeadSlug == "" || m.HeadSlug == baseSlug {
				sameRepoRefs = append(sameRepoRefs, m.Branch)
				continue
			}
			remote := fmt.Sprintf("pr-%d-fork", m.PRNumber)
			forkRemote[m.PRNumber] = remote
			forkURL := "https://github.com/" + m.HeadSlug + ".git"
			logTo("PR #%d is from fork %s; adding remote %s...", m.PRNumber, m.HeadSlug, remote)
			if err := jjc.GitRemoteAdd(repoPath, remote, forkURL); err != nil {
				logTo("warning: add remote %s: %v", remote, err)
			}
			if err := jjc.GitFetch(repoPath, remote, []string{m.Branch}); err != nil {
				return "", fmt.Errorf("fetch %s from %s: %w", m.Branch, remote, err)
			}
		}
		if len(sameRepoRefs) > 0 {
			logTo("fetching %d branch(es) from origin...", len(sameRepoRefs))
			if err := jjc.GitFetch(repoPath, "origin", sameRepoRefs); err != nil {
				return "", fmt.Errorf("fetch stack from origin: %w", err)
			}
		}

		// (B) Track or pin a bookmark per member (see trackOrPinMember).
		bmFor := map[int]string{}
		for i := range members {
			m := &members[i]
			remote := "origin"
			if r, ok := forkRemote[m.PRNumber]; ok {
				remote = r
			}
			bm, err := trackOrPinMember(jjc, cfg, repoPath, remote, m)
			if err != nil {
				return "", err
			}
			bmFor[m.PRNumber] = bm
		}

		// (C) Choose the landing node: the passed PR by default, or --on to land
		// on an interior stack node (or any existing bookmark) for forking.
		landOn, err := chooseLandingNode(members, bmFor, named, num, toOnParent,
			func(name string) (bool, error) { return bookmarkExists(jjc, repoPath, name) })
		if err != nil {
			return "", err
		}

		// (D) One workspace, landed on the chosen node. The workspace id stays a
		// stable, slash-free id even though bookmarks may carry slashes.
		wsName := fmt.Sprintf("pr-%d-%s", num, gh.SanitizeBranch(named.Branch))
		logTo("creating workspace at bookmark %s...", landOn)
		if err := addWorkspace(jjc, repoPath, wtPath, wsName, landOn); err != nil {
			return "", fmt.Errorf("workspace add failed: %w", err)
		}

	case gh.URLTypeBranch:
		exists, err := bookmarkExists(jjc, repoPath, branch)
		if err != nil {
			return "", err
		}
		if !exists {
			return "", fmt.Errorf("branch %q not found locally or on origin", branch)
		}
		// Track the branch so the workspace gets a mutable local bookmark,
		// unless it's protected (those stay untracked so immutable_heads()
		// keeps them immutable — see shouldTrack) or has no untracked origin
		// counterpart (local-only, or already tracked).
		if shouldTrack(cfg, branch) {
			trackable, err := remoteBookmarkTrackable(jjc, repoPath, branch, "origin")
			if err != nil {
				return "", err
			}
			if trackable {
				logTo("tracking %s@origin...", branch)
				if err := jjc.BookmarkTrack(repoPath, branch, "origin"); err != nil {
					logTo("warning: track %s@origin: %v", branch, err)
				}
			}
		}
		logTo("creating workspace for branch %s...", branch)
		if err := addWorkspace(jjc, repoPath, wtPath, branch, branch); err != nil {
			return "", fmt.Errorf("workspace add failed: %w", err)
		}

	default:
		return "", fmt.Errorf("unsupported URL type for workspace creation")
	}

	return wtPath, nil
}

// shouldTrack reports whether wgo should create a tracked local bookmark for
// branch when checking it out. Protected branches — matched against the
// configured doctor.exclude_bookmarks globs (main/master/develop, release/*)
// — are left untracked precisely so they stay immutable: jj's default
// immutable_heads() is `builtin_immutable_heads()`, which covers *untracked*
// remote bookmarks, so tracking one is what makes it locally rewritable.
// trunk() alone would not do it — that resolves to a single bookmark and says
// nothing about develop or release/*.
func shouldTrack(cfg *config.Config, branch string) bool {
	return !bookmarkExcluded(branch, cfg.Doctor.ExcludeBookmarks)
}

// bookmarkLister is the subset of jj.Client needed to inspect bookmarks; taking
// it (rather than the full client) keeps the tracking helpers easy to unit test.
type bookmarkLister interface {
	BookmarkList(repo string, opts jj.BookmarkListOpts) ([]jj.Bookmark, error)
}

// memberBookmarker is the subset of jj.Client trackOrPinMember needs. Note the
// absence of BookmarkCreate: pinning must be re-runnable, and `jj bookmark
// create` fails on an existing name.
type memberBookmarker interface {
	bookmarkLister
	BookmarkTrack(repo, name, remote string) error
	BookmarkSet(repo, name, revset string, allowBackwards bool) error
}

// prPinBookmark names the wgo-owned local bookmark that pins PR n's head when
// the head ref itself cannot be tracked.
//
// The shape lives in the github package rather than here because recognising a
// pin matters outside this file: `wgo sync` has to keep pins out of the
// bookmarks it pushes and opens PRs for, and it can only do that if the
// reserved namespace has one definition (gh.IsPinBookmark).
//
// Note this shares its shape with two *different* namespaces built from the
// same parts: the workspace directory slug and the jj workspace name. jj keeps
// bookmark, workspace, and remote names separate, so the collision is cosmetic.
func prPinBookmark(n int, branch string) string {
	return gh.PinBookmark(n, branch)
}

// trackOrPinMember gives one stack member a local bookmark and returns its
// name. A tracked bookmark is preferred — it is mutable, and for a same-repo
// PR pushable, so the workspace can build on the PR — but a protected ref, or
// a local bookmark of the same name already sitting on a different commit,
// falls back to a pinned `pr-<N>-<sanitized-branch>` bookmark that names the
// PR head without touching the protected ref or the colliding bookmark. (A
// fork PR tracks the contributor's remote, so pushing it needs write access to
// their fork.)
//
// The pin is set, not created: `wgo to <PR-URL>` is a lookup users re-run
// freely, so a second run must land on the same bookmark rather than fail with
// "Bookmark already exists". Backwards moves are allowed because a force-push
// can replace the PR head with a commit that is not a descendant of the old
// one — an amended or rebased sibling, or an outright rewind to an ancestor.
//
// Setting rather than creating gives up the accidental guard `jj bookmark
// create` provided against overwriting a name wgo does not own, so the pin is
// checked for a tracked remote counterpart first and any move is reported.
func trackOrPinMember(jjc memberBookmarker, cfg *config.Config, repoPath, remote string, m *stack.StackMember) (string, error) {
	// An empty head would make BookmarkSet omit -r, and jj defaults that to @ —
	// silently pinning the PR to whatever the main clone's working copy is.
	if m.HeadOID == "" {
		return "", fmt.Errorf("PR #%d (%s): GitHub reported no head commit, so there is nothing to pin a bookmark to; retry, or check the branch out directly with `wgo to %s`", m.PRNumber, m.Branch, m.Branch)
	}

	// Ask about a colliding local bookmark only on the path that reads the
	// answer. A protected ref goes straight to the pin below and never looks at
	// it, so listing there would abort the whole stack checkout on a question
	// nobody asked — and cost an extra working-copy snapshot per member.
	if shouldTrack(cfg, m.Branch) {
		conflicts, err := localBookmarkConflicts(jjc, repoPath, m.Branch, m.HeadOID)
		if err != nil {
			return "", err
		}
		if !conflicts {
			logTo("tracking %s@%s...", m.Branch, remote)
			err := jjc.BookmarkTrack(repoPath, m.Branch, remote)
			if err == nil {
				return m.Branch, nil
			}
			// BookmarkTrack already repairs a missing local bookmark itself, so
			// an error here means that repair failed too and `m.Branch` almost
			// certainly does not resolve. Returning it anyway would surface
			// much later as an opaque "workspace add failed"; pin instead.
			// The pin is not pushable, so say what that costs.
			logTo("warning: track %s@%s failed (%v); pinning instead — pushing from this workspace will NOT update PR #%d", m.Branch, remote, err, m.PRNumber)
		}
	}

	bm := prPinBookmark(m.PRNumber, m.Branch)
	pin, err := inspectPin(jjc, repoPath, bm)
	if err != nil {
		return "", err
	}
	if pin.published {
		return "", fmt.Errorf("bookmark %s is tracked on a remote, so it is not wgo's to move; rename or delete it, then re-run `wgo to`", bm)
	}
	logTo("not tracking %s; pinning bookmark %s to %s...", m.Branch, bm, m.HeadOID)
	// A conflicted pin reports no commitID, so without the first case it would
	// look identical to one already sitting on the head and the set below would
	// collapse the conflict silently.
	switch {
	case pin.conflicted:
		logTo("  %s is conflicted; pinning to the PR head resolves it (`jj undo` reverts)", bm)
	case pin.present && pin.commitID != "" && pin.commitID != m.HeadOID:
		logTo("  %s moves off %s (PR head changed; `jj undo` reverts)", bm, pin.commitID)
	}
	if err := jjc.BookmarkSet(repoPath, bm, m.HeadOID, true); err != nil {
		return "", fmt.Errorf("pin bookmark %s at %s: %w", bm, m.HeadOID, err)
	}
	return bm, nil
}

// pinState describes whatever local bookmark already occupies a pin's name.
type pinState struct {
	// present is true when a local bookmark of that name resolves to a commit.
	present bool
	// commitID is where it currently sits, or "" when unknown or conflicted.
	commitID string
	// conflicted is true when the bookmark has conflicting targets. jj reports
	// no commitID for those, so without this flag a conflicted pin is
	// indistinguishable from one sitting exactly where we want it.
	conflicted bool
	// published is true when the name has a tracked counterpart on a real
	// remote, which means someone pushed it — a wgo pin is never pushed, so
	// the name is not wgo's to move.
	published bool
}

// inspectPin reports what sits at a prospective pin name. A list failure is an
// error, not an empty pinState: "I cannot tell whether this name is mine" must
// not be spelled "the name is free".
func inspectPin(jjc bookmarkLister, repo, name string) (pinState, error) {
	bms, err := jjc.BookmarkList(repo, jj.BookmarkListOpts{AllRemotes: true, Names: []string{name}})
	if err != nil {
		return pinState{}, fmt.Errorf("list bookmark %s in %s: %w", name, repo, err)
	}
	var st pinState
	for _, b := range bms {
		if b.Name != name {
			continue
		}
		if b.Remote != "" {
			// jj auto-exports local bookmarks to the colocated git repo and
			// calls that tracked, so it says nothing about who owns the name.
			if b.Tracked && b.Remote != jj.GitPseudoRemote {
				st.published = true
			}
			continue
		}
		if b.Present {
			st.present = true
			st.commitID = b.CommitID
			st.conflicted = b.Conflict
		}
	}
	return st, nil
}

// localBookmarkConflicts reports whether a local bookmark named name already
// exists pointing somewhere other than oid (or is conflicted). Used to avoid
// clobbering a pre-existing divergent bookmark — e.g. a fork PR whose head ref
// collides with an already-tracked origin branch of the same name.
//
// A list failure is returned, not swallowed as "no conflict". The false branch
// leads to `jj bookmark track`, which on a genuine collision merges the user's
// local bookmark with the remote into a *conflicted* bookmark — and does it at
// exit 0, so nothing would warn. Guessing "safe" when the check itself failed
// is the one direction that damages the user's repo.
func localBookmarkConflicts(jjc bookmarkLister, repo, name, oid string) (bool, error) {
	bms, err := jjc.BookmarkList(repo, jj.BookmarkListOpts{AllRemotes: true, Names: []string{name}})
	if err != nil {
		return false, fmt.Errorf("check local bookmark %s in %s: %w", name, repo, err)
	}
	for _, b := range bms {
		if b.Name != name || b.Remote != "" || !b.Present {
			continue
		}
		if b.Conflict {
			return true, nil
		}
		if oid != "" && b.CommitID != "" && b.CommitID != oid {
			return true, nil
		}
	}
	return false, nil
}

// remoteBookmarkTrackable reports whether name has an untracked remote bookmark
// on remote — i.e. tracking it would create a useful local bookmark. Returns
// false for local-only branches (nothing to track) and already-tracked ones.
// A list failure is returned rather than reported as "not trackable", so a
// broken jj cannot quietly downgrade the workspace to an untracked bookmark.
func remoteBookmarkTrackable(jjc bookmarkLister, repo, name, remote string) (bool, error) {
	bms, err := jjc.BookmarkList(repo, jj.BookmarkListOpts{AllRemotes: true, Names: []string{name}})
	if err != nil {
		return false, fmt.Errorf("check remote bookmark %s@%s in %s: %w", name, remote, repo, err)
	}
	for _, b := range bms {
		if b.Name == name && b.Remote == remote && b.Present && !b.Tracked {
			return true, nil
		}
	}
	return false, nil
}

// bookmarkExists returns true when a bookmark of name exists locally or on
// any remote of repo.
//
// A list failure is returned, not flattened to "does not exist". Callers turn
// a false into a user-facing "branch %q not found locally or on origin" or
// "--on %q is not in PR #%d's stack nor an existing bookmark", both of which
// are flatly wrong when the real cause was jj failing to answer.
func bookmarkExists(jjc jj.Client, repo, name string) (bool, error) {
	bms, err := jjc.BookmarkList(repo, jj.BookmarkListOpts{AllRemotes: true, Names: []string{name}})
	if err != nil {
		return false, fmt.Errorf("check bookmark %s in %s: %w", name, repo, err)
	}
	for _, b := range bms {
		if b.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// firstLocalBookmark returns the first name in names that exists as a local
// bookmark in repo, or "" when none do. Names are tried in order, so callers
// express preference by ordering.
//
// Local only, unlike bookmarkExists: the caller's next move is `jj edit
// <name>`, and jj resolves a bare symbol against local bookmarks — a name that
// exists only as `name@origin` would fail there anyway.
func firstLocalBookmark(jjc bookmarkLister, repo string, names []string) (string, error) {
	bms, err := jjc.BookmarkList(repo, jj.BookmarkListOpts{Local: true, Names: names})
	if err != nil {
		return "", fmt.Errorf("list bookmarks %s in %s: %w", strings.Join(names, ", "), repo, err)
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		for _, b := range bms {
			if b.Name == name && b.Present {
				return name, nil
			}
		}
	}
	return "", nil
}

// recordStackParent used to persist a parent link in state.json so the new
// worktree participated in restack/sync. After the jj migration this is
// expressed in the jj DAG directly — `jj workspace add -r <parent-bookmark>`
// produces a child commit whose parent is the parent bookmark's tip — so
// the wgo annotation no longer carries Parents/StackID. This function is
// now a no-op kept to preserve --on's call site; the parent linkage lives
// in jj.
// memberByPR returns the stack member with the given PR number, or nil.
func memberByPR(members []stack.StackMember, pr int) *stack.StackMember {
	for i := range members {
		if members[i].PRNumber == pr {
			return &members[i]
		}
	}
	return nil
}

// memberByBranch returns the PR number of the stack member with the given head
// branch, and whether one was found.
func memberByBranch(members []stack.StackMember, branch string) (int, bool) {
	for _, m := range members {
		if m.Branch == branch {
			return m.PRNumber, true
		}
	}
	return 0, false
}

// chooseLandingNode resolves which bookmark the workspace should land on.
// Default is the named PR's bookmark; --on selects an interior stack member (by
// branch) or, failing that, any existing bmark. An --on that matches neither is
// an error. bmFor maps PR number → local bookmark; bookmarkExistsFn tests for a
// pre-existing bookmark and may fail, in which case its error is returned
// rather than being reported as "--on names nothing".
func chooseLandingNode(members []stack.StackMember, bmFor map[int]string, named *stack.StackMember, num int, onParent string, bookmarkExistsFn func(string) (bool, error)) (string, error) {
	if onParent == "" {
		return bmFor[named.PRNumber], nil
	}
	if pn, ok := memberByBranch(members, onParent); ok {
		return bmFor[pn], nil
	}
	exists, err := bookmarkExistsFn(onParent)
	if err != nil {
		return "", err
	}
	if exists {
		return onParent, nil
	}
	return "", fmt.Errorf("--on %q is not in PR #%d's stack nor an existing bookmark", onParent, num)
}

func recordStackParent(_, _, _ string) error {
	return nil
}
