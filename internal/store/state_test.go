package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetSpecPreservesPurpose(t *testing.T) {
	s := NewState()
	s.AddAnnotation("/repo", "main", "initial purpose")

	s.SetSpec("/repo", "main", "spec/FOO-1.md", "draft")

	ann := s.GetAnnotation("/repo", "main")
	require.NotNil(t, ann)
	assert.Equal(t, "initial purpose", ann.Purpose)
	assert.Equal(t, "spec/FOO-1.md", ann.SpecPath)
	assert.Equal(t, "draft", ann.SpecState)
}

func TestAddAnnotationPreservesSpecMetadata(t *testing.T) {
	s := NewState()
	s.AddAnnotation("/repo", "main", "first purpose")
	s.SetSpec("/repo", "main", "spec/FOO-1.md", "draft")

	s.AddAnnotation("/repo", "main", "updated purpose")

	ann := s.GetAnnotation("/repo", "main")
	require.NotNil(t, ann)
	assert.Equal(t, "updated purpose", ann.Purpose)
	assert.Equal(t, "spec/FOO-1.md", ann.SpecPath, "SetSpec metadata should survive AddAnnotation")
	assert.Equal(t, "draft", ann.SpecState, "SetSpec metadata should survive AddAnnotation")
}

func TestRemoveAnnotation(t *testing.T) {
	s := NewState()
	s.AddAnnotation("/repo", "foo", "purpose")
	require.NotNil(t, s.GetAnnotation("/repo", "foo"))

	s.RemoveAnnotation("/repo", "foo")
	assert.Nil(t, s.GetAnnotation("/repo", "foo"))
}

func TestUntrackRepoRemovesAnnotations(t *testing.T) {
	s := NewState()
	s.AddRepo("/repo", "git@example.com:org/repo.git")
	s.AddAnnotation("/repo", "main", "")
	s.AddAnnotation("/repo", "feature", "")
	s.AddAnnotation("/other", "keep", "")

	s.UntrackRepo("/repo")

	assert.Nil(t, s.GetRepo("/repo"))
	assert.Nil(t, s.GetAnnotation("/repo", "main"))
	assert.Nil(t, s.GetAnnotation("/repo", "feature"))
	assert.NotNil(t, s.GetAnnotation("/other", "keep"), "untracking one repo must not affect others")
}

func TestNewStateAtCurrentVersion(t *testing.T) {
	s := NewState()
	assert.Equal(t, StateVersion, s.Version)
}

func TestUpsertAgentSessionPreservesStartTime(t *testing.T) {
	s := NewState()
	s.UpsertAgentSession("/repo", "claude", "main", 123)

	first := s.GetAgentSession("/repo")
	require.NotNil(t, first)
	assert.Equal(t, "claude", first.Tool)
	assert.Equal(t, "main", first.Branch)
	assert.Equal(t, 123, first.PID)

	// Backdate LastActivity so the upsert visibly bumps it while StartTime holds.
	sess := s.AgentSessions["/repo"]
	sess.LastActivity = time.Now().Add(-time.Hour)
	s.AgentSessions["/repo"] = sess
	start := sess.StartTime

	s.UpsertAgentSession("/repo", "claude", "feature", 456)
	second := s.GetAgentSession("/repo")
	require.NotNil(t, second)
	assert.Equal(t, start, second.StartTime, "StartTime should be preserved across upserts")
	assert.Equal(t, "feature", second.Branch, "Branch should update")
	assert.True(t, second.LastActivity.After(sess.LastActivity), "LastActivity should be bumped")
}

func TestRemoveAgentSession(t *testing.T) {
	s := NewState()
	s.UpsertAgentSession("/repo", "claude", "main", 0)
	require.NotNil(t, s.GetAgentSession("/repo"))

	s.RemoveAgentSession("/repo")
	assert.Nil(t, s.GetAgentSession("/repo"))
}

func TestPruneStaleAgentSessions(t *testing.T) {
	s := NewState()
	s.UpsertAgentSession("/fresh", "claude", "main", 0)

	// A session comfortably within the window survives; one well past it is reaped.
	window := 10 * time.Minute
	s.AgentSessions["/recent"] = AgentSession{
		Tool:         "codex",
		WorktreePath: "/recent",
		LastActivity: time.Now().Add(-window / 2),
	}
	s.AgentSessions["/stale"] = AgentSession{
		Tool:         "cursor",
		WorktreePath: "/stale",
		LastActivity: time.Now().Add(-2 * window),
	}

	removed := s.PruneStaleAgentSessions(window)
	assert.Equal(t, 1, removed, "only the stale session should be reaped")
	assert.NotNil(t, s.GetAgentSession("/fresh"))
	assert.NotNil(t, s.GetAgentSession("/recent"), "a session within the window must not be pruned")
	assert.Nil(t, s.GetAgentSession("/stale"))
}

func TestActiveAgentSessionsFiltersStale(t *testing.T) {
	s := NewState()
	s.UpsertAgentSession("/fresh", "claude", "main", 0)

	// A stale session: last activity well beyond the window.
	s.AgentSessions["/stale"] = AgentSession{
		Tool:         "codex",
		WorktreePath: "/stale",
		StartTime:    time.Now().Add(-2 * time.Hour),
		LastActivity: time.Now().Add(-2 * time.Hour),
	}

	active := s.ActiveAgentSessions(10 * time.Minute)
	assert.Contains(t, active, "/fresh")
	assert.NotContains(t, active, "/stale")
}
