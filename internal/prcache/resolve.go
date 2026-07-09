package prcache

import (
	"flag"
	"os"
	"os/exec"
	"time"

	"github.com/virtru/wgo/models"
)

// Fetcher performs the live (network) PR lookup for a repo/branch. cmd supplies
// a GitHub-backed implementation; keeping it an interface here avoids a
// github -> prcache -> github import cycle and lets tests count calls.
type Fetcher interface {
	FetchPRs(repoPath, branch string) ([]models.PRRef, error)
}

// Opts controls how Resolve reconciles the on-disk cache with the network.
type Opts struct {
	// TTL is the freshness window: an entry younger than TTL is served without
	// any network call.
	TTL time.Duration
	// RefreshStale kicks a background refresh subprocess when the entry is Stale
	// or Miss, so the next invocation finds fresh data. Never blocks.
	RefreshStale bool
	// Synchronous bypasses the cache read: fetch now and write the result
	// through. Used by --refresh and the background warmer.
	Synchronous bool
	// SyncOnMiss fetches synchronously when the entry is absent (Miss) instead
	// of returning empty, so an interactive first run is not blank.
	SyncOnMiss bool
}

// refreshBackoff is how long acquiring a refresh lease suppresses further
// background refreshes for the same key, so rapid re-renders don't stampede the
// GitHub API.
const refreshBackoff = 30 * time.Second

// Resolve returns the PR refs for a branch, reconciling the on-disk cache with
// the network per opts, and reports the freshness State of the value served.
// The hot path (a Fresh hit, or a Stale hit without SyncOnMiss) never blocks on
// the network.
func Resolve(f Fetcher, remoteURL, repoPath, branch string, opts Opts) ([]models.PRRef, State, error) {
	if opts.Synchronous {
		return fetchAndStore(f, remoteURL, repoPath, branch)
	}

	refs, state := Read(remoteURL, repoPath, branch, opts.TTL)
	switch state {
	case Fresh:
		return refs, Fresh, nil
	case Stale:
		if opts.RefreshStale {
			startRefresh(remoteURL, repoPath, branch)
		}
		return refs, Stale, nil
	default: // Miss
		if opts.SyncOnMiss {
			return fetchAndStore(f, remoteURL, repoPath, branch)
		}
		if opts.RefreshStale {
			startRefresh(remoteURL, repoPath, branch)
		}
		return nil, Miss, nil
	}
}

// fetchAndStore performs the live fetch and writes the result through the cache.
// A fetch error is returned and the cache is left untouched; a successful fetch
// (including a "no PRs" result) is written so subsequent reads are served
// locally.
func fetchAndStore(f Fetcher, remoteURL, repoPath, branch string) ([]models.PRRef, State, error) {
	if f == nil {
		return nil, Miss, nil
	}
	refs, err := f.FetchPRs(repoPath, branch)
	if err != nil {
		return nil, Miss, err
	}
	// Ignore write errors: a failed cache write must not fail the command.
	_ = Write(remoteURL, repoPath, branch, refs)
	return refs, Fresh, nil
}

// startRefresh is the seam for kicking a background refresh. Tests override it
// to count invocations without forking a process.
var startRefresh = spawnRefreshProcess

// spawnRefreshProcess kicks a detached `wgo -C <repoPath> _refresh-pr <branch>`
// to repopulate the cache off the hot path. It is best-effort: the lease
// suppresses stampedes, and any failure (or running under `go test`) is a
// no-op.
func spawnRefreshProcess(remoteURL, repoPath, branch string) {
	if underTest() {
		return
	}
	if !LockRefresh(remoteURL, repoPath, branch, refreshBackoff) {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	// nil std streams discard the child's output; no Wait, so the orphaned
	// child finishes the refresh after the parent exits.
	_ = exec.Command(exe, "-C", repoPath, "_refresh-pr", branch).Start()
}

// underTest reports whether the process is a `go test` binary, so the default
// spawner never forks the test binary with arguments it cannot handle.
func underTest() bool {
	return flag.Lookup("test.v") != nil
}
