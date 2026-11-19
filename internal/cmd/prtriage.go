package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
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
)

var prTriageCmd = &cobra.Command{
	Use:   "triage",
	Short: "Sort your open PRs into buckets and write a worklist",
	Long: `Sorts the open PRs you authored into buckets (land, keep, close?, draft?,
fix ticket, active, parked) and writes them to a dated Markdown worklist,
~/.wgo/triage/<YYYY-MM-DD>-prs.md, with a checkbox per PR. Work through it on
GitHub at your own pace; wgo never closes, merges or converts a PR.

The buckets come from [[pr.triage.rules]] in ~/.wgo/config.toml: ordered
wgo dash queries over PR fields, the first match winning; the last rule has
no match and takes the rest. Without any, the built-in defaults apply.

It refreshes the PR inventory and ticket lookups first, as wgo dash --json
--refresh does; --cached uses what wgo already has.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPRTriage(cmd.Context(), cmd.OutOrStdout(), cmd.Flags().Changed("output"))
	},
}

func init() {
	prCmd.AddCommand(prTriageCmd)
	prTriageCmd.Flags().StringVarP(&triageOut, "output", "o", "", "write the worklist to `PATH` instead of ~/.wgo/triage/<date>-prs.md")
	prTriageCmd.Flags().BoolVar(&triageForce, "force", false, "overwrite an existing worklist (it may hold your notes)")
	prTriageCmd.Flags().BoolVar(&triageJSON, "json", false, "print the buckets as JSON instead of writing a worklist (unless -o is given)")
	prTriageCmd.Flags().BoolVar(&triageCached, "cached", false, "use cached PR and ticket data; make no remote lookups")
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
	if err := applyDashConfig(cfg.Dash, func(string) bool { return true }); err != nil {
		return nil, nil, err
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
func triageView(ctx context.Context, tc *triageConfig, collector *dash.Collector, cached bool) (*dash.View, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	opts := dash.Options{Dir: filepath.Join(tc.st.BaseDir(), "cache", "dash"), Collector: collector}
	v, err := collectDashView(ctx, opts, !cached, DefaultDashRefreshTimeout)
	if v == nil {
		return nil, err
	}
	// A failed lookup leaves its data unknown; the triage says which as
	// Incomplete, so it is not fatal here.
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
	if write && !triageForce {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s exists and may hold your notes; run wgo pr triage --resync to update it, or pass -o PATH or --force", path)
		}
	}
	v, err := triageView(ctx, tc, collector, triageCached)
	if err != nil {
		return err
	}
	t, err := dash.BuildTriage(v, tc.rules, "")
	if err != nil {
		return err
	}
	if write {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(dash.RenderTriage(t, true, time.Local)), 0o644); err != nil {
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
