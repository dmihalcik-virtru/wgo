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

// FetchPRs implements prcache.Fetcher. It uses the enriched list so the review
// decision, draft flag, and CI rollup are cached alongside the base PR data —
// the extra API calls land here (off the statusline hot path), never on reads.
func (g ghFetcher) FetchPRs(repoPath, branch string) ([]models.PRRef, error) {
	prs, err := g.c.ListPRsForBranchEnriched(repoPath, branch)
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
//
// The full Result is returned, not just the refs: a failed lookup can still
// carry last-known-good PRs, and the renderer needs the fetch time and error to
// say so.
func resolvePRs(cwd, remoteURL, branch string, opts contextOptions) prcache.Result {
	if branch == "" || branch == "(no bookmark)" {
		return prcache.Result{}
	}
	return prcache.Resolve(newGHFetcher(), remoteURL, cwd, branch, cacheOpts(opts))
}

// prLookup splits a cache Result into the two fields the context exposes: the
// refs to render, and — only when something is off — their provenance.
//
// Provenance is attached only for a failure over non-current data. A failure a
// fresh success has already superseded is not worth a warning: it would flicker
// on and off as background refreshes race the TTL.
func prLookup(res prcache.Result) ([]models.PRRef, *models.PRLookupRef) {
	if res.Err == nil || res.State == prcache.Fresh {
		return res.PRs, nil
	}
	ref := &models.PRLookupRef{Error: res.Err.Error()}
	if !res.FetchedAt.IsZero() {
		at := res.FetchedAt
		ref.FetchedAt = &at
	}
	return res.PRs, ref
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
			Number:         pr.Number,
			Title:          pr.Title,
			State:          pr.State,
			URL:            pr.URL,
			ReviewDecision: pr.ReviewDecision,
			IsDraft:        pr.IsDraft,
			Checks:         pr.Checks,
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
