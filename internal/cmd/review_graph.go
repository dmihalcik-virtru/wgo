package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/review"
	"github.com/virtru/wgo/internal/store"
)

var reviewGraphOut string

var reviewGraphCmd = &cobra.Command{
	Use:   "graph [label|path]",
	Short: "Emit the influence graph of a year-in-review run as JSON",
	Long: `Build the influence graph of a year-in-review run: people (authors,
reviewers, people who built on or discussed your work, bots), PRs, tickets and
repos. Edge kinds are authored, reviewed, built_on, i_reviewed, wrote,
discussed, refs, ticket, parent and in_repo.

The argument is a path to a run directory or a run label under
~/.wgo/cache/review/runs/ (an existing directory wins); with none, the most
recently modified run is used and named on stderr. Missing optional inputs
(coverage.json, people.json, cards/) are also reported on stderr. The graph is
written to stdout, or to --out FILE (a one-line summary goes to stdout then).
It is plain JSON, so it pipes into jq:

  wgo review graph | jq '[.edges[] | select(.kind=="built_on")] | length'`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		arg := ""
		if len(args) == 1 {
			arg = args[0]
		}
		return runReviewGraph(cmd.OutOrStdout(), cmd.ErrOrStderr(), arg, reviewGraphOut)
	},
}

func init() {
	reviewCmd.AddCommand(reviewGraphCmd)
	reviewGraphCmd.Flags().StringVarP(&reviewGraphOut, "out", "o", "", "write the graph to this file instead of stdout")
}

// runReviewGraph resolves the run, builds its graph and writes it as indented
// JSON to out (a file path) or w when out is empty. Notes about which run was
// picked and what it lacks go to errW; with out set, a one-line summary goes
// to w.
func runReviewGraph(w, errW io.Writer, arg, out string) error {
	st, err := store.New()
	if err != nil {
		return err
	}
	dir, err := review.Resolve(st.BaseDir(), arg)
	if err != nil {
		return err
	}
	run, err := review.Load(dir)
	if err != nil {
		return err
	}
	if arg == "" {
		fmt.Fprintf(errW, "using run %s (%s)\n", run.Label, run.Dir)
	}
	if len(run.Missing) > 0 {
		fmt.Fprintf(errW, "run %s has no %s: the graph lacks that data; finish the /year-in-review run to fill it in\n",
			run.Label, strings.Join(run.Missing, ", "))
	}
	g := review.Build(run)
	if len(g.Skipped) > 0 {
		kinds := make([]string, 0, len(g.Skipped))
		for k := range g.Skipped {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		parts := make([]string, len(kinds))
		for i, k := range kinds {
			parts[i] = fmt.Sprintf("%s=%d", k, g.Skipped[k])
		}
		fmt.Fprintf(errW, "ignored ledger rows of unhandled kinds: %s\n", strings.Join(parts, ", "))
	}

	if out == "" {
		return encodeGraph(w, g)
	}
	if err := writeGraphFile(out, g); err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote %d nodes, %d edges to %s\n", len(g.Nodes), len(g.Edges), out)
	return nil
}

func encodeGraph(w io.Writer, g *review.Graph) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	return enc.Encode(g)
}

// writeGraphFile writes g to a temp file beside path and renames it into
// place, so a failed write never leaves a truncated graph behind.
func writeGraphFile(path string, g *review.Graph) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	if err := f.Chmod(0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := encodeGraph(f, g); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
