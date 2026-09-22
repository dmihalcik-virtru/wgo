package prcache

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/models"
)

// countingFetcher records how many times FetchPRs is called so tests can assert
// the hot path made no network requests.
type countingFetcher struct {
	refs  []models.PRRef
	err   error
	calls int32
}

func (f *countingFetcher) FetchPRs(_, _ string) ([]models.PRRef, error) {
	atomic.AddInt32(&f.calls, 1)
	return f.refs, f.err
}

func (f *countingFetcher) count() int { return int(atomic.LoadInt32(&f.calls)) }

// stubRefresh replaces the background-refresh spawner with a counter for the
// duration of a test.
func stubRefresh(t *testing.T) *int32 {
	t.Helper()
	var n int32
	old := startRefresh
	startRefresh = func(_, _, _ string) { atomic.AddInt32(&n, 1) }
	t.Cleanup(func() { startRefresh = old })
	return &n
}

// TestResolveFreshNoNetwork: a Fresh cache hit is served without ever calling
// the fetcher. This is the core of the "second statusline within the TTL makes
// no network request" acceptance criterion.
func TestResolveFreshNoNetwork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Write(testRemote, testRepo, "feature-x", sampleRefs()))

	f := &countingFetcher{refs: sampleRefs()}
	res := Resolve(f, testRemote, testRepo, "feature-x", Opts{TTL: time.Hour, RefreshStale: true})
	require.NoError(t, res.Err)
	assert.Equal(t, Fresh, res.State)
	require.Len(t, res.PRs, 1)
	assert.Equal(t, 0, f.count(), "fresh hit must not touch the network")
}

// TestResolveZeroNetworkAcrossReads: the first miss fetches once; every read
// within the TTL after that is served locally with zero further fetches.
func TestResolveZeroNetworkAcrossReads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &countingFetcher{refs: sampleRefs()}

	// Cold miss with SyncOnMiss → one fetch, cache warmed.
	res := Resolve(f, testRemote, testRepo, "feature-x", Opts{TTL: time.Hour, RefreshStale: true, SyncOnMiss: true})
	require.NoError(t, res.Err)
	assert.Equal(t, Fresh, res.State)

	// Subsequent reads within the TTL: no more fetches.
	for range 3 {
		res := Resolve(f, testRemote, testRepo, "feature-x", Opts{TTL: time.Hour, RefreshStale: true})
		require.NoError(t, res.Err)
		assert.Equal(t, Fresh, res.State)
		require.Len(t, res.PRs, 1)
	}
	assert.Equal(t, 1, f.count(), "only the cold miss should hit the network")
}

// TestResolveSyncOnMissFetches: a cold miss with SyncOnMiss fetches synchronously
// and writes through so the next read is Fresh.
func TestResolveSyncOnMissFetches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &countingFetcher{refs: sampleRefs()}

	res := Resolve(f, testRemote, testRepo, "feature-x", Opts{TTL: time.Hour, SyncOnMiss: true})
	require.NoError(t, res.Err)
	assert.Equal(t, Fresh, res.State)
	require.Len(t, res.PRs, 1)
	assert.Equal(t, 1, f.count())

	cached := Read(testRemote, testRepo, "feature-x", time.Hour)
	assert.Equal(t, Fresh, cached.State)
	require.Len(t, cached.PRs, 1)
}

// TestResolveMissKicksBackgroundRefresh: a cold miss without SyncOnMiss returns
// empty immediately and kicks exactly one background refresh.
func TestResolveMissKicksBackgroundRefresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	n := stubRefresh(t)
	f := &countingFetcher{refs: sampleRefs()}

	res := Resolve(f, testRemote, testRepo, "feature-x", Opts{TTL: time.Hour, RefreshStale: true})
	require.NoError(t, res.Err)
	assert.Equal(t, Miss, res.State)
	assert.Nil(t, res.PRs)
	assert.Equal(t, 0, f.count(), "hot path never fetches synchronously")
	assert.Equal(t, int32(1), atomic.LoadInt32(n), "a background refresh should be kicked")
}

// TestResolveStaleServesAndKicks: a stale entry is served instantly and a
// background refresh is kicked without any synchronous fetch.
func TestResolveStaleServesAndKicks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Write(testRemote, testRepo, "feature-x", sampleRefs()))
	n := stubRefresh(t)
	f := &countingFetcher{refs: sampleRefs()}

	// TTL 0 makes the just-written entry stale.
	res := Resolve(f, testRemote, testRepo, "feature-x", Opts{TTL: 0, RefreshStale: true})
	require.NoError(t, res.Err)
	assert.Equal(t, Stale, res.State)
	require.Len(t, res.PRs, 1)
	assert.Equal(t, 0, f.count(), "stale hot path never blocks on the network")
	assert.Equal(t, int32(1), atomic.LoadInt32(n))
}

// TestResolveSynchronousBypassesCache: Synchronous fetches fresh data even when a
// (stale or fresh) entry exists, and writes it through.
func TestResolveSynchronousBypassesCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Write(testRemote, testRepo, "feature-x", sampleRefs())) // #7

	newRefs := []models.PRRef{{Number: 99, State: "open", URL: "u"}}
	f := &countingFetcher{refs: newRefs}
	res := Resolve(f, testRemote, testRepo, "feature-x", Opts{Synchronous: true})
	require.NoError(t, res.Err)
	assert.Equal(t, Fresh, res.State)
	require.Len(t, res.PRs, 1)
	assert.Equal(t, 99, res.PRs[0].Number)
	assert.Equal(t, 1, f.count())

	cached := Read(testRemote, testRepo, "feature-x", time.Hour)
	require.Len(t, cached.PRs, 1)
	assert.Equal(t, 99, cached.PRs[0].Number, "cache should be overwritten with fresh data")
}

