package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
	"github.com/virtru/wgo/pkg/models"
)

// lsCmd represents the `wgo ls` command.
var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List all tracked and discovered repositories",
	Long:  `List all repositories across configured discovery directories with their current branch and status.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return listRepos()
	},
}

func init() {
	rootCmd.AddCommand(lsCmd)
}

func listRepos() error {
	// Initialize config
	if err := config.Init(); err != nil {
		return fmt.Errorf("failed to initialize config: %w", err)
	}

	cfg := config.Get()

	// Discover repos
	d := discovery.New(cfg.Discovery.BaseDirs, cfg.Discovery.ScanDepth, cfg.Discovery.ExcludePatterns)
	repos, err := d.DiscoverAll()
	if err != nil {
		return fmt.Errorf("failed to discover repositories: %w", err)
	}

	// Load plan and state
	s, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}

	planContent, _ := s.LoadPlan()
	p, _ := plan.Parse(planContent)

	state, _ := s.LoadState()

	// Print header
	fmt.Printf("%-20s %-20s %-32s %-15s\n", "REPO", "BRANCH", "WHY", "STATUS")
	fmt.Println(strings.Repeat("-", 87))

	// Print repos
	for _, repo := range repos {
		gitClient := git.New(repo.Path)

		branch, err := gitClient.CurrentBranch(repo.Path)
		if err != nil {
			branch = "?"
		}

		status, err := gitClient.Status(repo.Path)
		if err != nil {
			status.Modified = -1
		}

		repoName := repo.Name
		if cfg.UI.TildeHome {
			repoName = trimHome(repoName)
		}

		// Get annotation if available
		why := "—"
		if p != nil && p.GetBranch(repoName, branch) != nil {
			why = p.GetBranch(repoName, branch).Reason
		} else if state != nil {
			ann := state.GetAnnotation(repo.Path, branch)
			if ann != nil {
				why = ann.Purpose
			}
		}

		// Limit why to 32 chars
		if len(why) > 32 {
			why = why[:29] + "..."
		}

		statusStr := formatStatusShort(status)

		fmt.Printf("%-20s %-20s %-32s %-15s\n", repoName, branch, why, statusStr)
	}

	return nil
}

func formatStatusShort(status models.GitStatus) string {
	if status.Modified == -1 {
		return "?"
	}

	if status.Modified == 0 && status.Added == 0 && status.Deleted == 0 && status.Untracked == 0 {
		return "clean"
	}

	var parts []string
	if status.Modified > 0 {
		parts = append(parts, fmt.Sprintf("%dM", status.Modified))
	}
	if status.Added > 0 {
		parts = append(parts, fmt.Sprintf("%dA", status.Added))
	}
	if status.Deleted > 0 {
		parts = append(parts, fmt.Sprintf("%dD", status.Deleted))
	}
	if status.Untracked > 0 {
		parts = append(parts, fmt.Sprintf("%dU", status.Untracked))
	}

	return strings.Join(parts, " ")
}

func trimHome(path string) string {
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}
