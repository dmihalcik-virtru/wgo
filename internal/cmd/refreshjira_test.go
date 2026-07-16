package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/jiracache"
)

// TestRunRefreshJiraWarmsCache: the background warmer fetches once and writes
// the result through the cache, so a subsequent hot-path (LocalOnly) read is
// served from disk without another fetch.
func TestRunRefreshJiraWarmsCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &stubJiraFetcher{info: jiracache.Info{Status: "In Review", Assignee: "Dana"}}
	installJiraFetcher(t, f)

	require.NoError(t, runRefreshJira("WGO-1"))
	require.Equal(t, 1, f.count(), "warmer should fetch exactly once")

	// LocalOnly never fetches synchronously; a warmed entry is served from cache.
	status, assignee, _ := resolveJiraStatus("WGO-1", contextOptions{LocalOnly: true})
	assert.Equal(t, "In Review", status)
	assert.Equal(t, "Dana", assignee)
	assert.Equal(t, 1, f.count(), "the cached read must not trigger another fetch")
}

// TestRunRefreshJiraEmptyTicketNoOp: an empty ticket short-circuits before any
// fetch.
func TestRunRefreshJiraEmptyTicketNoOp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &stubJiraFetcher{info: jiracache.Info{Status: "In Review"}}
	installJiraFetcher(t, f)

	require.NoError(t, runRefreshJira(""))
	assert.Equal(t, 0, f.count(), "an empty ticket must not fetch")
}
