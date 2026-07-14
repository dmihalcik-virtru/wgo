package cmd

import (
	"os"
	"os/exec"
	"time"

	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/prcache"
	"github.com/virtru/wgo/models"
)

// refreshBackoff is how long a background PR refresh suppresses further
// background refreshes for the same branch, so rapid prompt re-renders don't
// stampede the GitHub API.
const refreshBackoff = 30 * time.Second

// resolvePRs returns the PR refs for a branch according to opts.
//
//   - LocalOnly && !Refresh (the statusline hot path): read the on-disk cache
//     only. A hit (fresh or stale) is returned without any network call; a
//     stale/miss additionally kicks a best-effort background refresh. Never
//     blocks.
//   - otherwise (`wgo .`, or statusline --refresh): make the synchronous
//     GitHub call and write the result back so the cache stays warm.
func resolvePRs(cwd, remoteURL, branch string, opts contextOptions) []models.PRRef {
	if branch == "" || branch == "(no bookmark)" {
		return nil
	}

	if opts.LocalOnly && !opts.Refresh {
		refs, state := prcache.Read(remoteURL, cwd, branch, prTTL())
		if state != prcache.Fresh {
			kickBackgroundRefresh(remoteURL, cwd, branch)
		}
		return refs
	}

	gh := github.NewClient()
	prs, err := gh.ListPRsForBranch(cwd, branch)
	if err != nil {
		return nil
	}
	refs := toPRRefs(prs)
	// Write-through warms the cache for the next statusline render. Ignore
	// errors: a failed cache write must not break `wgo .`.
	_ = prcache.Write(remoteURL, cwd, branch, refs)
	return refs
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

// kickBackgroundRefresh spawns a detached `wgo -C <cwd> statusline --refresh`
// to repopulate the cache out of the hot path. It is best-effort: a lock file
// suppresses stampedes and any failure is silently ignored.
func kickBackgroundRefresh(remoteURL, cwd, branch string) {
	if !prcache.LockRefresh(remoteURL, cwd, branch, refreshBackoff) {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	// nil std streams (the exec.Cmd default) discard the child's output to the
	// null device. No Wait: the orphaned child finishes the refresh after we exit.
	c := exec.Command(exe, "-C", cwd, "statusline", "--refresh")
	_ = c.Start()
}
