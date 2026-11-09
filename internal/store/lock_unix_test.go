//go:build unix

package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMutateStateWithinGivesUp: while another holder keeps the state lock,
// MutateStateWithin returns ErrStateLocked after its timeout without running
// fn, and succeeds once the lock is released. flock locks belong to an open
// file description, so a second open in this process contends like another
// wgo would.
func TestMutateStateWithinGivesUp(t *testing.T) {
	s := NewWithDir(t.TempDir())
	require.NoError(t, s.EnsureDir())
	unlock, err := acquireStateLock(s.lockPath(), 0)
	require.NoError(t, err)

	called := false
	start := time.Now()
	err = s.MutateStateWithin(50*time.Millisecond, func(*State) (bool, error) {
		called = true
		return true, nil
	})
	assert.ErrorIs(t, err, ErrStateLocked)
	assert.Contains(t, err.Error(), s.lockPath())
	assert.False(t, called, "fn must not run without the lock")
	assert.Less(t, time.Since(start), 2*time.Second)

	unlock()
	require.NoError(t, s.MutateStateWithin(50*time.Millisecond, func(st *State) (bool, error) {
		st.AddRepo("/a", "")
		return true, nil
	}))
	state, err := s.LoadState()
	require.NoError(t, err)
	assert.NotNil(t, state.GetRepo("/a"))
}
