package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/proc"
	"github.com/virtru/wgo/internal/store"
)

var agentHookTool string

var agentHookCmd = &cobra.Command{
	Use:   "hook [event]",
	Short: "Update agent sessions from Claude Code hook JSON on stdin",
	Long: `Update agent sessions from a Claude Code hook.

Claude Code passes hook data as JSON on stdin; wgo reads session_id, cwd and
hook_event_name from it and ignores everything else. Hook data is never placed
on a command line or passed to a shell. The event argument overrides
hook_event_name when given.

  SessionStart, Stop, Notification      the session is waiting for input
  UserPromptSubmit, PreToolUse,
  PostToolUse, SubagentStop             the session is working
  SessionEnd                            the session is removed
  any other event                       refreshes the session's activity

The session is created on its first event, in the jj workspace containing cwd,
and records the Claude Code process (found by walking up from the hook's
parent) so a quiet session stays visible while Claude is running. If that
process has exited (claude --resume keeps the session but starts a new
process), the next event finds the new one.

The command prints nothing on stdout. Malformed input, a missing or invalid
session_id, or no event exits non-zero. A cwd outside any jj workspace is
skipped silently (logged under WGO_DEBUG=1). A state file wgo cannot read or
write (for example one written by a newer wgo) is reported on stderr. Either
way the command exits 0, so it never disrupts the agent. See
contrib/claude-code-hooks.md.`,
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

// hookPayload is the subset of Claude Code's hook JSON that wgo uses.
// Unknown fields (tool_input, tool_response, ...) are ignored.
type hookPayload struct {
	SessionID     string `json:"session_id"`
	HookEventName string `json:"hook_event_name"`
	Cwd           string `json:"cwd"`
}

// hookAction is what an event does to its session.
type hookAction struct {
	status  store.AgentStatus // "" keeps the current status
	remove  bool
	refresh bool // re-read the workspace and bookmark from cwd
	known   bool // the event is one wgo recognizes
}

func hookActionFor(event string) hookAction {
	act := hookActionForKnown(event)
	act.known = act != (hookAction{})
	return act
}

func hookActionForKnown(event string) hookAction {
	switch strings.ToLower(event) {
	case "sessionend":
		return hookAction{remove: true}
	case "sessionstart":
		return hookAction{status: store.AgentWaiting, refresh: true}
	case "stop":
		return hookAction{status: store.AgentWaiting, refresh: true}
	case "notification":
		return hookAction{status: store.AgentWaiting}
	case "userpromptsubmit":
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
// are reported on stderr (warnAgentState), because they silently disable
// session tracking.
func runAgentHook(r io.Reader, event, tool string) error {
	var p hookPayload
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		return fmt.Errorf("agent hook: stdin is not hook JSON: %w", err)
	}
	if event == "" {
		event = p.HookEventName
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
	if !act.known {
		debugf("agent hook: unrecognized event %q; treating it as activity", event)
	}
	err := applyHook(p, act, tool)
	switch {
	case errors.Is(err, errHookSkipped):
		debugf("agent hook %s: %v", event, err)
	case err != nil:
		warnAgentState("agent hook "+event, err)
	}
	return nil
}

func applyHook(p hookPayload, act hookAction, tool string) error {
	s, err := store.New()
	if err != nil {
		return err
	}
	if act.remove {
		return s.MutateState(func(state *store.State) (bool, error) {
			return state.RemoveAgentSession(p.SessionID), nil
		})
	}

	// A lock-free read decides whether this event needs a write at all, and
	// whether the slower lookups (jj subprocesses, process walk) are needed.
	snapshot, err := s.LoadState()
	if err != nil {
		return err
	}
	existing := snapshot.GetAgentSession(p.SessionID)
	if existing != nil && !act.refresh &&
		(act.status == "" || act.status == existing.Status) &&
		agentNow().Sub(existing.LastActivity) < heartbeatThrottle {
		return nil
	}

	var ws workspaceInfo
	if existing == nil || act.refresh {
		dir := p.Cwd
		if dir == "" {
			if dir, err = resolveCwd(); err != nil {
				return err
			}
		}
		ws, err = lookupWorkspace(dir)
		if err != nil {
			if existing == nil {
				return fmt.Errorf("not tracking session %s: %s: %v: %w", p.SessionID, dir, err, errHookSkipped)
			}
			debugf("agent hook: session %s: cannot resolve workspace for %s: %v; keeping %s",
				p.SessionID, dir, err, existing.WorktreePath)
			ws = workspaceInfo{}
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

	return s.MutateState(func(state *store.State) (bool, error) {
		now := agentNow()
		// Read the session before pruning: an event from a session that was
		// quiet long enough to be pruned still knows its workspace.
		cur := state.GetAgentSession(p.SessionID)
		state.PruneAgentSessions(now, agentPolicy())
		sess := store.AgentSession{
			ID: p.SessionID, Tool: tool, Source: store.SourceHook, Status: act.status,
			WorktreePath: ws.Root, RepoPath: ws.Repo, Branch: ws.Branch,
			PID: pid, ProcStart: start,
		}
		if sess.WorktreePath == "" {
			if cur == nil {
				// The session ended (SessionEnd) between the snapshot and the
				// lock; an in-flight event must not resurrect it.
				return false, fmt.Errorf("session %s ended; not recreating it: %w", p.SessionID, errHookSkipped)
			}
			sess.WorktreePath = cur.WorktreePath
		}
		saved, err := state.UpsertAgentSession(sess, now)
		if err != nil {
			return false, err
		}
		if ws.Root != "" && !ws.BranchUnknown {
			// A fresh lookup is authoritative, including "no bookmark", which
			// the merge in UpsertAgentSession would otherwise ignore.
			state.SetAgentBookmark(saved.ID, ws.Branch)
		}
		dropInferredDuplicate(state, tool, saved.WorktreePath)
		return true, nil
	})
}
