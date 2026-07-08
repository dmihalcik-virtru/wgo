// Package cmd provides CLI commands for wgo.
package cmd

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/cleanup"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/links"
	"github.com/virtru/wgo/internal/store"
)

var (
	cleanWorktrees bool
	cleanBranches  bool
	cleanRepos     bool
	cleanRemote    bool
	cleanDryRun    bool
	cleanForce     bool
	cleanYes       bool
)

func init() {
	cleanCmd.Flags().BoolVar(&cleanWorktrees, "worktrees", false, "Only clean worktrees")
	cleanCmd.Flags().BoolVar(&cleanBranches, "branches", false, "Only clean local branches")
	cleanCmd.Flags().BoolVar(&cleanRepos, "repos", false, "Only clean full repos")
	cleanCmd.Flags().BoolVar(&cleanRemote, "remote", false, "Only clean remote branches")
	cleanCmd.Flags().BoolVar(&cleanDryRun, "dry-run", false, "Preview only, no changes")
	cleanCmd.Flags().BoolVar(&cleanForce, "force", false, "Skip dirty working-tree checks")
	cleanCmd.Flags().BoolVar(&cleanYes, "yes", false, "Auto-confirm all removals")
	rootCmd.AddCommand(cleanCmd)
}

var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Remove stale worktrees, branches, and repos",
	Long: `Find and remove stale git worktrees, merged/closed branches, and unused repos.

Shows a dry-run summary first, then prompts y/n per item (or use --yes to skip).
Uses GitHub CLI (gh) for PR status when available.`,
	RunE: runClean,
}

func runClean(cmd *cobra.Command, args []string) error {
	if err := config.Init(); err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	cfg := config.Get()

	jjc := jj.NewCLI()
	ghClient := github.NewClient()

	fmt.Fprintln(os.Stderr, "Scanning for cleanup candidates...")

	candidates, err := cleanup.FindCandidates(cfg, jjc, ghClient)
	if err != nil {
		return fmt.Errorf("failed to find candidates: %w", err)
	}

	// Apply kind filters if any are set
	if cleanWorktrees || cleanBranches || cleanRepos || cleanRemote {
		var kinds []cleanup.CandidateKind
		if cleanWorktrees {
			kinds = append(kinds, cleanup.KindWorktree)
		}
		if cleanBranches {
			kinds = append(kinds, cleanup.KindLocalBranch)
		}
		if cleanRepos {
			kinds = append(kinds, cleanup.KindRepo)
		}
		if cleanRemote {
			kinds = append(kinds, cleanup.KindRemoteBranch)
		}
		candidates = cleanup.FilterKinds(candidates, kinds...)
	}

	// Stack-aware safety used to consult store.Annotation.Parents to keep
	// branches alive when something still pointed at them. The jj migration
	// removed that shadow state — jj's DAG is the single source of truth —
	// so we no longer second-guess the user's clean intent here. The user
	// (or `jj bookmark forget`) is responsible for breaking dependencies
	// first.

	if len(candidates) == 0 {
		fmt.Println("Nothing to clean.")
		return nil
	}

	// Print dry-run table
	printCandidateTable(candidates)

	if cleanDryRun {
		fmt.Printf("\n%d candidate(s) found. Run without --dry-run to remove.\n", len(candidates))
		return nil
	}

	// Interactive or auto-confirm removal
	st, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}
	state, err := st.LoadState()
	if err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	removed := 0
	stateChanged := false
	scanner := bufio.NewScanner(os.Stdin)

	for i, c := range candidates {
		if c.IsDirty && !cleanForce {
			fmt.Fprintf(os.Stderr, "  Skipping dirty %s %s (use --force to include)\n", c.Kind, c.DisplayPath())
			continue
		}

		if !cleanYes {
			promptPath := c.DisplayPath()
			promptReason := c.Reason
			if isTerminal() {
				remoteURL := getRemoteURL(jjc, c.RepoPath)
				if c.Branch != "" {
					if branchURL := links.BranchURL(remoteURL, c.Branch); branchURL != "" {
						promptPath = strings.Replace(promptPath, c.Branch, links.Hyperlink(branchURL, c.Branch), 1)
					}
				}
				if c.PRInfo != nil && c.PRInfo.URL != "" {
					promptReason = prNumberRe.ReplaceAllStringFunc(promptReason, func(match string) string {
						return links.Hyperlink(c.PRInfo.URL, match)
					})
				}
			}
			fmt.Printf("\n[%d/%d] Remove %s %s (%s)? [y/N/a(ll)/q(uit)]: ",
				i+1, len(candidates), c.Kind, promptPath, promptReason)
			if !scanner.Scan() {
				break
			}
			ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
			switch ans {
			case "a", "all":
				cleanYes = true
			case "q", "quit":
				fmt.Println("Aborted.")
				return nil
			case "y", "yes":
				// proceed
			default:
				fmt.Printf("  Skipped.\n")
				continue
			}
		}

		if err := executeRemoval(c, jjc, ghClient, state); err != nil {
			fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		} else {
			fmt.Printf("  Removed %s %s\n", c.Kind, c.DisplayPath())
			removed++
			if c.Kind == KindRepo {
				state.UntrackRepo(c.RepoPath)
				stateChanged = true
			}
		}
	}

	if stateChanged {
		if err := st.SaveState(state); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save state: %v\n", err)
		}
	}

	fmt.Printf("\nDone. Removed %d item(s).\n", removed)
	return nil
}

