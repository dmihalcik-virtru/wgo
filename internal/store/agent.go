package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/proc"
)

// AgentStatus is what an agent session is doing, as last reported.
type AgentStatus string

// Agent statuses. Hooks and `wgo agent heartbeat` report working, waiting or
// idle; unknown covers sessions nothing has reported on (migrated, inferred).
const (
	AgentWorking AgentStatus = "working"
	AgentWaiting AgentStatus = "waiting"
	AgentIdle    AgentStatus = "idle"
	AgentUnknown AgentStatus = "unknown"
)

// ParseAgentStatus validates a user-supplied status.
func ParseAgentStatus(s string) (AgentStatus, error) {
	switch st := AgentStatus(strings.ToLower(strings.TrimSpace(s))); st {
	case AgentWorking, AgentWaiting, AgentIdle, AgentUnknown:
		return st, nil
	}
	return "", fmt.Errorf("invalid status %q (want working, waiting, idle or unknown)", s)
}

// AgentSource records who manages a session, which decides how it ages.
type AgentSource string

const (
	// SourceExplicit sessions were created by `wgo agent start`.
	SourceExplicit AgentSource = "explicit"
	// SourceHook sessions are driven by agent hooks (`wgo agent hook`).
	SourceHook AgentSource = "hook"
	// SourceInferred sessions were created by the `wgo .`/statusline heartbeat
	// from the environment. They expire on the quiet timeout.
	SourceInferred AgentSource = "inferred"
)

// AgentSession is one AI agent session, keyed by ID. Several sessions may
// share a workspace or an effort.
type AgentSession struct {
	ID      string      `json:"id"`
	ThemeID string      `json:"theme_id,omitempty"`
	Status  AgentStatus `json:"status,omitempty"`
	Source  AgentSource `json:"source,omitempty"`
	Tool    string      `json:"tool"` // e.g., "claude", "codex", "cursor"
	// WorktreePath is the jj workspace root the agent works in.
	WorktreePath string `json:"worktree_path"`
	// RepoPath is the repository's main workspace root, so sessions in
	// different workspaces of one repository can be compared for conflicts.
	RepoPath     string    `json:"repo_path,omitempty"`
	Branch       string    `json:"branch"`
	StartTime    time.Time `json:"start_time"`
	LastActivity time.Time `json:"last_activity"`
	// PID and ProcStart identify the agent's own process (not a transient
	// shell). Both are zero when no reliable identity was found. ProcStart is
	// an opaque token from internal/proc; a PID is live only when the process
	// exists and its token still matches.
	PID       int   `json:"pid,omitempty"`
	ProcStart int64 `json:"proc_start,omitempty"`
}

// HasProcess reports whether the session carries a verifiable process identity.
func (s AgentSession) HasProcess() bool { return s.PID > 0 && s.ProcStart != 0 }

// AgentLiveness is the derived liveness of a session at a point in time.
type AgentLiveness string

const (
	// LivenessActive: activity within the quiet window.
	LivenessActive AgentLiveness = "active"
	// LivenessLive: quiet, but its recorded process is verified alive.
	LivenessLive AgentLiveness = "live"
	// LivenessUncertain: quiet with nothing to verify. Shown, not deleted.
	LivenessUncertain AgentLiveness = "uncertain"
	// LivenessGone: prunable. An inferred session past the window, a
	// session whose recorded process exited (or whose PID was reused), or an
	// unverifiable session past the maximum age.
	LivenessGone AgentLiveness = "gone"
)

// Visible reports whether a session with this liveness should be listed.
func (l AgentLiveness) Visible() bool { return l != LivenessGone && l != "" }

// AgentPolicy holds the liveness rules' parameters.
type AgentPolicy struct {
	// Window is how long after its last activity a session counts as active.
	Window time.Duration
	// MaxAge bounds how long an unverifiable (no process identity) explicit or
	// hook session is kept after its last activity.
	MaxAge time.Duration
	// Procs verifies recorded process identities. Nil verifies nothing.
	Procs proc.Inspector
}

