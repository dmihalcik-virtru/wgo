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

// Codex sends the same shared hook fields as Claude Code. Its session ID is
// kept distinct, and the hook records the codex process and lifecycle.
func TestAgentHookCodexLifecycle(t *testing.T) {
	e := newAgentEnv(t)
	e.procs[fakeClaudePID] = proc.Info{PID: fakeClaudePID, PPID: 1, Name: "codex", Start: fakeClaudeTok}
	const sessionID = "thr_123"
	send := func(event, extra string) {
		t.Helper()
		payload := fmt.Sprintf(`{"session_id":%q,"cwd":"/ws","hook_event_name":%q%s}`, sessionID, event, extra)
		require.NoError(t, runAgentHook(strings.NewReader(payload), "", "codex"))
		e.advance(time.Second)
	}

	send("SessionStart", `,"source":"startup"`)
	sess := loadState(t).GetAgentSession(sessionID)
	require.NotNil(t, sess)
	assert.Equal(t, "codex", sess.Tool)
	assert.Equal(t, store.AgentWaiting, sess.Status)
	assert.Equal(t, fakeClaudePID, sess.PID)
	assert.Equal(t, fakeClaudeTok, sess.ProcStart)

	send("UserPromptSubmit", `,"turn_id":"turn_1","prompt":"Fix the bug"`)
	assert.Equal(t, store.AgentWorking, loadState(t).GetAgentSession(sessionID).Status)
	send("PostToolUse", `,"turn_id":"turn_1","tool_name":"Bash"`)
	assert.Equal(t, store.AgentWorking, loadState(t).GetAgentSession(sessionID).Status)
	send("Stop", `,"turn_id":"turn_1"`)
	assert.Equal(t, store.AgentWaiting, loadState(t).GetAgentSession(sessionID).Status)
	send("UserPromptSubmit", `,"turn_id":"turn_2"`)
	send("Interrupt", `,"turn_id":"turn_2"`)
	assert.Equal(t, store.AgentWaiting, loadState(t).GetAgentSession(sessionID).Status)
	send("SessionEnd", `,"reason":"other"`)
	assert.Nil(t, loadState(t).GetAgentSession(sessionID))
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
	assert.Equal(t, store.AgentWorking, hookActionFor("PreInvocation").status)
	assert.True(t, hookActionFor("PreInvocation").refresh)
	assert.Equal(t, store.AgentWaiting, hookActionFor("Stop").status)
	assert.Equal(t, store.AgentWaiting, hookActionFor("Interrupt").status)
	assert.True(t, hookActionFor("SessionEnd").remove)
	assert.Equal(t, hookAction{}, hookActionFor("SomethingNew"))
}

