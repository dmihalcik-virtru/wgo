package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

var joinNoPush bool

var joinCmd = &cobra.Command{
	Use:   "join <owner/repo>",
	Short: "Add a repo to the current multi-repo workspace on the same branch",
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
	jjc := jj.NewCLI()

	// 1. Detect current workspace path.
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	currentWtPath, err := jjc.Root(cwd)
	if err != nil {
		return fmt.Errorf("not in a jj repository: %w", err)
	}

	// 2. Config must be initialized before findOrCloneRepo.
	if err := config.Init(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg := config.Get()
	if cfg.Worktree.WorktreesDir == "" {
		return fmt.Errorf("worktree.worktrees_dir not configured; set it in ~/.wgo/config.toml")
	}

	// 3. Current bookmark (jj-side equivalent of "current branch").
	branch := currentBookmark(jjc, currentWtPath)
	if branch == "" {
		return fmt.Errorf("could not determine current bookmark on workspace %s; check `jj log -r @`", currentWtPath)
	}

	// 4. Shared root is one level up from the current workspace.
	sharedRoot := filepath.Dir(currentWtPath)

	// 5. Parse owner/repo argument.
	specs, err := parseRepoSpecs([]string{ownerRepo})
	if err != nil {
		return err
	}
	spec := specs[0]

	// 6. Find or clone the target repo.
	repoPath, err := findOrCloneRepo(jjc, cfg, spec.owner, spec.repo)
	if err != nil {
		return fmt.Errorf("repo %s: %w", ownerRepo, err)
	}

	// 7. Fetch latest (best-effort).
	fmt.Fprintf(os.Stderr, "fetching %s...\n", ownerRepo)
	if err := jjc.GitFetch(repoPath, "", nil); err != nil {
		fmt.Fprintf(os.Stderr, "warning: fetch failed for %s (using cached state): %v\n", ownerRepo, err)
	}

	// 8. Target workspace path.
	newWtPath := filepath.Join(sharedRoot, spec.repo)

	// 9. Error if path already exists.
	if _, err := os.Stat(newWtPath); err == nil {
		return fmt.Errorf("workspace already exists at %s; remove it first or use cd %s", newWtPath, newWtPath)
	}

	// 10. Create workspace: attach existing bookmark or create new one.
	if bookmarkExists(jjc, repoPath, branch) {
		fmt.Fprintf(os.Stderr, "creating workspace for existing bookmark %s...\n", branch)
		if err := jjc.WorkspaceAdd(repoPath, branch, newWtPath, branch); err != nil {
			return fmt.Errorf("workspace add: %w", err)
		}
	} else {
		defaultBranch, err := defaultBranchFor(jjc, repoPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not detect default branch, assuming 'main': %v\n", err)
			defaultBranch = "main"
		}
		fmt.Fprintf(os.Stderr, "creating workspace with new bookmark %s from origin/%s...\n", branch, defaultBranch)
		if err := jjc.WorkspaceAdd(repoPath, branch, newWtPath, "origin/"+defaultBranch); err != nil {
			return fmt.Errorf("workspace add: %w", err)
		}
		if err := jjc.BookmarkCreate(repoPath, branch, "origin/"+defaultBranch); err != nil {
			return fmt.Errorf("create bookmark %s: %w", branch, err)
		}
		defer func() {
			if retErr != nil {
				fmt.Fprintf(os.Stderr, "rolling back workspace %s...\n", newWtPath)
				_ = jjc.WorkspaceForget(repoPath, branch)
				_ = os.RemoveAll(newWtPath)
			}
		}()

		if !noPush {
			fmt.Fprintf(os.Stderr, "pushing %s...\n", branch)
			if _, err := jjc.GitPush(repoPath, jj.PushOpts{Bookmarks: []string{branch}, AllowNew: true}); err != nil && !errors.Is(err, jj.ErrNothingToPush) {
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
