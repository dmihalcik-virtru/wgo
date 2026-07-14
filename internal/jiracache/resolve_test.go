package jiracache

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingFetcher records how many times FetchJira is called so tests can assert
// the hot path made no network requests.
type countingFetcher struct {
	info  Info
	err   error
	calls int32
}

func (f *countingFetcher) FetchJira(_ string) (Info, error) {
	atomic.AddInt32(&f.calls, 1)
	return f.info, f.err
}

func (f *countingFetcher) count() int { return int(atomic.LoadInt32(&f.calls)) }

// stubRefresh replaces the background-refresh spawner with a counter for the
// duration of a test.
func stubRefresh(t *testing.T) *int32 {
	t.Helper()
	var n int32
	old := startRefresh
	startRefresh = func(_ string) { atomic.AddInt32(&n, 1) }
	t.Cleanup(func() { startRefresh = old })
	return &n
}

// TestResolveFreshNoNetwork: a Fresh cache hit is served without ever calling
// the fetcher (the "statusline within the TTL makes no acli call" criterion).
func TestResolveFreshNoNetwork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Write(testTicket, sampleInfo()))

	f := &countingFetcher{info: sampleInfo()}
	info, state, err := Resolve(f, testTicket, Opts{TTL: time.Hour, RefreshStale: true})
	require.NoError(t, err)
	assert.Equal(t, Fresh, state)
	assert.Equal(t, "In Review", info.Status)
	assert.Equal(t, 0, f.count(), "fresh hit must not touch the network")
}

// TestResolveSyncOnMissFetches: a cold miss with SyncOnMiss fetches synchronously
// and writes through so the next read is Fresh.
func TestResolveSyncOnMissFetches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &countingFetcher{info: sampleInfo()}

	info, state, err := Resolve(f, testTicket, Opts{TTL: time.Hour, SyncOnMiss: true})
	require.NoError(t, err)
	assert.Equal(t, Fresh, state)
	assert.Equal(t, "In Review", info.Status)
	assert.Equal(t, 1, f.count())

	cached, cState := Read(testTicket, time.Hour)
	assert.Equal(t, Fresh, cState)
	assert.Equal(t, "In Review", cached.Status)
}

// TestResolveMissKicksBackgroundRefresh: a cold miss without SyncOnMiss returns
// empty immediately and kicks exactly one background refresh.
func TestResolveMissKicksBackgroundRefresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	n := stubRefresh(t)
	f := &countingFetcher{info: sampleInfo()}

	info, state, err := Resolve(f, testTicket, Opts{TTL: time.Hour, RefreshStale: true})
	require.NoError(t, err)
	assert.Equal(t, Miss, state)
	assert.Equal(t, Info{}, info)
	assert.Equal(t, 0, f.count(), "hot path never fetches synchronously")
	assert.Equal(t, int32(1), atomic.LoadInt32(n), "a background refresh should be kicked")
}

// TestResolveStaleServesAndKicks: a stale entry is served instantly and a
// background refresh is kicked without any synchronous fetch.
func TestResolveStaleServesAndKicks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Write(testTicket, sampleInfo()))
	n := stubRefresh(t)
	f := &countingFetcher{info: sampleInfo()}

	info, state, err := Resolve(f, testTicket, Opts{TTL: 0, RefreshStale: true})
	require.NoError(t, err)
	assert.Equal(t, Stale, state)
	assert.Equal(t, "In Review", info.Status)
	assert.Equal(t, 0, f.count(), "stale hot path never blocks on the network")
	assert.Equal(t, int32(1), atomic.LoadInt32(n))
}

// TestResolveSynchronousBypassesCache: Synchronous fetches fresh data even when
// an entry exists, and writes it through.
func TestResolveSynchronousBypassesCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Write(testTicket, sampleInfo()))

	newInfo := Info{Status: "Done", Assignee: "Bob"}
	f := &countingFetcher{info: newInfo}
	info, state, err := Resolve(f, testTicket, Opts{Synchronous: true})
	require.NoError(t, err)
	assert.Equal(t, Fresh, state)
	assert.Equal(t, "Done", info.Status)
	assert.Equal(t, 1, f.count())

	cached, _ := Read(testTicket, time.Hour)
	assert.Equal(t, "Done", cached.Status, "cache should be overwritten with fresh data")
}

// TestResolveFetchErrorLeavesCache: a fetch error surfaces and does not clobber
// the existing entry.
func TestResolveFetchErrorLeavesCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Write(testTicket, sampleInfo()))

	f := &countingFetcher{err: errors.New("boom")}
	_, _, err := Resolve(f, testTicket, Opts{Synchronous: true})
	require.Error(t, err)

	cached, state := Read(testTicket, time.Hour)
	assert.Equal(t, Fresh, state)
	assert.Equal(t, "In Review", cached.Status, "failed fetch must not wipe the cached entry")
}

// TestResolveFetchErrorNegativeCachesColdKey: a fetch error on a cold key writes
// a short-lived negative entry so an environment without acli serves it locally
// instead of respawning the warmer on every render.
func TestResolveFetchErrorNegativeCachesColdKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	f := &countingFetcher{err: errors.New("no acli")}
	_, _, err := Resolve(f, testTicket, Opts{Synchronous: true})
	require.Error(t, err)

	info, state := Read(testTicket, time.Hour)
	assert.Equal(t, Fresh, state, "a cold fetch failure should leave a negative entry")
	assert.Equal(t, Info{}, info, "the negative entry carries no status")
}