// TestAgentHookAntigravityLifecycle verifies Antigravity's camelCase protojson
// payloads (conversationId, hookEventName, workspacePaths, toolCall) and
// required JSON stdout responses ({}, {"decision":"allow"}, and PreInvocation
// injectSteps warnings).
func TestAgentHookAntigravityLifecycle(t *testing.T) {
	e := newAgentEnv(t)
	e.procs[fakeClaudePID] = proc.Info{PID: fakeClaudePID, PPID: 1, Name: "Antigravity Helper", Start: fakeClaudeTok}
	const convID = "79b14596-1d0f-4244-948d-8a8c03428d42"

	send := func(payload string) string {
		t.Helper()
		e.warnings.Reset()
		agentStateWarned = false
		require.NoError(t, runAgentHook(strings.NewReader(payload), "", "antigravity"))
		e.advance(time.Second)
		return strings.TrimSpace(e.warnings.String())
	}

	// SessionStart falls back to workspacePaths when cwd is outside a jj repo.
	out := send(fmt.Sprintf(`{"conversationId":%q,"cwd":"/scratch","workspacePaths":["/ws"],"hookEventName":"SessionStart"}`, convID))
	assert.Equal(t, `{}`, out)
	sess := loadState(t).GetAgentSession(convID)
	require.NotNil(t, sess)
	assert.Equal(t, "antigravity", sess.Tool)
	assert.Equal(t, store.AgentWaiting, sess.Status)
	assert.Equal(t, "/ws", sess.WorktreePath)
	assert.Equal(t, fakeClaudePID, sess.PID)

	out = send(fmt.Sprintf(`{"conversationId":%q,"cwd":"/ws","hookEventName":"PreInvocation","invocationNum":1}`, convID))
	assert.Equal(t, `{}`, out)
	assert.Equal(t, store.AgentWorking, loadState(t).GetAgentSession(convID).Status)

	out = send(fmt.Sprintf(`{"conversationId":%q,"cwd":"/ws","hookEventName":"PostToolUse","toolCall":{"id":"t1","name":"edit_file","args":{"Path":"/ws/a.go"}}}`, convID))
	assert.Equal(t, `{}`, out)
	assert.Equal(t, store.AgentWorking, loadState(t).GetAgentSession(convID).Status)

	out = send(fmt.Sprintf(`{"conversationId":%q,"cwd":"/ws","hookEventName":"Stop","terminationReason":"NO_TOOL_CALL"}`, convID))
	assert.Equal(t, `{"decision":"allow"}`, out)
	assert.Equal(t, store.AgentWaiting, loadState(t).GetAgentSession(convID).Status)

	// Outside any jj workspace still emits valid JSON so Antigravity never
	// fails parsing stdout.
	out = send(`{"conversationId":"other-conv","cwd":"/nowhere","hookEventName":"Stop"}`)
	assert.Equal(t, `{"decision":"allow"}`, out)
	assert.Nil(t, loadState(t).GetAgentSession("other-conv"))

	// State problems surface via injectSteps user_markdown on PreInvocation.
	home, _ := os.UserHomeDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".wgo", "state.json"), fmt.Appendf(nil, `{"version":%d}`, store.StateVersion+1), 0o644))
	out = send(fmt.Sprintf(`{"conversationId":%q,"cwd":"/ws","hookEventName":"PreInvocation"}`, convID))
	assert.Contains(t, out, `"injectSteps"`)
	assert.Contains(t, out, `"user_markdown"`)
	assert.Contains(t, out, "Upgrade wgo")
}

