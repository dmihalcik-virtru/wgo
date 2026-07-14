package cmd

import (
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
)

var (
	statuslineJSON     bool
	statuslineFormat   string
	statuslineNoColor  bool
	statuslineRefresh  bool
	statuslineSiblings bool
)

// statuslineCmd renders the current work context as a single line for shell
// and Claude Code prompts.
var statuslineCmd = &cobra.Command{
	Use:   "statusline",
	Short: "Render current work context as a single line for prompts",
	Long: `Fast, local-only rendering of the current work context as a single line.

Makes no network calls in the default path: PR status comes from the on-disk
cache (a miss simply omits the PR segment). The line is colored and clickable
(branch/repo/PR link to GitHub, ticket links to Jira) so it can be embedded in
a shell prompt or the Claude Code statusline. Use -C/--repo to target a
directory without changing into it, and --refresh in a background warmer to
populate the cache.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatusline(os.Stdout)
	},
}

func init() {
	rootCmd.AddCommand(statuslineCmd)
	statuslineCmd.Flags().BoolVar(&statuslineJSON, "json", false, "Emit the context as JSON (WGO-130 schema) instead of a line")
	statuslineCmd.Flags().StringVar(&statuslineFormat, "format", "", "Render a Go text/template over the context (funcs: upper, lower, color, link)")
	statuslineCmd.Flags().BoolVar(&statuslineNoColor, "no-color", false, "Disable ANSI color and hyperlinks (also honors NO_COLOR)")
	statuslineCmd.Flags().BoolVar(&statuslineRefresh, "refresh", false, "Force a synchronous PR refresh and warm the cache (for a background warmer, not the hot path)")
	statuslineCmd.Flags().BoolVar(&statuslineSiblings, "siblings", false, "Include sibling workspaces (adds a filesystem walk)")
	statuslineCmd.MarkFlagsMutuallyExclusive("json", "format")
}

// runStatusline resolves the local-only context and renders it per the flags.
func runStatusline(w io.Writer) error {
	cwd, err := resolveCwd()
	if err != nil {
		return err
	}
	// Best-effort: config supplies the Jira site for ticket links. A failure
	// just yields a non-linked ticket, so ignore the error.
	_ = config.Init()

	ctx, err := buildContextOpts(cwd, contextOptions{
		LocalOnly: !statuslineRefresh,
		Siblings:  statuslineSiblings,
		Refresh:   statuslineRefresh,
	})
	if err != nil {
		return err
	}

	rich := statuslineRich()
	switch {
	case statuslineJSON:
		return renderJSON(w, ctx)
	case statuslineFormat != "":
		return renderStatuslineFormat(w, ctx, statuslineFormat, rich)
	default:
		return renderStatuslineLine(w, ctx, rich)
	}
}

// statuslineRich reports whether ANSI color and OSC8 hyperlinks should be
// emitted. Unlike interactive commands it is NOT gated on stdout being a TTY:
// prompt hosts capture the output through a pipe yet render ANSI/OSC8. It is
// disabled by --no-color or the NO_COLOR environment variable.
func statuslineRich() bool {
	if statuslineNoColor {
		return false
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	return true
}
