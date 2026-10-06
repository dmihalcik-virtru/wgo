package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/proc"
	"github.com/virtru/wgo/internal/store"
	"github.com/virtru/wgo/models"
)

const (
	// heartbeatThrottle bounds how often a heartbeat (the statusline's
	// env-detected one, or a hook's PostToolUse) rewrites state.json: a
	// session touched within this window with nothing new to record is left
	// alone.
	heartbeatThrottle = 60 * time.Second
	// agentStaleAfter is the quiet window. A session with activity inside it
	// is active. Past it, a session with a verified live process stays live; a
	// session whose recorded process exited, or an inferred session, is
	// removed; any other session (no process identity, or one that cannot be
	// checked) becomes uncertain.
	agentStaleAfter = 10 * time.Minute
	// agentMaxAge bounds how long a session without a verifiable process is
	// kept after its last activity, so abandoned sessions cannot accumulate.
	agentMaxAge = 24 * time.Hour
	// agentAncestorDepth bounds the walk up the process tree when looking for
	// the agent process behind a hook or `agent start`.
	agentAncestorDepth = 16
)

// Seams for tests: the clock, the process table and the starting point of
// the ancestor walk.
var (
	agentNow                      = time.Now
	procInspector  proc.Inspector = proc.System()
	agentParentPID                = os.Getppid
)

func agentPolicy() store.AgentPolicy {
	return store.AgentPolicy{Window: agentStaleAfter, MaxAge: agentMaxAge, Procs: procInspector}
}

// Flag values, one set per subcommand so their defaults never interfere.
var (
	startOpts        agentStartOpts
	heartbeatSession string
	heartbeatStatus  string
	stopSession      string
	agentStatusJSON  bool
)

// agentCmd is the parent for agent-session tracking.
var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Track which AI agent is working in which workspace",
	Long: `Record and inspect AI agent sessions across workspaces.

Each session has its own ID, so several agents can share a workspace or an
effort. Sessions come from three places:

  wgo agent start      an explicit session, for any tool or script
  wgo agent hook       Claude Code hooks (see contrib/claude-code-hooks.md)
  wgo . / statusline   inside Claude Code (CLAUDECODE set), a heartbeat that
                       refreshes the workspace's session, or keeps a single
                       inferred one when none exists

Liveness: a session with activity in the last 10 minutes is active. After that,
a session whose agent process is verified alive (PID and start time match)
stays live however quiet it is; a session whose recorded process has exited,
and an inferred session, are removed; any other session (no recorded process,
or one wgo cannot check) is shown as uncertain and removed after 24 hours or by
"wgo agent stop".`,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runAgentStatus(os.Stdout, false)
	},
}

