package prcache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/models"
)

const (
	testRemote = "https://github.com/acme/widgets.git"
	testRepo   = "/tmp/acme/widgets"
)

func sampleRefs() []models.PRRef {
	return []models.PRRef{{Number: 7, Title: "Add widget", State: "open", URL: "https://github.com/acme/widgets/pull/7"}}
}

// TestWriteReadFresh round-trips refs and reports Fresh within the TTL.
func TestWriteReadFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, Write(testRemote, testRepo, "feature-x", sampleRefs()))

	res := Read(testRemote, testRepo, "feature-x", time.Hour)
	assert.Equal(t, Fresh, res.State)
	require.Len(t, res.PRs, 1)
	assert.Equal(t, 7, res.PRs[0].Number)
	assert.Equal(t, "open", res.PRs[0].State)
}

// TestWriteReadEnrichedFields round-trips the WGO-133 fields (review decision,
// draft, CI rollup) so the statusline hot path serves them from disk without a
// network call.
func TestWriteReadEnrichedFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	refs := []models.PRRef{{
		Number:         7,
		Title:          "Add widget",
		State:          "open",
		URL:            "https://github.com/acme/widgets/pull/7",
		ReviewDecision: "CHANGES_REQUESTED",
		IsDraft:        true,
		Checks: models.CIStatus{
			State: "failure", Passed: 2, Failed: 1, Total: 3,
			URL: "https://ci/job/1",
		},
	}}
	require.NoError(t, Write(testRemote, testRepo, "feature-x", refs))

	got := Read(testRemote, testRepo, "feature-x", time.Hour)
	assert.Equal(t, Fresh, got.State)
	require.Len(t, got.PRs, 1)
	assert.Equal(t, "CHANGES_REQUESTED", got.PRs[0].ReviewDecision)
	assert.True(t, got.PRs[0].IsDraft)
	assert.Equal(t, models.CIStatus{
		State: "failure", Passed: 2, Failed: 1, Total: 3, URL: "https://ci/job/1",
	}, got.PRs[0].Checks)
}

// TestReadStale returns the cached entry but flags it Stale past the TTL.
func TestReadStale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, Write(testRemote, testRepo, "feature-x", sampleRefs()))

	// A zero TTL makes any entry older than "now" stale.
	res := Read(testRemote, testRepo, "feature-x", 0)
	assert.Equal(t, Stale, res.State)
	assert.Len(t, res.PRs, 1)
}

// TestReadMiss returns Miss for an absent entry.
func TestReadMiss(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	res := Read(testRemote, testRepo, "never-written", time.Hour)
	assert.Equal(t, Miss, res.State)
	assert.Nil(t, res.PRs)
	assert.NoError(t, res.Err)
}

// TestNegativeCache caches a "no PRs" result as a valid hit so a branch with no
// PRs is not re-fetched every render.
func TestNegativeCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, Write(testRemote, testRepo, "feature-x", nil))

	res := Read(testRemote, testRepo, "feature-x", time.Hour)
	assert.Equal(t, Fresh, res.State)
	assert.Empty(t, res.PRs)
}

// TestWriteAtomicNoTemp ensures a completed write leaves no .tmp file behind.
func TestWriteAtomicNoTemp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	require.NoError(t, Write(testRemote, testRepo, "feature-x", sampleRefs()))

	dir := filepath.Join(home, ".wgo", "cache", "pr", "acme-widgets")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp", "temp file left behind")
	}
}

// TestSanitizedBranchPath verifies slashes in a branch map to a single safe
// file and round-trip correctly.
func TestSanitizedBranchPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	require.NoError(t, Write(testRemote, testRepo, "feat/foo/bar", sampleRefs()))

	// Slug from the GitHub remote is owner-repo; branch slashes become dashes.
	path := filepath.Join(home, ".wgo", "cache", "pr", "acme-widgets", "feat-foo-bar.json")
	_, err := os.Stat(path)
	assert.NoError(t, err)

	res := Read(testRemote, testRepo, "feat/foo/bar", time.Hour)
	assert.Equal(t, Fresh, res.State)
	assert.Len(t, res.PRs, 1)
}

