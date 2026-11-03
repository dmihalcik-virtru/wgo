package jiracache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubSprints struct {
	data  BoardSprints
	err   error
	calls int
}

func (s *stubSprints) FetchSprints(board int, assignees []string) (BoardSprints, error) {
	s.calls++
	return s.data, s.err
}

func TestSprintsCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	assert.Equal(t, Miss, ReadSprints(42, SprintTTL).State)

	f := &stubSprints{data: BoardSprints{Sprints: []SprintEntry{
		{ID: 1234, Name: "Sprint 12", State: "active", Goal: "Ship it", Tickets: []SprintTicket{{Key: "TEAM-1", Info: Info{Status: "To Do"}}}},
		{ID: 1235, Name: "Sprint 13", State: "future"},
	}}}
	require.NoError(t, RefreshSprints(f, 42, []string{"currentUser()", "acct-bob", "acct-bob"}))
	_, err := os.Stat(filepath.Join(home, ".wgo", "cache", "jira", "sprints", "board-42.json"))
	require.NoError(t, err)

	r := ReadSprints(42, SprintTTL)
	assert.Equal(t, Fresh, r.State)
	assert.False(t, r.Failed)
	assert.Equal(t, 42, r.Data.Board)
	assert.Equal(t, []string{"acct-bob", "currentUser()"}, r.Data.Assignees, "sorted and deduplicated")
	require.Len(t, r.Data.Sprints, 2)
	assert.Equal(t, "Ship it", r.Data.Sprints[0].Goal)
	assert.Nil(t, r.Data.Sprints[1].Tickets, "a future sprint is not searched")
	assert.Equal(t, Stale, ReadSprints(42, 0).State)

	// A failed refresh keeps the last good sprints.
	f.err = errors.New("unauthorized")
	assert.Error(t, RefreshSprints(f, 42, nil))
	r = ReadSprints(42, SprintTTL)
	assert.False(t, r.Failed)
	assert.Len(t, r.Data.Sprints, 2)

	// A board with nothing cached gets a short-lived negative entry.
	assert.Error(t, RefreshSprints(f, 7, nil))
	r = ReadSprints(7, SprintTTL)
	assert.True(t, r.Failed)
	assert.Equal(t, Fresh, r.State)
	assert.Contains(t, r.Err, "unauthorized")
	assert.Empty(t, r.Data.Sprints)
}

// TestRefreshSprintsReportsFailedSearches stores a sprint whose ticket
// search failed, and reports it.
func TestRefreshSprintsReportsFailedSearches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &stubSprints{data: BoardSprints{Sprints: []SprintEntry{{ID: 1234, Name: "Sprint 12", State: "active", Error: "search issues: exit status 1"}}}}
	err := RefreshSprints(f, 42, []string{"currentUser()"})
	assert.ErrorContains(t, err, "sprint 1234 ticket search")
	r := ReadSprints(42, SprintTTL)
	require.Len(t, r.Data.Sprints, 1)
	assert.Equal(t, "search issues: exit status 1", r.Data.Sprints[0].Error)
}

func TestLockSprintsRefreshBacksOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	assert.True(t, LockSprintsRefresh(42, time.Hour))
	assert.False(t, LockSprintsRefresh(42, time.Hour))
	assert.True(t, LockSprintsRefresh(43, time.Hour), "leases are per board")
}
