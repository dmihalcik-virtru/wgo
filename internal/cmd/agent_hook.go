package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/proc"
	"github.com/virtru/wgo/internal/store"
)

var agentHookTool string

// hookLockTimeout bounds how long a hook waits for another wgo process to
// release state.json's lock. Synchronous agent hooks hold up the triggering
// action, so a wedged wgo delays it by at most this lock wait.
var hookLockTimeout = 2 * time.Second

// hookRemoveLockTimeout is hookLockTimeout for Claude Code's SessionEnd. It is
// the last event of a session, so no later one retries a removal it gives up
// on, and it makes no jj calls, so it can spend most of the hook timeout
// waiting.
var hookRemoveLockTimeout = 8 * time.Second

// Codex caps SessionEnd command hooks at 3 seconds. Leave time for process
// startup and state I/O after waiting for the lock.
const codexHookRemoveLockTimeout = 2 * time.Second

var agentHookCmd = &cobra.Command{
	Use:   "hook [event]",
	Short: "Update agent sessions from an agent hook JSON event on stdin",
	Long: `Update agent sessions from a Claude Code, Codex, or Antigravity hook.

The agent passes hook data as JSON on stdin; wgo reads session_id (or
Antigravity's conversationId), cwd (falling back to workspacePaths) and
hook_event_name (or hookEventName) from it, plus tool_name/tool_input or
toolCall for file edits, and ignores everything else. Hook data is never placed
on a command line or passed to a shell. The event argument overrides
hook_event_name when given. Pass --tool codex for Codex hooks or
--tool antigravity for Antigravity hooks.

  SessionStart, Stop, Notification,
  Interrupt                             the session is waiting for input
  UserPromptSubmit, PreInvocation,
  PreToolUse, PostToolUse, SubagentStop the session is working
  SessionEnd                            the session is removed
  any other event                       refreshes the session's activity

The session is created on its first event, in the jj workspace containing cwd,
and records the agent process (found by walking up from the hook's parent) so
a quiet session stays visible while the agent is running. If that process has
exited, the next event finds the new one.

Malformed input, a missing or invalid session_id, or no event exits non-zero.
A cwd that holds jj workspaces without being one, such as a shared root from
wgo add with several repos or a rig root, records the session on that folder;
a file edit in one of its workspaces then records that workspace's repository
and bookmark, so conflicts with other sessions are still found.
Any other cwd outside a jj workspace is skipped silently (logged under
WGO_DEBUG=1).
A state file wgo cannot read or write (for example one written by a newer
wgo), or a workspace jj fails on (for example because jj is not on the hook's
PATH), is reported as a JSON systemMessage on stdout on PostToolUse and
UserPromptSubmit (or injectSteps on Antigravity PreInvocation), at most once an
hour for the same problem across all sessions. For Claude Code and Codex the
command otherwise prints nothing on stdout; for Antigravity it prints the JSON
response object the hook contract requires ({} or {"decision":"allow"}). Either
way it exits 0, so it never disrupts the agent.

The jj lookups (run with --ignore-working-copy, so they never snapshot)
share a 2s budget, and the wait for the state file's lock is also 2s (8s for
Claude Code SessionEnd, 2s for Codex SessionEnd, which are never retried).
See contrib/claude-code-hooks.md, contrib/codex-hooks.md, and
contrib/antigravity-hooks.md.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		event := ""
		if len(args) == 1 {
			event = args[0]
		}
		return runAgentHook(os.Stdin, event, agentHookTool)
	},
}

func init() {
	agentCmd.AddCommand(agentHookCmd)
	agentHookCmd.Flags().StringVar(&agentHookTool, "tool", "claude", "agent tool sending the hook")
}

// hookPayload is the subset of Claude Code, Codex, and Antigravity hook JSON
// that wgo uses. Antigravity encodes hook payloads as camelCase protojson
// (conversationId, hookEventName, workspacePaths, toolCall). Unknown fields
// (tool_response, ...) are ignored.
type hookPayload struct {
	SessionID          string   `json:"session_id"`
	ConversationID     string   `json:"conversationId"`
	HookEventName      string   `json:"hook_event_name"`
	HookEventNameCamel string   `json:"hookEventName"`
	Cwd                string   `json:"cwd"`
	WorkspacePaths     []string `json:"workspacePaths"`
	ToolName           string   `json:"tool_name"`
	// ToolInput and ToolCall are decoded only for the file tools (see
	// editedPath), and leniently: a shape wgo does not expect must not fail
	// the hook.
	ToolInput json.RawMessage `json:"tool_input"`
	ToolCall  json.RawMessage `json:"toolCall"`
}

func (p hookPayload) sessionID() string {
	if p.SessionID != "" {
		return p.SessionID
	}
	return p.ConversationID
}

func (p hookPayload) eventName() string {
	if p.HookEventName != "" {
		return p.HookEventName
	}
	return p.HookEventNameCamel
}

// fileEditTools are the Claude Code tools whose tool_input names the file
// they changed. Reads are left out: looking at another workspace is not
// working in it.
var fileEditTools = map[string]bool{"Edit": true, "MultiEdit": true, "Write": true, "NotebookEdit": true}

// antigravityFileEditTools are the Antigravity tools whose toolCall.args.TargetFile
// names the file they changed. The older edit_file/write_file names use Path.
var antigravityFileEditTools = map[string]bool{
	"write_to_file": true, "replace_file_content": true, "multi_replace_file_content": true,
	"edit_file": true, "write_file": true,
}

// editedPath returns the absolute path a file tool changed, or "". It is only
// ever compared with and stat'ed under the session's folder (see
// editedWorkspace), never run or passed to jj.
func (p hookPayload) editedPath() string {
	var path string
	if fileEditTools[p.ToolName] && len(p.ToolInput) > 0 {
		var in struct {
			FilePath     string `json:"file_path"`
			NotebookPath string `json:"notebook_path"`
		}
		if json.Unmarshal(p.ToolInput, &in) == nil {
			path = in.FilePath
			if path == "" {
				path = in.NotebookPath
			}
		}
	}
	if path == "" && len(p.ToolCall) > 0 {
		var tc struct {
			Name string `json:"name"`
			Args struct {
				TargetFile string `json:"TargetFile"`
				Path       string `json:"Path"`
			} `json:"args"`
		}
		if json.Unmarshal(p.ToolCall, &tc) == nil && antigravityFileEditTools[tc.Name] {
			path = tc.Args.TargetFile
			if path == "" {
				path = tc.Args.Path
			}
		}
	}
	if !filepath.IsAbs(path) {
		return ""
	}
	return filepath.Clean(path)
}

// editedWorkspace returns the workspace under root that path lies in, when
// root is a folder of workspaces rather than a workspace itself, or "". It
// walks up from path to root looking for a .jj directory, so it costs a few
// stats and no jj call.
func editedWorkspace(root, path string) string {
	if root == "" || path == "" {
		return ""
	}
	if rel, err := filepath.Rel(root, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	if info, err := os.Stat(filepath.Join(root, ".jj")); err == nil && info.IsDir() {
		return "" // root is the workspace; there is nothing to narrow down
	}
	for dir := filepath.Dir(path); dir != root && len(dir) > len(root); dir = filepath.Dir(dir) {
		if info, err := os.Stat(filepath.Join(dir, ".jj")); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}

// hookAction is what an event does to its session.
type hookAction struct {
	status  store.AgentStatus // "" keeps the current status
	remove  bool
	refresh bool // re-read the workspace and bookmark from cwd
}

func hookActionFor(event string) hookAction {
	switch strings.ToLower(event) {
	case "sessionend":
		return hookAction{remove: true}
	case "sessionstart":
		return hookAction{status: store.AgentWaiting, refresh: true}
	case "stop":
		return hookAction{status: store.AgentWaiting, refresh: true}
	case "notification", "interrupt":
		return hookAction{status: store.AgentWaiting}
	case "userpromptsubmit", "preinvocation":
		return hookAction{status: store.AgentWorking, refresh: true}
	case "pretooluse", "posttooluse", "subagentstop":
		return hookAction{status: store.AgentWorking}
	}
	return hookAction{}
}

// errHookSkipped marks an event that was deliberately not recorded, such as
// one from a directory outside any jj workspace. It is expected, so it is
// only logged under WGO_DEBUG.
var errHookSkipped = errors.New("skipped")

// runAgentHook applies one hook event. Only unusable input is an error; every
// internal failure is swallowed, because a failing hook must not break the
// user's agent session. Expected skips are logged via debugf; state problems
// are reported to the user (warnAgentState), because they silently disable
// session tracking.
func runAgentHook(r io.Reader, event, tool string) error {
	var p hookPayload
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		return fmt.Errorf("agent hook: stdin is not hook JSON: %w", err)
	}
	p.SessionID = p.sessionID()
	if event == "" {
		event = p.eventName()
	}
	if event == "" {
		return fmt.Errorf("agent hook: no event: pass one (wgo agent hook PostToolUse) or include hook_event_name")
	}
	if !store.ValidAgentSessionID(p.SessionID) {
		return fmt.Errorf("agent hook: missing or invalid session_id %q", p.SessionID)
	}
	tool = store.NormalizeAgentTool(tool)
	if tool == "" {
		tool = "claude"
	}
	act := hookActionFor(event)
	if act == (hookAction{}) {
		debugf("agent hook: unrecognized event %q; treating it as activity", event)
	}
	err := applyHook(p, act, tool)
	wroteStdout := false
	switch {
	case errors.Is(err, errHookSkipped):
		debugf("agent hook %s: %v", event, err)
	case err != nil:
		wroteStdout = warnAgentStateForTool(tool, event, err)
	}
	if tool == "antigravity" && !wroteStdout {
		writeAntigravityHookResult(event)
	}
	return nil
}

// writeAntigravityHookResult writes the single JSON object Antigravity expects
// on stdout for every hook invocation. Stop and PreToolUse require a "decision"
// field ("allow" lets Stop finish the turn and PreToolUse proceed); all other
// events accept an empty object.
func writeAntigravityHookResult(event string) {
	switch strings.ToLower(event) {
	case "stop", "pretooluse":
		fmt.Fprintln(agentWarnOut, `{"decision":"allow"}`)
	default:
		fmt.Fprintln(agentWarnOut, `{}`)
	}
}

// resolveHookWorkspace resolves p.Cwd to a jj workspace, falling back to
// p.WorkspacePaths when Cwd is empty or lies outside any jj workspace (for
// example when Antigravity runs in its scratch directory while a jj workspace
// is attached to the conversation).
func resolveHookWorkspace(p hookPayload) (ws workspaceInfo, dir string, err error) {
	dir = p.Cwd
	if dir == "" && len(p.WorkspacePaths) > 0 {
		dir = p.WorkspacePaths[0]
	}
	if dir == "" {
		if dir, err = resolveCwd(); err != nil {
			return workspaceInfo{}, "", err
		}
	}
	ws, err = lookupWorkspace(dir)
	if errors.Is(err, errNotJJRepo) {
		for _, wp := range p.WorkspacePaths {
			if wp == "" || wp == dir {
				continue
			}
			if altWS, altErr := lookupWorkspace(wp); !errors.Is(altErr, errNotJJRepo) {
				return altWS, wp, altErr
			}
		}
	}
	return ws, dir, err
}

func applyHook(p hookPayload, act hookAction, tool string) error {
	s, err := store.New()
	if err != nil {
		return err
	}
	if act.remove {
		lockTimeout := hookRemoveLockTimeout
		if tool == "codex" && lockTimeout > codexHookRemoveLockTimeout {
			lockTimeout = codexHookRemoveLockTimeout
		}
		err := s.MutateStateWithin(lockTimeout, func(state *store.State) (bool, error) {
			return state.RemoveAgentSession(p.SessionID), nil
		})
		return lockProblem(s, err, "the session stays listed until wgo sees its agent exit")
	}

	// A lock-free read decides whether this event needs a write at all, and
	// whether the slower lookups (jj subprocesses, process walk) are needed.
	snapshot, err := s.LoadState()
	if err != nil {
		return err
	}
	existing := snapshot.GetAgentSession(p.SessionID)
	edited := p.editedPath()
	// focus is a workspace this event edited in, when it is not the one the
	// session already records.
	focus := ""
	if existing != nil {
		if f := editedWorkspace(existing.WorktreePath, edited); f != existing.Focus {
			focus = f
		}
	}
	if existing != nil && !act.refresh && focus == "" &&
		(act.status == "" || act.status == existing.Status) &&
		agentNow().Sub(existing.LastActivity) < heartbeatThrottle {
		return nil
	}

	var ws workspaceInfo
	if existing == nil || act.refresh {
		var dir string
		ws, dir, err = resolveHookWorkspace(p)
		switch {
		case err == nil:
		case existing != nil:
			debugf("agent hook: session %s: cannot resolve workspace for %s: %v; keeping %s",
				p.SessionID, dir, err, existing.WorktreePath)
			ws = workspaceInfo{}
		case errors.Is(err, errNotJJRepo):
			return fmt.Errorf("not tracking session %s: %s: %v: %w", p.SessionID, dir, err, errHookSkipped)
		case errors.Is(err, context.DeadlineExceeded):
			// jj is slow or stuck. A later event retries, so this is
			// reported, but not as a PATH problem.
			return &hookProblem{
				key: "jj: timed out",
				err: fmt.Errorf("jj did not resolve the workspace at %s within %s; will retry on the next event: %w", dir, workspaceLookupTimeout, err),
			}
		default:
			// dir is in a jj workspace, but jj failed on it: it is missing from
			// the agent hook's PATH, too old, or misconfigured.
			// The warning is keyed on jj's error, not dir, so every session it
			// fails for shares one hourly warning.
			return &hookProblem{
				key: "jj: " + jj.BriefError(err),
				err: fmt.Errorf("cannot resolve the jj workspace at %s (check that jj is on the hook's PATH): %w", dir, err),
			}
		}
	}
	switch {
	case existing == nil:
		focus = editedWorkspace(ws.Root, edited)
	case focus == "" && act.refresh && ws.Root != "" && ws.Repo == "":
		// Still in the folder: re-read the edited workspace's bookmark, as a
		// refresh does for a session in a workspace. Resolving the folder
		// took no jj call, so this stays within one lookup's budget.
		focus = existing.Focus
	}
	// The workspace an edit landed in lends the session its repository and
	// bookmark, so AgentConflicts sees what the agent is changing.
	var focusWS workspaceInfo
	if focus != "" {
		if focusWS, err = lookupWorkspace(focus); err != nil {
			debugf("agent hook: session %s: cannot resolve edited workspace %s: %v", p.SessionID, focus, err)
			focusWS = workspaceInfo{}
		}
	}
	// Find the agent process when none is recorded, or when the recorded one
	// is no longer running: claude --resume keeps the session ID but starts a
	// new process, and a stale identity would get the session pruned as soon
	// as it goes quiet.
	var pid int
	var start int64
	if existing == nil || proc.Check(procInspector, existing.PID, existing.ProcStart) != proc.Running {
		pid, start = agentProcess(tool, 0)
	}

	err = s.MutateStateWithin(hookLockTimeout, func(state *store.State) (bool, error) {
		now := agentNow()
		cur := state.GetAgentSession(p.SessionID)
		if existing != nil && cur == nil {
			// The session was removed between the snapshot and the lock, by its
			// SessionEnd or `wgo agent stop`: this event was in flight when the
			// session ended and must not resurrect it, whatever the event. (If
			// another wgo pruned it instead, only this event is lost; the next
			// one recreates the session. A later resume starts from a snapshot
			// without the session, so it is recorded again.)
			return false, fmt.Errorf("session %s was removed while this event was in flight; not recreating it: %w", p.SessionID, errHookSkipped)
		}
		sess := store.AgentSession{
			ID: p.SessionID, Tool: tool, Source: store.SourceHook, Status: act.status,
			WorktreePath: ws.Root, RepoPath: ws.Repo, Branch: ws.Branch,
			PID: pid, ProcStart: start,
		}
		if sess.WorktreePath == "" && cur != nil {
			sess.WorktreePath = cur.WorktreePath
		}
		if focusWS.Root != "" {
			sess.Focus, sess.RepoPath, sess.Branch = focusWS.Root, focusWS.Repo, focusWS.Branch
		}
		// Merge before pruning: this event shows the session is in use, so one
		// quiet long enough to be prunable keeps its start time, effort, repo
		// and bookmark rather than being deleted and recreated without them.
		saved, err := state.UpsertAgentSession(sess, now)
		if err != nil {
			return false, err
		}
		if ws.Root != "" && ws.BranchKnown {
			// A fresh lookup is authoritative, including "no bookmark", which
			// the merge in UpsertAgentSession would otherwise ignore.
			state.SetAgentBookmark(saved.ID, ws.Branch)
		}
		if focusWS.Root != "" && focusWS.BranchKnown {
			state.SetAgentBookmark(saved.ID, focusWS.Branch)
		}
		if ws.Repo != "" && saved.Focus != "" {
			// The session moved into a workspace, which it now records itself.
			state.ClearAgentFocus(saved.ID)
		}
		if pid == 0 && proc.Check(procInspector, saved.PID, saved.ProcStart) == proc.Exited {
			// The recorded process is gone and the walk found no new one (a
			// resumed claude under a launcher wgo does not recognize, say).
			// The merge kept the dead identity; drop it, so the session turns
			// uncertain when quiet instead of being pruned as exited.
			state.ClearAgentProcess(saved.ID)
		}
		state.PruneAgentSessions(now, agentPolicy())
		dropInferredDuplicate(state, tool, saved.WorktreePath, saved.PID, saved.ProcStart)
		return true, nil
	})
	return lockProblem(s, err, "the next event retries")
}

// lockProblem turns a timed-out wait for the state lock into a hookProblem
// that says what became of the event (then) and how to find the process
// holding the lock. Every timeout shares one key, so it warns once an hour
// however the path or timeout is spelled. Other errors pass through.
func lockProblem(s *store.FileStore, err error, then string) error {
	if !errors.Is(err, store.ErrStateLocked) {
		return err
	}
	return &hookProblem{
		key: "state: locked",
		err: fmt.Errorf("%w; this event was not recorded and %s (find the process holding the lock with: lsof %s)", err, then, s.LockPath()),
	}
}
