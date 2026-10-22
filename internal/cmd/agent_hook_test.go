package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/proc"
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

// TestAgentHookResumedSessionFindsNewProcess: claude --resume keeps the
// session ID but runs a new process. The next event records the new process,
// so the session survives going quiet instead of being pruned (#61).
func TestAgentHookResumedSessionFindsNewProcess(t *testing.T) {
	e := newAgentEnv(t)
	runHookFixture(t, "stop.json", "Stop")
	require.Equal(t, fakeClaudePID, loadState(t).GetAgentSession(hookSessionID).PID)

	// The original claude exits; a resumed one takes over under a new PID.
	delete(e.procs, fakeClaudePID)
	e.procs[201] = proc.Info{PID: 201, PPID: 1, Name: "claude", Start: 515151}
	e.procs[fakeShellPID] = proc.Info{PID: fakeShellPID, PPID: 201, Name: "zsh", Start: 9}

	e.advance(2 * time.Minute)
	runHookFixture(t, "post_tool_use.json", "PostToolUse")
	sess := loadState(t).GetAgentSession(hookSessionID)
	require.NotNil(t, sess)
	assert.Equal(t, 201, sess.PID)
	assert.Equal(t, int64(515151), sess.ProcStart)

	e.advance(agentStaleAfter + time.Hour)
	startAgent(t, agentStartOpts{Tool: "codex", Session: "x"}) // prunes
	require.NotNil(t, loadState(t).GetAgentSession(hookSessionID), "a quiet resumed session stays live")
}

// TestAgentHookResumedUnfindableProcessIsUncertain: when the recorded process
// has exited and the walk finds no new one, the dead identity is dropped, so
// the quiet session is shown as uncertain instead of being pruned as exited.
func TestAgentHookResumedUnfindableProcessIsUncertain(t *testing.T) {
	e := newAgentEnv(t)
	runHookFixture(t, "stop.json", "Stop")
	require.Equal(t, fakeClaudePID, loadState(t).GetAgentSession(hookSessionID).PID)

	// The original claude exits; the resumed one runs under something the
	// walk does not recognize.
	delete(e.procs, fakeClaudePID)
	e.procs[fakeShellPID] = proc.Info{PID: fakeShellPID, PPID: 1, Name: "zsh", Start: 9}

	e.advance(2 * time.Minute)
	runHookFixture(t, "post_tool_use.json", "PostToolUse")
	sess := loadState(t).GetAgentSession(hookSessionID)
	require.NotNil(t, sess)
	assert.False(t, sess.HasProcess(), "the dead identity is not kept")

	e.advance(agentStaleAfter + time.Hour)
	startAgent(t, agentStartOpts{Tool: "codex", Session: "x"}) // prunes
	require.NotNil(t, loadState(t).GetAgentSession(hookSessionID), "a quiet session with no identity stays")
	assert.Contains(t, agentStatusOut(t), "uncertain")
}

// TestAgentHookAfterPruneRecreatesSession: a non-refresh event from a session
// that is prunable (quiet past the max age) keeps it, with its workspace,
// repo, bookmark and start time, rather than failing silently or recreating
// it without them.
func TestAgentHookAfterPruneRecreatesSession(t *testing.T) {
	e := newAgentEnv(t)
	e.noAgentProcess()
	runHookFixture(t, "stop.json", "Stop")
	before := loadState(t).GetAgentSession(hookSessionID)
	require.NotNil(t, before)
	e.advance(agentMaxAge + time.Hour) // past the max age: prunable

	runHookFixture(t, "post_tool_use.json", "PostToolUse")
	sess := loadState(t).GetAgentSession(hookSessionID)
	require.NotNil(t, sess)
	assert.Equal(t, "/ws", sess.WorktreePath)
	assert.Equal(t, "/ws", sess.RepoPath)
	assert.Equal(t, "feat", sess.Branch)
	assert.Equal(t, before.StartTime, sess.StartTime)
	assert.Equal(t, store.AgentWorking, sess.Status)
	assert.Empty(t, e.warnings.String())
}

