package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

// indexOf is a tiny helper for asserting relative order in captured output.
func indexOf(haystack, needle string) int {
	return bytes.Index([]byte(haystack), []byte(needle))
}

// fakeProcs is a mutable fake process table.
type fakeProcs map[int]proc.Info

func (f fakeProcs) Lookup(pid int) (proc.Info, error) {
	if p, ok := f[pid]; ok {
		return p, nil
	}
	return proc.Info{}, proc.ErrNotFound
}

func (f fakeProcs) Args(int) ([]string, error) { return nil, proc.ErrNotFound }

// agentEnv isolates the agent commands: a temp HOME, no agent env, a fake
// clock, a fake process table whose ancestor walk starts at a shell, and a
// fake jj workspace lookup keyed by directory.
type agentEnv struct {
	now        time.Time
	procs      fakeProcs
	workspaces map[string]workspaceInfo // dir -> workspace
	cwd        string
	warnings   *bytes.Buffer // what warnAgentState printed
}

// The fake process tree: a shell (300) under a claude process (200).
const (
	fakeShellPID  = 300
	fakeClaudePID = 200
	fakeClaudeTok = int64(424242)
)

func newAgentEnv(t *testing.T) *agentEnv {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDECODE", "")
	e := &agentEnv{
		now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
		procs: fakeProcs{
			fakeShellPID:  {PID: fakeShellPID, PPID: fakeClaudePID, Name: "zsh", Start: 9},
			fakeClaudePID: {PID: fakeClaudePID, PPID: 1, Name: "claude", Start: fakeClaudeTok},
		},
		workspaces: map[string]workspaceInfo{
			"/ws":         {Root: "/ws", Repo: "/ws", Branch: "feat"},
			"/ws/sub/dir": {Root: "/ws", Repo: "/ws", Branch: "feat"},
			"/wt/feat":    {Root: "/wt/feat", Repo: "/ws", Branch: "feat"},
			"/wt/other":   {Root: "/wt/other", Repo: "/ws", Branch: "other"},
		},
		cwd:      "/ws",
		warnings: &bytes.Buffer{},
	}
	oldWarnOut := agentWarnOut
	agentWarnOut, agentStateWarned = e.warnings, false
	t.Setenv("WGO_DEBUG", "")
	oldNow, oldProcs, oldPPID, oldLookup, oldRoot, oldCur := agentNow, procInspector, agentParentPID, lookupWorkspace, workspaceRoot, currentWorkspace
	agentNow = func() time.Time { return e.now }
	procInspector = e.procs
	agentParentPID = func() int { return fakeShellPID }
	lookupWorkspace = func(dir string) (workspaceInfo, error) {
		if ws, ok := e.workspaces[dir]; ok {
			return ws, nil
		}
		return workspaceInfo{}, fmt.Errorf("not a jj repository")
	}
	workspaceRoot = func() (string, error) {
		ws, err := lookupWorkspace(e.cwd)
		return ws.Root, err
	}
	currentWorkspace = func() (workspaceInfo, error) { return lookupWorkspace(e.cwd) }
	t.Cleanup(func() {
		agentNow, procInspector, agentParentPID, lookupWorkspace, workspaceRoot, currentWorkspace = oldNow, oldProcs, oldPPID, oldLookup, oldRoot, oldCur
		agentWarnOut, agentStateWarned = oldWarnOut, false
	})
	return e
}

func (e *agentEnv) advance(d time.Duration) { e.now = e.now.Add(d) }

// noAgentProcess makes the ancestor walk find no tool process.
func (e *agentEnv) noAgentProcess() { delete(e.procs, fakeClaudePID) }

func loadState(t *testing.T) *store.State {
	t.Helper()
	s, err := store.New()
	require.NoError(t, err)
	state, err := s.LoadState()
	require.NoError(t, err)
	return state
}

func saveState(t *testing.T, state *store.State) {
	t.Helper()
	s, err := store.New()
	require.NoError(t, err)
	require.NoError(t, s.SaveState(state))
}

