package prcache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/models"
)

// TestEmptyReviewerListIsKnownNone: a schema-2 entry whose PR has no requested
// reviewers reports ReviewersKnown with an empty list ("none requested"),
// while a pre-schema-2 entry reports ReviewersKnown false ("unknown"). The
// field is omitempty on disk, so the empty list reads back as nil: callers must
// consult ReviewersKnown, never the slice, to tell the two apart.
func TestEmptyReviewerListIsKnownNone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	refs := []models.PRRef{{Number: 8, State: "open", RequestedReviewers: []string{}}}
	require.NoError(t, Write(testRemote, testRepo, "known", refs))

	res := Read(testRemote, testRepo, "known", time.Hour)
	require.Len(t, res.PRs, 1)
	assert.True(t, res.ReviewersKnown)
	assert.Len(t, res.PRs[0].RequestedReviewers, 0)

	path, err := prPath(testRemote, testRepo, "old")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	old := `{"schema": 1, "prs": [{"number": 8, "state": "open"}], "fetched_at": "` +
		time.Now().Format(time.RFC3339Nano) + `"}`
	require.NoError(t, os.WriteFile(path, []byte(old), 0o644))

	res = Read(testRemote, testRepo, "old", time.Hour)
	require.Len(t, res.PRs, 1)
	assert.False(t, res.ReviewersKnown)
	assert.Len(t, res.PRs[0].RequestedReviewers, 0)
}
