package jiracache

import (
	"context"
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

// TestFailedSearchKeepsTheSprintsTickets: a sprint whose search fails keeps
// the tickets of its last good search, stamped with when that ran, and the
// entry is retried after negativeTTL rather than the full SprintTTL.
func TestFailedSearchKeepsTheSprintsTickets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	good := BoardSprints{Sprints: []SprintEntry{{ID: 1234, Name: "Sprint 12", State: "active", Truncated: true,
		Tickets: []SprintTicket{{Key: "TEAM-1", Info: Info{Status: "To Do"}}}}}}
	require.NoError(t, RefreshSprints(&stubSprints{data: good}, 42, nil))
	first := ReadSprints(42, SprintTTL).FetchedAt

	failed := BoardSprints{Sprints: []SprintEntry{{ID: 1234, Name: "Sprint 12", State: "active", Error: "search issues: exit status 1"}}}
	require.Error(t, RefreshSprints(&stubSprints{data: failed}, 42, nil))
	r := ReadSprints(42, SprintTTL)
	require.Len(t, r.Data.Sprints, 1)
	s := r.Data.Sprints[0]
	assert.Equal(t, "search issues: exit status 1", s.Error)
	assert.Equal(t, []SprintTicket{{Key: "TEAM-1", Info: Info{Status: "To Do"}}}, s.Tickets, "the last good tickets are kept")
	assert.True(t, s.Truncated)
	assert.True(t, s.TicketsFetchedAt.Equal(first), "stamped with the search that found them")

	// A second failure keeps the original stamp.
	require.Error(t, RefreshSprints(&stubSprints{data: failed}, 42, nil))
	assert.True(t, ReadSprints(42, SprintTTL).Data.Sprints[0].TicketsFetchedAt.Equal(first))

	// Retried soon: stale after negativeTTL although within SprintTTL.
	e := sprintsEntry{Data: ReadSprints(42, SprintTTL).Data, FetchedAt: time.Now().Add(-negativeTTL - time.Second)}
	require.NoError(t, writeSprints(42, e))
	assert.Equal(t, Stale, ReadSprints(42, SprintTTL).State)
	e.Data.Sprints[0].Error = ""
	require.NoError(t, writeSprints(42, e))
	assert.Equal(t, Fresh, ReadSprints(42, SprintTTL).State, "a clean entry lives its full TTL")
}

// killedSearch is a SprintContextFetcher whose sprint list worked but whose
// ticket search acli was killed in when the context ended: it returns the
// board with the search's failure on the sprint, and no error.
type killedSearch struct{ cancel context.CancelFunc }

func (k killedSearch) FetchSprints(int, []string) (BoardSprints, error) {
	panic("FetchSprintsContext is preferred")
}

func (k killedSearch) FetchSprintsContext(_ context.Context, board int, _ []string) (BoardSprints, error) {
	k.cancel()
	return BoardSprints{Board: board, Sprints: []SprintEntry{{ID: 7, Name: "Sprint 7", State: "active", Error: "search issues: signal: killed"}}}, nil
}

// TestRefreshSprintsCutShortRecordsNothing: a refresh whose context ended
// writes nothing, even with data in hand, so a killed search is never served
// as a failed one.
func TestRefreshSprintsCutShortRecordsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	err := RefreshSprintsContext(ctx, killedSearch{cancel: cancel}, 42, nil)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, Miss, ReadSprints(42, SprintTTL).State, "nothing cached")

	// With a good entry cached, it stays as it was.
	require.NoError(t, RefreshSprints(&stubSprints{data: BoardSprints{Sprints: []SprintEntry{{ID: 7, Name: "Sprint 7", State: "active"}}}}, 42, nil))
	before := ReadSprints(42, SprintTTL)
	ctx, cancel = context.WithCancel(context.Background())
	_ = RefreshSprintsContext(ctx, killedSearch{cancel: cancel}, 42, nil)
	after := ReadSprints(42, SprintTTL)
	assert.Equal(t, before.FetchedAt, after.FetchedAt)
	assert.Empty(t, after.Data.Sprints[0].Error)
}