// Liveness classifies a session. A verified live process always wins over
// quiet time, so a long-running quiet agent never expires (#61); a recorded
// process that is gone or reused never counts as live.
func (s AgentSession) Liveness(now time.Time, p AgentPolicy) AgentLiveness {
	quiet := now.Sub(s.LastActivity)
	if quiet <= p.Window {
		return LivenessActive
	}
	if s.HasProcess() && proc.Alive(p.Procs, s.PID, s.ProcStart) {
		return LivenessLive
	}
	switch {
	case s.Source == SourceInferred:
		return LivenessGone
	case s.HasProcess():
		return LivenessGone
	case p.MaxAge > 0 && quiet > p.MaxAge:
		return LivenessGone
	}
	return LivenessUncertain
}

// ObservedSession pairs a session with its liveness at observation time.
type ObservedSession struct {
	AgentSession
	Liveness AgentLiveness
}

// UpsertAgentSession inserts sess, or merges it into the existing session with
// the same ID. On merge StartTime, ThemeID, RepoPath, Branch, Status and
// process identity are kept unless sess supplies a new value, and Source is
// only ever upgraded from inferred. LastActivity is set to now. Sessions
// without an ID, tool or workspace are rejected.
func (s *State) UpsertAgentSession(sess AgentSession, now time.Time) (AgentSession, error) {
	if !ValidAgentSessionID(sess.ID) {
		return AgentSession{}, fmt.Errorf("invalid agent session ID %q", sess.ID)
	}
	if sess.Tool == "" || sess.WorktreePath == "" {
		return AgentSession{}, fmt.Errorf("agent session %s needs a tool and a workspace", sess.ID)
	}
	if existing, ok := s.AgentSessions[sess.ID]; ok {
		if !existing.StartTime.IsZero() {
			sess.StartTime = existing.StartTime
		}
		if sess.ThemeID == "" {
			sess.ThemeID = existing.ThemeID
		}
		if sess.RepoPath == "" {
			sess.RepoPath = existing.RepoPath
		}
		if sess.Branch == "" {
			sess.Branch = existing.Branch
		}
		if sess.Status == "" {
			sess.Status = existing.Status
		}
		if !sess.HasProcess() {
			sess.PID, sess.ProcStart = existing.PID, existing.ProcStart
		}
		if sess.Source == "" || (sess.Source == SourceInferred && existing.Source != "") {
			sess.Source = existing.Source
		}
	}
	if sess.StartTime.IsZero() {
		sess.StartTime = now
	}
	if sess.Status == "" {
		sess.Status = AgentUnknown
	}
	if sess.Source == "" {
		sess.Source = SourceExplicit
	}
	sess.LastActivity = now
	s.AgentSessions[sess.ID] = sess
	return sess, nil
}

// GetAgentSession returns the session with the given ID, or nil.
func (s *State) GetAgentSession(id string) *AgentSession {
	if sess, ok := s.AgentSessions[id]; ok {
		return &sess
	}
	return nil
}

// RemoveAgentSession deletes a session and reports whether it existed.
func (s *State) RemoveAgentSession(id string) bool {
	if _, ok := s.AgentSessions[id]; !ok {
		return false
	}
	delete(s.AgentSessions, id)
	return true
}

// AgentSessionsIn returns every stored session in a workspace, most recently
// active first, regardless of liveness.
func (s *State) AgentSessionsIn(wsRoot string) []AgentSession {
	var out []AgentSession
	for _, sess := range s.AgentSessions {
		if sess.WorktreePath == wsRoot {
			out = append(out, sess)
		}
	}
	sortByRecent(out)
	return out
}

// ObserveAgentSessions returns the visible sessions with their liveness, most
// recently active first.
func (s *State) ObserveAgentSessions(now time.Time, p AgentPolicy) []ObservedSession {
	all := make([]AgentSession, 0, len(s.AgentSessions))
	for _, sess := range s.AgentSessions {
		all = append(all, sess)
	}
	sortByRecent(all)
	var out []ObservedSession
	for _, sess := range all {
		if l := sess.Liveness(now, p); l.Visible() {
			out = append(out, ObservedSession{AgentSession: sess, Liveness: l})
		}
	}
	return out
}

