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
	"runtime/debug"
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

// DefaultDashPort is the default loopback port `wgo dash` serves on.
const DefaultDashPort = 8766

// dashCmd is `wgo dash`: the live dashboard of ongoing work (gh-70).
var dashCmd = &cobra.Command{
	Use:   "dash",
	Short: "Live dashboard of all ongoing work: efforts, workspaces, PRs, tickets and agents",
	Long: `Serve a read-only web dashboard of ongoing work across every discovered
workspace: efforts, workspaces, bookmarks and their PRs, tickets, and agent
sessions, with per-source freshness and a "since last look" delta.

The dashboard is served on http://127.0.0.1:8766 by default (only the
loopback address; change the port with --port or [dash] port) and opened in the browser unless
--no-open. The page appears at once and fills in when the first snapshot
is ready. In the background it re-collects local state and refreshes stale
PR, Jira and GitHub issue data every 30 seconds by default; page requests
only read the last published snapshot. Year-in-review runs under
~/.wgo/cache/review/runs/ are served under /review/ and linked to the live
efforts they share PRs or tickets with. Stop it with Ctrl-C.

With --json, wgo dash prints one snapshot instead of serving. The local
collection reads jj with --ignore-working-copy, so it never snapshots a
workspace, and reads PR, Jira and GitHub issue data from wgo's caches only:
anything not cached is reported as unknown. Add --refresh to also try to
fetch the missing or stale remote data before printing; lookups that fail
are listed in the output's diagnostics and make the command exit non-zero.

[dash] in ~/.wgo/config.toml sets port, days and refresh_seconds; --port
and --days win over it.

The last good snapshot is kept in ~/.wgo/cache/dash/ and loaded first.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDash(cmd.Context(), cmd.OutOrStdout(), cmd.Flags().Changed)
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

// runDash runs wgo dash. flagChanged reports whether a flag was passed on
// the command line; only flags left at their defaults yield to [dash] config.
func runDash(ctx context.Context, out io.Writer, flagChanged func(name string) bool) error {
	if dashDays < 1 || dashDays > 366 {
		return fmt.Errorf("--days must be between 1 and 366, got %d", dashDays)
	}
	if dashRefresh && !dashJSON {
		return errors.New("--refresh only applies with --json; the dashboard server refreshes remote data in the background by itself. Run wgo dash --json --refresh to print a refreshed snapshot")
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
	if err := applyDashConfig(cfg.Dash, flagChanged); err != nil {
		return err
	}
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
		// The server honors leases: a key that this or any other wgo process
		// tried within the backoff window is not fetched again.
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
			interval: dashInterval(cfg.Dash),
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

// applyDashConfig uses [dash] port and days in place of any flag the user
// did not pass, and checks refresh_seconds. A setting a flag overrides is
// not checked, and neither is port under --json, which never serves.
func applyDashConfig(dc config.DashConfig, flagChanged func(name string) bool) error {
	if dc.Port != 0 && !dashJSON && !flagChanged("port") {
		if dc.Port < 1 || dc.Port > 65535 {
			return fmt.Errorf("[dash] port in ~/.wgo/config.toml must be between 1 and 65535, got %d; fix it, or override it with wgo dash --port %d", dc.Port, DefaultDashPort)
		}
		dashPort = dc.Port
	}
	if dc.Days != 0 && !flagChanged("days") {
		if dc.Days < 1 || dc.Days > 366 {
			return fmt.Errorf("[dash] days in ~/.wgo/config.toml must be between 1 and 366, got %d; fix it, or override it with wgo dash --days %d", dc.Days, dash.DefaultDays)
		}
		dashDays = dc.Days
	}
	if dc.RefreshSeconds != 0 && !dashJSON {
		minS, maxS := int(dash.MinRefreshInterval/time.Second), int(dash.MaxRefreshInterval/time.Second)
		if dc.RefreshSeconds < minS || dc.RefreshSeconds > maxS {
			return fmt.Errorf("[dash] refresh_seconds in ~/.wgo/config.toml must be between %d and %d, got %d; fix it, or remove it to use the %v default", minS, maxS, dc.RefreshSeconds, dash.DefaultRefreshInterval)
		}
	}
	return nil
}

// dashInterval is [dash] refresh_seconds, already range-checked by
// applyDashConfig, or the default when unset.
func dashInterval(dc config.DashConfig) time.Duration {
	if dc.RefreshSeconds == 0 {
		return dash.DefaultRefreshInterval
	}
	return time.Duration(dc.RefreshSeconds) * time.Second
}

// listenDash binds the loopback address only. A busy port is an error
// naming --port; it never falls back to another port or address.
func listenDash(port int) (net.Listener, error) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return nil, busyPortError(addr, port)
		}
		return nil, fmt.Errorf("wgo dash: listen on %s: %w. Pick another port with --port", addr, err)
	}
	// On macOS and the BSDs, SO_REUSEADDR (which Go sets) lets 127.0.0.1:P
	// bind beside a socket on the wildcard address 0.0.0.0:P or [::]:P, and
	// the two then split loopback traffic. A wildcard bind collides with such
	// a holder but not with our own specific one, so probing it finds the
	// port shared rather than free.
	if port != 0 {
		for _, wild := range []string{"0.0.0.0", "::"} {
			probe, err := net.Listen("tcp", net.JoinHostPort(wild, strconv.Itoa(port)))
			if err == nil {
				probe.Close()
				continue
			}
			if errors.Is(err, syscall.EADDRINUSE) {
				ln.Close()
				return nil, busyPortError(net.JoinHostPort(wild, strconv.Itoa(port)), port)
			}
			// Any other failure (no IPv6, say) says nothing about the port.
		}
	}
	return ln, nil
}

// busyPortError reports that addr is taken and suggests another port.
func busyPortError(addr string, port int) error {
	next := port + 1
	if next > 65535 {
		next = port - 1
	}
	return fmt.Errorf("wgo dash: %s is already in use (is another wgo dash running?). Pick another port: wgo dash --port %d", addr, next)
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
		lastRefreshErr := ""
		dash.Loop(loopCtx, ds.interval, func(ctx context.Context) {
			// A panic here (say, on a hand-edited review run) would kill the
			// whole server; report it and let the next tick try again.
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(errOut, "wgo dash: refresh panicked: %v\n%s", r, debug.Stack())
				}
			}()
			// Collect first, so loading every review run never delays the
			// first snapshot.
			err := ds.refresh(ctx)
			if ctx.Err() != nil {
				return
			}
			// The page keeps the last good snapshot, shows its age and the
			// failure; print a failure when it changes rather than every tick.
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			if msg != lastRefreshErr {
				if msg != "" {
					fmt.Fprintf(errOut, "wgo dash: refresh failed: %s\n", msg)
				} else {
					fmt.Fprintln(errOut, "wgo dash: refresh is working again.")
				}
				lastRefreshErr = msg
			}
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