// KindRepo is re-exported from cleanup for use inside this file.
const KindRepo = cleanup.KindRepo

// remoteURLCache caches origin URLs by repo path to avoid repeated jj calls.
var remoteURLCache = map[string]string{}

func getRemoteURL(jjc jj.Client, repoPath string) string {
	if u, ok := remoteURLCache[repoPath]; ok {
		return u
	}
	remotes, err := jjc.RemoteURLs(repoPath)
	u := ""
	if err == nil {
		u = remotes["origin"]
	}
	remoteURLCache[repoPath] = u
	return u
}

var prNumberRe = regexp.MustCompile(`PR #(\d+)`)

func printCandidateTable(candidates []Candidate) {
	tty := isTerminal()
	jjc := jj.NewCLI()

	fmt.Printf("\n%-12s %-40s %s\n", "KIND", "LOCATION", "REASON")
	fmt.Println(strings.Repeat("-", 80))
	for _, c := range candidates {
		path := c.DisplayPath()

		// Link branch name in display path
		if tty && c.Branch != "" {
			remoteURL := getRemoteURL(jjc, c.RepoPath)
			branchURL := links.BranchURL(remoteURL, c.Branch)
			if branchURL != "" {
				linked := links.Hyperlink(branchURL, c.Branch)
				path = strings.Replace(path, c.Branch, linked, 1)
			}
		}

		// Link PR numbers in reason text
		reason := c.Reason
		if tty && c.PRInfo != nil && c.PRInfo.URL != "" {
			reason = prNumberRe.ReplaceAllStringFunc(reason, func(match string) string {
				return links.Hyperlink(c.PRInfo.URL, match)
			})
		}

		dirtyMarker := ""
		if c.IsDirty {
			dirtyMarker = " [dirty]"
		}
		fmt.Printf("%-12s %-40s %s%s\n", c.Kind, path, reason, dirtyMarker)
	}
}

type Candidate = cleanup.Candidate

