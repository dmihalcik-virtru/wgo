package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/urlhandler"
)

var (
	setupUninstall bool
	setupDryRun    bool
	// setupGOOS lets tests exercise the non-macOS path.
	setupGOOS = runtime.GOOS
)

// setupCmd groups optional, explicit installation steps.
var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Optional integrations that wgo installs only when asked",
}

// setupURLHandlerCmd is `wgo setup url-handler`.
var setupURLHandlerCmd = &cobra.Command{
	Use:   "url-handler",
	Short: "Install (or --uninstall) the macOS wgo:// link handler",
	Long: `Install a small macOS applet, ~/Applications/wgo URL Handler.app, that
handles wgo:// links by running this wgo binary as:

  <absolute path of wgo> open <url>

with the whole URL as one argument. wgo open accepts only
wgo://open?ws=<workspace-id>, asks before the first open of each workspace,
and can only open a terminal tab; see wgo open --help.

Setup compiles the applet with osacompile, adds the wgo URL scheme to its
Info.plist with plutil, re-signs it ad hoc with codesign and registers it
with LaunchServices (lsregister -f). The wgo path and your current PATH
(wgo needs jj on it) are fixed at install time: run setup again after
moving wgo.

--uninstall unregisters and deletes the applet, after checking it carries
the marker wgo wrote into its Info.plist; it never deletes anything else.
--dry-run prints the steps without running them.

macOS only; on other systems it reports that it is unavailable. wgo dash
does not need it.`,
	Args: cobra.NoArgs,
	// The error says what to do; usage text would bury it (and the
	// applet shows it in an alert).
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		in := &urlhandler.Installer{
			GOOS:    setupGOOS,
			PathEnv: os.Getenv("PATH"),
			Out:     cmd.OutOrStdout(),
			DryRun:  setupDryRun,
		}
		if setupGOOS != "darwin" {
			// Installer reports the unavailable OS before anything else.
			return in.Install(cmd.Context())
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("cannot find your home directory: %w", err)
		}
		in.Home = home
		if setupUninstall {
			return in.Uninstall(cmd.Context())
		}
		exe, err := wgoExecutable()
		if err != nil {
			return err
		}
		in.Executable = exe
		return in.Install(cmd.Context())
	},
}

func init() {
	setupURLHandlerCmd.Flags().BoolVar(&setupUninstall, "uninstall", false, "unregister and remove the applet wgo installed")
	setupURLHandlerCmd.Flags().BoolVar(&setupDryRun, "dry-run", false, "print the steps without running them")
	setupCmd.AddCommand(setupURLHandlerCmd)
	rootCmd.AddCommand(setupCmd)
}

// wgoExecutable is the absolute, symlink-resolved path of the running wgo.
// A `go run` build is refused: it is deleted when the command exits.
func wgoExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot find the wgo executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("cannot resolve the wgo executable: %w", err)
	}
	if !filepath.IsAbs(exe) {
		return "", fmt.Errorf("the wgo executable path %q is not absolute", exe)
	}
	if strings.Contains(exe, string(filepath.Separator)+"go-build") {
		return "", errors.New("wgo is running from a temporary go run build, which the applet could not run later; build or install wgo (go build -o wgo ./cmd/wgo) and run setup with that binary")
	}
	return exe, nil
}
