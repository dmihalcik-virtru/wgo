package sync_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/virtru/wgo/internal/jjtest"
	wgosync "github.com/virtru/wgo/internal/sync"
)

// TestDAGRevset_ParsesAgainstRealJJ is the revset-drift canary, the sibling of
// internal/jj's TestSmokeTemplate. DAGRevset reaches jj only as a string, so a
// revset-function rename upstream (`heads()` → `visible_heads()`) compiles
// fine, passes every mocked test, and then fails on the first real repo. This
// test runs the revset through the installed jj binary.
func TestDAGRevset_ParsesAgainstRealJJ(t *testing.T) {
	jjtest.RequireJJ(t)
	repo, c := jjtest.NewRepo(t)
	jjtest.Commit(t, repo, "seed", map[string]string{"a.txt": "hello"})

	entries, err := c.Log(repo, wgosync.DAGRevset)
	require.NoError(t, err, "jj rejected DAGRevset %q", wgosync.DAGRevset)

	// The seed commit carries no bookmark, so the result may legitimately be
	// empty; what matters is that jj parsed and evaluated the revset. Bookmark
	// it and re-run to prove the revset actually selects bookmarked changes.
	require.NoError(t, c.BookmarkSet(repo, "feature", "@-", false))
	entries, err = c.Log(repo, wgosync.DAGRevset)
	require.NoError(t, err)

	var found bool
	for _, e := range entries {
		for _, b := range e.Bookmarks {
			if b == "feature" {
				found = true
			}
		}
	}
	assert.True(t, found, "DAGRevset did not select the bookmarked change: %+v", entries)
}
