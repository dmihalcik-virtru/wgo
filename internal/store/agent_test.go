package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/proc"
)

// fakeProcs is a static process table for liveness tests.
type fakeProcs map[int]int64

func (f fakeProcs) Lookup(pid int) (proc.Info, error) {
	if start, ok := f[pid]; ok {
		return proc.Info{PID: pid, Start: start}, nil
	}
	return proc.Info{}, proc.ErrNotFound
}

func (f fakeProcs) Args(int) ([]string, error) { return nil, proc.ErrNotFound }

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func policy(procs proc.Inspector) AgentPolicy {
	return AgentPolicy{Window: 10 * time.Minute, MaxAge: 24 * time.Hour, Procs: procs}
}

func mustUpsert(t *testing.T, s *State, sess AgentSession, now time.Time) AgentSession {
	t.Helper()
	got, err := s.UpsertAgentSession(sess, now)
	require.NoError(t, err)
	return got
}

func TestUpsertAgentSessionMergesByID(t *testing.T) {
	s := NewState()
	first := mustUpsert(t, s, AgentSession{
		ID: "claude-aaaaaa", Tool: "claude", WorktreePath: "/ws", Branch: "main",
		ThemeID: "effort-x", PID: 10, ProcStart: 99, Source: SourceHook,
	}, t0)
	assert.Equal(t, t0, first.StartTime)
	assert.Equal(t, AgentUnknown, first.Status)

	later := t0.Add(time.Hour)
	second := mustUpsert(t, s, AgentSession{
		ID: "claude-aaaaaa", Tool: "claude", WorktreePath: "/ws", Status: AgentWaiting, Source: SourceInferred,
	}, later)
	assert.Equal(t, t0, second.StartTime, "StartTime is preserved")
	assert.Equal(t, later, second.LastActivity)
	assert.Equal(t, "main", second.Branch, "empty fields keep their stored value")
	assert.Equal(t, "effort-x", second.ThemeID)
	assert.Equal(t, 10, second.PID)
	assert.Equal(t, int64(99), second.ProcStart)
	assert.Equal(t, AgentWaiting, second.Status)
	assert.Equal(t, SourceHook, second.Source, "an inferred touch never downgrades a managed session")

	_, err := s.UpsertAgentSession(AgentSession{ID: "bad id", Tool: "x", WorktreePath: "/ws"}, t0)
	assert.Error(t, err)
	_, err = s.UpsertAgentSession(AgentSession{ID: "ok", WorktreePath: "/ws"}, t0)
	assert.Error(t, err, "a tool is required")
}

// TestTwoSessionsOneWorkspace: two sessions share a workspace and are
// refreshed and removed independently.
func TestTwoSessionsOneWorkspace(t *testing.T) {
	s := NewState()
	mustUpsert(t, s, AgentSession{ID: "a", Tool: "claude", WorktreePath: "/ws"}, t0)
	mustUpsert(t, s, AgentSession{ID: "b", Tool: "claude", WorktreePath: "/ws"}, t0.Add(time.Minute))
	require.Len(t, s.AgentSessionsIn("/ws"), 2)
	assert.Equal(t, "b", s.AgentSessionsIn("/ws")[0].ID, "most recent first")

	mustUpsert(t, s, AgentSession{ID: "a", Tool: "claude", WorktreePath: "/ws", Status: AgentWaiting}, t0.Add(2*time.Minute))
	assert.Equal(t, AgentWaiting, s.GetAgentSession("a").Status)
	assert.Equal(t, AgentUnknown, s.GetAgentSession("b").Status, "b is untouched")

	assert.True(t, s.RemoveAgentSession("a"))
	assert.False(t, s.RemoveAgentSession("a"))
	require.Len(t, s.AgentSessionsIn("/ws"), 1)
	assert.Equal(t, "b", s.AgentSessionsIn("/ws")[0].ID)
}

// TestLivenessRules covers the liveness table with a fake clock well past the
// ten-minute window.
func TestLivenessRules(t *testing.T) {
	procs := fakeProcs{100: 5000, 200: 7777}
	p := policy(procs)
	now := t0.Add(30 * time.Minute)
	quiet := func(sess AgentSession) AgentSession {
		sess.LastActivity = t0
		sess.WorktreePath = "/ws"
		sess.Tool = "claude"
		return sess
	}

	cases := []struct {
		name string
		sess AgentSession
		want AgentLiveness
	}{
		{"recent activity is active", AgentSession{ID: "r", Source: SourceInferred, LastActivity: now.Add(-time.Minute)}, LivenessActive},
		{"verified live process stays (#61)", quiet(AgentSession{ID: "l", Source: SourceExplicit, PID: 100, ProcStart: 5000}), LivenessLive},
		{"reused PID is not live", quiet(AgentSession{ID: "x", Source: SourceExplicit, PID: 200, ProcStart: 5000}), LivenessGone},
		{"exited process is gone", quiet(AgentSession{ID: "d", Source: SourceHook, PID: 300, ProcStart: 1}), LivenessGone},
		{"no identity is uncertain", quiet(AgentSession{ID: "u", Source: SourceExplicit}), LivenessUncertain},
		{"hook without identity is uncertain", quiet(AgentSession{ID: "h", Source: SourceHook}), LivenessUncertain},
		{"inferred expires", quiet(AgentSession{ID: "i", Source: SourceInferred}), LivenessGone},
		{"a PID without a start token is no identity", quiet(AgentSession{ID: "p", Source: SourceExplicit, PID: 100}), LivenessUncertain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.sess.Liveness(now, p))
		})
	}

	// Past the maximum age an unverifiable session is gone, a live one is not.
	old := t0.Add(48 * time.Hour)
	assert.Equal(t, LivenessGone, quiet(AgentSession{ID: "u", Source: SourceExplicit}).Liveness(old, p))
	assert.Equal(t, LivenessLive, quiet(AgentSession{ID: "l", Source: SourceExplicit, PID: 100, ProcStart: 5000}).Liveness(old, p))
}

