package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/contrib"
	"github.com/virtru/wgo/internal/store"
)

var contribWeeks int

// contribCmd represents the `wgo contrib` command.
var contribCmd = &cobra.Command{
	Use:   "contrib",
	Short: "Git activity heatmap across all tracked repos",
	Long:  `Show a contributions heatmap of local git activity across all tracked repositories.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return showContrib(contribWeeks)
	},
}

func init() {
	rootCmd.AddCommand(contribCmd)
	contribCmd.Flags().IntVar(&contribWeeks, "weeks", 12, "Number of weeks to show")
}

func showContrib(weeks int) error {
	s, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}

	state, err := s.LoadState()
	if err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	var repoPaths []string
	for path := range state.Repos {
		repoPaths = append(repoPaths, path)
	}

	if len(repoPaths) == 0 {
		fmt.Println("No tracked repos. Run `wgo track <path>` or `wgo ls` to discover repos.")
		return nil
	}

	now := time.Now()
	since := now.AddDate(0, 0, -weeks*7)

	activities, total, err := contrib.Collect(repoPaths, since)
	if err != nil {
		return fmt.Errorf("failed to collect git activity: %w", err)
	}

	fmt.Printf("Git Activity — Last %d weeks\n\n", weeks)
	fmt.Print(contrib.RenderHeatmap(total, weeks, now))

	// Summary
	totalCommits := 0
	for _, n := range total {
		totalCommits += n
	}
	fmt.Printf("\nTotal: %d commits · %d repos\n", totalCommits, len(activities))

	top := contrib.TopRepos(activities, 5)
	if len(top) > 0 {
		fmt.Print("Top: ")
		for i, r := range top {
			if i > 0 {
				fmt.Print(", ")
			}
			fmt.Printf("%s (%d)", r.Name, r.Total)
		}
		fmt.Println()
	}

	return nil
}
