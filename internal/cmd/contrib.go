package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/contrib"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/links"
)

var (
	contribWeeks  int
	contribAuthor string
)

// contribCmd represents the `wgo contrib` command.
var contribCmd = &cobra.Command{
	Use:   "contrib",
	Short: "Git activity heatmap across all discovered repos",
	Long: `Show a contributions heatmap of local git activity across all discovered repositories.

Repos are discovered automatically from configured base directories (default: ~/Documents/GitHub).
Configure discovery paths in ~/.wgo/config.toml under [discovery] base_dirs.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return showContrib(contribWeeks, contribAuthor)
	},
}

func init() {
	rootCmd.AddCommand(contribCmd)
	contribCmd.Flags().IntVar(&contribWeeks, "weeks", 12, "Number of weeks to show")
	contribCmd.Flags().StringVar(&contribAuthor, "author", "", "Filter commits by author (email or name); defaults to git user.email")
}

func showContrib(weeks int, author string) error {
	if err := config.Init(); err != nil {
		return fmt.Errorf("failed to initialize config: %w", err)
	}
	cfg := config.Get()

	// If no --author flag given, use config default (set from git user.email)
	if author == "" {
		author = cfg.Author
	}

	d := discovery.New(cfg.Discovery.BaseDirs, cfg.Discovery.ScanDepth, cfg.Discovery.ExcludePatterns)
	repos, err := d.DiscoverAll()
	if err != nil {
		return fmt.Errorf("failed to discover repositories: %w", err)
	}

	if len(repos) == 0 {
		fmt.Println("No repos found. Configure discovery paths in ~/.wgo/config.toml under [discovery] base_dirs.")
		return nil
	}

	repoPaths := make([]string, len(repos))
	for i, r := range repos {
		repoPaths[i] = r.Path
	}

	now := time.Now()
	since := now.AddDate(0, 0, -weeks*7)

	activities, total, err := contrib.Collect(repoPaths, since, author)
	if err != nil {
		return fmt.Errorf("failed to collect git activity: %w", err)
	}

	fmt.Printf("Git Activity — Last %d weeks\n\n", weeks)
	fmt.Print(contrib.RenderHeatmap(total, weeks, now))

	totalCommits := 0
	for _, n := range total {
		totalCommits += n
	}
	fmt.Printf("\nTotal: %d commits · %d repos\n", totalCommits, len(activities))

	top := contrib.TopRepos(activities, 5)
	if len(top) > 0 {
		isTTY := isTerminal()
		fmt.Print("Top: ")
		for i, r := range top {
			if i > 0 {
				fmt.Print(", ")
			}
			label := links.Link(r.GitHubURL, r.Name, isTTY)
			fmt.Printf("%s (%d)", label, r.Total)
		}
		fmt.Println()
	}

	return nil
}