// PruneAgentSessions deletes sessions whose liveness is gone and returns how
// many it removed. It applies the same rules as ObserveAgentSessions, so a
// session that is listed is never pruned, and a session with a verified live
// process is never pruned at all.
func (s *State) PruneAgentSessions(now time.Time, p AgentPolicy) int {
	removed := 0
	for id, sess := range s.AgentSessions {
		if sess.Liveness(now, p) == LivenessGone {
			delete(s.AgentSessions, id)
			removed++
		}
	}
	return removed
}

// AgentConflicts maps each session ID to the IDs of other active or live
// sessions on the same repository and bookmark, including sessions in other
// workspaces of that repository. Sessions without a bookmark, and uncertain
// sessions, never conflict.
func AgentConflicts(sessions []ObservedSession) map[string][]string {
	groups := make(map[string][]string)
	for _, o := range sessions {
		if o.Branch == "" || (o.Liveness != LivenessActive && o.Liveness != LivenessLive) {
			continue
		}
		repo := o.RepoPath
		if repo == "" {
			repo = o.WorktreePath
		}
		key := repo + "\x00" + o.Branch
		groups[key] = append(groups[key], o.ID)
	}
	out := make(map[string][]string)
	for _, ids := range groups {
		if len(ids) < 2 {
			continue
		}
		for _, id := range ids {
			for _, other := range ids {
				if other != id {
					out[id] = append(out[id], other)
				}
			}
		}
	}
	return out
}

func sortByRecent(ss []AgentSession) {
	sort.Slice(ss, func(i, j int) bool {
		if !ss[i].LastActivity.Equal(ss[j].LastActivity) {
			return ss[i].LastActivity.After(ss[j].LastActivity)
		}
		return ss[i].ID < ss[j].ID
	})
}

// NewAgentSessionID returns a short random ID prefixed by the tool, such as
// "claude-3f9a2c".
func NewAgentSessionID(tool string) string {
	var b [3]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails on supported platforms
	return sessionPrefix(tool) + "-" + hex.EncodeToString(b[:])
}

// InferredAgentSessionID is the stable ID of the single inferred session the
// statusline heartbeat keeps per tool and workspace.
func InferredAgentSessionID(tool, wsRoot string) string {
	return sessionPrefix(tool) + "-inferred-" + shortHash(wsRoot)
}

// legacyAgentSessionID derives a stable ID for a session that was stored
// without one (version-2 state, keyed by workspace root).
func legacyAgentSessionID(tool, key string) string {
	return sessionPrefix(tool) + "-v2-" + shortHash(key)
}

func sessionPrefix(tool string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(tool) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "agent"
	}
	return b.String()
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:3])
}

// ValidAgentSessionID accepts 1 to 128 characters from [A-Za-z0-9._:-]. That
// covers generated IDs and Claude Code's UUID session IDs while keeping IDs
// safe to print and to pass as a single shell word.
func ValidAgentSessionID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == ':', r == '-':
		default:
			return false
		}
	}
	return true
}

// normalizeAgentSessions rekeys sessions by ID. Version-2 entries (keyed by
// workspace root, no ID) get a stable derived ID, become explicit sessions
// with unknown status, and lose their PID: version 2 recorded the parent of
// wgo, usually a transient shell, with no start time to verify it by. The same
// repair applies to entries an older binary may have written into newer
// state, so a mixed-binary history still loads into distinct sessions.
func normalizeAgentSessions(in map[string]AgentSession) map[string]AgentSession {
	out := make(map[string]AgentSession, len(in))
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sess := in[key]
		if sess.ID == "" {
			if ValidAgentSessionID(key) && !strings.Contains(key, "/") {
				sess.ID = key
			} else {
				sess.ID = legacyAgentSessionID(sess.Tool, key)
			}
			if sess.WorktreePath == "" && strings.HasPrefix(key, "/") {
				sess.WorktreePath = key
			}
		}
		if sess.ProcStart == 0 {
			sess.PID = 0
		}
		if sess.Source == "" {
			sess.Source = SourceExplicit
		}
		if sess.Status == "" {
			sess.Status = AgentUnknown
		}
		id := sess.ID
		for n := 2; ; n++ {
			if _, taken := out[id]; !taken {
				break
			}
			id = fmt.Sprintf("%s-%d", sess.ID, n)
		}
		sess.ID = id
		out[id] = sess
	}
	return out
}
