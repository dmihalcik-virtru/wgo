package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

var joinNoPush bool

var joinCmd = &cobra.Command{
	Use:          "join <owner/repo>",
	Short:        "Add a repo to the current multi-repo workspace on the same branch",
	Long: `Detects the current worktree's branch and shared root, creates a sibling
worktree for the new repo on the same branch, and updates plan.md and state.json.

Output goes to stdout so you can use it with cd:
  cd $(wgo join virtru/cli)`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runJoin(args[0], joinNoPush)
	},
}

func init() {
	rootCmd.AddCommand(joinCmd)
	joinCmd.Flags().BoolVar(&joinNoPush, "no-push", false, "Skip pushing when a new branch is created")
}

func runJoin(ownerRepo string, noPush bool) (retErr error) {
	// 1. Detect current worktree path.
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return fmt.Errorf("not in a git repository")
	}
	currentWtPath := strings.TrimSpace(string(out))

	// 2. Config must be initialized before findOrCloneRepo.
	if err := config.Init(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg := config.Get()
	if cfg.Worktree.WorktreesDir == "" {
		return fmt.Errorf("worktree.worktrees_dir not configured; set it in ~/.wgo/config.toml")
	}

	gitClient := git.New("")

	// 3. Current branch.
	branch, err := gitClient.CurrentBranch(currentWtPath)
	if err != nil {
		return fmt.Errorf("could not determine current branch: %w", err)
	}

	// 4. Shared root is one level up from the current worktree.
	sharedRoot := filepath.Dir(currentWtPath)

	// 5. Parse owner/repo argument.
	specs, err := parseRepoSpecs([]string{ownerRepo})
	if err != nil {
		return err
	}
	spec := specs[0]

	// 6. Find or clone the target repo.
	repoPath, err := findOrCloneRepo(gitClient, cfg, spec.owner, spec.repo)
	if err != nil {
		return fmt.Errorf("repo %s: %w", ownerRepo, err)
	}

	// 7. Fetch latest (best-effort).
	fmt.Fprintf(os.Stderr, "fetching %s...\n", ownerRepo)
	if err := gitClient.Fetch(repoPath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: fetch failed for %s (using cached state): %v\n", ownerRepo, err)
	}

	// 8. Target worktree path.
	newWtPath := filepath.Join(sharedRoot, spec.repo)

	// 9. Error if path already exists.
	if _, err := os.Stat(newWtPath); err == nil {
		return fmt.Errorf("worktree already exists at %s; remove it first or use cd %s", newWtPath, newWtPath)
	}

	// 10. Create worktree: check out existing branch or create new one.
	exists, err := gitClient.BranchExists(repoPath, branch)
	if err != nil {
		return fmt.Errorf("could not check branch existence: %w", err)
	}

	if exists {
		fmt.Fprintf(os.Stderr, "creating worktree for existing branch %s...\n", branch)
		if err := gitClient.WorktreeAdd(repoPath, newWtPath, branch, false, ""); err != nil {
			return fmt.Errorf("worktree add: %w", err)
		}
	} else {
		defaultBranch, err := gitClient.DefaultBranch(repoPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not detect default branch, assuming 'main': %v\n", err)
			defaultBranch = "main"
		}
		fmt.Fprintf(os.Stderr, "creating worktree with new branch %s from origin/%s...\n", branch, defaultBranch)
		if err := gitClient.WorktreeAdd(repoPath, newWtPath, branch, true, "origin/"+defaultBranch); err != nil {
			return fmt.Errorf("worktree add: %w", err)
		}
		defer func() {
			if retErr != nil {
				fmt.Fprintf(os.Stderr, "rolling back worktree %s...\n", newWtPath)
				_ = gitClient.RemoveWorktree(repoPath, newWtPath, true)
			}
		}()

		if !noPush {
			fmt.Fprintf(os.Stderr, "pushing %s...\n", branch)
			if err := gitClient.Push(newWtPath, branch); err != nil {
				return fmt.Errorf("push %s: %w", branch, err)
			}
		}
	}

	// 11. Load state and plan.
	s, err := store.New()
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if err := s.EnsureDir(); err != nil {
		return fmt.Errorf("store ensure dir: %w", err)
	}
	state, err := s.LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	planContent, err := s.LoadPlan()
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}
	p, err := plan.Parse(planContent)
	if err != nil {
		return fmt.Errorf("parse plan: %w", err)
	}

	// 12. Look up reason from existing annotation; fall back to branch name.
	reason := branch
	if ann := state.GetAnnotation(currentWtPath, branch); ann != nil && ann.Purpose != "" {
		reason = ann.Purpose
	}

	// 13. Update plan and state.
	p.AddBranch(spec.repo, branch, reason, "")
	state.AddAnnotation(newWtPath, branch, reason)
	state.AddRepo(newWtPath, "")

	if err := s.SavePlan(p.Render()); err != nil {
		return fmt.Errorf("save plan: %w", err)
	}
	if err := s.SaveState(state); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// 14. Print path to stdout for cd $(...) usage.
	fmt.Println(newWtPath)
	return nil
}