func executeRemoval(c Candidate, jjc jj.Client, ghClient github.Client, _ *store.State) error {
	switch c.Kind {
	case cleanup.KindWorktree:
		// Look up the workspace name from its on-disk path.
		wsName := workspaceNameForPath(jjc, c.RepoPath, c.Path)
		if wsName != "" {
			if err := jjc.WorkspaceForget(c.RepoPath, wsName); err != nil {
				return err
			}
		}
		return os.RemoveAll(c.Path)

	case cleanup.KindLocalBranch:
		force := cleanForce
		if !force && c.PRInfo != nil && c.PRInfo.IsMerged() {
			defaultBranch := defaultBranchOrFallback(jjc, c.RepoPath)
			target := defaultBranch + "@origin"

			// Check 1: local bookmark commit is fully contained in target
			// (standard merge — bookmark's history matches target).
			if isContained(jjc, c.RepoPath, c.Branch, target) {
				force = true
			}

			// Check 2: PR merge commit is an ancestor of target
			// (squash/rebase merge — GitHub created a new commit on target).
			if !force && c.PRInfo.MergeCommit != nil && c.PRInfo.MergeCommit.OID != "" {
				sha := c.PRInfo.MergeCommit.OID
				if isAncestor(jjc, c.RepoPath, sha, target) {
					force = true
				} else if fetchErr := jjc.GitFetch(c.RepoPath, "", nil); fetchErr == nil {
					if isAncestor(jjc, c.RepoPath, sha, target) {
						force = true
					}
				}
			}

			// Check 3: local bookmark has no commits beyond the pushed PR head.
			if !force && c.PRInfo.HeadSHA != "" {
				if isContained(jjc, c.RepoPath, c.Branch, c.PRInfo.HeadSHA) {
					force = true
				}
			}

			// Check 4: local bookmark has no commits beyond its remote
			// counterpart (jj's bookmark@origin equivalent of git upstream).
			if !force {
				remoteRef := c.Branch + "@origin"
				if isContained(jjc, c.RepoPath, c.Branch, remoteRef) {
					force = true
				}
			}

			if !force {
				var reasons []string
				reasons = append(reasons, fmt.Sprintf("not reachable from %s", target))
				if c.PRInfo.MergeCommit != nil && c.PRInfo.MergeCommit.OID != "" {
					reasons = append(reasons, fmt.Sprintf("PR merge commit (%s) not found locally or not in %s", c.PRInfo.MergeCommit.OID[:7], target))
				}
				if c.PRInfo.HeadSHA != "" {
					reasons = append(reasons, fmt.Sprintf("local commits exist beyond pushed PR head (%s)", c.PRInfo.HeadSHA[:7]))
				}
				reasons = append(reasons, fmt.Sprintf("local commits exist beyond remote bookmark (%s@origin)", c.Branch))
				return fmt.Errorf("cannot verify bookmark is safe to delete:\n- %s\nuse --force to delete anyway", strings.Join(reasons, "\n- "))
			}
		}
		return jjc.BookmarkDelete(c.RepoPath, c.Branch)

	case cleanup.KindRemoteBranch:
		if ghClient == nil || !ghClient.Available() {
			return fmt.Errorf("github client not available for remote branch deletion")
		}
		return ghClient.DeleteRemoteBranch(c.RepoPath, c.Branch)

	case cleanup.KindRepo:
		return os.RemoveAll(c.Path)

	default:
		return fmt.Errorf("unknown candidate kind: %v", c.Kind)
	}
}

// defaultBranchOrFallback returns the default branch for repoPath, falling
// back to "main" on any error.
func defaultBranchOrFallback(jjc jj.Client, repoPath string) string {
	branch, err := defaultBranchFor(jjc, repoPath)
	if err != nil || branch == "" {
		return "main"
	}
	return branch
}

// isContained returns true when src has no commits beyond dst — i.e. src is
// fully merged into dst. Equivalent to git's `<dst>..<src>` returning empty.
func isContained(jjc jj.Client, repo, src, dst string) bool {
	n, err := jjc.CountRevset(repo, fmt.Sprintf("(%s)..(%s)", dst, src))
	if err != nil {
		return false
	}
	return n == 0
}

// isAncestor reports whether anc is an ancestor of desc.
func isAncestor(jjc jj.Client, repo, anc, desc string) bool {
	n, err := jjc.CountRevset(repo, fmt.Sprintf("(%s) & ::(%s)", anc, desc))
	if err != nil {
		return false
	}
	return n > 0
}

// workspaceNameForPath returns the jj workspace name whose on-disk root
// matches wsPath, or "" if no match. Required because jj.WorkspaceForget
// takes a workspace name, not a path.
func workspaceNameForPath(jjc jj.Client, repoPath, wsPath string) string {
	ws, err := jjc.ListWorkspaces(repoPath)
	if err != nil {
		return ""
	}
	for _, w := range ws {
		if w.Path == wsPath {
			return w.Name
		}
	}
	return ""
}