// TestResolveFetchErrorLeavesCache: a fetch error surfaces and does not clobber
// the existing entry.
func TestResolveFetchErrorLeavesCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Write(testRemote, testRepo, "feature-x", sampleRefs()))

	f := &countingFetcher{err: errors.New("boom")}
	res := Resolve(f, testRemote, testRepo, "feature-x", Opts{Synchronous: true})
	require.Error(t, res.Err)
	require.Len(t, res.PRs, 1, "the failed fetch must still serve last-known-good refs")

	cached := Read(testRemote, testRepo, "feature-x", time.Hour)
	assert.Equal(t, Fresh, cached.State)
	require.Len(t, cached.PRs, 1, "failed fetch must not wipe the cached entry")
	assert.EqualError(t, cached.Err, "boom", "the failure is recorded for the next process")
}

// TestInvalidateRemovesEntry: invalidation drops the entry so the next read is a
// Miss, and is idempotent for an absent entry.
func TestInvalidateRemovesEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Write(testRemote, testRepo, "gone", sampleRefs()))
	require.Equal(t, Fresh, Read(testRemote, testRepo, "gone", time.Hour).State)

	require.NoError(t, Invalidate(testRemote, testRepo, "gone"))
	assert.Equal(t, Miss, Read(testRemote, testRepo, "gone", time.Hour).State)

	assert.NoError(t, Invalidate(testRemote, testRepo, "gone"), "invalidating an absent entry is not an error")
}

// TestLockRefreshSingleWinnerConcurrent: under concurrency exactly one caller
// acquires the lease for a fresh key (at-most-one-refresh acceptance criterion).
func TestLockRefreshSingleWinnerConcurrent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const n = 32
	var wins int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if LockRefresh(testRemote, testRepo, "concurrent", time.Hour) {
				atomic.AddInt64(&wins, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	assert.Equal(t, int64(1), wins, "exactly one caller should win the refresh lease")
}

// TestWriteConcurrentAtomic: many concurrent writers never corrupt the entry or
// leave a temp file behind (atomic-write acceptance criterion).
func TestWriteConcurrentAtomic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const n = 32
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = Write(testRemote, testRepo, "race", []models.PRRef{{Number: i, State: "open"}})
		}(i)
	}
	wg.Wait()

	res := Read(testRemote, testRepo, "race", time.Hour)
	require.NotEqual(t, Miss, res.State, "final entry must be readable valid JSON")
	require.Len(t, res.PRs, 1, "final entry is one complete writer's content, never a mix")

	dir := filepath.Join(home, ".wgo", "cache", "pr", "acme-widgets")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp", "no temp file should survive concurrent writes")
	}
}

// TestResolveStaleServesGoodDataWithRecordedError: the common WGO-137 shape —
// a background refresh failed in another process, leaving good-but-stale refs
// annotated with the error. Resolve serves both, so `wgo .` can still print the
// PR link and say why it is old.
func TestResolveStaleServesGoodDataWithRecordedError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Write(testRemote, testRepo, "feature-x", sampleRefs()))
	require.NoError(t, WriteFailure(testRemote, testRepo, "feature-x", errors.New("503 bad gateway")))
	stubRefresh(t)
	f := &countingFetcher{refs: sampleRefs()}

	res := Resolve(f, testRemote, testRepo, "feature-x", Opts{TTL: 0, RefreshStale: true})
	assert.Equal(t, Stale, res.State)
	require.Len(t, res.PRs, 1, "last-known-good refs survive the failure")
	assert.EqualError(t, res.Err, "503 bad gateway")
	assert.Equal(t, 0, f.count(), "the hot path still never blocks")
}

// TestResolveSyncOnMissSkipsRecentFailure: with no good data but a failure
// recorded inside the TTL, SyncOnMiss must not re-fetch. Otherwise a GitHub
// outage turns every `wgo .` into a blocking call that will just fail again.
func TestResolveSyncOnMissSkipsRecentFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, WriteFailure(testRemote, testRepo, "feature-x", errors.New("boom")))
	f := &countingFetcher{refs: sampleRefs()}

	res := Resolve(f, testRemote, testRepo, "feature-x", Opts{TTL: time.Hour, SyncOnMiss: true})
	assert.Equal(t, Miss, res.State)
	assert.EqualError(t, res.Err, "boom")
	assert.Equal(t, 0, f.count(), "a failure inside the TTL is served, not retried")
}

// TestResolveSyncOnMissRetriesOldFailure: once the recorded failure ages past
// the TTL, SyncOnMiss tries again — the backoff must not become permanent.
func TestResolveSyncOnMissRetriesOldFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, writeEntry(testRemote, testRepo, "feature-x", entry{
		LastAttemptAt: time.Now().Add(-time.Hour),
		LastError:     "boom",
	}))
	f := &countingFetcher{refs: sampleRefs()}

	res := Resolve(f, testRemote, testRepo, "feature-x", Opts{TTL: time.Minute, SyncOnMiss: true})
	require.NoError(t, res.Err)
	assert.Equal(t, Fresh, res.State)
	require.Len(t, res.PRs, 1)
	assert.Equal(t, 1, f.count())
}
