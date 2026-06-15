package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/links"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
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

	jjc := jj.NewCLI()
	rows := make([]row, 0, len(repos))
	for _, repo := range repos {
		branch := currentBookmark(jjc, repo.Path)
		if branch == "" {
			branch = "?"
		}

		statusStr := "?"
		if st, err := jjc.Status(repo.Path); err == nil {
			statusStr = formatJJStatusShort(st)
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

		remotes, _ := jjc.RemoteURLs(repo.Path)
		remoteURL := remotes["origin"]
		rows = append(rows, row{
			Path:      repo.Path,
			Repo:      repoName,
			Branch:    branch,
			Why:       why,
			Status:    statusStr,
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

// formatJJStatusShort renders a jj.Status as a compact "5M 2A 1D"-style
// string for the table view. Returns "clean" when the workspace matches
// its parent commit. jj has no "untracked" concept — newly written files
// land in Added — so the trailing "U" column from the git formatter is
// dropped here.
func formatJJStatusShort(st jj.Status) string {
	if st.Clean {
		return "clean"
	}
	var parts []string
	if n := len(st.Modified); n > 0 {
		parts = append(parts, fmt.Sprintf("%dM", n))
	}
	if n := len(st.Added); n > 0 {
		parts = append(parts, fmt.Sprintf("%dA", n))
	}
	if n := len(st.Deleted); n > 0 {
		parts = append(parts, fmt.Sprintf("%dD", n))
	}
	if len(parts) == 0 {
		return "clean"
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
