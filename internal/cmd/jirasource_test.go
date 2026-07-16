package cmd

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/virtru/wgo/internal/jiracache"
)

// stubJiraFetcher is a jiracache.Fetcher that returns canned data and counts
// calls, so tests can drive resolveJiraStatus/runRefreshJira without acli.
type stubJiraFetcher struct {
	info  jiracache.Info
	err   error
	calls int32
}

func (f *stubJiraFetcher) FetchJira(_ string) (jiracache.Info, error) {
	atomic.AddInt32(&f.calls, 1)
	return f.info, f.err
}

func (f *stubJiraFetcher) count() int { return int(atomic.LoadInt32(&f.calls)) }

// installJiraFetcher swaps the package fetcher seam for the duration of a test.
func installJiraFetcher(t *testing.T, f jiracache.Fetcher) {
	t.Helper()
	old := jiraFetcherFn
	jiraFetcherFn = func() jiracache.Fetcher { return f }
	t.Cleanup(func() { jiraFetcherFn = old })
}

// TestResolveJiraStatusSkipsNonJira: empty and GitHub-issue tickets never touch
// the cache or acli — they return empty immediately.
func TestResolveJiraStatusSkipsNonJira(t *testing.T) {
	status, assignee, site := resolveJiraStatus("", contextOptions{LocalOnly: true})
	assert.Empty(t, status)
	assert.Empty(t, assignee)
	assert.Empty(t, site)

	status, assignee, site = resolveJiraStatus("GH-9", contextOptions{LocalOnly: true})
	assert.Empty(t, status)
	assert.Empty(t, assignee)
	assert.Empty(t, site)
}

// TestJiraCacheOpts maps context options onto cache behavior, mirroring the PR
// cache: statusline (LocalOnly) never fetches synchronously.
func TestJiraCacheOpts(t *testing.T) {
	assert.True(t, jiraCacheOpts(contextOptions{Refresh: true}).Synchronous)

	local := jiraCacheOpts(contextOptions{LocalOnly: true})
	assert.True(t, local.RefreshStale)
	assert.False(t, local.SyncOnMiss, "statusline hot path must not fetch synchronously")
	assert.False(t, local.Synchronous)

	def := jiraCacheOpts(contextOptions{})
	assert.True(t, def.RefreshStale)
	assert.True(t, def.SyncOnMiss)
}

// TestJiraTTLFallback returns the 10m default when config is unset.
func TestJiraTTLFallback(t *testing.T) {
	assert.Equal(t, 600*time.Second, jiraTTL())
}

// TestResolveJiraStatusDegradesOnFetchError: when the live lookup fails (no
// acli / not authenticated), resolveJiraStatus swallows the error and returns
// empty strings so wgo . degrades cleanly rather than erroring.
func TestResolveJiraStatusDegradesOnFetchError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &stubJiraFetcher{err: errors.New("acli unavailable")}
	installJiraFetcher(t, f)

	// Default opts fetch synchronously on a cold miss, exercising the error path.
	status, assignee, site := resolveJiraStatus("WGO-1", contextOptions{})
	assert.Empty(t, status)
	assert.Empty(t, assignee)
	assert.Empty(t, site)
	assert.Equal(t, 1, f.count(), "the fetcher should have been consulted once")
}
