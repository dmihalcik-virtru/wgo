package cmd

import (
	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/prcache"
)

// refreshPRCmd is the hidden background warmer for the on-disk PR cache. It
// performs the synchronous GitHub fetch for one repo/branch and writes it
// through the cache, so short-lived hot-path commands (wgo ., statusline) never
// block on the network. prcache spawns it detached via
// `wgo -C <repoPath> _refresh-pr <branch>`; it can also be run by hand to warm
// a specific entry.
var refreshPRCmd = &cobra.Command{
	Use:    "_refresh-pr <branch>",
	Short:  "Internal: warm the on-disk PR cache for a branch",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runRefreshPR(args[0])
	},
}

func init() {
	rootCmd.AddCommand(refreshPRCmd)
}

// runRefreshPR resolves the repo (from -C/cwd) and its origin remote, then does
// a synchronous cache-writing fetch for branch.
func runRefreshPR(branch string) error {
	if branch == "" || branch == "(no bookmark)" {
		return nil
	}
	cwd, err := resolveCwd()
	if err != nil {
		return err
	}
	// config is only consulted for defaults elsewhere; a failure is non-fatal.
	_ = config.Init()

	remoteURL := ""
	if remotes, err := jj.NewCLI().RemoteURLs(cwd); err == nil {
		remoteURL = remotes["origin"]
	}

	_, _, err = prcache.Resolve(newGHFetcher(), remoteURL, cwd, branch, prcache.Opts{Synchronous: true})
	return err
}
