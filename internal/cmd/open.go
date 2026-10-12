package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

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

where the ID is a dashboard workspace ID (ws- and 16 lowercase hex
digits; see wgo dash --json). Anything else is rejected: other hosts,
paths, ports, fragments, extra, empty or repeated parameters,
percent-encoding, and any command or resume parameter. A link can never run a command.

The ID is resolved through current discovery and must be an existing jj
workspace under a [discovery] base_dirs root. The first time a link opens a
workspace (and again whenever its path changes), wgo shows a macOS dialog
with the full path, defaulting to Cancel; without a dialog it asks y/N on a
terminal, and otherwise refuses. Accepted workspaces are remembered in
~/.wgo/url-handler-approvals.json. A path with control or invisible
formatting characters is refused rather than shown.

Only one wgo open runs at a time: while one is handling a link (say, its
dialog is up), another exits at once and drops its link.

Each link's outcome and any warnings are also appended to
~/.wgo/url-handler.log (the workspace ID, never the URL), since the URL
handler discards what wgo open prints.

The tab opens with the same launcher as wgo dash's Open tab ([dash]
terminal settings); when no terminal can be opened it prints a cd command
to copy.`,
	Args: cobra.ExactArgs(1),
	// The error says what to do; usage text would bury it (and the
	// applet shows it in a notification).
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
	st, err := store.New()
	if err != nil {
		return err
	}
	// The applet discards stdout and stderr, so everything this link
	// reports also goes to the log.
	lg, lerr := urlhandler.OpenLog(urlhandler.LogPath(st.BaseDir()), nil)
	defer lg.Close()
	if lerr != nil {
		fmt.Fprintf(errOut, "wgo open: %v; continuing without a log\n", lerr)
	}
	err = runOpenLogged(ctx, raw, st, lg, io.MultiWriter(out, lg), io.MultiWriter(errOut, lg), errOut)
	if err != nil {
		lg.Printf("%v", err)
	}
	return err
}

// runOpenLogged is runOpen once the log is open: out and warn also write
// to it, prompt (the y/N question) does not.
func runOpenLogged(ctx context.Context, raw string, st *store.FileStore, lg *urlhandler.Log, out, warn, prompt io.Writer) error {
	// Reject a malformed link before loading config or running discovery.
	// Neither the log nor the error quotes the URL.
	id, err := urlhandler.ParseOpenURL(raw)
	if err != nil {
		lg.Printf("wgo open: rejected a link")
		return fmt.Errorf("wgo open: rejected wgo:// link: %w. Only %s is accepted; nothing was opened", err, urlhandler.AcceptedForm)
	}
	lg.Printf("wgo open: link to %s", id)
	if err := config.Init(); err != nil {
		return err
	}
	cfg := config.Get()
	collector := dash.NewCollector(dash.Config{
		JJ:       jj.NewCLI(),
		Discover: discovery.FromConfig(cfg).DiscoverAll,
	})
	logf := func(format string, args ...any) { fmt.Fprintf(warn, "wgo open: "+format+"\n", args...) }
	lcfg := dashLaunchConfig(cfg.Dash, warn)
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
			Prompt: prompt,
		},
		Approvals: urlhandler.NewApprovals(urlhandler.ApprovalsPath(st.BaseDir())),
		Lock: func() (func(), error) {
			return urlhandler.TryLock(urlhandler.LockPath(st.BaseDir()))
		},
		Warnf: logf,
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
	if len(res.Fallbacks) > 0 {
		fmt.Fprintln(out, "wgo open: tried first: "+strings.Join(res.Fallbacks, "; "))
	}
	return nil
}

// stdinIsTerminal reports whether stdin is an interactive terminal.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
