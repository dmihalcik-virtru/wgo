package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

// planCmd represents the `wgo plan` command.
var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Manage the plan file",
	Long:  `View and manage the plan file that tracks your work across repositories.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return showPlan()
	},
}

// planAddCmd represents the `wgo plan add` command.
var planAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add annotation to current branch",
	Long:  `Annotate the current branch with a description of why it exists.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return addAnnotation(args[0])
	},
}

// planEditCmd represents the `wgo plan edit` command.
var planEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Edit the plan file",
	Long:  `Open the plan file in your default editor.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return editPlan()
	},
}

func init() {
	rootCmd.AddCommand(planCmd)
	planCmd.AddCommand(planAddCmd)
	planCmd.AddCommand(planEditCmd)
}

func showPlan() error {
	s, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}

	content, err := s.LoadPlan()
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	fmt.Print(content)

	// When pair is configured, dynamically append the "Active With" section.
	if err := config.Init(); err == nil {
		cfg := config.Get()
		if cfg.HasPair() {
			p, err := plan.Parse(content)
			if err == nil {
				printActiveWithSection(p, cfg)
			}
		}
	}

	return nil
}

// printActiveWithSection computes and prints the "Active With" section by scanning
// spec frontmatter for branches co-authored by both pair members.
func printActiveWithSection(p *plan.Plan, cfg *config.Config) {
	// Build a repo-name→local-path map from discovered repos.
	repoPathMap := buildRepoPathMap(cfg)

	activeWith := p.FindActiveWithBranches(cfg.Author, cfg.Pair.Teammate, func(repo string) string {
		return repoPathMap[repo]
	})

	if len(activeWith) == 0 {
		return
	}

	fmt.Printf("\n## Active With %s\n\n", cfg.PairDisplayName())
	for _, entry := range activeWith {
		line := fmt.Sprintf("- **%s:%s** — %s", entry.Repo, entry.Branch, entry.Reason)
		if entry.SpecPath != "" {
			line += " 📄 " + entry.SpecPath
		}
		fmt.Println(line)
	}
}

// buildRepoPathMap returns a map of repo display name → local filesystem path.
func buildRepoPathMap(cfg *config.Config) map[string]string {
	m := map[string]string{}
	for _, baseDir := range cfg.Discovery.BaseDirs {
		entries, err := os.ReadDir(baseDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			ownerPath := baseDir + "/" + e.Name()
			subs, err := os.ReadDir(ownerPath)
			if err != nil {
				continue
			}
			for _, sub := range subs {
				if sub.IsDir() {
					repoPath := ownerPath + "/" + sub.Name()
					// Use same naming logic as repoDisplayName.
					name := strings.TrimSuffix(e.Name()+"/"+sub.Name(), ".git")
					m[name] = repoPath
				}
			}
		}
	}
	return m
}

func addAnnotation(reason string) error {
	cwd, err := resolveCwd()
	if err != nil {
		return err
	}

	jjc := jj.NewCLI()
	if !jjc.IsRepo(cwd) {
		return fmt.Errorf("not a jj repository")
	}

	root, err := jjc.Root(cwd)
	if err != nil {
		return fmt.Errorf("failed to get repository root: %w", err)
	}
	repoName := filepath.Base(root)

	branch := currentBookmark(jjc, cwd)
	if branch == "" {
		return fmt.Errorf("could not determine current bookmark; check `jj log -r @`")
	}

	// Load store
	s, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}

	if err := s.EnsureDir(); err != nil {
		return err
	}

	// Load and update state under the lock so a concurrent write can't clobber
	// the annotation.
	if err := s.MutateState(func(state *store.State) (bool, error) {
		state.AddAnnotation(cwd, branch, reason)
		state.AddRepo(cwd, "")
		return true, nil
	}); err != nil {
		return fmt.Errorf("failed to save state: %w", err)
	}

	// Load and update plan
	content, err := s.LoadPlan()
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	p, err := plan.Parse(content)
	if err != nil {
		return fmt.Errorf("failed to parse plan: %w", err)
	}

	p.AddBranch(repoName, branch, reason)

	if err := s.SavePlan(p.Render()); err != nil {
		return fmt.Errorf("failed to save plan: %w", err)
	}

	// Create symlink
	if err := s.CreatePlanSymlink(); err != nil {
		// Log but don't fail
		fmt.Fprintf(os.Stderr, "warning: failed to create ~/.plan symlink: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "Added annotation: %s:%s — %s\n", repoName, branch, reason)
	return nil
}

func editPlan() error {
	s, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}

	planPath := s.GetPlanSymlinkPath()

	// Check if ~/.plan exists, if not use ~/.wgo/plan.md
	if _, err := os.Stat(planPath); err != nil {
		home, _ := os.UserHomeDir()
		planPath = home + "/.wgo/plan.md"
	}

	// Get editor from environment
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}

	cmd := exec.Command(editor, planPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to open editor: %w", err)
	}

	return nil
}
