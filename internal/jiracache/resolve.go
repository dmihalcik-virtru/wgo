package jiracache

import (
	"flag"
	"os"
	"os/exec"
	"time"
)

// Fetcher performs the live (acli) Jira lookup for a ticket. cmd supplies a
// jira-backed implementation; keeping it an interface here avoids a
// jira -> jiracache -> jira import cycle and lets tests count calls.
type Fetcher interface {
	FetchJira(ticket string) (Info, error)
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
// background refreshes for the same ticket, so rapid re-renders don't stampede
// acli / the Jira API.
const refreshBackoff = 30 * time.Second

// Resolve returns the Jira info for a ticket, reconciling the on-disk cache with
// the network per opts, and reports the freshness State of the value served.
// The hot path (a Fresh hit, or a Stale hit without SyncOnMiss) never blocks on
// the network.
func Resolve(f Fetcher, ticket string, opts Opts) (Info, State, error) {
	if opts.Synchronous {
		return fetchAndStore(f, ticket)
	}

	info, state := Read(ticket, opts.TTL)
	switch state {
	case Fresh:
		return info, Fresh, nil
	case Stale:
		if opts.RefreshStale {
			startRefresh(ticket)
		}
		return info, Stale, nil
	default: // Miss
		if opts.SyncOnMiss {
			return fetchAndStore(f, ticket)
		}
		if opts.RefreshStale {
			startRefresh(ticket)
		}
		return Info{}, Miss, nil
	}
}

// fetchAndStore performs the live fetch and writes the result through the cache.
// A fetch error is returned and the cache is left untouched; a successful fetch
// is written so subsequent reads are served locally.
func fetchAndStore(f Fetcher, ticket string) (Info, State, error) {
	if f == nil {
		return Info{}, Miss, nil
	}
	info, err := f.FetchJira(ticket)
	if err != nil {
		return Info{}, Miss, err
	}
	// Ignore write errors: a failed cache write must not fail the command.
	_ = Write(ticket, info)
	return info, Fresh, nil
}

// startRefresh is the seam for kicking a background refresh. Tests override it
// to count invocations without forking a process.
var startRefresh = spawnRefreshProcess

// spawnRefreshProcess kicks a detached `wgo _refresh-jira <ticket>` to
// repopulate the cache off the hot path. It is best-effort: the lease
// suppresses stampedes, and any failure (or running under `go test`) is a
// no-op.
func spawnRefreshProcess(ticket string) {
	if underTest() {
		return
	}
	if !LockRefresh(ticket, refreshBackoff) {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	// nil std streams discard the child's output; no Wait, so the orphaned
	// child finishes the refresh after the parent exits.
	_ = exec.Command(exe, "_refresh-jira", ticket).Start()
}

// underTest reports whether the process is a `go test` binary, so the default
// spawner never forks the test binary with arguments it cannot handle.
func underTest() bool {
	return flag.Lookup("test.v") != nil
}
