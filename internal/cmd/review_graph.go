package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/review"
	"github.com/virtru/wgo/internal/store"
)

var reviewGraphOut string

var reviewGraphCmd = &cobra.Command{
	Use:   "graph [label|path]",
	Short: "Emit the influence graph of a year-in-review run as JSON",
	Long: `Build the influence graph of a year-in-review run: people (authors,
reviewers, people who built on your work), PRs, tickets and repos, joined by
who reviewed, wrote, built on or referenced what.

The argument is a run label under ~/.wgo/cache/review/runs/ or a path to a run
directory; with none, the most recent run is used. The graph is written to
stdout, or to --out FILE. It is plain JSON, so it pipes into jq:

  wgo review graph | jq '[.edges[] | select(.kind=="built_on")] | length'`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		arg := ""
		if len(args) == 1 {
			arg = args[0]
		}
		return runReviewGraph(cmd.OutOrStdout(), arg, reviewGraphOut)
	},
}

func init() {
	reviewCmd.AddCommand(reviewGraphCmd)
	reviewGraphCmd.Flags().StringVarP(&reviewGraphOut, "out", "o", "", "write the graph to this file instead of stdout")
}

// runReviewGraph resolves the run, builds its graph and writes it as indented
// JSON to out (a file path) or w when out is empty.
func runReviewGraph(w io.Writer, arg, out string) error {
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
	g := review.Build(run)

	dst := w
	if out != "" {
		f, err := os.Create(out)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		dst = f
	}
	enc := json.NewEncoder(dst)
	enc.SetIndent("", " ")
	if err := enc.Encode(g); err != nil {
		return err
	}
	if out != "" {
		fmt.Fprintf(w, "wrote %d nodes, %d edges to %s\n", len(g.Nodes), len(g.Edges), out)
	}
	return nil
}
