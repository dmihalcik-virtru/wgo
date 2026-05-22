// Package cmd provides CLI commands for wgo.
package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"regexp"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/cleanup"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/github"
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

	gitClient := git.New("")
	ghClient := github.NewClient()

	fmt.Fprintln(os.Stderr, "Scanning for cleanup candidates...")

	candidates, err := cleanup.FindCandidates(cfg, gitClient, ghClient)
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

	// Stack-aware safety: never offer a parent whose stack children still record it.
	preStackState, stateErr := loadStateForCleanFilter()
	if stateErr == nil {
		var blocked []cleanup.StackParentBlock
		candidates, blocked = cleanup.FilterStackParents(candidates, preStackState)
		for _, b := range blocked {
			fmt.Fprintf(os.Stderr,
				"  Skipping %s %s — stack children still depend on it: %s\n",
				b.Candidate.Kind, b.Candidate.DisplayPath(), strings.Join(b.Children, ", "))
		}
	}

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
				remoteURL := getRemoteURL(gitClient, c.RepoPath)
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

		if err := executeRemoval(c, gitClient, ghClient, state); err != nil {
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

// loadStateForCleanFilter loads state.json so FilterStackParents has access to
// recorded parent links. Returns nil state if loading fails — that just disables
// the stack-aware filter (cleanup falls back to its existing behavior).
func loadStateForCleanFilter() (*store.State, error) {
	s, err := store.New()
	if err != nil {
		return nil, err
	}
	return s.LoadState()
}

// remoteURLCache caches remote URLs by repo path to avoid repeated git calls.
var remoteURLCache = map[string]string{}

func getRemoteURL(gitClient git.Client, repoPath string) string {
	if u, ok := remoteURLCache[repoPath]; ok {
		return u
	}
	u, err := gitClient.RemoteURL(repoPath)
	if err != nil {
		u = ""
	}
	remoteURLCache[repoPath] = u
	return u
}

var prNumberRe = regexp.MustCompile(`PR #(\d+)`)

func printCandidateTable(candidates []Candidate) {
	tty := isTerminal()
	gitClient := git.New("")

	fmt.Printf("\n%-12s %-40s %s\n", "KIND", "LOCATION", "REASON")
	fmt.Println(strings.Repeat("-", 80))
	for _, c := range candidates {
		path := c.DisplayPath()

		// Link branch name in display path
		if tty && c.Branch != "" {
			remoteURL := getRemoteURL(gitClient, c.RepoPath)
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

func executeRemoval(c Candidate, gitClient git.Client, ghClient github.Client, _ *store.State) error {
	switch c.Kind {
	case cleanup.KindWorktree:
		// Find the main repo for this worktree
		repoPath := c.RepoPath
		if err := gitClient.RemoveWorktree(repoPath, c.Path, cleanForce); err != nil {
			return err
		}
		// Prune after removal
		_ = gitClient.PruneWorktrees(repoPath)
		return nil

	case cleanup.KindLocalBranch:
		force := cleanForce
		if !force && c.PRInfo != nil && c.PRInfo.IsMerged() {
			defaultBranch, _ := gitClient.DefaultBranch(c.RepoPath)
			if defaultBranch == "" {
				defaultBranch = "main"
			}
			target := "origin/" + defaultBranch

			// Check 1: local tip is reachable from target (standard merge).
			if hasExtra, err := gitClient.HasLocalOnlyCommits(c.RepoPath, c.Branch, target); err == nil && !hasExtra {
				force = true
			}

			// Check 2: PR merge commit is an ancestor of target (squash/rebase merge).
			if !force && c.PRInfo.MergeCommit != nil && c.PRInfo.MergeCommit.OID != "" {
				sha := c.PRInfo.MergeCommit.OID
				isAnc, ancErr := gitClient.IsAncestor(c.RepoPath, sha, target)
				if ancErr != nil {
					if fetchErr := gitClient.Fetch(c.RepoPath); fetchErr == nil {
						isAnc, _ = gitClient.IsAncestor(c.RepoPath, sha, target)
					}
				}
				if isAnc {
					force = true
				}
			}

			// Check 3: local tip has no commits beyond the pushed PR head.
			if !force && c.PRInfo.HeadSHA != "" {
				if hasExtra, err := gitClient.HasLocalOnlyCommits(c.RepoPath, c.Branch, c.PRInfo.HeadSHA); err == nil && !hasExtra {
					force = true
				}
			}

			// Check 4: local tip has no commits beyond the upstream tracking ref.
			if !force {
				if upstream, _ := gitClient.UpstreamRef(c.RepoPath, c.Branch); upstream != "" {
					if hasExtra, err := gitClient.HasLocalOnlyCommits(c.RepoPath, c.Branch, upstream); err == nil && !hasExtra {
						force = true
					}
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
				upstream, _ := gitClient.UpstreamRef(c.RepoPath, c.Branch)
				if upstream == "" {
					reasons = append(reasons, "no upstream branch configured")
				} else {
					reasons = append(reasons, fmt.Sprintf("local commits exist beyond upstream (%s)", upstream))
				}
				return fmt.Errorf("cannot verify branch is safe to delete:\n- %s\nuse --force to delete anyway", strings.Join(reasons, "\n- "))
			}
		}
		return gitClient.DeleteBranch(c.RepoPath, c.Branch, force)

	case cleanup.KindRemoteBranch:
		if ghClient == nil || !ghClient.Available() {
			return fmt.Errorf("gh CLI not available for remote branch deletion")
		}
		return ghClient.DeleteRemoteBranch(c.RepoPath, c.Branch)

	case cleanup.KindRepo:
		return os.RemoveAll(c.Path)

	default:
		return fmt.Errorf("unknown candidate kind: %v", c.Kind)
	}
}
