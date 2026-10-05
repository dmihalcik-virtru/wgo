package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/store"
)

// hookSessionID is the session_id in the recorded payloads under
// testdata/hooks.
const hookSessionID = "3f0e8c7a-1b2c-4d5e-8f90-123456789abc"

func runHookFixture(t *testing.T, name, event string) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "hooks", name))
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, runAgentHook(f, event, "claude"))
}

// TestAgentHookLifecycle replays recorded Claude Code payloads: PostToolUse
// creates a working session in the workspace containing cwd, with the claude
// process identity; Stop marks it waiting; SessionEnd removes it.
func TestAgentHookLifecycle(t *testing.T) {
	e := newAgentEnv(t)

	runHookFixture(t, "post_tool_use.json", "PostToolUse")
	sess := loadState(t).GetAgentSession(hookSessionID)
	require.NotNil(t, sess)
	assert.Equal(t, store.AgentWorking, sess.Status)
	assert.Equal(t, store.SourceHook, sess.Source)
	assert.Equal(t, "claude", sess.Tool)
	assert.Equal(t, "/ws", sess.WorktreePath, "cwd in a subdirectory resolves to the workspace root")
	assert.Equal(t, "feat", sess.Branch)
	assert.Equal(t, fakeClaudePID, sess.PID, "the claude process, not the hook's shell")
	assert.Equal(t, fakeClaudeTok, sess.ProcStart)

	e.advance(time.Second)
	runHookFixture(t, "stop.json", "Stop")
	sess = loadState(t).GetAgentSession(hookSessionID)
	require.NotNil(t, sess)
	assert.Equal(t, store.AgentWaiting, sess.Status)

	// The event can come from the payload when no argument is given.
	e.advance(time.Second)
	runHookFixture(t, "post_tool_use.json", "")
	assert.Equal(t, store.AgentWorking, loadState(t).GetAgentSession(hookSessionID).Status)

	runHookFixture(t, "session_end.json", "SessionEnd")
	assert.Nil(t, loadState(t).GetAgentSession(hookSessionID))
}

// TestAgentHookThrottlesRepeatedToolUse: repeated PostToolUse events with no
// status change inside the throttle window do not rewrite state.
func TestAgentHookThrottlesRepeatedToolUse(t *testing.T) {
	e := newAgentEnv(t)
	runHookFixture(t, "post_tool_use.json", "PostToolUse")
	first := loadState(t).GetAgentSession(hookSessionID).LastActivity

	e.advance(5 * time.Second)
	runHookFixture(t, "post_tool_use.json", "PostToolUse")
	assert.Equal(t, first, loadState(t).GetAgentSession(hookSessionID).LastActivity)

	e.advance(heartbeatThrottle)
	runHookFixture(t, "post_tool_use.json", "PostToolUse")
	assert.Equal(t, e.now, loadState(t).GetAgentSession(hookSessionID).LastActivity.UTC())
}

// TestAgentHookMalformedInput: unusable input is an error; usable input that
// wgo cannot act on (cwd outside a jj workspace) exits cleanly.
func TestAgentHookMalformedInput(t *testing.T) {
	newAgentEnv(t)
	for name, in := range map[string]string{
		"empty":        "",
		"not json":     "PostToolUse",
		"truncated":    `{"session_id": "abc`,
		"no session":   `{"hook_event_name":"PostToolUse","cwd":"/ws"}`,
		"shell in id":  `{"session_id":"x; rm -rf ~","hook_event_name":"Stop","cwd":"/ws"}`,
		"wrong type":   `{"session_id": 42, "cwd": "/ws"}`,
		"no event":     `{"session_id":"abc","cwd":"/ws"}`,
		"array":        `[{"session_id":"abc"}]`,
		"empty object": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, runAgentHook(strings.NewReader(in), "", "claude"))
		})
	}
	assert.Empty(t, loadState(t).AgentSessions)

	// Unknown fields are ignored, and a cwd outside any workspace is skipped
	// without failing the hook.
	require.NoError(t, runAgentHook(strings.NewReader(
		`{"session_id":"abc","hook_event_name":"PostToolUse","cwd":"/not/jj","extra":{"x":[1,2]}}`), "", "claude"))
	assert.Empty(t, loadState(t).AgentSessions)
}

func TestHookActionFor(t *testing.T) {
	assert.Equal(t, store.AgentWorking, hookActionFor("PostToolUse").status)
	assert.Equal(t, store.AgentWorking, hookActionFor("posttooluse").status)
	assert.Equal(t, store.AgentWaiting, hookActionFor("Stop").status)
	assert.True(t, hookActionFor("SessionEnd").remove)
	assert.Equal(t, hookAction{}, hookActionFor("SomethingNew"))
}