// TestAgentHookDoesNotResurrectEndedSession: an event in flight when its
// session ends (here, SessionEnd lands while Stop resolves the workspace) does
// not bring the session back, though a later event, as from a resume, does.
func TestAgentHookDoesNotResurrectEndedSession(t *testing.T) {
	e := newAgentEnv(t)
	runHookFixture(t, "stop.json", "Stop")
	require.NotNil(t, loadState(t).GetAgentSession(hookSessionID))

	lookup := lookupWorkspace
	lookupWorkspace = func(dir string) (workspaceInfo, error) {
		runHookFixture(t, "session_end.json", "SessionEnd")
		return lookup(dir)
	}
	e.advance(time.Second)
	runHookFixture(t, "stop.json", "Stop")
	assert.Nil(t, loadState(t).GetAgentSession(hookSessionID), "a refresh event in flight does not resurrect it")
	assert.Empty(t, e.warnings.String())

	lookupWorkspace = lookup
	e.advance(time.Second)
	runHookFixture(t, "stop.json", "Stop")
	assert.NotNil(t, loadState(t).GetAgentSession(hookSessionID), "a resumed session is recorded again")
}

// TestAgentHookBookmark: a fresh lookup that finds no bookmark clears the
// recorded one; a failed bookmark lookup keeps it.
func TestAgentHookBookmark(t *testing.T) {
	e := newAgentEnv(t)
	runHookFixture(t, "stop.json", "Stop")
	require.Equal(t, "feat", loadState(t).GetAgentSession(hookSessionID).Branch)

	ws := e.workspaces["/ws"]
	ws.Branch, ws.BranchKnown = "", false
	e.workspaces["/ws"] = ws
	e.advance(time.Second)
	runHookFixture(t, "stop.json", "Stop")
	assert.Equal(t, "feat", loadState(t).GetAgentSession(hookSessionID).Branch, "a failed lookup is not \"no bookmark\"")

	ws.BranchKnown = true
	e.workspaces["/ws"] = ws
	e.advance(time.Second)
	runHookFixture(t, "stop.json", "Stop")
	assert.Empty(t, loadState(t).GetAgentSession(hookSessionID).Branch)
}

// hookWarning returns the systemMessage the hook wrote, checking that stdout
// held that one JSON object and nothing else, as Claude Code requires.
func hookWarning(t *testing.T, out string) string {
	t.Helper()
	var msg struct {
		SystemMessage string `json:"systemMessage"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &msg), "stdout is one JSON object: %q", out)
	return msg.SystemMessage
}

// TestAgentHookReportsStateProblems: a state file the hook cannot write is
// reported as a JSON systemMessage on stdout (still exit 0), only on an event
// whose message Claude Code shows, and at most hourly for the same problem;
// a cwd outside jj is skipped quietly.
func TestAgentHookReportsStateProblems(t *testing.T) {
	e := newAgentEnv(t)
	home, _ := os.UserHomeDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".wgo"), 0o755))
	path := filepath.Join(home, ".wgo", "state.json")
	require.NoError(t, os.WriteFile(path, fmt.Appendf(nil, `{"version":%d}`, store.StateVersion+1), 0o644))
	// Each hook event is a new process.
	nextEvent := func() { e.warnings.Reset(); agentStateWarned = false }

	runHookFixture(t, "stop.json", "Stop")
	assert.Empty(t, e.warnings.String(), "Claude Code discards a systemMessage on Stop, so the warning waits")

	nextEvent()
	runHookFixture(t, "post_tool_use.json", "PostToolUse")
	assert.Contains(t, hookWarning(t, e.warnings.String()), "Upgrade wgo")

	nextEvent()
	e.advance(10 * time.Minute)
	runHookFixture(t, "post_tool_use.json", "PostToolUse")
	assert.Empty(t, e.warnings.String(), "the same problem is not repeated on every tool call")

	nextEvent()
	e.advance(hookWarnEvery)
	runHookFixture(t, "post_tool_use.json", "PostToolUse")
	assert.Contains(t, hookWarning(t, e.warnings.String()), "Upgrade wgo", "but is repeated hourly")

	require.NoError(t, os.Remove(path))
	nextEvent()
	require.NoError(t, runAgentHook(strings.NewReader(`{"session_id":"s1","cwd":"/nowhere"}`), "PostToolUse", "claude"))
	assert.Empty(t, e.warnings.String(), "outside a jj workspace is expected, not a warning")
	assert.Nil(t, loadState(t).GetAgentSession("s1"))
}
