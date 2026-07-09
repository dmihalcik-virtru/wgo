package cmd

import (
	"time"

	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/prcache"
	"github.com/virtru/wgo/models"
)

// ghFetcher adapts the GitHub client to prcache.Fetcher: it lists a branch's
// PRs over the network and projects them onto the compact models.PRRef the
// cache stores.
type ghFetcher struct {
	c *github.CLIClient
}

// newGHFetcher builds a prcache.Fetcher backed by a fresh GitHub client.
func newGHFetcher() prcache.Fetcher {
	return ghFetcher{c: github.NewClient()}
}

// FetchPRs implements prcache.Fetcher.
func (g ghFetcher) FetchPRs(repoPath, branch string) ([]models.PRRef, error) {
	prs, err := g.c.ListPRsForBranch(repoPath, branch)
	if err != nil {
		return nil, err
	}
	return toPRRefs(prs), nil
}

// resolvePRs returns the PR refs for a branch according to opts, delegating the
// cache/network reconciliation to prcache.Resolve:
//
//   - statusline hot path (LocalOnly): read-through only; a Stale/Miss serves
//     whatever is cached and kicks a background refresh. Never blocks.
//   - wgo . (default): read-through with a synchronous fetch on a cold miss so
//     the first run is never blank; a Stale hit serves instantly and warms in
//     the background.
//   - --refresh (opts.Refresh): bypass the cache and fetch synchronously.
func resolvePRs(cwd, remoteURL, branch string, opts contextOptions) []models.PRRef {
	if branch == "" || branch == "(no bookmark)" {
		return nil
	}
	refs, _, _ := prcache.Resolve(newGHFetcher(), remoteURL, cwd, branch, cacheOpts(opts))
	return refs
}

// cacheOpts maps the context resolver options onto prcache.Opts.
func cacheOpts(opts contextOptions) prcache.Opts {
	switch {
	case opts.Refresh:
		return prcache.Opts{Synchronous: true}
	case opts.LocalOnly:
		return prcache.Opts{TTL: prTTL(), RefreshStale: true}
	default:
		return prcache.Opts{TTL: prTTL(), RefreshStale: true, SyncOnMiss: true}
	}
}

// toPRRefs projects GitHub PRInfo values onto the compact models.PRRef used by
// the context. Keeping this the single mapping site prevents `wgo .` and
// statusline from drifting.
func toPRRefs(prs []github.PRInfo) []models.PRRef {
	refs := make([]models.PRRef, 0, len(prs))
	for _, pr := range prs {
		refs = append(refs, models.PRRef{
			Number: pr.Number,
			Title:  pr.Title,
			State:  pr.State,
			URL:    pr.URL,
		})
	}
	return refs
}

// prTTL returns the configured PR cache freshness window (config cache.pr_ttl),
// falling back to 120s when config is unavailable or unset.
func prTTL() time.Duration {
	if cfg := config.Get(); cfg != nil && cfg.Cache.PRTTL > 0 {
		return time.Duration(cfg.Cache.PRTTL) * time.Second
	}
	return 120 * time.Second
}
