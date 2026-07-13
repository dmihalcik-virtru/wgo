package cmd

import (
	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/jiracache"
)

// refreshJiraCmd is the hidden background warmer for the on-disk Jira cache. It
// performs the synchronous acli fetch for one ticket and writes it through the
// cache, so short-lived hot-path commands (wgo ., statusline) never block on
// acli. jiracache spawns it detached via `wgo _refresh-jira <ticket>`; it can
// also be run by hand to warm a specific entry.
var refreshJiraCmd = &cobra.Command{
	Use:    "_refresh-jira <ticket>",
	Short:  "Internal: warm the on-disk Jira status cache for a ticket",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runRefreshJira(args[0])
	},
}

func init() {
	rootCmd.AddCommand(refreshJiraCmd)
}

// runRefreshJira does a synchronous cache-writing fetch for ticket.
func runRefreshJira(ticket string) error {
	if ticket == "" {
		return nil
	}
	// config is only consulted for defaults elsewhere; a failure is non-fatal.
	_ = config.Init()

	_, _, err := jiracache.Resolve(jiraFetcherFn(), ticket, jiracache.Opts{Synchronous: true})
	return err
}
