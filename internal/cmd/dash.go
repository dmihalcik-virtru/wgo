package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/dash"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/issuecache"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/store"
)

var (
	dashJSON    bool
	dashDays    int
	dashRefresh bool
)

// dashCmd is `wgo dash`: the live dashboard of ongoing work (gh-70).
var dashCmd = &cobra.Command{
	Use:   "dash",
	Short: "Snapshot of all ongoing work: efforts, workspaces, PRs, tickets and agents",
	Long: `Build a snapshot of ongoing work across every discovered workspace: efforts,
workspaces, bookmarks and their PRs, tickets, and agent sessions, with
per-source freshness and a "since last look" delta.

The local collection reads jj with --ignore-working-copy, so it never
snapshots a workspace, and reads PR, Jira and GitHub issue data from wgo's
caches only: anything not cached is reported as unknown. Pass --refresh to
also fetch the missing or stale remote data before printing.

The last good snapshot is kept in ~/.wgo/cache/dash/ and loaded first.

Only --json output exists for now; the local web dashboard comes later.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDash(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
	// Route otherwise-swallowed dash and issue-cache faults (a failed
	// background republish, an unwritable cache) to the WGO_DEBUG logger.
	dash.Logf = debugf
	issuecache.Logf = debugf
	dashCmd.Flags().BoolVar(&dashJSON, "json", false, "print the snapshot and delta as JSON")
	dashCmd.Flags().IntVar(&dashDays, "days", dash.DefaultDays, "activity window in days")
	dashCmd.Flags().BoolVar(&dashRefresh, "refresh", false, "also fetch missing or stale PR, Jira and GitHub issue data")
	rootCmd.AddCommand(dashCmd)
}

func runDash(ctx context.Context, out io.Writer) error {
	if !dashJSON {
		return errors.New("wgo dash needs --json for now; the web dashboard is not built yet. Run: wgo dash --json")
	}
	if dashDays < 1 || dashDays > 366 {
		return fmt.Errorf("--days must be between 1 and 366, got %d", dashDays)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := config.Init(); err != nil {
		return err
	}
	cfg := config.Get()
	st, err := store.New()
	if err != nil {
		return err
	}
	collector := dash.NewCollector(dash.Config{
		JJ:          jj.NewCLI(),
		Discover:    discovery.FromConfig(cfg).DiscoverAll,
		Store:       st,
		AgentPolicy: agentPolicy(),
		Days:        dashDays,
		PRTTL:       prTTL(),
		JiraTTL:     jiraTTL(),
		IssueTTL:    jiraTTL(),
		JiraSite:    cfg.Jira.Site,
	})
	opts := dash.Options{Dir: filepath.Join(st.BaseDir(), "cache", "dash"), Collector: collector}
	if dashRefresh {
		r := dash.NewRefresher(dash.Fetchers{
			PR:    newGHFetcher(),
			Jira:  jiraFetcherFn(),
			Issue: ghIssueFetcher{c: github.NewClient()},
		}, dash.RefresherOptions{Workers: 4, IgnoreLeases: true})
		defer r.Close()
		opts.Refresher = r
	}
	d, err := dash.Open(opts)
	if err != nil {
		return err
	}
	refreshErr := d.Refresh(ctx, dash.RefreshOptions{Remote: dashRefresh, Wait: true})
	v := d.Current()
	if v == nil {
		if refreshErr != nil {
			return refreshErr
		}
		return errors.New("wgo dash: no snapshot was produced")
	}
	if err := v.WriteJSON(out, time.Now()); err != nil {
		return err
	}
	// A failed collection still printed the last good snapshot; say why it
	// is old.
	return refreshErr
}

// ghIssueFetcher adapts the GitHub client to issuecache.Fetcher.
type ghIssueFetcher struct {
	c *github.CLIClient
}

// FetchIssue implements issuecache.Fetcher.
func (g ghIssueFetcher) FetchIssue(k issuecache.Key) (issuecache.Info, error) {
	is, err := g.c.GetIssue(k.Owner, k.Repo, k.Number)
	if err != nil {
		return issuecache.Info{}, err
	}
	return issuecache.Info{
		Number:      is.Number,
		Title:       is.Title,
		State:       is.State,
		StateReason: is.StateReason,
		URL:         is.URL,
		UpdatedAt:   is.UpdatedAt,
		IsPR:        is.IsPR,
	}, nil
}
