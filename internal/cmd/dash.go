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
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/dash"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/inventory"
	"github.com/virtru/wgo/internal/issuecache"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/launch"
	"github.com/virtru/wgo/internal/review"
	"github.com/virtru/wgo/internal/store"
)

var (
	dashJSON    bool
	dashDays    int
	dashRefresh bool
	dashPort    int
	dashNoOpen  bool
	// gh-100: query, saved filter, Markdown extract and fixture flags.
	dashQuery   string
	dashPreset  string
	dashMD      bool
	dashCols    string
	dashGroup   string
	dashLayout  string
	dashView    string
	dashSprint  string
	dashFixture string
	// gh-101: the analysis table's sort, for --md.
	dashSort string
)

// DefaultDashPort is the default loopback port `wgo dash` serves on.
const DefaultDashPort = 8766

// dashCmd is `wgo dash`: the live dashboard of ongoing work (gh-70).
var dashCmd = &cobra.Command{
	Use:   "dash",
	Short: "Live dashboard of all ongoing work: efforts, workspaces, PRs, tickets and agents",
	Long: `Serve a web dashboard of ongoing work across every discovered
workspace: efforts, workspaces, bookmarks and their PRs, tickets, and agent
sessions, with per-source freshness and a "since last look" delta. It also
follows every PR the configured people author or are asked to review, and
their recently merged or closed PRs, whether or not a local bookmark leads
to them; those PRs join their bookmark, ticket effort or a per-repository
group.

The dashboard is served on http://127.0.0.1:8766 by default (only the
loopback address; change the port with --port or [dash] port) and opened in the browser unless
--no-open. The page appears at once and fills in when the first snapshot
is ready. In the background it re-collects local state and refreshes stale
PR, PR inventory, Jira and GitHub issue data every 30 seconds by default; page requests
only read the last published snapshot. Year-in-review runs under
~/.wgo/cache/review/runs/ are served under /review/ and linked to the live
efforts they share PRs or tickets with. Stop it with Ctrl-C.

With --json, wgo dash prints one snapshot instead of serving. The local
collection reads jj with --ignore-working-copy, so it never snapshots a
workspace, and reads PR, PR inventory, Jira and GitHub issue data from wgo's caches only:
anything not cached is reported as unknown. Add --refresh to also try to
fetch the missing or stale remote data before printing; lookups that fail
are listed in the output's diagnostics and make the command exit non-zero.

The page's buttons open a workspace in a new Ghostty tab (or the terminal
configured in [dash]), resume an agent there, open it in an editor or
Finder, open its .plan entry or spec, and mark the current state seen.
They only work from the page wgo dash served: each run of wgo dash issues a
secret token embedded in that page, and the server checks it, the exact Origin
and Host, and re-resolves the workspace ID through discovery before
launching anything. Requests never carry paths or commands. Nothing a
launch does changes jj, the plan or agent state.

[dash] in ~/.wgo/config.toml sets port, days, refresh_seconds, terminal,
terminal_command, resume and editor; --port and --days win over it.
[[dash.people]] lists the people whose PRs and tickets the dash follows
(default: you and [pair]'s teammate). [dash] inventory_ttl_seconds (60 to
86400, default 300) is how long their open PRs stay fresh; their merged
and closed PRs stay fresh six times as long. [dash] ticket_prefixes lists
the Jira project keys whose ticket IDs count in PR titles and bodies
(default: [jira] project, default_project and project_rules projects;
local bookmarks' ticket prefixes always count).

With [jira] board set, the dash also shows that board's active and future
sprints, with their goals and dates, and every ticket in an active sprint
assigned to those people, even one no PR or workspace leads to. Ticket
facts (status category, type, sprints, story points, parent, team) come
from acli; [jira.fields] names the site's custom field IDs.

The last good snapshot is kept in ~/.wgo/cache/dash/ and loaded first.

Queries filter the dashboard. A query is clauses joined by ";" (all must
hold), each [!]field:value — a value, a comma-separated list (any of them),
* (any known value), or a range such as 2026-10-01..2026-10-07, >=30 or
-7d.. (relative to the snapshot). For example kind:pr;prstate:open,draft or
activity:-7d..;repo:acme/api. A clause never matches a node whose value is
unknown, negated or not; list unknown to include those. kind:… clauses
choose which matches are rows. The page shows the query in its address, so
every view is a link; the same text works here:

  wgo dash --query 'kind:pr;prstate:open'    open the dashboard at it
  wgo dash --json --query EXPR               add {"query": {expr, matches,
                                             rows}} and keep only the
                                             nodes it shows
  wgo dash --md --query EXPR                 print its rows as Markdown,
                                             with --cols, --group,
                                             --layout table, and --sort
                                             (-waiting,size: the table's
                                             order, unknown last)
  wgo dash --md --preset NAME                the same for a saved filter

Saved filters are built in (my-move, review-inbox, shipped, … — each hidden
until the fields it needs exist) or named under [dash.filters] in
~/.wgo/config.toml, which override built-ins of the same name.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		err := runDash(cmd.Context(), cmd.OutOrStdout(), cmd.Flags().Changed)
		var qe *dashQueryError
		if errors.As(err, &qe) {
			// The message already shows the query, the column and a fix.
			cmd.SilenceUsage = true
		}
		return err
	},
}

func init() {
	// Route otherwise-swallowed dash and issue-cache faults (a failed
	// background republish, an unwritable cache) to the WGO_DEBUG logger.
	dash.Logf = debugf
	issuecache.Logf = debugf
	inventory.Logf = debugf
	dashCmd.Flags().BoolVar(&dashJSON, "json", false, "print the snapshot and delta as JSON")
	dashCmd.Flags().IntVar(&dashDays, "days", dash.DefaultDays, "activity window in days")
	dashCmd.Flags().BoolVar(&dashRefresh, "refresh", false, "with --json, also fetch missing or stale PR, PR inventory, Jira and GitHub issue data")
	dashCmd.Flags().IntVar(&dashPort, "port", DefaultDashPort, "loopback port to serve the dashboard on")
	dashCmd.Flags().BoolVar(&dashNoOpen, "no-open", false, "serve without opening a browser")
	dashCmd.Flags().StringVar(&dashQuery, "query", "", "filter with a query: with --json add the matches and keep only what it shows, with --md choose the rows, alone open the dashboard at it")
	dashCmd.Flags().StringVar(&dashPreset, "preset", "", "use a saved filter's query (built-in or [dash.filters]) in place of --query")
	dashCmd.Flags().BoolVar(&dashMD, "md", false, "print the query's rows as Markdown, with links and provenance, and exit")
	dashCmd.Flags().StringVar(&dashCols, "cols", "", "with --md, comma-separated fields to show per row, e.g. prstate,repo")
	dashCmd.Flags().StringVar(&dashGroup, "group", "", "with --md, section the rows by effort (default), repo or none")
	dashCmd.Flags().StringVar(&dashLayout, "layout", "", "with --md, bullets (default) or table")
	dashCmd.Flags().StringVar(&dashView, "view", "", "with --md, the extract view: rows (default); sprint arrives with the sprint view")
	dashCmd.Flags().StringVar(&dashSprint, "sprint", "", "with --md --view sprint, the sprint to summarize (arrives with the sprint view)")
	dashCmd.Flags().StringVar(&dashSort, "sort", "", "with --md, sort rows by these fields as the table does, e.g. -waiting,size (- sorts descending; unknown values sort last)")
	// --fixture serves or prints a saved snapshot instead of collecting one:
	// for screenshots and for comparing the CLI with the page. Hidden.
	dashCmd.Flags().StringVar(&dashFixture, "fixture", "", "load this snapshot JSON instead of collecting (testing)")
	_ = dashCmd.Flags().MarkHidden("fixture")
	rootCmd.AddCommand(dashCmd)
}

// runDash runs wgo dash. flagChanged reports whether a flag was passed on
// the command line; only flags left at their defaults yield to [dash] config.
func runDash(ctx context.Context, out io.Writer, flagChanged func(name string) bool) error {
	if dashDays < 1 || dashDays > 366 {
		return fmt.Errorf("--days must be between 1 and 366, got %d", dashDays)
	}
	if dashJSON && dashMD {
		return errors.New("--json and --md are alternatives: --json prints the snapshot, --md the Markdown extract; pick one")
	}
	if dashRefresh && !dashJSON && !dashMD {
		return errors.New("--refresh only applies with --json or --md; the dashboard server refreshes remote data in the background by itself. Run wgo dash --json --refresh to print a refreshed snapshot")
	}
	if flagChanged("query") && dashPreset != "" {
		return errors.New("give --query or --preset, not both: --preset NAME stands for --query with that saved filter's expression")
	}
	for _, name := range []string{"cols", "group", "layout", "view", "sprint", "sort"} {
		if flagChanged(name) && !dashMD {
			return fmt.Errorf("--%s only applies with --md (the Markdown extract); run wgo dash --md --%s …", name, name)
		}
	}
	serving := !dashJSON && !dashMD
	if serving && (dashPort < 1 || dashPort > 65535) {
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
	filters := dash.MergeConfigFilters(cfg.Dash.Filters, dash.Fields())
	for _, f := range filters {
		if f.Hidden && !f.Builtin {
			fmt.Fprintf(os.Stderr, "wgo dash: saved filter %s is unavailable: %s\n", f.Name, f.Reason)
		}
	}
	expr, hasQuery, err := dashQueryExpr(filters, flagChanged)
	if err != nil {
		return err
	}
	extract := dash.ExtractArgs{Query: expr, Cols: dash.SplitCols(dashCols), Group: dashGroup, Layout: dashLayout, View: dashView, Sprint: dashSprint, Sort: dash.SplitCols(dashSort)}
	if dashMD {
		if err := dash.CheckExtractArgs(dash.Fields(), extract); err != nil {
			return dashQueryFailure(err, expr)
		}
	}
	emit := func(v *dash.View) error {
		switch {
		case dashMD:
			md, err := dash.Extract(v, dash.Fields(), filters, extract)
			if err != nil {
				return dashQueryFailure(err, expr)
			}
			_, err = io.WriteString(out, md)
			return err
		case hasQuery:
			res, err := v.Query(expr)
			if err != nil {
				return dashQueryFailure(err, expr)
			}
			return v.WriteQueryJSON(out, time.Now(), res, filters)
		}
		return v.WriteJSON(out, time.Now())
	}
	hash := ""
	if hasQuery && expr != "" {
		hash = "#q=" + hashEscape(expr)
	}
	if dashFixture != "" {
		v, err := dash.LoadFixture(dashFixture)
		if err != nil {
			return fmt.Errorf("wgo dash --fixture: %w", err)
		}
		if !serving {
			return emit(v)
		}
		ln, err := listenDash(dashPort)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		// A fixture is served as it is: no collection, refresh or actions.
		return serveDash(ctx, out, ln, dashServer{source: dash.StaticSource{V: v}, open: !dashNoOpen, filters: filters, hash: hash})
	}
	st, err := store.New()
	if err != nil {
		return err
	}
	facts, _ := dashFactThresholds(cfg.Dash.Facts)
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
		People:   dashPeople(cfg.Dash.People),
		Pair:     dash.Person{Login: cfg.Pair.Teammate, Jira: cfg.Pair.TeammateJira, Name: cfg.Pair.DisplayName},
		// The login cached by the background identity lookup: a file read,
		// never a network call or a process.
		Identity: func() (string, string) {
			login, _ := inventory.ReadIdentity(inventory.IdentityTTL, time.Now())
			return login, ""
		},
		InventoryTTL:       dashInventoryTTL(cfg.Dash),
		InventoryClosedTTL: dash.ClosedInventoryTTL(dashInventoryTTL(cfg.Dash)),
		TicketPrefixes:     dashTicketPrefixes(cfg),
		GitHubUnavailable:  githubUnavailable(),
		// The board whose sprints the dash shows (gh-98).
		JiraBoard: cfg.Jira.Board,
		// Thresholds of the derived facts (gh-99), checked by
		// applyDashConfig.
		Facts: facts,
	})
	opts := dash.Options{Dir: filepath.Join(st.BaseDir(), "cache", "dash"), Collector: collector}
	if serving {
		// Bind first: a busy port fails before any collection starts.
		ln, err := listenDash(dashPort)
		if err != nil {
			return err
		}
		// The server honors leases: a key that this or any other wgo process
		// tried within the backoff window is not fetched again.
		r := dash.NewRefresher(dashFetchers(), dash.RefresherOptions{Workers: 4, Queue: dashQueue})
		defer r.Close()
		opts.Refresher = r
		d, err := dash.Open(opts)
		if err != nil {
			ln.Close()
			return err
		}
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		lcfg := dashLaunchConfig("wgo dash", cfg.Dash, os.Stderr)
		logf := prefixLogf(os.Stderr)
		readOnly := jj.NewCLI().ReadOnly()
		return serveDash(ctx, out, ln, dashServer{
			source:  d,
			reviews: dash.NewReviewIndex(review.RunsDir(st.BaseDir())),
			refresh: func(ctx context.Context) error {
				return d.Refresh(ctx, dash.RefreshOptions{Remote: true})
			},
			interval: dashInterval(cfg.Dash),
			open:     !dashNoOpen,
			ack:      d,
			logf:     logf,
			filters:  filters,
			hash:     hash,
			actions: &dash.ActionOptions{
				Resolver: d,
				Launcher: launch.NewSystem(lcfg, launch.ExecRunner{Logf: logf}),
				Roots:    cfg.Discovery.BaseDirs,
				PlanPath: st.PlanPath(),
				Resume:   lcfg.Resume,
				Bookmark: func(_ context.Context, root string) (string, error) {
					return readOnly.NearestBookmark(root)
				},
			},
		})
	}
	if dashRefresh {
		// One waited-for batch: queue it whole (see dashQueue).
		r := dash.NewRefresher(dashFetchers(), dash.RefresherOptions{Workers: 4, Queue: dashQueue, IgnoreLeases: true})
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
	if err := emit(v); err != nil {
		return err
	}
	// Any refresh error (a failed collection, a snapshot that could not be
	// saved, failed remote lookups) is also in the printed diagnostics; the
	// error makes it visible to scripts through the exit status.
	return refreshErr
}

// dashQueue bounds a refresh's pending lookups. The PR inventory can bring a
// few hundred PRs, each naming a ticket to look up, and two full sprints'
// tickets alone are 400 lookups (gh-98); the default bound would drop them
// and report them as not run.
const dashQueue = 4096

// githubUnavailable is github.ErrNoAuth's message when there is no
// GITHUB_TOKEN and no gh to get a token from, else "". It only looks at the
// environment and PATH: it starts no process.
func githubUnavailable() string {
	if strings.TrimSpace(os.Getenv("GITHUB_TOKEN")) != "" {
		return ""
	}
	if _, err := exec.LookPath("gh"); err == nil {
		return ""
	}
	return github.ErrNoAuth.Error()
}

// dashQueryExpr resolves --query and --preset to an expression, and reports
// whether either was given. A query that does not parse is a
// dashQueryError.
func dashQueryExpr(filters []dash.SavedFilter, flagChanged func(string) bool) (string, bool, error) {
	if dashPreset != "" {
		f, ok := dash.LookupFilter(filters, dashPreset)
		if !ok {
			var names []string
			for _, f := range filters {
				if !f.Hidden {
					names = append(names, f.Name)
				}
			}
			avail := "none can run yet"
			if len(names) > 0 {
				avail = "available: " + strings.Join(names, ", ")
			}
			return "", false, &dashQueryError{msg: fmt.Sprintf("no saved filter named %q (%s); name one under [dash.filters] in ~/.wgo/config.toml", dashPreset, avail)}
		}
		if f.Hidden {
			return "", false, &dashQueryError{msg: fmt.Sprintf("saved filter %s cannot run: %s", f.Name, f.Reason)}
		}
		return f.Expr, true, nil
	}
	if !flagChanged("query") {
		return "", false, nil
	}
	if _, err := dash.ParseQuery(dashQuery, dash.Fields()); err != nil {
		return "", false, dashQueryFailure(err, dashQuery)
	}
	return dashQuery, true, nil
}

// dashQueryError is a query, saved filter or extract request that cannot
// run, explained in full (for a query: a caret under the column and a
// corrected command when there is one), so no usage text follows it.
type dashQueryError struct{ msg string }

func (e *dashQueryError) Error() string { return e.msg }

// dashQueryFailure turns a *dash.QueryError into a dashQueryError and
// strips the marker from extract argument errors; others pass through.
func dashQueryFailure(err error, expr string) error {
	var qe *dash.QueryError
	if !errors.As(err, &qe) {
		if errors.Is(err, dash.ErrExtractArgs) {
			return &dashQueryError{msg: "wgo dash --md: " + strings.TrimPrefix(err.Error(), dash.ErrExtractArgs.Error()+": ")}
		}
		return err
	}
	var b strings.Builder
	b.WriteString("query error: " + qe.Msg + "\n")
	b.WriteString("  " + expr + "\n")
	b.WriteString("  " + strings.Repeat(" ", max(qe.Pos-1, 0)) + "^")
	if qe.Fixed != "" {
		b.WriteString("\nTry: " + dashCommand(qe.Fixed))
	} else {
		b.WriteString("\nEdit the query at column " + strconv.Itoa(qe.Pos) + " as above and rerun: " + dashCommand(expr))
	}
	return &dashQueryError{msg: b.String()}
}

// dashCommand is the wgo dash command line that runs expr in the current
// output mode.
func dashCommand(expr string) string {
	parts := []string{"wgo dash"}
	switch {
	case dashJSON:
		parts = append(parts, "--json")
	case dashMD:
		parts = append(parts, "--md")
	}
	return strings.Join(append(parts, "--query", shellQuote(expr)), " ")
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// hashEscape percent-encodes a query for the page's #q=, keeping the
// characters queries are made of readable, as live.js does.
func hashEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_.~!*'():;,/@", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// dashFetchers returns the lookups this machine can actually make. An
// integration that is absent (no GitHub token or gh, no acli) gets no fetcher,
// so its jobs are skipped rather than failing every run.
func dashFetchers() dash.Fetchers {
	f := dash.Fetchers{Missing: map[dash.JobKind]string{}}
	if gc := github.NewClient(); gc.Available() {
		f.PR = newGHFetcher()
		f.Issue = ghIssueFetcher{c: gc}
		f.Inventory = gc
	} else {
		f.Missing[dash.JobPR] = "no GitHub token or gh"
		f.Missing[dash.JobIssue] = "no GitHub token or gh"
		f.Missing[dash.JobInventory] = "no GitHub token or gh"
		f.Missing[dash.JobIdentity] = "no GitHub token or gh"
	}
	if _, err := exec.LookPath("acli"); err == nil {
		f.Jira = jiraFetcherFn()
		f.Sprints = sprintFetcherFn()
	} else {
		f.Missing[dash.JobJira] = "acli not on PATH"
		f.Missing[dash.JobSprints] = "acli not on PATH"
	}
	return f
}

// applyDashConfig uses [dash] port and days in place of any flag the user
// did not pass, and checks refresh_seconds, inventory_ttl_seconds,
// ticket_prefixes and [[dash.people]]. A setting a flag overrides is not
// checked, and neither is port under --json, which never serves.
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
	if s := dc.InventoryTTLSeconds; s != 0 && (s < minInventoryTTLSeconds || s > maxInventoryTTLSeconds) {
		return fmt.Errorf("[dash] inventory_ttl_seconds in ~/.wgo/config.toml must be between %d and %d, got %d; fix it, or remove it to use the %d default",
			minInventoryTTLSeconds, maxInventoryTTLSeconds, s, int(dash.DefaultInventoryTTL/time.Second))
	}
	for _, p := range dc.TicketPrefixes {
		if !ticketPrefixRe.MatchString(p) {
			return fmt.Errorf("[dash] ticket_prefixes in ~/.wgo/config.toml has %q, which is not a Jira project key; fix it to the letters before the dash of a ticket ID, such as \"TEAM\" for TEAM-123", p)
		}
	}
	if _, err := dashFactThresholds(dc.Facts); err != nil {
		return err
	}
	return dc.ValidatePeople()
}

// dashFactThresholds is [dash.facts], with the default for each key left
// unset. A value out of range is an error naming the key, its range and the
// default.
func dashFactThresholds(fc config.DashFactsConfig) (dash.FactThresholds, error) {
	th := dash.DefaultFactThresholds()
	if v := fc.StaleDays; v != nil {
		if *v < dash.MinStaleDays || *v > dash.MaxStaleDays {
			return th, fmt.Errorf("[dash.facts] stale_days in ~/.wgo/config.toml must be between %d and %d days, got %d; fix it, or remove it to use the %d default",
				dash.MinStaleDays, dash.MaxStaleDays, *v, dash.DefaultStaleDays)
		}
		th.StaleDays = *v
	}
	if v := fc.SystemicRatio; v != nil {
		if !(*v > 0 && *v <= 1) {
			return th, fmt.Errorf("[dash.facts] systemic_ratio in ~/.wgo/config.toml must be greater than 0 and at most 1 (the share of open PRs a check fails on), got %v; fix it, or remove it to use the %v default",
				*v, dash.DefaultSystemicRatio)
		}
		th.SystemicRatio = *v
	}
	if v := fc.SystemicMinPRs; v != nil {
		if *v < dash.MinSystemicMinPRs || *v > dash.MaxSystemicMinPRs {
			return th, fmt.Errorf("[dash.facts] systemic_min_prs in ~/.wgo/config.toml must be between %d and %d PRs, got %d; fix it, or remove it to use the %d default",
				dash.MinSystemicMinPRs, dash.MaxSystemicMinPRs, *v, dash.DefaultSystemicMinPRs)
		}
		th.SystemicMinPRs = *v
	}
	return th, nil
}

// Bounds of [dash] inventory_ttl_seconds. GitHub's search rate limits make
// refetching every person's sets more often than once a minute unwise.
const (
	minInventoryTTLSeconds = 60
	maxInventoryTTLSeconds = 86400
)

// ticketPrefixRe is a ticket prefix as spec.ParseTicketFromBranch reads one.
var ticketPrefixRe = regexp.MustCompile(`^[A-Za-z]+$`)

// dashInventoryTTL is [dash] inventory_ttl_seconds, already range-checked by
// applyDashConfig, or the default when unset.
func dashInventoryTTL(dc config.DashConfig) time.Duration {
	if dc.InventoryTTLSeconds == 0 {
		return dash.DefaultInventoryTTL
	}
	return time.Duration(dc.InventoryTTLSeconds) * time.Second
}

// dashTicketPrefixes are the Jira prefixes whose keys count in PR titles and
// bodies: [dash] ticket_prefixes, else every project [jira] names. Prefixes
// that cannot be one (a project key with digits) are left out.
func dashTicketPrefixes(cfg *config.Config) []string {
	src := cfg.Dash.TicketPrefixes
	if len(src) == 0 {
		src = []string{cfg.Jira.Project, cfg.Jira.DefaultProject}
		for _, r := range cfg.Jira.ProjectRules {
			src = append(src, r.Project)
		}
	}
	var out []string
	for _, p := range src {
		p = strings.ToUpper(strings.TrimSpace(p))
		if ticketPrefixRe.MatchString(p) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// dashPeople turns [[dash.people]], already checked by applyDashConfig, into
// the collector's configured people.
func dashPeople(entries []config.DashPerson) []dash.Person {
	var out []dash.Person
	for _, p := range entries {
		out = append(out, dash.Person{Login: p.GitHub, Jira: p.Jira, Name: p.Name})
	}
	return out
}

// dashInterval is [dash] refresh_seconds, already range-checked by
// applyDashConfig, or the default when unset.
func dashInterval(dc config.DashConfig) time.Duration {
	if dc.RefreshSeconds == 0 {
		return dash.DefaultRefreshInterval
	}
	return time.Duration(dc.RefreshSeconds) * time.Second
}

// dashLaunchConfig turns [dash] into a launcher config. An invalid setting
// is reported on errOut and dropped, so the dashboard still starts with the
// default launchers rather than failing.
func dashLaunchConfig(who string, dc config.DashConfig, errOut io.Writer) launch.Config {
	lc := launch.Config{
		Terminal:        dc.Terminal,
		TerminalCommand: dc.TerminalCommand,
		Resume:          dc.Resume,
		Editor:          dc.Editor,
	}
	// Check each group on its own so the warning names only its problem. A
	// dropped resume hides Resume; a dropped terminal means Ghostty.
	if err := (launch.Config{Resume: lc.Resume}).Validate(); err != nil {
		fmt.Fprintf(errOut, "%s: ignoring [dash] resume in ~/.wgo/config.toml: %v\n", who, err)
		lc.Resume = ""
	}
	if err := (launch.Config{Terminal: lc.Terminal, TerminalCommand: lc.TerminalCommand}).Validate(); err != nil {
		fmt.Fprintf(errOut, "%s: ignoring [dash] terminal and terminal_command in ~/.wgo/config.toml: %v\n", who, err)
		lc.Terminal, lc.TerminalCommand = "", nil
	}
	return lc
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
	errOut   io.Writer           // refresh and scan failures; nil means stderr
	actions  *dash.ActionOptions // nil disables /api/action
	ack      dash.Acker          // nil disables /api/ack
	// logf receives server and launcher faults; nil writes them to errOut.
	logf func(format string, args ...any)
	// filters are the saved filters the page offers; hash is appended to
	// the URL opened and printed (#q=… for --query).
	filters []dash.SavedFilter
	hash    string
}

// prefixLogf logs "wgo dash: ..." lines to w.
func prefixLogf(w io.Writer) func(format string, args ...any) {
	return func(format string, args ...any) { fmt.Fprintf(w, "wgo dash: "+format+"\n", args...) }
}

// serveDash serves the dashboard on ln until ctx is done, running
// the refresh loop beside it, then shuts both down. The HTTP handlers only
// read what the loop last published.
func serveDash(ctx context.Context, out io.Writer, ln net.Listener, ds dashServer) error {
	host := ln.Addr().String()
	errOut := ds.errOut
	if errOut == nil {
		errOut = os.Stderr
	}
	logf := ds.logf
	if logf == nil {
		logf = prefixLogf(errOut)
	}
	handler, err := dash.NewHandler(dash.HandlerOptions{
		Source:  ds.source,
		Host:    host,
		Reviews: ds.reviews,
		Actions: ds.actions,
		Ack:     ds.ack,
		Logf:    logf,
		Filters: ds.filters,
	})
	if err != nil {
		ln.Close()
		return err
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	loopCtx, stopLoop := context.WithCancel(ctx)
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		if ds.refresh == nil {
			return // a fixed view (--fixture): nothing to refresh
		}
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

	url := "http://" + host + "/" + ds.hash
	mode := "read-only"
	if ds.actions != nil {
		mode = "actions enabled for this page only"
	}
	if ds.refresh == nil {
		mode = "read-only, a fixed snapshot"
	}
	fmt.Fprintf(out, "wgo dash: serving %s (%s). Press Ctrl-C to stop.\n", url, mode)
	if ds.open {
		if err := openInBrowser(url); err != nil {
			fmt.Fprintf(out, "wgo dash: could not open a browser (%v); open %s yourself.\n", err, url)
		}
	}

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
