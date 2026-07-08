package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/cache"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
	specpkg "github.com/virtru/wgo/internal/spec"
	"github.com/virtru/wgo/internal/status"
)

var teamRefresh bool

// teamCache caches teammate PR data between calls within the same process.
var teamCache = cache.NewTTL[[]github.ExtendedPRInfo](60 * time.Second)

var teamCmd = &cobra.Command{
	Use:   "team",
	Short: "Two-column dashboard: your active branches alongside your pair's",
	Long: `Shows a side-by-side view of your active branches and your pair's open PRs.

Requires [pair] teammate to be set in ~/.wgo/config.toml.

Use --refresh to bypass the 60-second cache and re-fetch pair data from GitHub.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeam()
	},
}

func init() {
	rootCmd.AddCommand(teamCmd)
	teamCmd.Flags().BoolVar(&teamRefresh, "refresh", false, "Bypass cache and re-fetch pair data")
}

// teamBranchRow is one row in either the mine or pair column.
type teamBranchRow struct {
	Repo     string
	Branch   string
	PRNumber int
	PRURL    string
	Ticket   string
	Status   string // spec status or ""
}

func runTeam() error {
	if err := config.Init(); err != nil {
		return fmt.Errorf("failed to initialize config: %w", err)
	}
	cfg := config.Get()

	if !cfg.HasPair() {
		return fmt.Errorf("pair not configured: set [pair] teammate in ~/.wgo/config.toml")
	}

	gh := github.NewClient()
	if !gh.Available() {
		return fmt.Errorf("gh CLI not available — install from https://cli.github.com/")
	}

	// Discover local repos.
	d := discovery.New(cfg.Discovery.BaseDirs, cfg.Discovery.ScanDepth, cfg.Discovery.ExcludePatterns)
	repos, err := d.DiscoverAll()
	if err != nil {
		return fmt.Errorf("failed to discover repos: %w", err)
	}

	repoPaths := make([]string, len(repos))
	for i, r := range repos {
		repoPaths[i] = r.Path
	}

	myBranches := collectMyTeamRows(repoPaths)

	// Collect pair data (cached).
	if teamRefresh {
		teamCache.Invalidate()
	}
	pairPRs, pairErr := teamCache.Get(func() ([]github.ExtendedPRInfo, error) {
		return gh.ListPRsByAuthor(cfg.Pair.Teammate)
	})

	pairRows := buildPairRows(pairPRs)

	// Together: branches where spec frontmatter lists both authors.
	togetherRows := collectTogetherTeamRows(repoPaths, cfg.Author, cfg.Pair.Teammate)

	renderTeamTable(cfg, myBranches, pairRows, togetherRows, pairErr)
	return nil
}

// collectMyTeamRows walks active branches and returns rows for the "mine" column.
func collectMyTeamRows(repoPaths []string) []teamBranchRow {
	branches := collectActiveBranches(repoPaths)
	var rows []teamBranchRow
	for _, b := range branches {
		rows = append(rows, teamBranchRow{
			Repo:   b.RepoName,
			Branch: b.Branch,
		})
	}
	return rows
}

// buildPairRows converts pair PRs into team rows.
func buildPairRows(prs []github.ExtendedPRInfo) []teamBranchRow {
	var rows []teamBranchRow
	for _, pr := range prs {
		rows = append(rows, teamBranchRow{
			Repo:     pr.RepoSlug(),
			Branch:   fmt.Sprintf("PR #%d", pr.Number),
			PRNumber: pr.Number,
			PRURL:    pr.URL,
		})
	}
	return rows
}

// collectTogetherTeamRows finds specs co-authored by both users.
func collectTogetherTeamRows(repoPaths []string, myAuthor, pairAuthor string) []teamBranchRow {
	jjc := jj.NewCLI()
	var rows []teamBranchRow
	seen := map[string]bool{}
	for _, repoPath := range repoPaths {
		specDir := repoPath + "/spec"
		specFiles, err := findSpecFiles(specDir)
		if err != nil {
			continue
		}
		for _, sf := range specFiles {
			parsed, err := specpkg.Parse(sf)
			if err != nil {
				continue
			}
			authors := parsed.Frontmatter.Authors
			if !containsAuthorSlice(authors, myAuthor) || !containsAuthorSlice(authors, pairAuthor) {
				continue
			}
			key := parsed.Frontmatter.Ticket
			if seen[key] {
				continue
			}
			seen[key] = true
			rows = append(rows, teamBranchRow{
				Repo:   repoDisplayName(jjc, repoPath),
				Ticket: parsed.Frontmatter.Ticket,
				Status: string(parsed.Frontmatter.Status),
			})
		}
	}
	return rows
}

// renderTeamTable prints the two-column dashboard.
func renderTeamTable(cfg *config.Config, mine, pair, together []teamBranchRow, pairErr error) {
	myName := cfg.Author
	if myName == "" {
		myName = "me"
	}
	pairName := cfg.PairDisplayName()

	colWidth := 42
	header := fmt.Sprintf("  %-*s | %s", colWidth, myName, pairName)
	sep := "  " + strings.Repeat("─", colWidth) + "─┼─" + strings.Repeat("─", colWidth)

	fmt.Println(header)
	fmt.Println(sep)

	maxRows := len(mine)
	if len(pair) > maxRows {
		maxRows = len(pair)
	}

	for i := 0; i < maxRows; i++ {
		left := ""
		right := ""
		if i < len(mine) {
			r := mine[i]
			left = fmt.Sprintf("%s  %s", r.Repo, r.Branch)
			if r.Ticket != "" {
				left += "  " + r.Ticket
			}
		}
		if i < len(pair) {
			r := pair[i]
			right = fmt.Sprintf("%s  %s", r.Repo, r.Branch)
			if r.Ticket != "" {
				right += "  " + r.Ticket
			}
		}
		fmt.Printf("  %-*s | %s\n", colWidth, truncatePR(left, colWidth), right)
	}

	if pairErr != nil {
		fmt.Printf("\n  (could not fetch %s's data: %v)\n", pairName, pairErr)
	}

	if len(together) > 0 {
		fmt.Println(sep)
		tickets := make([]string, 0, len(together))
		for _, r := range together {
			entry := r.Ticket
			if r.Status != "" {
				entry += " (" + r.Status + ")"
			}
			tickets = append(tickets, entry)
		}
		fmt.Printf("  Together: %s\n", strings.Join(tickets, ", "))
	}

	fmt.Println()
	fmt.Printf("  %s's data cached for 60s — use --refresh to update\n", pairName)
}

// findSpecFiles returns all .md files in a spec directory.
func findSpecFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			files = append(files, dir+"/"+e.Name())
		}
	}
	return files, nil
}

// Satisfy the compiler: import status for its ParseSince (already used in pr.go).
var _ = status.ParseSince
