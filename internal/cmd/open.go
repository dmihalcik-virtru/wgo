package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/dash"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/launch"
	"github.com/virtru/wgo/internal/store"
	"github.com/virtru/wgo/internal/urlhandler"
)

// openCmd is `wgo open <url>`: what the optional wgo:// handler runs (gh-70).
var openCmd = &cobra.Command{
	Use:   "open <wgo://open?ws=ID>",
	Short: "Open a terminal tab for a wgo:// workspace link",
	Long: `Open a new terminal tab in the workspace a wgo:// link names. This is what
the optional URL handler (wgo setup url-handler) runs when you click a
wgo:// link; you can also run it yourself.

Only one form is accepted, exactly:

  wgo://open?ws=<workspace-id>

where the ID is a dashboard workspace ID (ws- and 16 hex digits; see
wgo dash --json). Anything else is rejected: other hosts, paths, ports,
fragments, extra, empty or repeated parameters, percent-encoding, and any
command or resume parameter. A link can never run a command.

The ID is resolved through current discovery and must be an existing jj
workspace under a [discovery] base_dirs root. The first time a link opens a
workspace (and again whenever its path changes), wgo shows a macOS dialog
with the full path, defaulting to Cancel; without a dialog it asks y/N on a
terminal, and otherwise refuses. Accepted workspaces are remembered in
~/.wgo/url-handler-approvals.json.

The tab opens with the same launcher as wgo dash's Open tab ([dash]
terminal settings); when no terminal can be opened it prints a cd command
to copy.`,
	Args: cobra.ExactArgs(1),
	// The error says what to do; usage text would bury it (and the
	// applet shows it in an alert).
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runOpen(cmd.Context(), args[0], cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func init() {
	rootCmd.AddCommand(openCmd)
}

func runOpen(ctx context.Context, raw string, out, errOut io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Reject a malformed link before loading config or running discovery.
	if _, err := urlhandler.ParseOpenURL(raw); err != nil {
		return fmt.Errorf("wgo open: rejected wgo:// link: %w; nothing was opened", err)
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
		JJ:       jj.NewCLI(),
		Discover: discovery.FromConfig(cfg).DiscoverAll,
	})
	logf := func(format string, args ...any) { fmt.Fprintf(errOut, "wgo open: "+format+"\n", args...) }
	lcfg := dashLaunchConfig(cfg.Dash, errOut)
	lcfg.Resume = "" // a link never resumes an agent
	return openWith(ctx, raw, out, urlhandler.OpenOptions{
		Resolver: collector,
		Roots:    cfg.Discovery.BaseDirs,
		Launcher: launch.NewSystem(lcfg, launch.ExecRunner{Logf: logf}),
		Confirmer: urlhandler.SystemConfirmer{
			GOOS:   runtime.GOOS,
			Run:    urlhandler.ExecRunner{},
			Stdin:  os.Stdin,
			TTY:    stdinIsTerminal(),
			Prompt: errOut,
		},
		Approvals: urlhandler.NewApprovals(urlhandler.ApprovalsPath(st.BaseDir())),
		Warnf:     logf,
	})
}

// openWith runs urlhandler.Open and reports its outcome. A declined link is
// not an error: the user chose not to open it.
func openWith(ctx context.Context, raw string, out io.Writer, o urlhandler.OpenOptions) error {
	res, err := urlhandler.Open(ctx, raw, o)
	if errors.Is(err, urlhandler.ErrDeclined) {
		fmt.Fprintln(out, "wgo open: not opened; you declined the link.")
		return nil
	}
	if err != nil {
		if res.Copy != "" {
			fmt.Fprintln(out, res.Copy)
		}
		return fmt.Errorf("wgo open: %w", err)
	}
	fmt.Fprintln(out, "wgo open: "+res.Message)
	return nil
}

// stdinIsTerminal reports whether stdin is an interactive terminal.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
