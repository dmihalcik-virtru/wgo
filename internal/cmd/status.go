package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/status"
	"github.com/virtru/wgo/internal/store"
	"github.com/virtru/wgo/pkg/models"
)

var (
	statusSince    string
	statusFilter   string
	statusSort     string
	statusWatch    bool
	statusInterval int
	statusVerbose  bool
	statusJSON     bool
	statusCSV      bool
	statusGo       int
	statusOpen     int
	statusStale    int
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show development activity across all repositories",
	Long: `Shows a dashboard of all discovered repositories with their current
branch, status, recent commits, and activity. Supports filtering, sorting,
watch mode, and machine-readable output.

Examples:
  wgo status                      # All repos, sorted by recent activity
  wgo status --since today        # What did I work on today?
  wgo status --since 1h           # Activity in the last hour
  wgo status --filter modified    # Only repos with uncommitted changes
  wgo status --sort lines         # Sort by most lines changed
  wgo status --watch              # Auto-refresh like top
  wgo status --json               # Machine-readable output
  wgo status --go 3               # Print path of row #3 (for cd integration)
  wgo status --open 3             # Open row #3's PR in browser`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatus()
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)

	statusCmd.Flags().StringVar(&statusSince, "since", "", "Time filter: 1h, today, yesterday, 3d, 1w")
	statusCmd.Flags().StringVarP(&statusFilter, "filter", "f", "", "State filter: modified, clean, stale, staged, conflict, dirty")
	statusCmd.Flags().StringVarP(&statusSort, "sort", "s", "", "Sort: activity, name, status, changes, commits, lines")
	statusCmd.Flags().BoolVarP(&statusWatch, "watch", "w", false, "Auto-refresh mode")
	statusCmd.Flags().IntVarP(&statusInterval, "interval", "i", 0, "Refresh interval in seconds (default from config)")
	statusCmd.Flags().BoolVarP(&statusVerbose, "verbose", "v", false, "Show extra columns (lines, why, path)")
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "JSON output")
	statusCmd.Flags().BoolVar(&statusCSV, "csv", false, "CSV output")
	statusCmd.Flags().IntVar(&statusGo, "go", 0, "Print path of row N (for cd integration)")
	statusCmd.Flags().IntVar(&statusOpen, "open", 0, "Open row N's PR in browser, or path in Finder")
	statusCmd.Flags().IntVar(&statusStale, "stale-days", 0, "Days of inactivity before marking stale (default from config)")
}

func runStatus() error {
	// Initialize config
	if err := config.Init(); err != nil {
		return fmt.Errorf("failed to initialize config: %w", err)
	}
	cfg := config.Get()

	// Apply config defaults for unset flags
	sortBy := statusSort
	if sortBy == "" {
		sortBy = cfg.Status.DefaultSort
	}
	if sortBy == "" {
		sortBy = "activity"
	}

	staleDays := statusStale
	if staleDays == 0 {
		staleDays = cfg.Status.StaleDays
	}
	if staleDays == 0 {
		staleDays = 14
	}

	interval := statusInterval
	if interval == 0 {
		interval = cfg.Status.RefreshInterval
	}
	if interval == 0 {
		interval = 5
	}

	// Parse --since
	var since time.Time
	if statusSince != "" {
		var err error
		since, err = status.ParseSince(statusSince)
		if err != nil {
			return fmt.Errorf("invalid --since value: %w", err)
		}
	}

	// Discover repos
	d := discovery.New(cfg.Discovery.BaseDirs, cfg.Discovery.ScanDepth, cfg.Discovery.ExcludePatterns)
	repos, err := d.DiscoverAll()
	if err != nil {
		return fmt.Errorf("failed to discover repositories: %w", err)
	}

	if len(repos) == 0 {
		fmt.Println("No repositories discovered. Check your config: ~/.wgo/config.toml")
		return nil
	}

	// Load plan and state
	s, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}

	planContent, _ := s.LoadPlan()
	p, _ := plan.Parse(planContent)
	state, _ := s.LoadState()

	// Build collector
	gitClient := git.New("")
	staleThreshold := time.Duration(staleDays) * 24 * time.Hour

	collectorOpts := []status.CollectorOption{
		status.WithStaleThreshold(staleThreshold),
	}
	if !since.IsZero() {
		collectorOpts = append(collectorOpts, status.WithSince(since))
	}

	collector := status.NewCollector(gitClient, p, state, collectorOpts...)

	// Watch mode
	if statusWatch {
		return runWatchMode(collector, repos, since, sortBy, interval)
	}

	// Single run
	ctx := context.Background()
	activities := collector.CollectAll(ctx, repos)
	activities = status.FilterActivities(activities, statusFilter, since)
	status.SortActivities(activities, sortBy)

	// --go: print path and exit
	if statusGo > 0 {
		return handleGo(activities, statusGo)
	}

	// --open: open PR or path
	if statusOpen > 0 {
		return handleOpen(activities, statusOpen)
	}

	// Render
	if statusJSON {
		return status.RenderJSON(os.Stdout, activities)
	}
	if statusCSV {
		return status.RenderCSV(os.Stdout, activities, statusVerbose)
	}

	status.RenderTable(os.Stdout, activities, statusVerbose)
	return nil
}

func runWatchMode(collector *status.Collector, repos []discovery.DiscoveredRepo, since time.Time, sortBy string, interval int) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	// Initial render
	renderWatch(ctx, collector, repos, since, sortBy)

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\n[Stopped]")
			return nil
		case <-ticker.C:
			renderWatch(ctx, collector, repos, since, sortBy)
		}
	}
}

func renderWatch(ctx context.Context, collector *status.Collector, repos []discovery.DiscoveredRepo, since time.Time, sortBy string) {
	// Clear screen
	fmt.Print("\033[2J\033[H")

	activities := collector.CollectAll(ctx, repos)
	activities = status.FilterActivities(activities, statusFilter, since)
	status.SortActivities(activities, sortBy)

	status.RenderWatchHeader(os.Stdout, activities, statusSince, sortBy)
	status.RenderTable(os.Stdout, activities, statusVerbose)

	fmt.Println("\n[Press Ctrl+C to exit]")
}

func handleGo(activities []models.RepoActivity, row int) error {
	if row < 1 || row > len(activities) {
		return fmt.Errorf("row %d out of range (1-%d)", row, len(activities))
	}
	fmt.Print(activities[row-1].Path)
	return nil
}

func handleOpen(activities []models.RepoActivity, row int) error {
	if row < 1 || row > len(activities) {
		return fmt.Errorf("row %d out of range (1-%d)", row, len(activities))
	}

	path := activities[row-1].Path

	// Try gh to open PR
	ghCmd := exec.Command("gh", "pr", "view", "--web")
	ghCmd.Dir = path
	ghCmd.Stdout = os.Stdout
	ghCmd.Stderr = os.Stderr
	if err := ghCmd.Run(); err == nil {
		return nil
	}

	// Fallback: open path in Finder (macOS)
	openCmd := exec.Command("open", path)
	return openCmd.Run()
}
