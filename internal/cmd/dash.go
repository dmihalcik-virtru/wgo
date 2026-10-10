package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/dash"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/issuecache"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/review"
	"github.com/virtru/wgo/internal/store"
)

var (
	dashJSON    bool
	dashDays    int
	dashRefresh bool
	dashPort    int
	dashNoOpen  bool
)

// DefaultDashPort is the fixed loopback port `wgo dash` serves on.
const DefaultDashPort = 8766

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
also try to fetch the missing or stale remote data before printing; lookups
that fail are listed in the output's diagnostics and make the command exit
non-zero.

The last good snapshot is kept in ~/.wgo/cache/dash/ and loaded first.

Without --json, wgo dash serves a read-only web dashboard on
http://127.0.0.1:8766 (only the loopback address; change the port with
--port) and opens it in the browser unless --no-open. The page appears at
once and fills in when the first snapshot is ready. In the background it
re-collects local state and refreshes stale PR, Jira and GitHub issue data
every 30 seconds; page requests only read the last published snapshot.
Year-in-review runs under ~/.wgo/review/ are served under /review/ and
linked to the live efforts they share PRs or tickets with. Stop it with
Ctrl-C.`,
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
	dashCmd.Flags().BoolVar(&dashRefresh, "refresh", false, "with --json, also fetch missing or stale PR, Jira and GitHub issue data")
	dashCmd.Flags().IntVar(&dashPort, "port", DefaultDashPort, "loopback port to serve the dashboard on")
	dashCmd.Flags().BoolVar(&dashNoOpen, "no-open", false, "serve without opening a browser")
	rootCmd.AddCommand(dashCmd)
}

func runDash(ctx context.Context, out io.Writer) error {
	if dashDays < 1 || dashDays > 366 {
		return fmt.Errorf("--days must be between 1 and 366, got %d", dashDays)
	}
	if !dashJSON && (dashPort < 1 || dashPort > 65535) {
		return fmt.Errorf("--port must be between 1 and 65535, got %d", dashPort)
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
		// GitHub issue status moves at ticket speed, not PR speed, so it
		// shares the Jira TTL rather than having its own setting.
		IssueTTL: jiraTTL(),
		JiraSite: cfg.Jira.Site,
	})
	opts := dash.Options{Dir: filepath.Join(st.BaseDir(), "cache", "dash"), Collector: collector}
	if !dashJSON {
		// Bind first: a busy port fails before any collection starts.
		ln, err := listenDash(dashPort)
		if err != nil {
			return err
		}
		// The server honors leases, so it never refetches what another wgo
		// process is already fetching.
		r := dash.NewRefresher(dashFetchers(), dash.RefresherOptions{Workers: 4})
		defer r.Close()
		opts.Refresher = r
		d, err := dash.Open(opts)
		if err != nil {
			ln.Close()
			return err
		}
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		return serveDash(ctx, out, ln, dashServer{
			source:  d,
			reviews: dash.NewReviewIndex(review.RunsDir(st.BaseDir())),
			refresh: func(ctx context.Context) error {
				return d.Refresh(ctx, dash.RefreshOptions{Remote: true})
			},
			interval: dash.DefaultRefreshInterval,
			open:     !dashNoOpen,
		})
	}
	if dashRefresh {
		r := dash.NewRefresher(dashFetchers(), dash.RefresherOptions{Workers: 4, IgnoreLeases: true})
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
	// Any refresh error (a failed collection, a snapshot that could not be
	// saved, failed remote lookups) is also in the printed diagnostics; the
	// error makes it visible to scripts through the exit status.
	return refreshErr
}

// dashFetchers returns the lookups this machine can actually make. An
// integration that is absent (no GitHub token or gh, no acli) gets no fetcher,
// so its jobs are skipped rather than failing every run.
func dashFetchers() dash.Fetchers {
	f := dash.Fetchers{Missing: map[dash.JobKind]string{}}
	if gc := github.NewClient(); gc.Available() {
		f.PR = newGHFetcher()
		f.Issue = ghIssueFetcher{c: gc}
	} else {
		f.Missing[dash.JobPR] = "no GitHub token or gh"
		f.Missing[dash.JobIssue] = "no GitHub token or gh"
	}
	if _, err := exec.LookPath("acli"); err == nil {
		f.Jira = jiraFetcherFn()
	} else {
		f.Missing[dash.JobJira] = "acli not on PATH"
	}
	return f
}

// listenDash binds the loopback address only. A busy port is an error
// naming --port; it never falls back to another port or address.
func listenDash(port int) (net.Listener, error) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, nil
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return nil, fmt.Errorf("wgo dash: %s is already in use (is another wgo dash running?). Pick another port: wgo dash --port %d", addr, port+1)
	}
	return nil, fmt.Errorf("wgo dash: listen on %s: %w. Pick another port with --port", addr, err)
}

// dashServer is what serveDash runs.
type dashServer struct {
	source   dash.ViewSource
	reviews  *dash.ReviewIndex // nil disables /review/
	refresh  func(context.Context) error
	interval time.Duration
	open     bool
	errOut   io.Writer // refresh and scan failures; nil means stderr
}

// serveDash serves the read-only dashboard on ln until ctx is done, running
// the refresh loop beside it, then shuts both down. The HTTP handlers only
// read what the loop last published.
func serveDash(ctx context.Context, out io.Writer, ln net.Listener, ds dashServer) error {
	host := ln.Addr().String()
	srv := &http.Server{
		Handler: dash.NewHandler(dash.HandlerOptions{
			Source:  ds.source,
			Host:    host,
			Reviews: ds.reviews,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	errOut := ds.errOut
	if errOut == nil {
		errOut = os.Stderr
	}
	loopCtx, stopLoop := context.WithCancel(ctx)
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		var lastScanErrs []string
		dash.Loop(loopCtx, ds.interval, func(ctx context.Context) {
			if ds.reviews != nil {
				// Scan records every failure in Errors, which the page shows
				// too; print them when they change rather than every tick.
				_ = ds.reviews.Scan()
				if errs := ds.reviews.Errors(); !slices.Equal(errs, lastScanErrs) {
					for _, e := range errs {
						fmt.Fprintf(errOut, "wgo dash: %s\n", e)
					}
					lastScanErrs = errs
				}
			}
			if err := ds.refresh(ctx); err != nil && ctx.Err() == nil {
				// The page keeps the last good snapshot and shows its age.
				fmt.Fprintf(errOut, "wgo dash: refresh failed: %v\n", err)
			}
		})
	}()

	url := "http://" + host + "/"
	fmt.Fprintf(out, "wgo dash: serving %s (read-only). Press Ctrl-C to stop.\n", url)
	if ds.open {
		if err := openInBrowser(url); err != nil {
			fmt.Fprintf(out, "wgo dash: could not open a browser (%v); open %s yourself.\n", err, url)
		}
	}

	var err error
	select {
	case <-ctx.Done():
	case err = <-served:
	}
	stopLoop()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if serr := srv.Shutdown(shutCtx); serr != nil && err == nil {
		err = serr
	}
	<-loopDone
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	if err == nil {
		fmt.Fprintln(out, "wgo dash: stopped.")
	}
	return err
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