func startAgent(t *testing.T, o agentStartOpts) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, runAgentStart(&buf, o))
	return buf.String()
}

func agentStatusOut(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, runAgentStatus(&buf, false))
	return buf.String()
}

// TestDetectAgent gates on the CLAUDECODE env var.
func TestDetectAgent(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	assert.Equal(t, "claude", detectAgent())

	t.Setenv("CLAUDECODE", "")
	assert.Equal(t, "", detectAgent())
}

// TestRunAgentStartRejectsEmptyName: start validates the name before touching
// the workspace or state, so a blank name is a clear error, not a nameless 🤖.
func TestRunAgentStartRejectsEmptyName(t *testing.T) {
	newAgentEnv(t)
	assert.Error(t, runAgentStart(io.Discard, agentStartOpts{}))
	assert.Error(t, runAgentStart(io.Discard, agentStartOpts{Tool: "   "}), "a whitespace-only name is also empty")
	assert.Error(t, runAgentStart(io.Discard, agentStartOpts{Tool: "claude", Session: "a b"}), "IDs are validated")
	assert.Error(t, runAgentStart(io.Discard, agentStartOpts{Tool: "claude", Status: "busy"}), "statuses are validated")
}

// TestRunAgentStatusEmpty: with no sessions, status reports the empty state.
func TestRunAgentStatusEmpty(t *testing.T) {
	newAgentEnv(t)
	assert.Contains(t, agentStatusOut(t), "No active agent sessions.")
}

// TestAgentStartGeneratesMachineReadableID: without --session an ID is
// generated, and --json exposes it so callers never parse the human line.
func TestAgentStartGeneratesMachineReadableID(t *testing.T) {
	e := newAgentEnv(t)
	var got agentSessionJSON
	require.NoError(t, json.Unmarshal([]byte(startAgent(t, agentStartOpts{Tool: "codex", Theme: "gh-72", JSON: true})), &got))
	assert.Regexp(t, `^codex-[0-9a-f]{6}$`, got.ID)
	assert.Equal(t, "/ws", got.Workspace)
	assert.Equal(t, "feat", got.Bookmark)
	assert.Equal(t, "gh-72", got.Theme)
	assert.Equal(t, "working", got.Status)

	human := startAgent(t, agentStartOpts{Tool: "codex"})
	assert.Contains(t, human, "🤖 codex working in /ws")
	assert.Contains(t, human, "wgo agent stop --session codex-")
	assert.Len(t, loadState(t).AgentSessionsIn("/ws"), 2, "a second start never overwrites the first")
	_ = e
}

// TestAgentStartRecordsAgentProcessNotShell: the recorded PID is the claude
// ancestor, with its start token, not the transient shell that ran wgo.
func TestAgentStartRecordsAgentProcessNotShell(t *testing.T) {
	newAgentEnv(t)
	startAgent(t, agentStartOpts{Tool: "claude", Session: "a"})
	sess := loadState(t).GetAgentSession("a")
	require.NotNil(t, sess)
	assert.Equal(t, fakeClaudePID, sess.PID)
	assert.Equal(t, fakeClaudeTok, sess.ProcStart)

	// A tool with no matching ancestor records no identity at all.
	startAgent(t, agentStartOpts{Tool: "codex", Session: "b"})
	b := loadState(t).GetAgentSession("b")
	require.NotNil(t, b)
	assert.False(t, b.HasProcess())
}

