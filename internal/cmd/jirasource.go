package cmd

import (
	"strings"
	"time"

	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/jira"
	"github.com/virtru/wgo/internal/jiracache"
)

// Route jiracache's otherwise-swallowed cache faults through the WGO_DEBUG
// logger so a broken (unwritable/unreadable) cache is diagnosable without
// polluting the hot-path output.
func init() {
	jiracache.Logf = debugf
}

// jiraFetcher adapts the acli-backed jira client to jiracache.Fetcher: it reads
// a ticket's live status and assignee and projects them onto the compact
// jiracache.Info the cache stores.
type jiraFetcher struct{}

// newJiraFetcher builds a jiracache.Fetcher backed by the jira client.
func newJiraFetcher() jiracache.Fetcher {
	return jiraFetcher{}
}

// jiraFetcherFn is the seam used to obtain the Fetcher for cache resolution.
// Production uses the acli-backed newJiraFetcher; tests override it to inject a
// stub so they never shell out to acli.
var jiraFetcherFn = newJiraFetcher

// FetchJira implements jiracache.Fetcher. The acli shell-out lands here (off the
// statusline hot path), never on reads.
func (jiraFetcher) FetchJira(ticket string) (jiracache.Info, error) {
	issue, err := jira.GetIssue(ticket)
	if err != nil {
		return jiracache.Info{}, err
	}
	info := jiracache.Info{Status: issue.Fields.Status.Name}
	if issue.Fields.Assignee != nil {
		info.Assignee = issue.Fields.Assignee.DisplayName
	}
	// Best-effort: also cache the acli-detected site host so ticketURL can build
	// a Jira browse link when jira.site isn't configured. A failure here (no
	// acli, not authenticated) just leaves Site empty — it doesn't fail the
	// status fetch.
	if site, err := jira.SiteHost(); err == nil {
		info.Site = site
	}
	return info, nil
}

// resolveJiraStatus returns the live Jira status, assignee, and acli-detected
// site host for a ticket according to opts, delegating the cache/network
// reconciliation to jiracache.Resolve (mirroring resolvePRs):
//
//   - statusline hot path (LocalOnly): read-through only; a Stale/Miss serves
//     whatever is cached and kicks a background refresh. Never blocks on acli.
//   - wgo . (default): read-through with a synchronous fetch on a cold miss.
//   - --refresh (opts.Refresh): bypass the cache and fetch synchronously.
//
// It returns empty strings for non-Jira tickets (GH-<n> GitHub issues) and when
// the status/site is unavailable (no acli / not authenticated). site is only
// ever used by ticketURL as a fallback when jira.site isn't configured.
func resolveJiraStatus(ticket string, opts contextOptions) (status, assignee, site string) {
	if ticket == "" || strings.HasPrefix(ticket, "GH-") {
		return "", "", ""
	}
	info, _, _ := jiracache.Resolve(jiraFetcherFn(), ticket, jiraCacheOpts(opts))
	return info.Status, info.Assignee, info.Site
}

// jiraCacheOpts maps the context resolver options onto jiracache.Opts.
func jiraCacheOpts(opts contextOptions) jiracache.Opts {
	switch {
	case opts.Refresh:
		return jiracache.Opts{Synchronous: true}
	case opts.LocalOnly:
		return jiracache.Opts{TTL: jiraTTL(), RefreshStale: true}
	default:
		return jiracache.Opts{TTL: jiraTTL(), RefreshStale: true, SyncOnMiss: true}
	}
}

// jiraTTL returns the configured Jira cache freshness window (config
// cache.jira_ttl), falling back to 600s (10m) when config is unavailable or
// unset.
func jiraTTL() time.Duration {
	if cfg := config.Get(); cfg != nil && cfg.Cache.JiraTTL > 0 {
		return time.Duration(cfg.Cache.JiraTTL) * time.Second
	}
	return 600 * time.Second
}