func TestAgentHookAntigravityMultipleMountedWorkspaces(t *testing.T) {
	e := newAgentEnv(t)
	e.procs[fakeClaudePID] = proc.Info{PID: fakeClaudePID, PPID: 1, Name: "Antigravity Helper", Start: fakeClaudeTok}
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	api, web := filepath.Join(root, "api"), filepath.Join(root, "web")
	require.NoError(t, os.MkdirAll(filepath.Join(api, ".jj"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(web, ".jj"), 0o755))
	e.workspaces[api] = workspaceInfo{Root: api, Repo: api, Branch: "api-feature", BranchKnown: true}
	e.workspaces[web] = workspaceInfo{Root: web, Repo: web, Branch: "web-feature", BranchKnown: true}
	const convID = "multi-mounted-conversation"
	send := func(event, mounts, toolCall string) {
		t.Helper()
		payload := fmt.Sprintf(`{"conversationId":%q,"workspacePaths":%s,"toolCall":%s}`, convID, mounts, toolCall)
		require.NoError(t, runAgentHook(strings.NewReader(payload), event, "antigravity"))
		e.advance(time.Second)
	}
	apiFirst := fmt.Sprintf(`[%q,%q]`, api, web)
	webFirst := fmt.Sprintf(`[%q,%q]`, web, api)

	send("SessionStart", apiFirst, `{}`)
	sess := loadState(t).GetAgentSession(convID)
	require.NotNil(t, sess)
	assert.Empty(t, sess.WorktreePath)
	assert.Empty(t, sess.RepoPath)
	assert.Empty(t, sess.Branch, "mount order is not evidence of an active bookmark")
	assert.Equal(t, []string{api, web}, sess.MountedWorkspaces)
	assert.Contains(t, agentStatusOut(t), "2 mounted workspaces")
	var statusJSON strings.Builder
	require.NoError(t, runAgentStatus(&statusJSON, true))
	assert.Contains(t, statusJSON.String(), `"mounted_workspaces"`)
	assert.NotContains(t, statusJSON.String(), `"bookmark": "api-feature"`)

	// A hook runner launched from another jj repo must not assign that repo to
	// a conversation whose mounted workspaces are api and web.
	otherCwd := fmt.Sprintf(`{"conversationId":%q,"cwd":"/ws","workspacePaths":%s}`, convID, apiFirst)
	require.NoError(t, runAgentHook(strings.NewReader(otherCwd), "PreInvocation", "antigravity"))
	assert.Empty(t, loadState(t).GetAgentSession(convID).WorktreePath)

	send("PostToolUse", apiFirst, fmt.Sprintf(`{"name":"replace_file_content","args":{"TargetFile":%q}}`, filepath.Join(web, "page.go")))
	sess = loadState(t).GetAgentSession(convID)
	require.NotNil(t, sess)
	assert.Equal(t, web, sess.WorktreePath)
	assert.Equal(t, web, sess.RepoPath)
	assert.Equal(t, "web-feature", sess.Branch)

	send("PreInvocation", webFirst, `{}`)
	assert.Equal(t, "web-feature", loadState(t).GetAgentSession(convID).Branch, "reordering mounts does not change focus")

	send("PostToolUse", webFirst, fmt.Sprintf(`{"name":"run_command","args":{"Cwd":%q,"CommandLine":"go test ./..."}}`, api))
	sess = loadState(t).GetAgentSession(convID)
	require.NotNil(t, sess)
	assert.Equal(t, api, sess.WorktreePath)
	assert.Equal(t, "api-feature", sess.Branch)
	assert.Equal(t, []string{web, api}, sess.MountedWorkspaces)

	send("Stop", webFirst, `{}`)
	assert.Equal(t, "api-feature", loadState(t).GetAgentSession(convID).Branch)

	// Unmounting the previously focused workspace (api) while keeping 2+ mounts
	// clears WorktreePath, RepoPath and Branch instead of leaving stale conflict
	// state pointing at api.
	cli := filepath.Join(root, "cli")
	require.NoError(t, os.MkdirAll(filepath.Join(cli, ".jj"), 0o755))
	e.workspaces[cli] = workspaceInfo{Root: cli, Repo: cli, Branch: "cli-feature", BranchKnown: true}
	webAndCLI := fmt.Sprintf(`[%q,%q]`, web, cli)
	send("PreInvocation", webAndCLI, `{}`)
	sess = loadState(t).GetAgentSession(convID)
	require.NotNil(t, sess)
	assert.Empty(t, sess.WorktreePath)
	assert.Empty(t, sess.RepoPath)
	assert.Empty(t, sess.Branch)
	assert.Equal(t, []string{web, cli}, sess.MountedWorkspaces)
}

func TestAgentHookAntigravityZeroAndSingleMountContainment(t *testing.T) {
	e := newAgentEnv(t)
	e.procs[fakeClaudePID] = proc.Info{PID: fakeClaudePID, PPID: 1, Name: "Antigravity Helper", Start: fakeClaudeTok}
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	api, web := filepath.Join(root, "api"), filepath.Join(root, "web")
	require.NoError(t, os.MkdirAll(filepath.Join(api, ".jj"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(web, ".jj"), 0o755))
	e.workspaces[api] = workspaceInfo{Root: api, Repo: api, Branch: "api-feature", BranchKnown: true}
	e.workspaces[web] = workspaceInfo{Root: web, Repo: web, Branch: "web-feature", BranchKnown: true}

	// 1 mount: an unrelated hook-runner cwd (/ws) must not override mounts[0] (api).
	singleMount := fmt.Sprintf(`{"conversationId":"single-mount","cwd":"/ws","workspacePaths":[%q]}`, api)
	require.NoError(t, runAgentHook(strings.NewReader(singleMount), "SessionStart", "antigravity"))
	sess := loadState(t).GetAgentSession("single-mount")
	require.NotNil(t, sess)
	assert.Equal(t, api, sess.WorktreePath)
	assert.Equal(t, "api-feature", sess.Branch)

	// 0 mounts (scratch cwd): file edits inside jj workspaces still track and
	// switch workspaces, and a subsequent Stop in /scratch preserves it.
	const unmountedID = "unmounted-conv"
	editAPI := fmt.Sprintf(`{"conversationId":%q,"cwd":"/scratch","workspacePaths":[],"toolCall":{"name":"edit_file","args":{"Path":%q}}}`,
		unmountedID, filepath.Join(api, "main.go"))
	require.NoError(t, runAgentHook(strings.NewReader(editAPI), "PostToolUse", "antigravity"))
	sess = loadState(t).GetAgentSession(unmountedID)
	require.NotNil(t, sess)
	assert.Equal(t, api, sess.WorktreePath)
	assert.Equal(t, "api-feature", sess.Branch)

	e.advance(time.Second)
	editWeb := fmt.Sprintf(`{"conversationId":%q,"cwd":"/scratch","workspacePaths":[],"toolCall":{"name":"write_file","args":{"Path":%q}}}`,
		unmountedID, filepath.Join(web, "index.ts"))
	require.NoError(t, runAgentHook(strings.NewReader(editWeb), "PostToolUse", "antigravity"))
	sess = loadState(t).GetAgentSession(unmountedID)
	require.NotNil(t, sess)
	assert.Equal(t, web, sess.WorktreePath)
	assert.Equal(t, "web-feature", sess.Branch)

	e.advance(time.Second)
	stopScratch := fmt.Sprintf(`{"conversationId":%q,"cwd":"/scratch","workspacePaths":[]}`, unmountedID)
	require.NoError(t, runAgentHook(strings.NewReader(stopScratch), "Stop", "antigravity"))
	sess = loadState(t).GetAgentSession(unmountedID)
	require.NotNil(t, sess)
	assert.Equal(t, store.AgentWaiting, sess.Status)
	assert.Equal(t, web, sess.WorktreePath)
	assert.Equal(t, "web-feature", sess.Branch)
}

func TestAgentHookAntigravityQuietConversationExpires(t *testing.T) {
	e := newAgentEnv(t)
	e.procs[fakeClaudePID] = proc.Info{PID: fakeClaudePID, PPID: 1, Name: "Antigravity Helper", Start: fakeClaudeTok}
	const convID = "quiet-antigravity-conversation"
	payload := fmt.Sprintf(`{"conversationId":%q,"workspacePaths":["/ws"]}`, convID)
	require.NoError(t, runAgentHook(strings.NewReader(payload), "Stop", "antigravity"))
	sess := loadState(t).GetAgentSession(convID)
	require.NotNil(t, sess)
	assert.Equal(t, store.AgentWaiting, sess.Status)
	assert.Equal(t, fakeClaudePID, sess.PID)

	e.advance(11 * time.Minute)
	assert.Contains(t, agentStatusOut(t), "uncertain")
	e.advance(50 * time.Minute)
	assert.Contains(t, agentStatusOut(t), "No active agent sessions.")
	assert.Contains(t, e.procs, fakeClaudePID, "the app process is still running")

	require.NoError(t, runAgentHook(strings.NewReader(payload), "PreInvocation", "antigravity"))
	assert.Equal(t, store.AgentWorking, loadState(t).GetAgentSession(convID).Status)
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
// session ends (here, SessionEnd lands while UserPromptSubmit resolves the
// workspace) does not bring the session back, and is not reported as a
// problem, though a later event, as from a resume, does bring it back.
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
	runHookFixture(t, "stop.json", "UserPromptSubmit") // an event that shows warnings
	assert.Nil(t, loadState(t).GetAgentSession(hookSessionID), "a refresh event in flight does not resurrect it")
	assert.Empty(t, e.warnings.String())

	lookupWorkspace = lookup
	e.advance(time.Second)
	runHookFixture(t, "stop.json", "Stop")
	assert.NotNil(t, loadState(t).GetAgentSession(hookSessionID), "a resumed session is recorded again")
}

// TestAgentHookKeepsSessionWhenWorkspaceLookupFails: once a session exists,
// a refresh event from a cwd outside jj (claude cd'd elsewhere) or one jj
// fails on still updates its status, keeps its workspace, repo and bookmark,
// and is not reported.
func TestAgentHookKeepsSessionWhenWorkspaceLookupFails(t *testing.T) {
	e := newAgentEnv(t)
	fake := lookupWorkspace
	lookupWorkspace = func(dir string) (workspaceInfo, error) {
		if dir == "/broken" {
			return workspaceInfo{}, fmt.Errorf("failed to get workspace root: exec: \"jj\": executable file not found in $PATH")
		}
		return fake(dir)
	}
	runHookFixture(t, "stop.json", "Stop")
	require.NotNil(t, loadState(t).GetAgentSession(hookSessionID))

	for _, cwd := range []string{"/broken", "/tmp"} {
		e.advance(time.Second)
		require.NoError(t, runAgentHook(strings.NewReader(fmt.Sprintf(`{"session_id":%q,"cwd":%q}`, hookSessionID, cwd)), "UserPromptSubmit", "claude"))
		sess := loadState(t).GetAgentSession(hookSessionID)
		require.NotNil(t, sess, cwd)
		assert.Equal(t, store.AgentWorking, sess.Status, cwd)
		assert.Equal(t, "/ws", sess.WorktreePath, cwd)
		assert.Equal(t, "/ws", sess.RepoPath, cwd)
		assert.Equal(t, "feat", sess.Branch, cwd)
		assert.Empty(t, e.warnings.String(), cwd)

		e.advance(time.Second)
		runHookFixture(t, "stop.json", "Stop")
	}
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

// TestAgentHookReportsJJFailure: a cwd in a jj workspace that jj fails on (jj
// missing from Claude Code's PATH, say) is reported, unlike a cwd outside any
// workspace, because no session there would ever be tracked.
func TestAgentHookReportsJJFailure(t *testing.T) {
	e := newAgentEnv(t)
	lookupWorkspace = func(string) (workspaceInfo, error) {
		return workspaceInfo{}, fmt.Errorf("failed to get workspace root: exec: \"jj\": executable file not found in $PATH")
	}
	runHookFixture(t, "post_tool_use.json", "PostToolUse")
	assert.Nil(t, loadState(t).GetAgentSession(hookSessionID))
	msg := hookWarning(t, e.warnings.String())
	assert.Contains(t, msg, "/ws/sub/dir")
	assert.Contains(t, msg, "hook's PATH")
}

// TestAgentHookWarningLimitIsPerProblem: sessions in different directories
// that hit the same jj failure in turn share one hourly warning, a different
// problem within the hour is still shown, and a marker dated ahead of the
// clock does not hold a warning off.
func TestAgentHookWarningLimitIsPerProblem(t *testing.T) {
	e := newAgentEnv(t)
	fake := lookupWorkspace
	lookupWorkspace = func(dir string) (workspaceInfo, error) {
		if strings.HasPrefix(dir, "/broken/") {
			return workspaceInfo{}, fmt.Errorf("failed to get workspace root: exec: \"jj\": executable file not found in $PATH")
		}
		return fake(dir)
	}
	hook := func(session, cwd string) string {
		e.warnings.Reset()
		agentStateWarned = false // each hook event is a new process
		e.advance(time.Minute)
		require.NoError(t, runAgentHook(strings.NewReader(fmt.Sprintf(`{"session_id":%q,"cwd":%q}`, session, cwd)), "PostToolUse", "claude"))
		return e.warnings.String()
	}

	assert.Contains(t, hookWarning(t, hook("a", "/broken/a")), "/broken/a")
	for range 2 {
		assert.Empty(t, hook("b", "/broken/b"), "the same jj failure in another directory is the same problem")
		assert.Empty(t, hook("a", "/broken/a"))
	}

	home, _ := os.UserHomeDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".wgo", "state.json"), fmt.Appendf(nil, `{"version":%d}`, store.StateVersion+1), 0o644))
	assert.Contains(t, hookWarning(t, hook("c", "/ws")), "Upgrade wgo", "a different problem is shown within the hour")
	assert.Empty(t, hook("a", "/broken/a"), "and does not displace the first")

	e.advance(-2 * hookWarnEvery)
	assert.Contains(t, hookWarning(t, hook("a", "/broken/a")), "/broken/a", "a marker from the future does not suppress it")
}
