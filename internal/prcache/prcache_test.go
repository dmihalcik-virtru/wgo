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

	refs, state := Read(testRemote, testRepo, "feature-x", time.Hour)
	assert.Equal(t, Fresh, state)
	require.Len(t, refs, 1)
	assert.Equal(t, 7, refs[0].Number)
	assert.Equal(t, "open", refs[0].State)
}

// TestReadStale returns the cached entry but flags it Stale past the TTL.
func TestReadStale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, Write(testRemote, testRepo, "feature-x", sampleRefs()))

	// A zero TTL makes any entry older than "now" stale.
	refs, state := Read(testRemote, testRepo, "feature-x", 0)
	assert.Equal(t, Stale, state)
	assert.Len(t, refs, 1)
}

// TestReadMiss returns Miss for an absent entry.
func TestReadMiss(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	refs, state := Read(testRemote, testRepo, "never-written", time.Hour)
	assert.Equal(t, Miss, state)
	assert.Nil(t, refs)
}

// TestNegativeCache caches a "no PRs" result as a valid hit so a branch with no
// PRs is not re-fetched every render.
func TestNegativeCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, Write(testRemote, testRepo, "feature-x", nil))

	refs, state := Read(testRemote, testRepo, "feature-x", time.Hour)
	assert.Equal(t, Fresh, state)
	assert.Empty(t, refs)
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

	refs, state := Read(testRemote, testRepo, "feat/foo/bar", time.Hour)
	assert.Equal(t, Fresh, state)
	assert.Len(t, refs, 1)
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