// TestTwoSessionsOneWorkspaceIndependentLifecycle: two agents in one
// workspace survive independent heartbeat and stop, and stop without
// --session refuses to guess.
func TestTwoSessionsOneWorkspaceIndependentLifecycle(t *testing.T) {
	e := newAgentEnv(t)
	startAgent(t, agentStartOpts{Tool: "claude", Session: "a", Theme: "effort-1"})
	startAgent(t, agentStartOpts{Tool: "claude", Session: "b", Theme: "effort-1"})

	out := agentStatusOut(t)
	assert.Contains(t, out, "session a")
	assert.Contains(t, out, "session b")

	e.advance(2 * time.Minute)
	require.NoError(t, runAgentHeartbeat(io.Discard, "a", "waiting"))
	state := loadState(t)
	assert.Equal(t, store.AgentWaiting, state.GetAgentSession("a").Status)
	assert.Equal(t, store.AgentWorking, state.GetAgentSession("b").Status, "b is untouched by a's heartbeat")
	assert.True(t, state.GetAgentSession("a").LastActivity.After(state.GetAgentSession("b").LastActivity))

	// Heartbeat and stop without --session are ambiguous here.
	err := runAgentHeartbeat(io.Discard, "", "idle")
	require.Error(t, err)
	err = runAgentStop(io.Discard, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--session")
	assert.Contains(t, err.Error(), "\n  a ")
	assert.Contains(t, err.Error(), "\n  b ")
	assert.Len(t, loadState(t).AgentSessionsIn("/ws"), 2, "an ambiguous stop removes nothing")

	var buf bytes.Buffer
	require.NoError(t, runAgentStop(&buf, "a"))
	assert.Contains(t, buf.String(), "cleared agent session a")
	state = loadState(t)
	assert.Nil(t, state.GetAgentSession("a"))
	require.NotNil(t, state.GetAgentSession("b"))

	// With one session left, the workspace default applies.
	require.NoError(t, runAgentHeartbeat(io.Discard, "", "idle"))
	assert.Equal(t, store.AgentIdle, loadState(t).GetAgentSession("b").Status)
	buf.Reset()
	require.NoError(t, runAgentStop(&buf, ""))
	assert.Contains(t, buf.String(), "cleared agent session b")
	assert.Empty(t, loadState(t).AgentSessions)

	err = runAgentHeartbeat(io.Discard, "gone", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wgo agent start --session gone")
}

// TestIssue61QuietLiveAgentSurvives reproduces #61 end to end: an agent that
// starts a session and then works quietly for 35 minutes, while another agent
// starts elsewhere (which prunes), is still listed and can still stop.
func TestIssue61QuietLiveAgentSurvives(t *testing.T) {
	e := newAgentEnv(t)
	startAgent(t, agentStartOpts{Tool: "claude", Session: "sweep-1"})

	e.advance(35 * time.Minute)
	e.cwd = "/wt/other"
	startAgent(t, agentStartOpts{Tool: "claude", Session: "sweep-2"}) // prunes under the lock

	out := agentStatusOut(t)
	assert.Contains(t, out, "session sweep-1", "a quiet session with a live agent process is still listed")
	assert.Contains(t, out, "process alive")

	var buf bytes.Buffer
	require.NoError(t, runAgentStop(&buf, "sweep-1"))
	assert.Contains(t, buf.String(), "cleared agent session sweep-1")
	assert.NotContains(t, buf.String(), "no agent session")
}

// TestReusedPIDIsNotLive: when the recorded PID now belongs to a different
// process (start token differs), the session is not live and is pruned.
func TestReusedPIDIsNotLive(t *testing.T) {
	e := newAgentEnv(t)
	startAgent(t, agentStartOpts{Tool: "claude", Session: "a"})
	e.procs[fakeClaudePID] = proc.Info{PID: fakeClaudePID, PPID: 1, Name: "claude", Start: fakeClaudeTok + 1}

	e.advance(11 * time.Minute)
	assert.NotContains(t, agentStatusOut(t), "session a", "a reused PID does not keep the session visible")
	startAgent(t, agentStartOpts{Tool: "codex", Session: "b"})
	assert.Nil(t, loadState(t).GetAgentSession("a"), "and the next prune removes it")
}

// TestNoProcessIdentityIsUncertain: a quiet session with nothing to verify is
// shown as uncertain and survives pruning until the maximum age.
func TestNoProcessIdentityIsUncertain(t *testing.T) {
	e := newAgentEnv(t)
	e.noAgentProcess()
	startAgent(t, agentStartOpts{Tool: "claude", Session: "a"})

	e.advance(2 * time.Hour)
	startAgent(t, agentStartOpts{Tool: "codex", Session: "b"}) // prunes
	out := agentStatusOut(t)
	assert.Contains(t, out, "session a")
	assert.Contains(t, out, "uncertain")
	require.NotNil(t, loadState(t).GetAgentSession("a"), "uncertain sessions are not deleted")

	ref := resolveAgent("/ws")
	require.NotNil(t, ref)
	assert.Equal(t, "b", ref.Session, "the glyph shows the most recently active session")
	assert.Equal(t, 1, ref.Others)

	e.advance(agentMaxAge)
	startAgent(t, agentStartOpts{Tool: "codex", Session: "c"})
	assert.Nil(t, loadState(t).GetAgentSession("a"), "past the maximum age it is pruned")
}

// TestInferredSessionExpires: the statusline's inferred session still ages out
// on the quiet timeout.
func TestInferredSessionExpires(t *testing.T) {
	e := newAgentEnv(t)
	t.Setenv("CLAUDECODE", "1")
	heartbeatAgent("/ws", "/ws", "feat")
	ref := resolveAgent("/ws")
	require.NotNil(t, ref)
	assert.Equal(t, "claude", ref.Name)

	e.advance(agentStaleAfter + time.Minute)
	assert.Nil(t, resolveAgent("/ws"), "an inferred session expires")
	t.Setenv("CLAUDECODE", "")
	startAgent(t, agentStartOpts{Tool: "codex", Session: "x"})
	assert.Nil(t, loadState(t).GetAgentSession(store.InferredAgentSessionID("claude", "/ws")), "and is pruned")
}

// TestHeartbeatAgentWritesThenThrottles: the first hot-path heartbeat records
// a single inferred session; another within the throttle window does not
// rewrite it.
func TestHeartbeatAgentWritesThenThrottles(t *testing.T) {
	e := newAgentEnv(t)
	t.Setenv("CLAUDECODE", "1")
	heartbeatAgent("/ws", "/ws", "(no bookmark)")
	id := store.InferredAgentSessionID("claude", "/ws")
	first := loadState(t).GetAgentSession(id)
	require.NotNil(t, first)
	assert.Equal(t, store.SourceInferred, first.Source)
	assert.Empty(t, first.Branch, "the display placeholder is not stored as a bookmark")

	e.advance(10 * time.Second)
	heartbeatAgent("/ws", "/ws", "")
	assert.Equal(t, first.LastActivity, loadState(t).GetAgentSession(id).LastActivity)

	e.advance(heartbeatThrottle)
	heartbeatAgent("/ws", "/ws", "")
	assert.Equal(t, e.now, loadState(t).GetAgentSession(id).LastActivity.UTC())
	assert.Len(t, loadState(t).AgentSessions, 1, "one inferred session per tool and workspace")
}

// TestHeartbeatAgentNoEnvIsNoOp: without an agent env, nothing is recorded.
func TestHeartbeatAgentNoEnvIsNoOp(t *testing.T) {
	newAgentEnv(t)
	heartbeatAgent("/ws", "/ws", "feat")
	assert.Nil(t, resolveAgent("/ws"))
}

// TestStatuslineHeartbeatRespectsHookSession: with a hook-managed session in
// the workspace, the statusline heartbeat refreshes it without creating an
// inferred duplicate or changing its status, and collapses an inferred
// session that predates it.
func TestStatuslineHeartbeatRespectsHookSession(t *testing.T) {
	e := newAgentEnv(t)
	t.Setenv("CLAUDECODE", "1")

	// The statusline ran before any hook fired: an inferred session exists.
	heartbeatAgent("/ws", "/ws", "feat")
	require.Len(t, loadState(t).AgentSessions, 1)

	runHookFixture(t, "stop.json", "Stop")
	state := loadState(t)
	require.Len(t, state.AgentSessions, 1, "the hook session replaces the inferred one")
	hook := state.GetAgentSession(hookSessionID)
	require.NotNil(t, hook)
	assert.Equal(t, store.AgentWaiting, hook.Status)

	e.advance(5 * time.Minute)
	heartbeatAgent("/ws", "/ws", "feat")
	state = loadState(t)
	require.Len(t, state.AgentSessions, 1, "no duplicate next to the hook session")
	after := state.GetAgentSession(hookSessionID)
	require.NotNil(t, after)
	assert.Equal(t, store.AgentWaiting, after.Status, "the statusline never clobbers the hook's status")
	assert.Equal(t, store.SourceHook, after.Source)
	assert.Equal(t, hook.StartTime, after.StartTime)
	assert.Equal(t, e.now, after.LastActivity.UTC(), "but it does refresh activity")

	ref := resolveAgent("/ws")
	require.NotNil(t, ref)
	assert.Equal(t, hookSessionID, ref.Session)
	assert.Equal(t, "waiting", ref.Status)
}

// TestAgentStatusFlagsConflicts: two active sessions on the same repository
// and bookmark, in different workspaces, are flagged.
func TestAgentStatusFlagsConflicts(t *testing.T) {
	e := newAgentEnv(t)
	startAgent(t, agentStartOpts{Tool: "claude", Session: "main-ws"})
	e.cwd = "/wt/feat"
	out := startAgent(t, agentStartOpts{Tool: "codex", Session: "feat-ws"})
	assert.Contains(t, out, "possible conflict: main-ws")
	e.cwd = "/wt/other"
	startAgent(t, agentStartOpts{Tool: "codex", Session: "other-ws"})

	out = agentStatusOut(t)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 3)
	for _, l := range lines {
		switch {
		case strings.Contains(l, "session main-ws"):
			assert.Contains(t, l, "⚠ same bookmark as feat-ws")
		case strings.Contains(l, "session feat-ws"):
			assert.Contains(t, l, "⚠ same bookmark as main-ws")
		default:
			assert.NotContains(t, l, "⚠")
		}
	}

	var buf bytes.Buffer
	require.NoError(t, runAgentStatus(&buf, true))
	var list []agentSessionJSON
	require.NoError(t, json.Unmarshal(buf.Bytes(), &list))
	require.Len(t, list, 3)
	for _, s := range list {
		if s.ID == "other-ws" {
			assert.Empty(t, s.Conflicts)
		} else {
			assert.Len(t, s.Conflicts, 1)
		}
	}
}

// TestRunAgentStatusListsSessionsSorted: status lists sessions grouped by
// workspace root and falls back to "(no bookmark)".
func TestRunAgentStatusListsSessionsSorted(t *testing.T) {
	e := newAgentEnv(t)
	e.cwd = "/nowhere" // not a workspace: nothing is marked current
	state := loadState(t)
	for _, s := range []store.AgentSession{
		{ID: "b1", Tool: "codex", WorktreePath: "/tmp/wgo-ws-b"},
		{ID: "a1", Tool: "claude", WorktreePath: "/tmp/wgo-ws-a", Branch: "WGO-134"},
	} {
		_, err := state.UpsertAgentSession(s, e.now)
		require.NoError(t, err)
	}
	saveState(t, state)

	out := agentStatusOut(t)
	assert.Contains(t, out, "WGO-134")
	assert.Contains(t, out, "(no bookmark)")
	assert.NotContains(t, out, "current")
	assert.Less(t, indexOf(out, "/tmp/wgo-ws-a"), indexOf(out, "/tmp/wgo-ws-b"))
}

// TestNewerStateRefusesWrites: an older binary (this one, relative to a
// version-4 file) still reads but will not write newer state.
func TestNewerStateRefusesWrites(t *testing.T) {
	newAgentEnv(t)
	home, _ := os.UserHomeDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".wgo"), 0o755))
	path := filepath.Join(home, ".wgo", "state.json")
	body := fmt.Appendf(nil, `{"version":%d,"agent_sessions":{"x":{"id":"x","tool":"claude","worktree_path":"/ws","last_activity":"2026-10-05T12:00:00Z"}}}`, store.StateVersion+1)
	require.NoError(t, os.WriteFile(path, body, 0o644))

	assert.Contains(t, agentStatusOut(t), "session x", "reads still work")
	err := runAgentStart(io.Discard, agentStartOpts{Tool: "claude", Session: "a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Upgrade wgo")

	t.Setenv("CLAUDECODE", "1")
	e := heartbeatEnvWarnings(t)
	heartbeatAgent("/ws", "/ws", "feat") // never fails the hot path...
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, body, after)
	assert.Contains(t, e.String(), "Upgrade wgo", "...but says why tracking stopped")
}

func heartbeatEnvWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	b, ok := agentWarnOut.(*bytes.Buffer)
	require.True(t, ok, "newAgentEnv captures warnings")
	return b
}

// TestStatuslineHeartbeatRespectsExplicitSession: after an explicit
// `agent start`, the statusline heartbeat for the same tool and workspace
// refreshes that session without adding an inferred duplicate or changing its
// status.
func TestStatuslineHeartbeatRespectsExplicitSession(t *testing.T) {
	e := newAgentEnv(t)
	startAgent(t, agentStartOpts{Tool: "claude", Session: "explicit-1", Status: "waiting"})

	t.Setenv("CLAUDECODE", "1")
	e.advance(5 * time.Minute)
	heartbeatAgent("/ws", "/ws", "feat")

	state := loadState(t)
	require.Len(t, state.AgentSessions, 1, "no inferred duplicate next to the explicit session")
	assert.Nil(t, state.GetAgentSession(store.InferredAgentSessionID("claude", "/ws")))
	sess := state.GetAgentSession("explicit-1")
	require.NotNil(t, sess)
	assert.Equal(t, store.AgentWaiting, sess.Status, "the statusline never clobbers the explicit status")
	assert.Equal(t, store.SourceExplicit, sess.Source)
	assert.Equal(t, e.now, sess.LastActivity.UTC(), "but it does refresh activity")
}

