package jiracache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testTicket = "WGO-134"

func sampleInfo() Info {
	return Info{Status: "In Review", Assignee: "Alice Dev"}
}

// TestWriteReadFresh round-trips info and reports Fresh within the TTL.
func TestWriteReadFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, Write(testTicket, sampleInfo()))

	info, state := Read(testTicket, time.Hour)
	assert.Equal(t, Fresh, state)
	assert.Equal(t, "In Review", info.Status)
	assert.Equal(t, "Alice Dev", info.Assignee)
}

// TestReadStale returns the cached entry but flags it Stale past the TTL.
func TestReadStale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, Write(testTicket, sampleInfo()))

	info, state := Read(testTicket, 0)
	assert.Equal(t, Stale, state)
	assert.Equal(t, "In Review", info.Status)
}

// TestReadMiss returns Miss for an absent entry.
func TestReadMiss(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	info, state := Read("NEVER-1", time.Hour)
	assert.Equal(t, Miss, state)
	assert.Equal(t, Info{}, info)
}

// TestNegativeCache caches an empty status as a valid hit so a ticket with no
// mappable status is not re-fetched every render.
func TestNegativeCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	require.NoError(t, Write(testTicket, Info{}))

	info, state := Read(testTicket, time.Hour)
	assert.Equal(t, Fresh, state)
	assert.Equal(t, Info{}, info)
}

// TestWriteAtomicNoTemp ensures a completed write leaves no .tmp file behind.
func TestWriteAtomicNoTemp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	require.NoError(t, Write(testTicket, sampleInfo()))

	dir := filepath.Join(home, ".wgo", "cache", "jira")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp", "temp file left behind")
	}
}

// TestTicketPath verifies the on-disk path is keyed by ticket alone.
func TestTicketPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	require.NoError(t, Write(testTicket, sampleInfo()))

	path := filepath.Join(home, ".wgo", "cache", "jira", "WGO-134.json")
	_, err := os.Stat(path)
	assert.NoError(t, err)
}

// TestLockRefreshBacksOff allows the first refresh then suppresses a second
// within the back-off window, but always allows one when the window is zero.
func TestLockRefreshBacksOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	assert.True(t, LockRefresh(testTicket, time.Hour), "first attempt should win")
	assert.False(t, LockRefresh(testTicket, time.Hour), "second attempt within window should back off")
	assert.True(t, LockRefresh(testTicket, 0), "zero window always allows")
}