// TestSlugFallsBackToRepoBase uses the repo's base directory name when the
// remote is not a GitHub URL.
func TestSlugFallsBackToRepoBase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	require.NoError(t, Write("git@gitlab.com:acme/thing.git", "/src/localrepo", "main", sampleRefs()))

	path := filepath.Join(home, ".wgo", "cache", "pr", "localrepo", "main.json")
	_, err := os.Stat(path)
	assert.NoError(t, err)
}

// TestLockRefreshBacksOff allows the first refresh then suppresses a second
// within the back-off window, but always allows one when the window is zero.
func TestLockRefreshBacksOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	assert.True(t, LockRefresh(testRemote, testRepo, "feature-x", time.Hour), "first attempt should win")
	assert.False(t, LockRefresh(testRemote, testRepo, "feature-x", time.Hour), "second attempt within window should back off")
	assert.True(t, LockRefresh(testRemote, testRepo, "feature-x", 0), "zero window always allows")
}

// TestWriteFailurePreservesGoodEntry: recording a failure must leave the last
// successful result byte-for-byte intact. This is the WGO-137 regression: a
// transient GitHub error used to be written through as "this branch has no
// PRs", hiding an open PR until something happened to refetch.
func TestWriteFailurePreservesGoodEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Write(testRemote, testRepo, "feature-x", sampleRefs()))

	before, ok := readEntry(testRemote, testRepo, "feature-x")
	require.True(t, ok)

	require.NoError(t, WriteFailure(testRemote, testRepo, "feature-x", errors.New("429 rate limited")))

	after := Read(testRemote, testRepo, "feature-x", time.Hour)
	assert.Equal(t, Fresh, after.State, "freshness still comes from the last success")
	require.Len(t, after.PRs, 1)
	assert.Equal(t, 7, after.PRs[0].Number)
	assert.True(t, before.FetchedAt.Equal(after.FetchedAt), "a failure must not restamp fetched_at")
	assert.EqualError(t, after.Err, "429 rate limited")
}

// TestWriteFailureWithNoPriorEntry: a failure with nothing cached records the
// error without inventing an empty PR list, so the entry reads as "unknown"
// rather than "no PRs".
func TestWriteFailureWithNoPriorEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, WriteFailure(testRemote, testRepo, "feature-x", errors.New("no GitHub credentials")))

	res := Read(testRemote, testRepo, "feature-x", time.Hour)
	assert.Equal(t, Miss, res.State, "an error-only entry is not data")
	assert.Nil(t, res.PRs)
	assert.True(t, res.FetchedAt.IsZero())
	assert.EqualError(t, res.Err, "no GitHub credentials")
}

// TestWriteClearsLastError: a later success supersedes the recorded failure, so
// the warning stops being shown once the lookup recovers.
func TestWriteClearsLastError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, WriteFailure(testRemote, testRepo, "feature-x", errors.New("boom")))
	require.NoError(t, Write(testRemote, testRepo, "feature-x", sampleRefs()))

	res := Read(testRemote, testRepo, "feature-x", time.Hour)
	assert.Equal(t, Fresh, res.State)
	assert.NoError(t, res.Err)
	require.Len(t, res.PRs, 1)
}

// TestReadPreWGO137Entry: entries written before the failure fields existed
// still load as an ordinary success.
func TestReadPreWGO137Entry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := prPath(testRemote, testRepo, "feature-x")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	legacy := `{"prs":[{"number":7,"title":"Add widget","state":"open","url":"u"}],` +
		`"fetched_at":"` + time.Now().Format(time.RFC3339Nano) + `"}`
	require.NoError(t, os.WriteFile(path, []byte(legacy), 0o600))

	res := Read(testRemote, testRepo, "feature-x", time.Hour)
	assert.Equal(t, Fresh, res.State)
	require.Len(t, res.PRs, 1)
	assert.Equal(t, 7, res.PRs[0].Number)
	assert.NoError(t, res.Err)
}