var agentStartCmd = &cobra.Command{
	Use:   "start [tool]",
	Short: "Record an agent session for the current workspace",
	Long: `Record an agent session for the current workspace.

The tool (claude, codex, cursor, ...) defaults to the detected agent when wgo
runs inside one. Without --session an ID such as "claude-3f9a2c" is generated.

Scripts should pass --json and read the "id" field rather than parse the
human-readable line:

  id=$(wgo agent start codex --json | jq -r .id)
  ...
  wgo agent stop --session "$id"

wgo looks up the tool's process by walking up from its parent process, and
records its PID and start time so a quiet but running agent is never expired.
Use --pid when the tool reports its own PID.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		o := startOpts
		if len(args) == 1 {
			o.Tool = args[0]
		}
		return runAgentStart(os.Stdout, o)
	},
}

var agentHeartbeatCmd = &cobra.Command{
	Use:   "heartbeat",
	Short: "Refresh an agent session and optionally set its status",
	Long: `Refresh an agent session's last activity, and optionally its status.

--session may be omitted only when exactly one session exists in the current
workspace.`,
	Args: cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runAgentHeartbeat(os.Stdout, heartbeatSession, heartbeatStatus)
	},
}

var agentStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Clear an agent session",
	Long: `Clear an agent session.

--session may be omitted only when exactly one session exists in the current
workspace; otherwise the candidates are listed.`,
	Args: cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runAgentStop(os.Stdout, stopSession)
	},
}

var agentStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show agent sessions across workspaces",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runAgentStatus(os.Stdout, agentStatusJSON)
	},
}

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.AddCommand(agentStartCmd, agentHeartbeatCmd, agentStopCmd, agentStatusCmd)

	agentStartCmd.Flags().StringVar(&startOpts.Session, "session", "", "session ID (default: generated, e.g. claude-3f9a2c)")
	agentStartCmd.Flags().StringVar(&startOpts.Theme, "theme", "", "effort or theme this session works on")
	agentStartCmd.Flags().StringVar(&startOpts.Status, "status", string(store.AgentWorking), "initial status: working, waiting or idle")
	agentStartCmd.Flags().IntVar(&startOpts.PID, "pid", 0, "PID of the agent process, when the tool provides it")
	agentStartCmd.Flags().BoolVar(&startOpts.JSON, "json", false, "print the session as JSON (machine-readable; includes the id)")

	agentHeartbeatCmd.Flags().StringVar(&heartbeatSession, "session", "", "session ID")
	agentHeartbeatCmd.Flags().StringVar(&heartbeatStatus, "status", "", "new status: working, waiting or idle")

	agentStopCmd.Flags().StringVar(&stopSession, "session", "", "session ID")

	agentStatusCmd.Flags().BoolVar(&agentStatusJSON, "json", false, "print sessions as JSON")
}

// workspaceRoot resolves the current workspace root, matching the key
// buildContextOpts uses when populating ctx.Agent. A variable so tests can
// run the commands without a jj workspace.
var workspaceRoot = func() (string, error) {
	cwd, err := resolveCwd()
	if err != nil {
		return "", err
	}
	return workspaceRootOf(cwd)
}

func workspaceRootOf(dir string) (string, error) {
	jjc := jj.NewCLI()
	if !jjc.IsRepo(dir) {
		return "", fmt.Errorf("not a jj repository")
	}
	wsRoot, err := jjc.Root(dir)
	if err != nil {
		return "", fmt.Errorf("failed to get workspace root: %w", err)
	}
	return wsRoot, nil
}

// workspaceInfo is what a new session records about where it runs.
type workspaceInfo struct {
	Root, Repo, Branch string
	// BranchUnknown is set when the bookmark lookup failed, so Branch ""
	// does not mean "no bookmark".
	BranchUnknown bool
}

// lookupWorkspace resolves the workspace root, its repository's main root and
// the current bookmark for dir. These are jj subprocess calls, so callers on
// a hot path (hook PostToolUse) only make them when creating a session.
var lookupWorkspace = func(dir string) (workspaceInfo, error) {
	wsRoot, err := workspaceRootOf(dir)
	if err != nil {
		return workspaceInfo{}, err
	}
	jjc := jj.NewCLI()
	repo, err := jjc.MainWorkspaceRoot(wsRoot)
	if err != nil {
		debugf("agent: main workspace root for %s: %v; using the workspace itself, so conflicts with other workspaces of this repo go undetected", wsRoot, err)
		repo = wsRoot
	}
	branch, known := bookmarkOf(jjc, wsRoot)
	return workspaceInfo{Root: wsRoot, Repo: repo, Branch: branch, BranchUnknown: !known}, nil
}

// bookmarkOf returns the workspace's nearest bookmark ("" for none). ok is
// false when the lookup failed (jj busy, say), so callers keep what they
// recorded instead of treating the failure as "no bookmark".
func bookmarkOf(jjc jj.Client, workspacePath string) (bookmark string, ok bool) {
	bm, err := jjc.NearestBookmark(workspacePath)
	if err != nil {
		debugf("agent: bookmark for %s: %v", workspacePath, err)
		return "", false
	}
	return bm, true
}

var currentWorkspace = func() (workspaceInfo, error) {
	cwd, err := resolveCwd()
	if err != nil {
		return workspaceInfo{}, err
	}
	return lookupWorkspace(cwd)
}

// agentProcess finds the agent's own process: the given PID when the tool
// provides one, otherwise the nearest ancestor whose name (or, for script
// runtimes, arguments) matches the tool. It returns zeros when there is no
// reliable identity, which leaves liveness to the quiet-time rules.
func agentProcess(tool string, pid int) (int, int64) {
	if pid > 0 {
		info, err := procInspector.Lookup(pid)
		if err != nil {
			debugf("agent: --pid %d: %v", pid, err)
			return 0, 0
		}
		return info.PID, info.Start
	}
	info, ok, err := proc.FindAncestor(procInspector, agentParentPID(), agentAncestorDepth, func(i proc.Info) bool {
		return proc.MatchesTool(procInspector, i, tool)
	})
	if err != nil {
		debugf("agent: process walk for %s failed (%v); recording no process identity", tool, err)
		return 0, 0
	}
	if !ok {
		debugf("agent: no %s process among ancestors; recording no process identity", tool)
		return 0, 0
	}
	return info.PID, info.Start
}

type agentStartOpts struct {
	Tool, Session, Theme, Status string
	PID                          int
	JSON                         bool
}

func runAgentStart(w io.Writer, o agentStartOpts) error {
	tool := store.NormalizeAgentTool(o.Tool)
	if tool == "" {
		tool = detectAgent()
	}
	if tool == "" {
		return fmt.Errorf("agent name must not be empty: name the tool, e.g. wgo agent start claude")
	}
	if o.Session != "" && !store.ValidAgentSessionID(o.Session) {
		return fmt.Errorf("invalid --session %q: use letters, digits, '.', '_', ':' or '-' (max 128)", o.Session)
	}
	status := store.AgentWorking
	if o.Status != "" {
		st, err := store.ParseAgentStatus(o.Status)
		if err != nil {
			return err
		}
		status = st
	}
	ws, err := currentWorkspace()
	if err != nil {
		return err
	}
	pid, start := agentProcess(tool, o.PID)

	s, err := store.New()
	if err != nil {
		return err
	}
	var saved store.AgentSession
	var conflicts []string
	err = s.MutateState(func(state *store.State) (bool, error) {
		now := agentNow()
		state.PruneAgentSessions(now, agentPolicy())
		id := o.Session
		for id == "" || (o.Session == "" && state.GetAgentSession(id) != nil) {
			id = store.NewAgentSessionID(tool)
		}
		sess, err := state.UpsertAgentSession(store.AgentSession{
			ID: id, Tool: tool, ThemeID: o.Theme, Status: status, Source: store.SourceExplicit,
			WorktreePath: ws.Root, RepoPath: ws.Repo, Branch: ws.Branch,
			PID: pid, ProcStart: start,
		}, now)
		if err != nil {
			return false, err
		}
		dropInferredDuplicate(state, tool, ws.Root)
		saved = sess
		conflicts = store.AgentConflicts(state.ObserveAgentSessions(now, agentPolicy()))[sess.ID]
		return true, nil
	})
	if err != nil {
		return err
	}
	if o.JSON {
		return writeJSON(w, sessionJSON(store.ObservedSession{AgentSession: saved, Liveness: store.LivenessActive}, conflicts))
	}
	fmt.Fprintf(w, "🤖 %s %s in %s\n", saved.Tool, saved.Status, saved.WorktreePath)
	fmt.Fprintf(w, "   session %s (stop with: wgo agent stop --session %s)\n", saved.ID, saved.ID)
	if len(conflicts) > 0 {
		fmt.Fprintf(w, "   ⚠ possible conflict: %s also active on %s\n", strings.Join(conflicts, ", "), saved.Branch)
	}
	return nil
}

// dropInferredDuplicate removes the statusline's inferred session for a tool
// and workspace once a managed session exists there, so the two never both
// appear.
func dropInferredDuplicate(state *store.State, tool, wsRoot string) bool {
	return state.RemoveAgentSession(store.InferredAgentSessionID(tool, wsRoot))
}

// selectSession resolves an explicit or implied session ID. An omitted ID is
// accepted only when exactly one session exists in the current workspace.
// found is false when an explicit ID names no session.
func selectSession(state *store.State, id string, wsRoot func() (string, error)) (sess store.AgentSession, found bool, err error) {
	if id != "" {
		if got := state.GetAgentSession(id); got != nil {
			return *got, true, nil
		}
		return store.AgentSession{}, false, nil
	}
	root, err := wsRoot()
	if err != nil {
		return store.AgentSession{}, false, fmt.Errorf("%w; pass --session to name the session", err)
	}
	candidates := state.AgentSessionsIn(root)
	switch len(candidates) {
	case 0:
		return store.AgentSession{}, false, nil
	case 1:
		return candidates[0], true, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d agent sessions in %s; choose one with --session:", len(candidates), root)
	for _, c := range candidates {
		fmt.Fprintf(&b, "\n  %s  %s %s, since %s", c.ID, c.Tool, c.Status, formatTime(c.StartTime))
	}
	return store.AgentSession{}, false, fmt.Errorf("%s", b.String())
}

func runAgentHeartbeat(w io.Writer, id, status string) error {
	var st store.AgentStatus
	if status != "" {
		parsed, err := store.ParseAgentStatus(status)
		if err != nil {
			return err
		}
		st = parsed
	}
	if id != "" && !store.ValidAgentSessionID(id) {
		return fmt.Errorf("invalid --session %q", id)
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	var saved store.AgentSession
	err = s.MutateState(func(state *store.State) (bool, error) {
		sess, found, err := selectSession(state, id, workspaceRoot)
		if err != nil {
			return false, err
		}
		if !found {
			if id == "" {
				return false, fmt.Errorf("no agent session in this workspace; start one with: wgo agent start <tool>")
			}
			return false, fmt.Errorf("no agent session %s; start one with: wgo agent start --session %s <tool>", id, id)
		}
		now := agentNow()
		sess.Status = st
		saved, err = state.UpsertAgentSession(sess, now)
		if err != nil {
			return false, err
		}
		state.PruneAgentSessions(now, agentPolicy())
		return true, nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "🤖 %s %s (session %s)\n", saved.Tool, saved.Status, saved.ID)
	return nil
}

func runAgentStop(w io.Writer, id string) error {
	if id != "" && !store.ValidAgentSessionID(id) {
		return fmt.Errorf("invalid --session %q", id)
	}
	s, err := store.New()
	if err != nil {
		return err
	}
	var removed store.AgentSession
	found := false
	where := id
	err = s.MutateState(func(state *store.State) (bool, error) {
		sess, ok, err := selectSession(state, id, func() (string, error) {
			root, err := workspaceRoot()
			where = root
			return root, err
		})
		if err != nil || !ok {
			return false, err
		}
		removed, found = sess, state.RemoveAgentSession(sess.ID)
		return found, nil
	})
	if err != nil {
		return err
	}
	if !found {
		if id != "" {
			return fmt.Errorf("no agent session %s; list sessions with: wgo agent status", id)
		}
		fmt.Fprintf(w, "no agent session for %s\n", where)
		return nil
	}
	fmt.Fprintf(w, "cleared agent session %s (%s in %s)\n", removed.ID, removed.Tool, removed.WorktreePath)
	return nil
}

// agentSessionJSON is the machine-readable form of a session.
type agentSessionJSON struct {
	ID           string    `json:"id"`
	Tool         string    `json:"tool"`
	Status       string    `json:"status"`
	Liveness     string    `json:"liveness"`
	Source       string    `json:"source"`
	Workspace    string    `json:"workspace"`
	Repo         string    `json:"repo,omitempty"`
	Bookmark     string    `json:"bookmark,omitempty"`
	Theme        string    `json:"theme,omitempty"`
	PID          int       `json:"pid,omitempty"`
	StartTime    time.Time `json:"start_time"`
	LastActivity time.Time `json:"last_activity"`
	Conflicts    []string  `json:"conflicts,omitempty"`
}

func sessionJSON(o store.ObservedSession, conflicts []string) agentSessionJSON {
	return agentSessionJSON{
		ID: o.ID, Tool: o.Tool, Status: string(o.Status), Liveness: string(o.Liveness),
		Source: string(o.Source), Workspace: o.WorktreePath, Repo: o.RepoPath,
		Bookmark: o.Branch, Theme: o.ThemeID, PID: o.PID,
		StartTime: o.StartTime, LastActivity: o.LastActivity, Conflicts: conflicts,
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func runAgentStatus(w io.Writer, asJSON bool) error {
	s, err := store.New()
	if err != nil {
		return err
	}
	state, err := s.LoadState()
	if err != nil {
		return err
	}
	now := agentNow()
	observed := state.ObserveAgentSessions(now, agentPolicy())
	conflicts := store.AgentConflicts(observed)
	// Group by workspace; within one, most recently active first (the order
	// ObserveAgentSessions returns).
	sort.SliceStable(observed, func(i, j int) bool { return observed[i].WorktreePath < observed[j].WorktreePath })

	if asJSON {
		out := make([]agentSessionJSON, 0, len(observed))
		for _, o := range observed {
			out = append(out, sessionJSON(o, conflicts[o.ID]))
		}
		return writeJSON(w, out)
	}
	if len(observed) == 0 {
		fmt.Fprintln(w, "No active agent sessions.")
		return nil
	}

	// Mark the current workspace when it is a jj repo (best-effort).
	current, err := workspaceRoot()
	if err != nil {
		current = ""
	}
	for _, o := range observed {
		branch := o.Branch
		if branch == "" {
			branch = "(no bookmark)"
		}
		var notes []string
		if o.ThemeID != "" {
			notes = append(notes, "theme "+o.ThemeID)
		}
		switch o.Liveness {
		case store.LivenessLive:
			notes = append(notes, fmt.Sprintf("quiet %s, process alive", quietFor(now, o.LastActivity)))
		case store.LivenessUncertain:
			notes = append(notes, fmt.Sprintf("uncertain: quiet %s, no process to check", quietFor(now, o.LastActivity)))
		}
		if o.Source == store.SourceInferred {
			notes = append(notes, "inferred")
		}
		if current != "" && o.WorktreePath == current {
			notes = append(notes, "current")
		}
		line := fmt.Sprintf("🤖 %-8s %-8s %s  %s  since %s  session %s",
			o.Tool, o.Status, o.WorktreePath, branch, formatTime(o.StartTime), o.ID)
		if len(notes) > 0 {
			line += "  (" + strings.Join(notes, "; ") + ")"
		}
		if c := conflicts[o.ID]; len(c) > 0 {
			line += "  ⚠ same bookmark as " + strings.Join(c, ", ")
		}
		fmt.Fprintln(w, line)
	}
	return nil
}

func quietFor(now, last time.Time) string {
	return now.Sub(last).Round(time.Minute).String()
}

// detectAgent returns the agent name when wgo is running inside a recognized
// agent, or "" otherwise. Claude Code sets CLAUDECODE in the environment.
func detectAgent() string {
	if os.Getenv("CLAUDECODE") != "" {
		return "claude"
	}
	return ""
}

// heartbeatAgent is the implicit heartbeat on the `wgo .`/statusline hot path.
// It is env-detected, best-effort, throttled, and local-disk only (no network,
// no subprocess).
//
// branch is the workspace's bookmark: "(no bookmark)" when it has none, and ""
// when the lookup failed, which keeps the recorded bookmark rather than
// clearing it.
//
// When a hook- or command-managed session for the same tool exists in the
// workspace, it refreshes that session (last activity and bookmark, never its
// status) and drops any inferred duplicate. With several, it refreshes the one
// whose recorded process is this heartbeat's own agent, so one agent's
// statusline never keeps another, crashed, session looking active. Otherwise
// it keeps a single inferred session per tool and workspace, which expires on
// the quiet timeout.
func heartbeatAgent(wsRoot, repoPath, branch string) {
	name := detectAgent()
	if name == "" || wsRoot == "" {
		return
	}
	branchKnown := branch != ""
	if branch == "(no bookmark)" {
		branch = ""
	}
	s, err := store.New()
	if err != nil {
		warnAgentState("agent heartbeat", err)
		return
	}
	// MutateState holds a lock across load+save so this hot-path write can't
	// clobber a concurrent state mutation. Returning changed=false (throttled,
	// nothing pruned) skips the write entirely.
	err = s.MutateState(func(state *store.State) (bool, error) {
		now := agentNow()
		changed := state.PruneAgentSessions(now, agentPolicy()) > 0

		var managed []store.AgentSession
		var inferred *store.AgentSession
		for _, sess := range state.AgentSessionsIn(wsRoot) {
			if sess.Tool != name {
				continue
			}
			if sess.Source != store.SourceInferred {
				managed = append(managed, sess)
			} else if inferred == nil {
				inferred = &sess
			}
		}
		target := store.AgentSession{
			ID: store.InferredAgentSessionID(name, wsRoot), Tool: name,
			WorktreePath: wsRoot, RepoPath: repoPath, Branch: branch, Source: store.SourceInferred,
		}
		existing := inferred
		if len(managed) > 0 {
			if inferred != nil {
				changed = dropInferredDuplicate(state, name, wsRoot) || changed
			}
			own := ownSession(managed, name)
			// Refresh, don't clobber: only activity, bookmark and repo move.
			target = store.AgentSession{ID: own.ID, Tool: own.Tool, WorktreePath: own.WorktreePath, RepoPath: repoPath, Branch: branch}
			existing = &own
		}
		bookmarkChanged := branchKnown && (existing == nil || branch != existing.Branch)
		if existing != nil && now.Sub(existing.LastActivity) < heartbeatThrottle && !bookmarkChanged {
			return changed, nil
		}
		saved, err := state.UpsertAgentSession(target, now)
		if err != nil {
			return false, err
		}
		if branchKnown {
			state.SetAgentBookmark(saved.ID, branch)
		}
		return true, nil
	})
	if err != nil {
		warnAgentState("agent heartbeat", err)
	}
}

// ownSession picks, from managed sessions of one tool in one workspace (most
// recent first), the one belonging to the agent this process runs under. The
// process walk only happens when there is a choice to make; without a match
// the most recently active session is used.
func ownSession(managed []store.AgentSession, tool string) store.AgentSession {
	if len(managed) == 1 {
		return managed[0]
	}
	pid, start := agentProcess(tool, 0)
	for _, sess := range managed {
		if pid != 0 && sess.PID == pid && sess.ProcStart == start {
			return sess
		}
	}
	return managed[0]
}

// agentWarnOut receives warnings about agent-session state problems. A
// variable so tests can capture it.
var agentWarnOut io.Writer = os.Stderr

var agentStateWarned bool

// warnAgentState reports an error from the best-effort agent paths (hooks,
// the `wgo .` heartbeat). These paths never fail their caller, but a state
// file wgo cannot read or write (newer schema, unparseable, lock or write
// failure) disables session tracking entirely, so it is printed even without
// WGO_DEBUG, once per process.
func warnAgentState(what string, err error) {
	debugf("%s: %v", what, err)
	if agentStateWarned || os.Getenv("WGO_DEBUG") != "" {
		return
	}
	agentStateWarned = true
	fmt.Fprintf(agentWarnOut, "wgo: %s: %v\n", what, err)
}

// resolveAgent returns the most recently active visible session in wsRoot for
// the single-agent glyph, or nil.
func resolveAgent(wsRoot string) *models.AgentRef {
	if wsRoot == "" {
		return nil
	}
	s, err := store.New()
	if err != nil {
		warnAgentState("resolve agent", err)
		return nil
	}
	state, err := s.LoadState()
	if err != nil {
		warnAgentState("resolve agent", err)
		return nil
	}
	var ref *models.AgentRef
	for _, o := range state.ObserveAgentSessions(agentNow(), agentPolicy()) {
		if o.WorktreePath != wsRoot {
			continue
		}
		if ref != nil {
			ref.Others++
			continue
		}
		ref = &models.AgentRef{
			Name: o.Tool, Since: o.StartTime, Session: o.ID,
			Status: string(o.Status), Uncertain: o.Liveness == store.LivenessUncertain,
		}
	}
	return ref
}
