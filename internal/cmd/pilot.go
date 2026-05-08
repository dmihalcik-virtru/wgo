package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/pilot"
	"github.com/virtru/wgo/internal/store"
)

var pilotCmd = &cobra.Command{
	Use:   "pilot",
	Short: "Pilot workflow utilities",
}

var (
	pilotSince  string
	pilotUntil  string
	pilotTeam   string
	pilotOutput string
	pilotJSON   bool
)

var pilotSummaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Generate pilot workflow summary draft",
	Long: `Aggregate spec, PR, review, and daily-log data over a date range and
emit a markdown draft mapped to the pilot's required workflow-summary sections.`,
	RunE: runPilotSummary,
}

func runPilotSummary(cmd *cobra.Command, args []string) error {
	since, err := time.ParseInLocation("2006-01-02", pilotSince, time.Local)
	if err != nil {
		return fmt.Errorf("invalid --since date (expected YYYY-MM-DD): %w", err)
	}

	untilStr := pilotUntil
	if untilStr == "" {
		untilStr = time.Now().Format("2006-01-02")
	}
	until, err := time.ParseInLocation("2006-01-02", untilStr, time.Local)
	if err != nil {
		return fmt.Errorf("invalid --until date (expected YYYY-MM-DD): %w", err)
	}
	// Include the full until day
	until = until.Add(24*time.Hour - time.Second)

	var team []string
	if pilotTeam != "" {
		for _, t := range strings.Split(pilotTeam, ",") {
			if h := strings.TrimSpace(t); h != "" {
				team = append(team, h)
			}
		}
	}

	s, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to open store: %w", err)
	}

	state, err := s.LoadState()
	if err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	var repos []string
	for repoPath := range state.Repos {
		repos = append(repos, repoPath)
	}

	home, _ := os.UserHomeDir()
	logsDir := filepath.Join(home, ".wgo", "logs")

	opts := pilot.Options{
		Since: since,
		Until: until,
		Team:  team,
	}

	m, err := pilot.Collect(repos, logsDir, opts)
	if err != nil {
		return fmt.Errorf("failed to collect metrics: %w", err)
	}

	var output string
	if pilotJSON {
		data, jsonErr := pilot.RenderJSON(m)
		if jsonErr != nil {
			return fmt.Errorf("failed to render JSON: %w", jsonErr)
		}
		output = string(data) + "\n"
	} else {
		output = pilot.RenderMarkdown(m, opts)
	}

	if pilotOutput != "" {
		if err := os.WriteFile(pilotOutput, []byte(output), 0o644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Summary written to %s\n", pilotOutput)
		return nil
	}

	fmt.Print(output)
	return nil
}

func init() {
	pilotSummaryCmd.Flags().StringVar(&pilotSince, "since", "", "start date YYYY-MM-DD (required)")
	pilotSummaryCmd.Flags().StringVar(&pilotUntil, "until", "", "end date YYYY-MM-DD (default: today)")
	pilotSummaryCmd.Flags().StringVar(&pilotTeam, "team", "", "comma-separated GitHub handles")
	pilotSummaryCmd.Flags().StringVar(&pilotOutput, "output", "", "write output to this file instead of stdout")
	pilotSummaryCmd.Flags().BoolVar(&pilotJSON, "json", false, "emit structured JSON instead of markdown")
	_ = pilotSummaryCmd.MarkFlagRequired("since")

	pilotCmd.AddCommand(pilotSummaryCmd)
	rootCmd.AddCommand(pilotCmd)
}
