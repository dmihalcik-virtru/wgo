package cmd

import (
	"bytes"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/store"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it
// printed. Used to assert on command output that goes to stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()
	require.NoError(t, w.Close())

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	return buf.String()
}

// seedAgentSession writes a non-stale agent session directly to state so the
// command wrappers can be exercised without a live jj workspace.
func seedAgentSession(t *testing.T, wsRoot, tool, branch string) {
	t.Helper()
	s, err := store.New()
	require.NoError(t, err)
	state, err := s.LoadState()
	require.NoError(t, err)
	now := time.Now()
	state.AgentSessions[wsRoot] = store.AgentSession{
		Tool: tool, WorktreePath: wsRoot, Branch: branch, StartTime: now, LastActivity: now,
	}
	require.NoError(t, s.SaveState(state))
}

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

// TestRunAgentStartRejectsEmptyName: start validates the name before touching
// the workspace or state, so a blank name is a clear error, not a nameless 🤖.
func TestRunAgentStartRejectsEmptyName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	assert.Error(t, runAgentStart(""))
	assert.Error(t, runAgentStart("   "), "a whitespace-only name is also empty")
}

// TestRunAgentStatusEmpty: with no sessions, status reports the empty state.
func TestRunAgentStatusEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	out := captureStdout(t, func() {
		require.NoError(t, runAgentStatus())
	})
	assert.Contains(t, out, "No active agent sessions.")
}

// TestRunAgentStatusListsSessionsSorted: status lists active sessions sorted by
// workspace root and falls back to "(no bookmark)" when a session has no branch.
func TestRunAgentStatusListsSessionsSorted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Roots that cannot match the test process's real workspace, so no session
	// is marked "(current)" regardless of where the test runs.
	seedAgentSession(t, "/tmp/wgo-ws-b", "codex", "")
	seedAgentSession(t, "/tmp/wgo-ws-a", "claude", "WGO-134")

	out := captureStdout(t, func() {
		require.NoError(t, runAgentStatus())
	})

	assert.Contains(t, out, "claude")
	assert.Contains(t, out, "WGO-134")
	assert.Contains(t, out, "(no bookmark)", "a session without a branch shows the fallback")
	assert.NotContains(t, out, "(current)")
	assert.Less(t, indexOf(out, "/tmp/wgo-ws-a"), indexOf(out, "/tmp/wgo-ws-b"),
		"sessions are listed sorted by workspace root")
}

// indexOf is a tiny helper for asserting relative order in captured output.
func indexOf(haystack, needle string) int {
	return bytes.Index([]byte(haystack), []byte(needle))
}
