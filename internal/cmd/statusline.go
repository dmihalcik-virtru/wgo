package cmd

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/models"
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
populate the cache.

Claude Code integration:
  Add these keys to ~/.claude/settings.json (requires jq; merge into any
  existing settings without clobbering other hooks). The statusLine renders the
  context; the hooks warm the cache with --refresh so PR/CI/Jira are current at
  session start (so the very first line is right) and after each turn, while
  refreshInterval re-renders so the warmed data shows within a few seconds:

    "statusLine": {
      "type": "command",
      "command": "wgo statusline -C \"$(jq -r '.workspace.current_dir // empty')\"",
      "refreshInterval": 5
    },
    "hooks": {
      "SessionStart": [
        { "hooks": [ {
          "type": "command",
          "command": "wgo statusline --refresh -C \"$(jq -r '.cwd // .workspace.current_dir // empty')\" >/dev/null 2>&1",
          "statusMessage": "Warming wgo PR/CI status..."
        } ] }
      ],
      "Stop": [
        { "hooks": [ {
          "type": "command",
          "command": "wgo statusline --refresh -C \"$(jq -r '.cwd // .workspace.current_dir // empty')\" >/dev/null 2>&1 &"
        } ] }
      ]
    }

  The SessionStart warm is synchronous (no trailing &) so the first render is
  correct; the Stop warm is backgrounded so it never delays a turn.

In a folder that holds jj workspaces without being one, such as a shared root
from wgo add with several repos or a rig root, the line summarizes up to six of
them: each one's bookmark and PRs, after the group's ticket or rig. Anywhere
else outside a jj workspace the line is empty and the exit status 0, so a
prompt never shows an error; --json still reports it.`,
	// A runtime failure is not a usage mistake; the usage text would only
	// bury the error in a prompt.
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatusline(os.Stdout)
	},
}

func init() {
	rootCmd.AddCommand(statuslineCmd)
	statuslineCmd.Flags().BoolVar(&statuslineJSON, "json", false, "Emit the context as JSON (WGO-130 schema) instead of a line")
	statuslineCmd.Flags().StringVar(&statuslineFormat, "format", "", "Render a Go text/template over the context (funcs: upper, lower, color, link)")
	statuslineCmd.Flags().BoolVar(&statuslineNoColor, "no-color", false, "Disable ANSI color and hyperlinks (also honors NO_COLOR)")
	statuslineCmd.Flags().BoolVar(&statuslineRefresh, "refresh", false, "Force a synchronous PR and Jira refresh and warm the caches (for a background warmer, not the hot path)")
	statuslineCmd.Flags().BoolVar(&statuslineSiblings, "siblings", false, "Include sibling workspaces (adds a filesystem walk)")
	statuslineCmd.MarkFlagsMutuallyExclusive("json", "format")
}

// runStatusline resolves the local-only context and renders it per the flags.
func runStatusline(w io.Writer) error {
	cwd, err := resolveCwd()
	if err != nil {
		return err
	}
	// Best-effort: config supplies the Jira site for ticket links. On a
	// failure Init leaves the built-in defaults in place, which just yields a
	// non-linked ticket, so ignore the error: the statusline has nowhere to
	// show it, and every other command reports it.
	_ = config.Init()

	ctx, err := statuslineContext(cwd, contextOptions{
		LocalOnly: !statuslineRefresh,
		Siblings:  statuslineSiblings,
		Refresh:   statuslineRefresh,
	})
	switch {
	case errors.Is(err, errNotJJRepo) && !statuslineJSON:
		// A prompt segment is not worth an error: outside any workspace
		// there is simply nothing to show.
		return nil
	case err != nil:
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

// statuslineContext resolves the context for cwd: its workspace's or, in a
// folder holding several workspaces, a summary of them (see groupWorkspaces).
func statuslineContext(cwd string, opts contextOptions) (*models.Context, error) {
	ctx, err := buildContextOpts(cwd, opts)
	if !errors.Is(err, errNotJJRepo) {
		return ctx, err
	}
	dir, absErr := filepath.Abs(cwd)
	if absErr != nil {
		return nil, err
	}
	members := groupWorkspaces(dir)
	if len(members) == 0 {
		return nil, err
	}
	return buildGroupContext(dir, members, opts), nil
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
