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

// runRefreshJira does a synchronous cache-writing fetch for ticket. The
// synchronous path bypasses the TTL cache read, but the fetch needs config:
// [jira.fields] names the site's custom fields, and an entry written without
// them would lose its story points and team (and wgo dash reads the same
// entry). A config that cannot be loaded leaves the defaults, as elsewhere.
func runRefreshJira(ticket string) error {
	if ticket == "" {
		return nil
	}
	_ = config.Init()
	_, _, err := jiracache.Resolve(jiraFetcherFn(), ticket, jiracache.Opts{Synchronous: true})
	return err
}
