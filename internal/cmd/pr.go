package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/links"
	"github.com/virtru/wgo/internal/status"
)

var (
	prMine   bool
	prReview bool
	prSince  string
	prWatch  bool
	prJSON   bool
	prOpen   int
)

var prCmd = &cobra.Command{
	Use:   "pr",
	Short: "Show pull request dashboard",
	Long: `Shows your open pull requests and PRs you're involved with that have new activity.

MY PULL REQUESTS lists all open and draft PRs you authored.

NEEDS ATTENTION lists PRs where you've commented or been assigned that have been
updated after your last recorded activity (comment or review) on them.

Use --since to filter both sections to PRs updated within a time window.
Use --open N to open PR #N directly in your browser.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPRCmd()
	},
}

func init() {
	rootCmd.AddCommand(prCmd)
	prCmd.Flags().BoolVar(&prMine, "mine", false, "Only show my authored PRs")
	prCmd.Flags().BoolVar(&prReview, "review", false, "Only show PRs needing attention")
	prCmd.Flags().StringVar(&prSince, "since", "", "Only show PRs updated since TIME (e.g. 1h, today, 2026-02-20)")
	prCmd.Flags().BoolVar(&prWatch, "watch", false, "Refresh dashboard on an interval (every 60s)")
	prCmd.Flags().BoolVar(&prJSON, "json", false, "Output as JSON")
	prCmd.Flags().IntVar(&prOpen, "open", 0, "Open PR `N` in browser and exit")
}

func runPRCmd() error {
	if err := config.Init(); err != nil {
		return fmt.Errorf("failed to initialize config: %w", err)
	}

	gh := github.NewClient()
	if !gh.Available() {
		return fmt.Errorf("gh CLI not available — install from https://cli.github.com/")
	}

	// --open N: open the PR in browser and exit immediately. Resolve the
	// PR's URL via the same HTTP client used by the rest of the dashboard.
	if prOpen > 0 {
		cwd, _ := os.Getwd()
		pr, err := gh.GetPRByNumber(cwd, prOpen)
		if err != nil {
			return fmt.Errorf("look up PR #%d: %w", prOpen, err)
		}
		if pr == nil || pr.URL == "" {
			return fmt.Errorf("PR #%d not found or has no URL", prOpen)
		}
		return links.OpenInBrowser(pr.URL)
	}

	if prWatch {
		return runPRWatch(gh)
	}
	return runPROnce(gh)
}

func runPROnce(gh *github.CLIClient) error {
	mine, involved, myLogin, err := fetchAllPRData(gh)
	if err != nil {
		return err
	}

	cfg := config.Get()

	if prJSON {
		return renderPRJSON(mine, involved, myLogin)
	}
	renderPRTable(mine, involved, myLogin, cfg)
	return nil
}

func runPRWatch(gh *github.CLIClient) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
	}()

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	// Show cursor on exit
	fmt.Print("\033[?25l") // hide cursor
	defer fmt.Print("\033[?25h\033[0m\n")

	cfg := config.Get()
	refresh := func() {
		mine, involved, myLogin, err := fetchAllPRData(gh)
		fmt.Print("\033[2J\033[H") // clear screen
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return
		}
		renderPRTable(mine, involved, myLogin, cfg)
		fmt.Printf("\n  Refreshed %s — Ctrl+C to exit\n", time.Now().Format("15:04:05"))
	}

	refresh()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			refresh()
		}
	}
}

// fetchAllPRData collects all PR data: my PRs, involved PRs, checks, and last-activity.
func fetchAllPRData(gh *github.CLIClient) (mine, involved []github.ExtendedPRInfo, myLogin string, err error) {
	// Resolve the current GitHub user first
	myLogin, err = gh.CurrentUser()
	if err != nil {
		return nil, nil, "", fmt.Errorf("could not determine GitHub user: %w", err)
	}

	// Parse --since filter
	var sinceTime time.Time
	if prSince != "" {
		sinceTime, err = parsePRSince(prSince)
		if err != nil {
			return nil, nil, "", fmt.Errorf("invalid --since value: %w", err)
		}
	}

	showMine := !prReview // show MY PULL REQUESTS unless --review only
	showReview := !prMine // show NEEDS ATTENTION unless --mine only

	var fetchErr error
	var wg sync.WaitGroup

	if showMine {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var e error
			mine, e = gh.ListMyOpenPRs()
			if e != nil {
				fetchErr = e
				return
			}
			// Apply --since filter
			if !sinceTime.IsZero() {
				mine = filterPRsSince(mine, sinceTime)
			}
			// Enrich with CI check status in parallel
			gh.EnrichWithChecks(mine)
		}()
	}

	if showReview {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var e error
			involved, e = gh.ListInvolvedPRs(myLogin)
			if e != nil {
				fetchErr = e
				return
			}
			// Apply --since filter; if no --since, we still need all PRs to compute HasNewActivity
			// Enrich with last-activity via GraphQL
			gh.EnrichWithActivity(involved, myLogin)
			// After enrichment, filter: if --since is set show updated since that time,
			// otherwise show only PRs with new activity since my last comment
			if !sinceTime.IsZero() {
				involved = filterPRsSince(involved, sinceTime)
			} else {
				involved = filterPRsWithNewActivity(involved)
			}
		}()
	}

	wg.Wait()
	if fetchErr != nil {
		return nil, nil, "", fetchErr
	}
	return mine, involved, myLogin, nil
}

func filterPRsSince(prs []github.ExtendedPRInfo, since time.Time) []github.ExtendedPRInfo {
	var result []github.ExtendedPRInfo
	for _, pr := range prs {
		if pr.UpdatedAt.After(since) {
			result = append(result, pr)
		}
	}
	return result
}

func filterPRsWithNewActivity(prs []github.ExtendedPRInfo) []github.ExtendedPRInfo {
	var result []github.ExtendedPRInfo
	for _, pr := range prs {
		// Include PRs where I have no prior activity too — they always need attention
		if pr.MyLastActivity.IsZero() || pr.HasNewActivity {
			result = append(result, pr)
		}
	}
	return result
}

// renderPRTable prints the PR dashboard: optional Pair section, then mine and needs-attention.
func renderPRTable(mine, involved []github.ExtendedPRInfo, myLogin string, cfg *config.Config) {
	tty := isTerminal()
	gh := github.NewClient()

	// Pair section — shown when pair is configured.
	if cfg.HasPair() {
		renderPairSection(gh, cfg, myLogin, tty)
	}

	if len(mine) == 0 && len(involved) == 0 {
		if !cfg.HasPair() {
			fmt.Println("  No pull requests found.")
		}
		return
	}

	if len(mine) > 0 {
		fmt.Println("MY PULL REQUESTS")
		for _, pr := range mine {
			printPRRow(pr, tty, false)
		}
		fmt.Println()
	}

	if len(involved) > 0 {
		if prSince != "" {
			fmt.Printf("NEEDS ATTENTION  (updated since %s)\n", prSince)
		} else {
			fmt.Println("NEEDS ATTENTION  (updated after your last activity)")
		}
		for _, pr := range involved {
			printPRRow(pr, tty, true)
		}
		fmt.Println()
	}
}

// renderPairSection fetches and renders the teammate's open PRs and review requests.
func renderPairSection(gh *github.CLIClient, cfg *config.Config, myLogin string, tty bool) {
	pairName := cfg.PairDisplayName()
	fmt.Printf("PAIR  (%s)\n", pairName)

	var teamPRs, reviewReqs []github.ExtendedPRInfo
	var wg sync.WaitGroup
	var mu sync.Mutex
	var fetchErr string

	wg.Add(1)
	go func() {
		defer wg.Done()
		prs, err := gh.ListPRsByAuthor(cfg.Pair.Teammate)
		if err != nil {
			mu.Lock()
			fetchErr = err.Error()
			mu.Unlock()
			return
		}
		mu.Lock()
		teamPRs = prs
		mu.Unlock()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		reqs, err := gh.ListPRsReviewRequestedFor(myLogin)
		if err != nil {
			return
		}
		// We don't have the author field from searchPRItem after conversion,
		// so include all review-requested PRs (they are not the user's own).
		filtered := append([]github.ExtendedPRInfo(nil), reqs...)
		mu.Lock()
		reviewReqs = filtered
		mu.Unlock()
	}()

	wg.Wait()

	if fetchErr != "" {
		fmt.Printf("  could not fetch pair activity: %s\n\n", fetchErr)
		return
	}

	if len(teamPRs) > 0 {
		fmt.Printf("  open (%d)\n", len(teamPRs))
		for _, pr := range teamPRs {
			printPRRow(pr, tty, false)
		}
	}

	if len(reviewReqs) > 0 {
		fmt.Printf("  review requested (%d)\n", len(reviewReqs))
		for _, pr := range reviewReqs {
			printPRRow(pr, tty, false)
		}
	}

	if len(teamPRs) == 0 && len(reviewReqs) == 0 {
		fmt.Printf("  no open PRs\n")
	}
	fmt.Println()
}

// printPRRow prints a single PR row with OSC8 hyperlinks on PR number and repo name.
func printPRRow(pr github.ExtendedPRInfo, tty bool, showLastActivity bool) {
	// #42 as clickable link → PR URL
	prLabel := fmt.Sprintf("#%-4d", pr.Number)
	prLink := links.Link(pr.URL, prLabel, tty)

	// owner/repo as clickable link → repo URL
	repoLabel := fmt.Sprintf("%-22s", pr.RepoSlug())
	repoLink := links.Link(pr.RepoURL(), repoLabel, tty)

	title := truncatePR(pr.Title, 42)
	state := fmt.Sprintf("%-8s", pr.StateLabel())
	updated := fmt.Sprintf("%-12s", formatTimeSincePR(pr.UpdatedAt))

	suffix := formatChecks(pr)
	if showLastActivity && !pr.MyLastActivity.IsZero() {
		suffix = fmt.Sprintf("(your activity: %s)", formatTimeSincePR(pr.MyLastActivity))
	}

	fmt.Printf("  %s  %s  %-42s  %s  %s  %s\n",
		prLink, repoLink, title, state, updated, suffix)
}

// renderPRJSON outputs both sections as structured JSON.
func renderPRJSON(mine, involved []github.ExtendedPRInfo, currentUser string) error {
	type prJSON struct {
		Number         int       `json:"number"`
		Title          string    `json:"title"`
		State          string    `json:"state"`
		IsDraft        bool      `json:"is_draft"`
		URL            string    `json:"url"`
		Repo           string    `json:"repo"`
		UpdatedAt      time.Time `json:"updated_at"`
		CheckPassing   int       `json:"check_passing,omitempty"`
		CheckTotal     int       `json:"check_total,omitempty"`
		MyLastActivity time.Time `json:"my_last_activity,omitempty"`
		HasNewActivity bool      `json:"has_new_activity,omitempty"`
	}
	toJSON := func(prs []github.ExtendedPRInfo) []prJSON {
		out := make([]prJSON, len(prs))
		for i, pr := range prs {
			out[i] = prJSON{
				Number:         pr.Number,
				Title:          pr.Title,
				State:          pr.StateLabel(),
				IsDraft:        pr.IsDraft,
				URL:            pr.URL,
				Repo:           pr.RepoSlug(),
				UpdatedAt:      pr.UpdatedAt,
				CheckPassing:   pr.CheckPassing,
				CheckTotal:     pr.CheckTotal,
				MyLastActivity: pr.MyLastActivity,
				HasNewActivity: pr.HasNewActivity,
			}
		}
		return out
	}
	out := map[string]interface{}{
		"user":            currentUser,
		"my_prs":          toJSON(mine),
		"needs_attention": toJSON(involved),
		"generated_at":    time.Now().Format(time.RFC3339),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// parsePRSince parses a time string, supporting both relative (1h, today) and
// absolute date (2026-02-20) formats.
func parsePRSince(s string) (time.Time, error) {
	// Try absolute date format first
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t, nil
	}
	// Fall back to relative formats from the status package
	return status.ParseSince(s)
}

// formatChecks returns a compact CI status string like "✓ 3/3" or "✗ 1/3".
func formatChecks(pr github.ExtendedPRInfo) string {
	if pr.CheckTotal < 0 {
		return ""
	}
	if pr.CheckTotal == 0 {
		return "no checks"
	}
	if pr.CheckPassing == pr.CheckTotal {
		return fmt.Sprintf("✓ %d/%d", pr.CheckPassing, pr.CheckTotal)
	}
	return fmt.Sprintf("✗ %d/%d", pr.CheckPassing, pr.CheckTotal)
}

// formatTimeSincePR returns a short human-friendly relative time string.
func formatTimeSincePR(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	diff := time.Since(t)
	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		m := int(diff.Minutes())
		return fmt.Sprintf("%dm ago", m)
	case diff < 24*time.Hour:
		h := int(diff.Hours())
		return fmt.Sprintf("%dh ago", h)
	default:
		days := int(diff.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	}
}

func truncatePR(s string, maxLen int) string {
	// Strip newlines that can appear in PR titles
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return s[:maxLen]
	}
	return s[:maxLen-1] + "…"
}
