package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestResolveJiraStatusSkipsNonJira: empty and GitHub-issue tickets never touch
// the cache or acli — they return empty immediately.
func TestResolveJiraStatusSkipsNonJira(t *testing.T) {
	status, assignee := resolveJiraStatus("", contextOptions{LocalOnly: true})
	assert.Empty(t, status)
	assert.Empty(t, assignee)

	status, assignee = resolveJiraStatus("GH-9", contextOptions{LocalOnly: true})
	assert.Empty(t, status)
	assert.Empty(t, assignee)
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