// TestUnverifiableProcessIsUncertainNotGone: when the process table cannot be
// read (sandbox, hidepid, another machine), a quiet session with a recorded
// process is shown as uncertain rather than deleted.
func TestUnverifiableProcessIsUncertainNotGone(t *testing.T) {
	e := newAgentEnv(t)
	startAgent(t, agentStartOpts{Tool: "claude", Session: "a"})
	require.True(t, loadState(t).GetAgentSession("a").HasProcess())

	procInspector = deniedProcs{}
	e.advance(agentStaleAfter + time.Hour)
	startAgent(t, agentStartOpts{Tool: "codex", Session: "b"}) // prunes
	require.NotNil(t, loadState(t).GetAgentSession("a"), "an unverifiable process is not proof it exited")
	assert.Contains(t, agentStatusOut(t), "uncertain")
}

// deniedProcs fails every lookup without saying the process is gone.
type deniedProcs struct{}

func (deniedProcs) Lookup(int) (proc.Info, error) {
	return proc.Info{}, fmt.Errorf("operation not permitted")
}
func (deniedProcs) Args(int) ([]string, error) { return nil, fmt.Errorf("operation not permitted") }

// TestAgentStartWithPID: --pid records that process's start token; an
// unknown PID records no identity rather than a half one.
func TestAgentStartWithPID(t *testing.T) {
	e := newAgentEnv(t)
	e.procs[777] = proc.Info{PID: 777, PPID: 1, Name: "mytool", Start: 31337}
	startAgent(t, agentStartOpts{Tool: "mytool", Session: "a", PID: 777})
	sess := loadState(t).GetAgentSession("a")
	assert.Equal(t, 777, sess.PID)
	assert.Equal(t, int64(31337), sess.ProcStart)

	startAgent(t, agentStartOpts{Tool: "mytool", Session: "b", PID: 778})
	sess = loadState(t).GetAgentSession("b")
	assert.False(t, sess.HasProcess())
	assert.Zero(t, sess.PID)
}

