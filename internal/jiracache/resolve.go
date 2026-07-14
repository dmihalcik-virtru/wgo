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
// A fetch error is returned; a successful fetch is written so subsequent reads
// are served locally. On error the cache is left untouched when a usable entry
// already exists (a transient Jira outage keeps serving the last-known status),
// and only a cold key gets a short-lived negative entry so an environment
// without acli doesn't respawn the background warmer on every render forever.
func fetchAndStore(f Fetcher, ticket string) (Info, State, error) {
	if f == nil {
		return Info{}, Miss, nil
	}
	info, err := f.FetchJira(ticket)
	if err != nil {
		if _, state := Read(ticket, 0); state == Miss {
			if werr := writeNegative(ticket); werr != nil {
				logf("jira cache: negative write for %s: %v", ticket, werr)
			}
		}
		return Info{}, Miss, err
	}
	// Ignore write errors on the command path, but surface them for diagnosis:
	// a failed cache write must not fail the command, yet a silently unwritable
	// cache would make the feature appear permanently broken.
	if werr := Write(ticket, info); werr != nil {
		logf("jira cache: write for %s: %v", ticket, werr)
	}
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
		logf("jira cache: locate executable for refresh: %v", err)
		return
	}
	// nil std streams discard the child's output; no Wait, so the orphaned
	// child finishes the refresh after the parent exits.
	if err := exec.Command(exe, "_refresh-jira", ticket).Start(); err != nil {
		logf("jira cache: spawn refresh for %s: %v", ticket, err)
	}
}

// underTest reports whether the process is a `go test` binary, so the default
// spawner never forks the test binary with arguments it cannot handle.
func underTest() bool {
	return flag.Lookup("test.v") != nil
}
