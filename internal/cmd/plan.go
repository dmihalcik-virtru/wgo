package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/git"
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
	return nil
}

func addAnnotation(reason string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	gitClient, err := git.NewFromCwd()
	if err != nil {
		return fmt.Errorf("failed to create git client: %w", err)
	}

	isRepo, err := gitClient.IsRepo(cwd)
	if err != nil || !isRepo {
		return fmt.Errorf("not a git repository")
	}

	repoName, err := gitClient.RepoName(cwd)
	if err != nil {
		return fmt.Errorf("failed to get repository name: %w", err)
	}

	branch, err := gitClient.CurrentBranch(cwd)
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}

	// Load store
	s, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}

	if err := s.EnsureDir(); err != nil {
		return err
	}

	// Load and update state
	state, err := s.LoadState()
	if err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	state.AddAnnotation(cwd, branch, reason)
	state.AddRepo(cwd, "")

	if err := s.SaveState(state); err != nil {
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

	fmt.Printf("Added annotation: %s:%s — %s\n", repoName, branch, reason)
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