// TestPruneAgentSessionsFollowsLiveness: pruning removes exactly the gone
// sessions and keeps live and uncertain ones, matching what is listed.
func TestPruneAgentSessionsFollowsLiveness(t *testing.T) {
	s := NewState()
	add := func(sess AgentSession) {
		sess.Tool, sess.WorktreePath, sess.LastActivity = "claude", "/ws", t0
		s.AgentSessions[sess.ID] = sess
	}
	add(AgentSession{ID: "live", Source: SourceExplicit, PID: 100, ProcStart: 5000})
	add(AgentSession{ID: "reused", Source: SourceExplicit, PID: 200, ProcStart: 5000})
	add(AgentSession{ID: "uncertain", Source: SourceHook})
	add(AgentSession{ID: "inferred", Source: SourceInferred})

	p := policy(fakeProcs{100: 5000, 200: 1})
	now := t0.Add(11 * time.Minute)
	assert.Equal(t, 2, s.PruneAgentSessions(now, p))
	assert.NotNil(t, s.GetAgentSession("live"))
	assert.NotNil(t, s.GetAgentSession("uncertain"))
	assert.Nil(t, s.GetAgentSession("reused"))
	assert.Nil(t, s.GetAgentSession("inferred"))

	var ids []string
	for _, o := range s.ObserveAgentSessions(now, p) {
		ids = append(ids, o.ID)
	}
	assert.ElementsMatch(t, []string{"live", "uncertain"}, ids)
}

// TestAgentConflictsAcrossWorkspaces: two active sessions on the same
// repository and bookmark conflict even from different workspaces; a different
// bookmark, a missing bookmark or an uncertain session does not.
func TestAgentConflictsAcrossWorkspaces(t *testing.T) {
	obs := []ObservedSession{
		{AgentSession: AgentSession{ID: "a", WorktreePath: "/repo", RepoPath: "/repo", Branch: "feat"}, Liveness: LivenessActive},
		{AgentSession: AgentSession{ID: "b", WorktreePath: "/wt/feat", RepoPath: "/repo", Branch: "feat"}, Liveness: LivenessLive},
		{AgentSession: AgentSession{ID: "c", WorktreePath: "/wt/other", RepoPath: "/repo", Branch: "other"}, Liveness: LivenessActive},
		{AgentSession: AgentSession{ID: "d", WorktreePath: "/wt/nob", RepoPath: "/repo"}, Liveness: LivenessActive},
		{AgentSession: AgentSession{ID: "e", WorktreePath: "/wt/x", RepoPath: "/repo", Branch: "feat"}, Liveness: LivenessUncertain},
	}
	got := AgentConflicts(obs)
	assert.Equal(t, []string{"b"}, got["a"])
	assert.Equal(t, []string{"a"}, got["b"])
	assert.NotContains(t, got, "c")
	assert.NotContains(t, got, "d")
	assert.NotContains(t, got, "e")
}

func TestNewAgentSessionID(t *testing.T) {
	id := NewAgentSessionID("Claude")
	assert.Regexp(t, `^claude-[0-9a-f]{6}$`, id)
	assert.NotEqual(t, id, NewAgentSessionID("claude"))
	assert.Regexp(t, `^agent-[0-9a-f]{6}$`, NewAgentSessionID(""))
	assert.True(t, ValidAgentSessionID("3f0e8c7a-1b2c-4d5e-8f90-123456789abc"))
	assert.False(t, ValidAgentSessionID("a;rm -rf"))
	assert.False(t, ValidAgentSessionID(""))
}

// TestNormalizeRepairsOldBinaryWrites: an entry written into v3 state by an
// older binary (workspace-keyed, no ID) is rekeyed rather than colliding.
func TestNormalizeRepairsOldBinaryWrites(t *testing.T) {
	in := map[string]AgentSession{
		"claude-abc123": {ID: "claude-abc123", Tool: "claude", WorktreePath: "/ws", Source: SourceHook},
		"/ws":           {Tool: "claude", WorktreePath: "/ws", PID: 77},
	}
	out := normalizeAgentSessions(in)
	require.Len(t, out, 2)
	assert.Equal(t, SourceHook, out["claude-abc123"].Source)
	legacy := out[legacyAgentSessionID("claude", "/ws")]
	assert.Equal(t, "/ws", legacy.WorktreePath)
	assert.Zero(t, legacy.PID)
}