// TestAgentStopUnknownSessionFails: naming a session that does not exist is an
// error, so a typo in a script does not pass silently.
func TestAgentStopUnknownSessionFails(t *testing.T) {
	newAgentEnv(t)
	err := runAgentStop(io.Discard, "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wgo agent status")
}

// TestHeartbeatTracksBookmark: a bookmark change is recorded even inside the
// throttle window; moving off every bookmark clears it; a failed lookup ("")
// keeps the recorded one.
func TestHeartbeatTracksBookmark(t *testing.T) {
	e := newAgentEnv(t)
	startAgent(t, agentStartOpts{Tool: "claude", Session: "a"})
	t.Setenv("CLAUDECODE", "1")

	e.advance(time.Second)
	heartbeatAgent("/ws", "/ws", "next")
	assert.Equal(t, "next", loadState(t).GetAgentSession("a").Branch, "a bookmark change bypasses the throttle")

	e.advance(time.Second)
	heartbeatAgent("/ws", "/ws", "")
	assert.Equal(t, "next", loadState(t).GetAgentSession("a").Branch, "an unknown bookmark keeps the recorded one")

	e.advance(time.Second)
	heartbeatAgent("/ws", "/ws", "(no bookmark)")
	assert.Empty(t, loadState(t).GetAgentSession("a").Branch, "no bookmark clears it, so it cannot cause a false conflict")
}

