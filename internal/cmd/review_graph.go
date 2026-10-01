package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/links"
	"github.com/virtru/wgo/internal/review"
	"github.com/virtru/wgo/internal/store"
)

var (
	reviewGraphOut    string
	reviewGraphFormat string
	reviewGraphOpen   bool
)

// openInBrowser opens the written explorer; tests replace it so no browser
// is launched.
var openInBrowser = links.OpenInBrowser

var reviewGraphCmd = &cobra.Command{
	Use:   "graph [label|path]",
	Short: "Emit the influence graph of a year-in-review run as JSON or an HTML explorer",
	Long: `Build the influence graph of a year-in-review run: people (authors,
reviewers, people who built on your work), PRs, tickets and repos, joined by
who reviewed, wrote, built on or referenced what.

The argument is a run label under ~/.wgo/cache/review/runs/ or a path to a run
directory; with none, the most recent run is used.

--format json (the default) writes plain JSON to stdout or --out FILE, so it
pipes into jq. --format html writes one self-contained, offline HTML explorer;
it is chosen automatically when --out ends in .html. Without --out, the HTML
goes to ~/.wgo/reviews/<label>.graph.html. Use --out - to send HTML to stdout.

Examples:
  wgo review graph | jq '[.edges[] | select(.kind=="built_on")] | length'
  wgo review graph --open
  wgo review graph 2026-H1 --out /tmp/h1.html
  wgo review graph --out - --format html > explorer.html`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := reviewGraphOpts{Out: reviewGraphOut, Format: reviewGraphFormat, Open: reviewGraphOpen}
		if len(args) == 1 {
			opts.Arg = args[0]
		}
		return runReviewGraph(cmd.OutOrStdout(), opts)
	},
}

func init() {
	reviewCmd.AddCommand(reviewGraphCmd)
	f := reviewGraphCmd.Flags()
	f.StringVarP(&reviewGraphOut, "out", "o", "", "write the graph to this file (- for stdout) instead of the default location")
	f.StringVar(&reviewGraphFormat, "format", "", "output format: json or html (default json, or html when --out ends in .html)")
	f.BoolVar(&reviewGraphOpen, "open", false, "write the HTML explorer (implies --format html) and open it in the browser")
}

// reviewGraphOpts are the inputs of runReviewGraph.
type reviewGraphOpts struct {
	Arg    string // run label or path; empty means the latest run
	Out    string // output file; "-" means the writer; empty means default
	Format string // "json", "html" or "" to infer
	Open   bool   // open the HTML file in a browser afterwards
}

// resolveReviewGraphFormat picks json or html from --format, else the --out
// extension, else json.
func resolveReviewGraphFormat(format, out string) (string, error) {
	switch strings.ToLower(format) {
	case "json", "html":
		return strings.ToLower(format), nil
	case "":
		if strings.HasSuffix(strings.ToLower(out), ".html") {
			return "html", nil
		}
		return "json", nil
	}
	return "", fmt.Errorf("unknown --format %q; use --format json or --format html", format)
}

// runReviewGraph resolves the run, builds its graph and writes it as indented
// JSON or a self-contained HTML page, per opts.
func runReviewGraph(w io.Writer, opts reviewGraphOpts) error {
	// --open only makes sense for the explorer, so it implies html unless the
	// format was chosen explicitly.
	if opts.Open && opts.Format == "" {
		opts.Format = "html"
	}
	format, err := resolveReviewGraphFormat(opts.Format, opts.Out)
	if err != nil {
		return err
	}
	if opts.Open && format != "html" {
		return fmt.Errorf("--open only works with the html format; drop --format json")
	}
	if opts.Open && opts.Out == "-" {
		return fmt.Errorf("--open needs a file to open; drop --out - or give --out a path")
	}

	st, err := store.New()
	if err != nil {
		return err
	}
	dir, err := review.Resolve(st.BaseDir(), opts.Arg)
	if err != nil {
		return err
	}
	run, err := review.Load(dir)
	if err != nil {
		return err
	}
	g := review.Build(run)

	var body []byte
	if format == "html" {
		body, err = review.Render(g)
		if err != nil {
			return err
		}
	} else {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", " ")
		if err := enc.Encode(g); err != nil {
			return err
		}
		body = buf.Bytes()
	}

	out := opts.Out
	if out == "" && format == "html" {
		reviews := filepath.Join(st.BaseDir(), "reviews")
		if err := os.MkdirAll(reviews, 0o755); err != nil {
			return err
		}
		out = filepath.Join(reviews, filepath.Base(g.Label)+".graph.html")
	}
	if out == "" || out == "-" {
		_, err := w.Write(body)
		return err
	}

	if err := os.WriteFile(out, body, 0o644); err != nil {
		return err
	}
	shown := out
	if abs, err := filepath.Abs(out); err == nil {
		shown = links.Link("file://"+abs, out, isTerminal())
	}
	fmt.Fprintf(w, "wrote %d nodes, %d edges to %s\n", len(g.Nodes), len(g.Edges), shown)
	if opts.Open {
		abs, err := filepath.Abs(out)
		if err != nil {
			abs = out
		}
		if err := openInBrowser(abs); err != nil {
			return fmt.Errorf("opening %s: %w (open the file manually)", abs, err)
		}
	}
	return nil
}
