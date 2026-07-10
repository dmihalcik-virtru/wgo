package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/store"
)

// TestDetectAgent gates on the CLAUDECODE env var.
func TestDetectAgent(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	assert.Equal(t, "claude", detectAgent())

	t.Setenv("CLAUDECODE", "")
	assert.Equal(t, "", detectAgent())
}

// TestHeartbeatAgentWritesThenThrottles: the first hot-path heartbeat records a
// session; a second within the throttle window does not rewrite it.
func TestHeartbeatAgentWritesThenThrottles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDECODE", "1")

	heartbeatAgent("/ws", "WGO-134")

	agent := resolveAgent("/ws")
	require.NotNil(t, agent)
	assert.Equal(t, "claude", agent.Name)

	s, err := store.New()
	require.NoError(t, err)
	state, err := s.LoadState()
	require.NoError(t, err)
	firstSeen := state.AgentSessions["/ws"].LastActivity

	// Second heartbeat within the throttle window: no rewrite.
	heartbeatAgent("/ws", "WGO-134")
	state, err = s.LoadState()
	require.NoError(t, err)
	assert.Equal(t, firstSeen, state.AgentSessions["/ws"].LastActivity,
		"a heartbeat within the throttle window must not rewrite the session")
}

// TestHeartbeatAgentNoEnvIsNoOp: without an agent env, nothing is recorded.
func TestHeartbeatAgentNoEnvIsNoOp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDECODE", "")

	heartbeatAgent("/ws", "WGO-134")
	assert.Nil(t, resolveAgent("/ws"))
}

// TestResolveAgentStaleReturnsNil: a session past the staleness window is not
// surfaced.
func TestResolveAgentStaleReturnsNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s, err := store.New()
	require.NoError(t, err)
	state, err := s.LoadState()
	require.NoError(t, err)
	old := time.Now().Add(-2 * agentStaleAfter)
	state.AgentSessions["/ws"] = store.AgentSession{
		Tool: "claude", WorktreePath: "/ws", StartTime: old, LastActivity: old,
	}
	require.NoError(t, s.SaveState(state))

	assert.Nil(t, resolveAgent("/ws"), "a stale session must not be surfaced")
}