// TestHeartbeatRefreshesOwnSession: with two managed sessions of one tool in a
// workspace, the statusline refreshes the one whose process it runs under, so
// a crashed sibling still ages out.
func TestHeartbeatRefreshesOwnSession(t *testing.T) {
	e := newAgentEnv(t)
	state := loadState(t)
	for _, sess := range []store.AgentSession{
		{ID: "mine", Tool: "claude", Source: store.SourceHook, WorktreePath: "/ws", PID: fakeClaudePID, ProcStart: fakeClaudeTok},
		{ID: "other", Tool: "claude", Source: store.SourceHook, WorktreePath: "/ws"},
	} {
		_, err := state.UpsertAgentSession(sess, e.now)
		require.NoError(t, err)
	}
	saveState(t, state)
	e.advance(time.Minute)
	// "other" is now the more recent, which the old code would have picked.
	state = loadState(t)
	_, err := state.UpsertAgentSession(store.AgentSession{ID: "other", Tool: "claude", WorktreePath: "/ws"}, e.now)
	require.NoError(t, err)
	saveState(t, state)
	before := loadState(t).GetAgentSession("other").LastActivity

	t.Setenv("CLAUDECODE", "1")
	e.advance(5 * time.Minute)
	heartbeatAgent("/ws", "/ws", "feat")
	state = loadState(t)
	assert.Equal(t, e.now, state.GetAgentSession("mine").LastActivity.UTC())
	assert.Equal(t, before, state.GetAgentSession("other").LastActivity)
}

// TestToolNamesCompareNormalized: a hook sent with --tool Claude is the same
// tool the statusline detects, so no inferred duplicate appears.
func TestToolNamesCompareNormalized(t *testing.T) {
	e := newAgentEnv(t)
	f, err := os.Open(filepath.Join("testdata", "hooks", "stop.json"))
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, runAgentHook(f, "Stop", " Claude "))
	assert.Equal(t, "claude", loadState(t).GetAgentSession(hookSessionID).Tool)

	t.Setenv("CLAUDECODE", "1")
	e.advance(5 * time.Minute)
	heartbeatAgent("/ws", "/ws", "feat")
	assert.Len(t, loadState(t).AgentSessions, 1)
}
