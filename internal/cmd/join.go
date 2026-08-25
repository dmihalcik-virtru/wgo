package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/rig"
	specpkg "github.com/virtru/wgo/internal/spec"
	"github.com/virtru/wgo/internal/store"
)

var (
	joinNoPush     bool
	joinNoClaudeMD bool
)

var joinCmd = &cobra.Command{
	Use:   "join <owner/repo>",
	Short: "Add a repo to the current multi-repo workspace on the same branch",
	Long: `Detects the current worktree's branch and shared root, creates a sibling
worktree for the new repo on the same branch, and updates plan.md and state.json.

For a workspace holding 2+ repos, regenerates a shared CLAUDE.md at the
workspace root listing every repo. A CLAUDE.md that wgo did not generate is
never overwritten; pass --no-claude-md to skip this entirely.

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
	joinCmd.Flags().BoolVar(&joinNoClaudeMD, "no-claude-md", false, "Skip regenerating the shared CLAUDE.md for the workspace")
}

func runJoin(ownerRepo string, noPush bool) (retErr error) {
	jjc := jj.NewCLI()

	// 1. Detect current workspace path.
	cwd, err := resolveCwd()
	if err != nil {
		return err
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

	// Refuse to join from inside a rig. Step 4 takes the parent directory as the
	// shared root, which inside a rig is `<rig>/src` — the new workspace would
	// land among the pinned checkouts, invisible to `wgo rig` and foreign to
	// `wgo rig rm`. `wgo add` needs no such guard: it always builds its shared
	// root under worktrees_dir.
	if rig.UnderDir(cfg.Rig.Dir, currentWtPath) {
		return fmt.Errorf("%s is inside the rig directory %s; rigs hold pinned checkouts, not branch workspaces.\nTo add a module to this rig: wgo rig add -m <module>@<version>\nTo start branch work: cd to a worktree under %s first", currentWtPath, cfg.Rig.Dir, cfg.Worktree.WorktreesDir)
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

	if enabled, err := jjc.EnsureColocated(repoPath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not enable colocation for %s: %v\n", repoPath, err)
	} else if enabled {
		fmt.Fprintf(os.Stderr, "enabling colocation for %s...\n", repoPath)
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
		if err := jjc.WorkspaceAdd(repoPath, newWtPath, jj.WorkspaceAddOpts{Name: branch, Revset: branch}); err != nil {
			return fmt.Errorf("workspace add: %w", err)
		}
	} else {
		defaultBranch, err := defaultBranchFor(jjc, repoPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not detect default branch, assuming 'main': %v\n", err)
			defaultBranch = "main"
		}
		// A commitless remote has no trunk to branch from; seed one first.
		if err := ensureTrunk(jjc, repoPath, defaultBranch, spec.repo); err != nil {
			return fmt.Errorf("bootstrap %s: %w", defaultBranch, err)
		}
		startPoint := defaultBranch + "@origin"
		fmt.Fprintf(os.Stderr, "creating workspace with new bookmark %s from %s...\n", branch, startPoint)
		if err := ensureWorkspaceAndBookmark(jjc, repoPath, branch, newWtPath, startPoint, spec.repo); err != nil {
			return err
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
	planContent, err := s.LoadPlan()
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}
	p, err := plan.Parse(planContent)
	if err != nil {
		return fmt.Errorf("parse plan: %w", err)
	}

	// 12/13. Look up reason (falling back to branch name) and update state under
	// the lock so a concurrent wgo process can't clobber the annotation.
	reason := branch
	if err := s.MutateState(func(state *store.State) (bool, error) {
		if ann := state.GetAnnotation(currentWtPath, branch); ann != nil && ann.Purpose != "" {
			reason = ann.Purpose
		}
		state.AddAnnotation(newWtPath, branch, reason)
		state.AddRepo(newWtPath, "")
		return true, nil
	}); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// 13b. Update plan with the resolved reason.
	p.AddBranch(spec.repo, branch, reason, "")
	if err := s.SavePlan(p.Render()); err != nil {
		return fmt.Errorf("save plan: %w", err)
	}

	// 14. Regenerate the shared CLAUDE.md so a newly joined repo is reflected
	// (see writeSharedClaudeMD). Only inside worktrees_dir: sharedRoot is
	// inferred from the cwd, so outside our own tree it can name an arbitrary
	// directory of the user's that we have no business writing into.
	if !joinNoClaudeMD {
		if !isManagedWorkspaceRoot(cfg.Worktree.WorktreesDir, sharedRoot) {
			fmt.Fprintf(os.Stderr, "note: %s is outside worktrees_dir; skipping shared CLAUDE.md\n", sharedRoot)
		} else if repos, derr := discoverSharedRepos(jjc, sharedRoot); derr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not scan workspace for shared CLAUDE.md: %v\n", derr)
		} else {
			ticket := specpkg.ParseTicketFromBranch(branch)
			// Deliberately no fallback to `reason`: it defaults to the raw
			// branch name, and a slug rendered as the goal reads like a real
			// description. An empty desc renders a visible placeholder instead.
			err := writeSharedClaudeMD(sharedClaudeMD{
				Root:        sharedRoot,
				Ticket:      ticket,
				Description: planDescriptionForBranch(p, branch, ticket),
				Repos:       repos,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not update shared CLAUDE.md: %v\n", err)
			}
		}
	}

	// 15. Print path to stdout for cd $(...) usage.
	fmt.Println(newWtPath)
	return nil
}

// planDescriptionForBranch recovers the human description for branch by
// looking for a plan entry (in any repo already joined to this effort) whose
// Reason follows add.go's "<ticket>: <description>" convention. The state
// annotation `reason` resolved above doesn't reliably follow that format —
// it falls back to the raw branch name when no annotation was ever set — so
// trimming the ticket prefix off it directly can leave the branch slug in
// place of a real description.
//
// Returns "" if no entry matches, and the caller deliberately does not fall
// back to `reason`: no description at all is better than a slug that reads
// like one. Note the search includes the entry this run just added, which is
// harmless since non-matching entries are skipped.
func planDescriptionForBranch(p *plan.Plan, branch, ticket string) string {
	if ticket == "" {
		return ""
	}
	prefix := ticket + ": "
	for _, entry := range p.ActiveBranches {
		if entry.Branch != branch {
			continue
		}
		if trimmed, ok := strings.CutPrefix(entry.Reason, prefix); ok {
			return trimmed
		}
	}
	return ""
}
