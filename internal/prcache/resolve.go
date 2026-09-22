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
// the network per opts. The hot path (a Fresh hit, or a Stale hit without
// SyncOnMiss) never blocks on the network.
//
// It returns a single Result rather than (refs, state, error) because here the
// error is data: a failed refresh over surviving last-known-good data yields
// both populated PRs and a non-nil Err, and a (value, error) signature invites
// callers to discard the value they should still be rendering.
func Resolve(f Fetcher, remoteURL, repoPath, branch string, opts Opts) Result {
	if opts.Synchronous {
		return fetchAndStore(f, remoteURL, repoPath, branch)
	}

	cached := Read(remoteURL, repoPath, branch, opts.TTL)
	switch cached.State {
	case Fresh:
		return cached
	case Stale:
		if opts.RefreshStale {
			startRefresh(remoteURL, repoPath, branch)
		}
		return cached
	default: // Miss
		// A recent failure is itself a result: re-fetching now would just
		// re-fail, and SyncOnMiss would turn a GitHub outage into a blocking
		// call on every single invocation.
		recentlyFailed := cached.Err != nil && time.Since(cached.LastAttemptAt) < opts.TTL
		if opts.SyncOnMiss && !recentlyFailed {
			return fetchAndStore(f, remoteURL, repoPath, branch)
		}
		if opts.RefreshStale {
			startRefresh(remoteURL, repoPath, branch)
		}
		return cached
	}
}

// fetchAndStore performs the live fetch and writes the result through the
// cache. A successful fetch (including a genuine "no PRs" result) replaces the
// cached refs. A failed fetch is recorded as a failed *attempt*: any previously
// cached refs are preserved and returned alongside the error, so a transient
// GitHub failure degrades to a stale PR line rather than a blank one.
func fetchAndStore(f Fetcher, remoteURL, repoPath, branch string) Result {
	if f == nil {
		return readAsStale(remoteURL, repoPath, branch)
	}
	refs, err := f.FetchPRs(repoPath, branch)
	if err != nil {
		// Ignore write errors: a failed cache write must not fail the command.
		_ = WriteFailure(remoteURL, repoPath, branch, err)
		prior := readAsStale(remoteURL, repoPath, branch)
		prior.Err = err
		return prior
	}
	_ = Write(remoteURL, repoPath, branch, refs)
	return Result{
		PRs:           refs,
		State:         Fresh,
		FetchedAt:     time.Now(),
		LastAttemptAt: time.Now(),
	}
}

// readAsStale reads the cached entry with a zero TTL, so whatever it holds is
// reported as Stale rather than Fresh. Used after a live fetch failed: the
// surviving refs are by definition not current.
func readAsStale(remoteURL, repoPath, branch string) Result {
	return Read(remoteURL, repoPath, branch, 0)
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
