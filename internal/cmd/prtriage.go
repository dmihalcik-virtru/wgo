package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/atomicfile"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/dash"
	"github.com/virtru/wgo/internal/links"
	"github.com/virtru/wgo/internal/store"
)

var (
	triageOut    string
	triageForce  bool
	triageJSON   bool
	triageCached bool
	triageResync bool
)

var prTriageCmd = &cobra.Command{
	Use:   "triage [--resync [worklist]]",
	Short: "Sort your open PRs into buckets and write a worklist",
	Long: `Sorts the open PRs you authored into buckets (land, keep, close?, draft?,
fix ticket, active, parked) and writes them to a dated Markdown worklist,
~/.wgo/triage/<YYYY-MM-DD>-prs.md, with a checkbox per PR. Work through it on
GitHub at your own pace; wgo never closes, merges or converts a PR.

The buckets come from [[pr.triage.rules]] in ~/.wgo/config.toml: ordered
wgo dash queries over PR fields, the first match winning; the last rule has
no match and takes the rest. Without any, the built-in defaults apply.

It refreshes the PR inventory and ticket lookups first, as wgo dash --json
--refresh does; --cached uses what wgo already has.

--resync updates a worklist, by default the newest, instead: it ticks the
PRs that closed or merged, appends open PRs the file lacks under "New
since", and prints only what changed. Items you ticked stay ticked, and
everything else you wrote in the file is kept. With --json it only reports
what would change and leaves the file alone.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if !triageResync && len(args) > 0 {
			return fmt.Errorf("a worklist path only applies with --resync; run wgo pr triage --resync %s", args[0])
		}
		return cobra.MaximumNArgs(1)(cmd, args)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if triageResync {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			return runPRTriageResync(cmd.Context(), cmd.OutOrStdout(), path)
		}
		return runPRTriage(cmd.Context(), cmd.OutOrStdout(), cmd.Flags().Changed("output"))
	},
}

func init() {
	prCmd.AddCommand(prTriageCmd)
	prTriageCmd.Flags().StringVarP(&triageOut, "output", "o", "", "write the worklist to `PATH` instead of ~/.wgo/triage/<date>-prs.md")
	prTriageCmd.Flags().BoolVar(&triageForce, "force", false, "overwrite an existing worklist (it may hold your notes)")
	prTriageCmd.Flags().BoolVar(&triageJSON, "json", false, "print the buckets (with --resync, what would change) as JSON instead of writing a worklist, unless -o is given")
	prTriageCmd.Flags().BoolVar(&triageCached, "cached", false, "use cached PR and ticket data; make no remote lookups")
	prTriageCmd.Flags().BoolVar(&triageResync, "resync", false, "update a worklist (default: the newest) with what closed, merged or opened since")
}

// triageConfig is the checked config wgo pr triage needs.
type triageConfig struct {
	cfg   *config.Config
	st    *store.FileStore
	rules *dash.TriageRules
}

func loadTriageConfig() (*triageConfig, *dash.Collector, error) {
	if err := config.Init(); err != nil {
		return nil, nil, err
	}
	cfg := config.Get()
	// Every flag counts as passed, so [dash] port and days leave wgo dash's
	// flag values alone; days, which triage does use, is checked here.
	if err := applyDashConfig(cfg.Dash, func(string) bool { return true }); err != nil {
		return nil, nil, err
	}
	if d := cfg.Dash.Days; d != 0 && (d < 1 || d > 366) {
		return nil, nil, fmt.Errorf("[dash] days in ~/.wgo/config.toml must be between 1 and 366, got %d; fix it, or remove it to use the %d default", d, dash.DefaultDays)
	}
	bots, err := dash.CompileBotLogins(cfg.PR.Triage.BotLogins)
	if err != nil {
		return nil, nil, fmt.Errorf("%w; fix it in ~/.wgo/config.toml, or remove bot_logins to use the defaults", err)
	}
	rules, err := triageRules(cfg)
	if err != nil {
		return nil, nil, err
	}
	st, err := store.New()
	if err != nil {
		return nil, nil, err
	}
	days := cfg.Dash.Days
	if days == 0 {
		days = dash.DefaultDays
	}
	return &triageConfig{cfg: cfg, st: st, rules: rules}, newDashCollector(cfg, st, bots, days), nil
}

// triageRules are [[pr.triage.rules]], or the defaults for [dash.facts]
// stale_days.
func triageRules(cfg *config.Config) (*dash.TriageRules, error) {
	var rules []dash.TriageRule
	for _, r := range cfg.PR.Triage.Rules {
		rules = append(rules, dash.TriageRule{Bucket: r.Bucket, Note: r.Note, Match: r.Match})
	}
	if cfg.PR.Triage.Rules == nil {
		th, _ := dashFactThresholds(cfg.Dash.Facts)
		rules = dash.DefaultTriageRules(th.StaleDays)
	}
	rs, err := dash.CompileTriageRules(rules, dash.Fields())
	if err != nil {
		return nil, fmt.Errorf("%w; fix it in ~/.wgo/config.toml", err)
	}
	return rs, nil
}

// triageView collects a snapshot, refreshing remote data unless cached.
func triageView(ctx context.Context, errOut io.Writer, tc *triageConfig, collector *dash.Collector, cached bool) (*dash.View, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	opts := dash.Options{Dir: filepath.Join(tc.st.BaseDir(), "cache", "dash"), Collector: collector}
	v, err := collectDashView(ctx, opts, !cached, DefaultDashRefreshTimeout)
	if v == nil {
		return nil, err
	}
	// A failed refresh is not fatal: inventory gaps show in Incomplete,
	// tickets with no data as "no status for", and older data in Sources.
	// But older data can read as complete, so the cause is reported.
	if err != nil {
		fmt.Fprintf(errOut, "wgo pr triage: refresh failed, using data from %s: %v\n", v.Snapshot.GeneratedAt.Local().Format("2006-01-02 15:04"), err)
	}
	return v, nil
}

// triageDir is ~/.wgo/triage.
func triageDir(st *store.FileStore) string { return filepath.Join(st.BaseDir(), "triage") }

func runPRTriage(ctx context.Context, out io.Writer, outputSet bool) error {
	tc, collector, err := loadTriageConfig()
	if err != nil {
		return err
	}
	path := triageOut
	if path == "" {
		path = filepath.Join(triageDir(tc.st), time.Now().Format("2006-01-02")+"-prs.md")
	}
	write := !triageJSON || outputSet
	// Checked before the refresh, so a refusal is quick, and again before
	// writing, in case the file appeared meanwhile.
	if write && !triageForce {
		if err := refuseOverwrite(path); err != nil {
			return err
		}
	}
	v, err := triageView(ctx, os.Stderr, tc, collector, triageCached)
	if err != nil {
		return err
	}
	t, err := dash.BuildTriage(v, tc.rules, "")
	if err != nil {
		return err
	}
	if write {
		if !triageForce {
			if err := refuseOverwrite(path); err != nil {
				return err
			}
		}
		if err := atomicfile.Write(path, []byte(dash.RenderTriage(t, true, time.Local)), 0o644); err != nil {
			return err
		}
	}
	if triageJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(t)
	}
	tty := isTerminal()
	fmt.Fprintln(out, links.Link("file://"+path, path, tty))
	if len(t.Incomplete) > 0 {
		fmt.Fprintf(out, "  INCOMPLETE: %s\n", strings.Join(t.Incomplete, "; "))
	}
	for _, b := range t.Buckets {
		fmt.Fprintf(out, "  %4d  %s\n", len(b.Items), b.Name)
	}
	return nil
}

// refuseOverwrite is an error when path exists: a worklist may hold notes.
func refuseOverwrite(path string) error {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return fmt.Errorf("%s exists and may hold your notes; run wgo pr triage --resync to update it, or pass -o PATH or --force", path)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return err
	}
}

// worklistGlob matches the dated worklists wgo pr triage writes.
const worklistGlob = "[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]-prs.md"

// newestWorklist is the newest dated worklist in dir.
func newestWorklist(dir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, worklistGlob))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no worklist in %s; run wgo pr triage to write one, or pass its path: wgo pr triage --resync PATH", dir)
	}
	slices.Sort(matches)
	return matches[len(matches)-1], nil
}

// readWorklist reads a worklist as lines, without their line endings, and
// whether they end in CRLF. A file with neither the worklist heading nor a
// worklist item is refused, so a mistyped path is not appended to.
func readWorklist(path string) ([]string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	text := string(data)
	crlf := strings.Contains(text, "\r\n")
	if crlf {
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}
	lines := strings.Split(text, "\n")
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, dash.WorklistHeading) }) && len(dash.ParseWorklist(lines)) == 0 {
		return nil, false, fmt.Errorf("%s is not a worklist: it has no %q heading and no PR items; pass a file wgo pr triage wrote, or run wgo pr triage to write one", path, strings.TrimSpace(dash.WorklistHeading))
	}
	return lines, crlf, nil
}

func runPRTriageResync(ctx context.Context, out io.Writer, path string) error {
	if triageForce || triageOut != "" {
		return errors.New("--force and --output only apply when writing a worklist; --resync updates one in place")
	}
	tc, collector, err := loadTriageConfig()
	if err != nil {
		return err
	}
	if path == "" {
		if path, err = newestWorklist(triageDir(tc.st)); err != nil {
			return err
		}
	}
	// Checked before the refresh, so a bad path fails fast.
	if _, _, err := readWorklist(path); err != nil {
		return err
	}
	v, err := triageView(ctx, os.Stderr, tc, collector, triageCached)
	if err != nil {
		return err
	}
	t, err := dash.BuildTriage(v, tc.rules, "")
	if err != nil {
		return err
	}
	r, err := resyncFile(path, v, t, !triageJSON)
	if err != nil {
		return err
	}
	if triageJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Path string `json:"path"`
			*dash.Resync
		}{path, r})
	}
	writeResync(out, path, r, isTerminal())
	return nil
}

// resyncFile resyncs the worklist at path with t, built from v, and with
// write replaces the file when that changed it. It reads the file after
// the refresh, so edits saved meanwhile are kept, and writes it atomically.
func resyncFile(path string, v *dash.View, t *dash.Triage, write bool) (*dash.Resync, error) {
	lines, crlf, err := readWorklist(path)
	if err != nil {
		return nil, err
	}
	out, r := dash.ResyncWorklist(lines, v, t, time.Local)
	if !write || slices.Equal(out, lines) {
		return r, nil
	}
	sep := "\n"
	if crlf {
		sep = "\r\n"
	}
	return r, atomicfile.Write(path, []byte(strings.Join(out, sep)), 0o644)
}

// writeResync prints a resync's changes: the whole context an agent needs
// to continue a triage pass.
func writeResync(out io.Writer, path string, r *dash.Resync, tty bool) {
	left := 0
	for _, s := range r.Left {
		left += s.Left
	}
	fmt.Fprintln(out, links.Link("file://"+path, path, tty))
	fmt.Fprintf(out, "%d newly closed or merged, %d unchecked, %d open on GitHub\n", len(r.Done), left, r.Open)
	for _, src := range r.Sources {
		if src.State != dash.Fresh && src.Name != dash.SourceJira {
			fmt.Fprintf(out, "  %s is %s, fetched %s: closes since then are not seen yet\n", src.Name, src.State, src.FetchedAt.Local().Format("2006-01-02 15:04"))
		}
	}
	if r.TickSkipped {
		fmt.Fprintf(out, "  INCOMPLETE: %s; nothing was ticked, since a PR missing from the list may still be open\n", strings.Join(r.Incomplete, "; "))
	} else if len(r.Incomplete) > 0 {
		fmt.Fprintf(out, "  INCOMPLETE: %s\n", strings.Join(r.Incomplete, "; "))
	}
	for _, d := range r.Done {
		when := d.State
		if when == "gone" {
			when = "no longer open"
		}
		if !d.At.IsZero() {
			when += " " + d.At.Local().Format("2006-01-02")
		}
		fmt.Fprintf(out, "  ✓ %s  %s  (%s)\n", links.Link(d.URL, prRef(d.URL), tty), when, d.Section)
	}
	for _, s := range r.Left {
		fmt.Fprintf(out, "  %4d left  %s\n", s.Left, s.Section)
	}
	for _, m := range r.Moved {
		fmt.Fprintf(out, "  ~ %s is now %s (listed under %s)\n", links.Link(m.URL, prRef(m.URL), tty), m.Bucket, m.Section)
	}
	if len(r.New) > 0 {
		fmt.Fprintf(out, "%d new open %s, added under New since:\n", len(r.New), map[bool]string{true: "PR", false: "PRs"}[len(r.New) == 1])
		for _, n := range r.New {
			fmt.Fprintf(out, "  + %s  %s  %s\n", links.Link(n.Item.URL, n.Item.Ref, tty), n.Bucket, n.Item.Title)
		}
	}
}

// prRef is owner/repo#N for a pull request URL.
func prRef(url string) string {
	rest := strings.TrimPrefix(url, "https://github.com/")
	if owner, tail, ok := strings.Cut(rest, "/pull/"); ok {
		return owner + "#" + tail
	}
	return url
}
