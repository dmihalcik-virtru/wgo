package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/links"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
	"github.com/virtru/wgo/models"
)

var lsFormat string

// lsCmd represents the `wgo ls` command.
var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List all tracked and discovered repositories",
	Long: `List all repositories across configured discovery directories with their current branch and status.

When stdout is not a terminal (e.g. in a pipe), prints one path per line by default.
Use --format to override: table, path, or json.

Examples:
  wgo ls                          # table when a TTY, paths when piped
  wgo ls --format=table           # always table
  wgo ls --format=path            # one path per line
  wgo ls --format=json            # JSON array
  wgo ls | fzf | xargs -I{} git -C {} pull`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return listRepos(cmd)
	},
}

func init() {
	rootCmd.AddCommand(lsCmd)
	lsCmd.Flags().StringVar(&lsFormat, "format", "", "Output format: table, path, json (default: table when TTY, path when piped)")
}

func listRepos(cmd *cobra.Command) error {
	if err := config.Init(); err != nil {
		return fmt.Errorf("failed to initialize config: %w", err)
	}

	cfg := config.Get()

	d := discovery.New(cfg.Discovery.BaseDirs, cfg.Discovery.ScanDepth, cfg.Discovery.ExcludePatterns)
	repos, err := d.DiscoverAll()
	if err != nil {
		return fmt.Errorf("failed to discover repositories: %w", err)
	}

	// Determine format
	format := lsFormat
	if format == "" {
		if isTerminal() {
			format = "table"
		} else {
			format = "path"
		}
	}

	if format == "path" {
		for _, repo := range repos {
			fmt.Println(repo.Path)
		}
		return nil
	}

	// For table and json we need branch/status/annotations
	s, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}

	planContent, _ := s.LoadPlan()
	p, _ := plan.Parse(planContent)
	state, _ := s.LoadState()

	type row struct {
		Path      string `json:"path"`
		Repo      string `json:"repo"`
		Branch    string `json:"branch"`
		Why       string `json:"why"`
		Status    string `json:"status"`
		RepoURL   string `json:"repo_url,omitempty"`
		BranchURL string `json:"branch_url,omitempty"`
	}

	rows := make([]row, 0, len(repos))
	for _, repo := range repos {
		gitClient := git.New(repo.Path)

		branch, err := gitClient.CurrentBranch(repo.Path)
		if err != nil {
			branch = "?"
		}

		gitStatus, err := gitClient.Status(repo.Path)
		if err != nil {
			gitStatus.Modified = -1
		}

		repoName := repo.Name
		if cfg.UI.TildeHome {
			repoName = trimHome(repoName)
		}

		why := "—"
		if p != nil && p.GetBranch(repoName, branch) != nil {
			why = p.GetBranch(repoName, branch).Reason
		} else if state != nil {
			ann := state.GetAnnotation(repo.Path, branch)
			if ann != nil {
				why = ann.Purpose
			}
		}

		remoteURL, _ := gitClient.RemoteURL(repo.Path)
		rows = append(rows, row{
			Path:      repo.Path,
			Repo:      repoName,
			Branch:    branch,
			Why:       why,
			Status:    formatStatusShort(gitStatus),
			RepoURL:   links.RepoURL(remoteURL),
			BranchURL: links.BranchURL(remoteURL, branch),
		})
	}

	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	// table
	tty := isTerminal()
	fmt.Printf("%-20s %-20s %-32s %-15s\n", "REPO", "BRANCH", "WHY", "STATUS")
	fmt.Println(strings.Repeat("-", 87))
	for _, r := range rows {
		why := r.Why
		if len(why) > 32 {
			why = why[:29] + "..."
		}
		repoDisplay := links.Link(r.RepoURL, r.Repo, tty)
		branchDisplay := links.Link(r.BranchURL, r.Branch, tty)
		fmt.Printf("%-20s %-20s %-32s %-15s\n", repoDisplay, branchDisplay, why, r.Status)
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
