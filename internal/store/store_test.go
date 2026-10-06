package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStoreNew(t *testing.T) {
	s, err := New()
	require.NoError(t, err, "New failed")
	assert.NotNil(t, s)
}

// TestMutateStateSkipsSaveWhenUnchanged: a callback reporting changed=false must
// not write state (the throttled-heartbeat optimization).
func TestMutateStateSkipsSaveWhenUnchanged(t *testing.T) {
	s := NewWithDir(t.TempDir())
	require.NoError(t, s.MutateState(func(st *State) (bool, error) {
		st.AddRepo("/a", "")
		return false, nil // report no change: must not persist
	}))

	state, err := s.LoadState()
	require.NoError(t, err)
	assert.Nil(t, state.GetRepo("/a"), "an unchanged mutation must not be saved")
}

// TestMutateStateConcurrentNoLostUpdates: concurrent read-modify-write cycles
// each adding a distinct annotation must all survive — this is the lost-update
// race the store lock exists to prevent.
func TestMutateStateConcurrentNoLostUpdates(t *testing.T) {
	s := NewWithDir(t.TempDir())
	require.NoError(t, s.EnsureDir())

	const n = 20
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("/repo-%d", i)
			require.NoError(t, s.MutateState(func(st *State) (bool, error) {
				st.AddAnnotation(key, "main", fmt.Sprintf("purpose-%d", i))
				return true, nil
			}))
		}(i)
	}
	wg.Wait()

	state, err := s.LoadState()
	require.NoError(t, err)
	assert.Len(t, state.Annotations, n, "every concurrent annotation must survive")
}

func TestFileStoreEnsureDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Temporarily change home
	t.Setenv("HOME", tmpDir)

	s, err := New()
	require.NoError(t, err, "New failed")

	require.NoError(t, s.EnsureDir(), "EnsureDir failed")

	storeDir := filepath.Join(tmpDir, ".wgo")
	_, err = os.Stat(storeDir)
	require.NoError(t, err, "expected store directory to exist")
}

func TestSaveLoadState(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	s, err := New()
	require.NoError(t, err, "New failed")
	require.NoError(t, s.EnsureDir(), "EnsureDir failed")

	state := NewState()
	state.AddAnnotation("/path/to/repo", "feature", "Test feature")
	state.AddRepo("/path/to/repo", "https://github.com/test/repo.git")

	require.NoError(t, s.SaveState(state), "SaveState failed")

	loaded, err := s.LoadState()
	require.NoError(t, err, "LoadState failed")

	ann := loaded.GetAnnotation("/path/to/repo", "feature")
	require.NotNil(t, ann, "expected to find annotation")
	assert.Equal(t, "Test feature", ann.Purpose)

	repo := loaded.GetRepo("/path/to/repo")
	require.NotNil(t, repo, "expected to find repo")
	assert.Equal(t, "https://github.com/test/repo.git", repo.RemoteURL)
}

func TestSaveLoadPlan(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	s, err := New()
	require.NoError(t, err, "New failed")
	require.NoError(t, s.EnsureDir(), "EnsureDir failed")

	planContent := `# Plan

## Active Branches

- **repo:branch** — Test reason

## Notes

Test notes
`

	require.NoError(t, s.SavePlan(planContent), "SavePlan failed")

	loaded, err := s.LoadPlan()
	require.NoError(t, err, "LoadPlan failed")
	assert.Equal(t, planContent, loaded)
}

// TestLoadStateRejectsOldVersion: a pre-jj (version 1) state file is refused
// with a message that names the file and the rm remedy, rather than being
// silently accepted at the wrong schema.
func TestLoadStateRejectsOldVersion(t *testing.T) {
	dir := t.TempDir()
	s := NewWithDir(dir)
	require.NoError(t, s.EnsureDir())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"version":1}`), 0o644))

	_, err := s.LoadState()
	require.Error(t, err, "a version-1 state file must be refused")
	assert.Contains(t, err.Error(), "version 1")
	assert.Contains(t, err.Error(), filepath.Join(dir, "state.json"), "error should name the file to delete")
}

// TestLoadStateCorruptJSON: a truncated/garbage state file surfaces a parse
// error instead of panicking or silently resetting.
func TestLoadStateCorruptJSON(t *testing.T) {
	dir := t.TempDir()
	s := NewWithDir(dir)
	require.NoError(t, s.EnsureDir())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{`), 0o644))

	_, err := s.LoadState()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse state file")
}

// TestLoadStateAcceptsCurrentVersion: the current version loads with its maps
// initialized.
func TestLoadStateAcceptsCurrentVersion(t *testing.T) {
	dir := t.TempDir()
	s := NewWithDir(dir)
	require.NoError(t, s.EnsureDir())
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "state.json"),
		fmt.Appendf(nil, `{"version":%d}`, StateVersion), 0o644))
	state, err := s.LoadState()
	require.NoError(t, err)
	assert.NotNil(t, state.AgentSessions, "maps should be initialized on load")
}

// TestLoadStateMigratesV2: workspace-keyed version-2 agent sessions become
// distinct ID-keyed sessions, other state survives, and the next save writes
// version 3.
func TestLoadStateMigratesV2(t *testing.T) {
	dir := t.TempDir()
	s := NewWithDir(dir)
	fixture, err := os.ReadFile(filepath.Join("testdata", "state-v2.json"))
	require.NoError(t, err)
	require.NoError(t, s.EnsureDir())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), fixture, 0o644))

	state, err := s.LoadState()
	require.NoError(t, err)
	assert.Equal(t, StateVersion, state.Version)
	require.Len(t, state.AgentSessions, 2, "both v2 entries survive as distinct sessions")

	byPath := map[string]AgentSession{}
	for id, sess := range state.AgentSessions {
		assert.Equal(t, id, sess.ID, "sessions are keyed by their ID")
		assert.NotContains(t, id, "/", "IDs are no longer workspace paths")
		byPath[sess.WorktreePath] = sess
	}
	claude := byPath["/Users/dev/src/wgo"]
	assert.Equal(t, "claude", claude.Tool)
	assert.Equal(t, "WGO-134", claude.Branch)
	assert.True(t, strings.HasPrefix(claude.ID, "claude-"))
	assert.Equal(t, SourceExplicit, claude.Source)
	assert.Equal(t, AgentUnknown, claude.Status)
	assert.Zero(t, claude.PID, "a v2 PID was the transient shell and cannot be verified")
	codex := byPath["/Users/dev/worktrees/wgo-gh-9"]
	assert.Equal(t, "codex", codex.Tool)
	assert.True(t, strings.HasPrefix(codex.ID, "codex-"))
	assert.NotNil(t, state.GetAnnotation("/Users/dev/src/wgo", "WGO-134"), "non-agent state survives")

	// Migration is deterministic, so reloading gives the same IDs.
	again, err := s.LoadState()
	require.NoError(t, err)
	assert.Equal(t, state.AgentSessions, again.AgentSessions)

	require.NoError(t, s.SaveState(state))
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"version": 3`)
	reloaded, err := s.LoadState()
	require.NoError(t, err)
	assert.Equal(t, state.AgentSessions, reloaded.AgentSessions)
}

// TestNewerStateIsReadOnly: state from a newer wgo still loads for reading,
// but neither SaveState nor MutateState will write it, and the error tells the
// user to upgrade.
func TestNewerStateIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	s := NewWithDir(dir)
	require.NoError(t, s.EnsureDir())
	path := filepath.Join(dir, "state.json")
	body := fmt.Appendf(nil, `{"version":%d,"agent_sessions":{"claude-abc123":{"id":"claude-abc123","tool":"claude","worktree_path":"/ws"}}}`, StateVersion+1)
	require.NoError(t, os.WriteFile(path, body, 0o644))

	state, err := s.LoadState()
	require.NoError(t, err, "reads of newer state still work")
	assert.Equal(t, StateVersion+1, state.Version)
	assert.NotNil(t, state.GetAgentSession("claude-abc123"))

	err = s.SaveState(state)
	var newer *NewerStateError
	require.ErrorAs(t, err, &newer)
	assert.Contains(t, err.Error(), "Upgrade wgo")

	called := false
	err = s.MutateState(func(st *State) (bool, error) {
		called = true
		st.AddRepo("/a", "")
		return true, nil
	})
	require.ErrorAs(t, err, &newer)
	assert.False(t, called, "the mutation must not run against state it cannot write")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, body, after, "the newer file is untouched")
}

// TestMutateStateErrorDoesNotPersist: when the callback returns an error, the
// mutation must not be written to disk even if it reported changed==true.
func TestMutateStateErrorDoesNotPersist(t *testing.T) {
	s := NewWithDir(t.TempDir())

	boom := fmt.Errorf("boom")
	err := s.MutateState(func(st *State) (bool, error) {
		st.AddRepo("/a", "")
		return true, boom
	})
	require.ErrorIs(t, err, boom)

	state, err := s.LoadState()
	require.NoError(t, err)
	assert.Nil(t, state.GetRepo("/a"), "a callback error must leave state on disk untouched")
}

func TestLoadNonexistentState(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	s, err := New()
	require.NoError(t, err, "New failed")

	// Should return empty state if file doesn't exist
	state, err := s.LoadState()
	require.NoError(t, err, "LoadState failed")

	require.NotNil(t, state)
	assert.Empty(t, state.Repos)
}

func TestCreatePlanSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	s, err := New()
	require.NoError(t, err, "New failed")
	require.NoError(t, s.EnsureDir(), "EnsureDir failed")

	// Create a plan file
	require.NoError(t, s.SavePlan("# Plan\n"), "SavePlan failed")
	require.NoError(t, s.CreatePlanSymlink(), "CreatePlanSymlink failed")

	symlinkPath := s.GetPlanSymlinkPath()
	info, err := os.Lstat(symlinkPath)
	require.NoError(t, err, "failed to stat symlink")
	assert.NotEqual(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "expected symlink, got regular file")
}

// TestAgentSessionsStoredAsArray: version 3 writes agent sessions as an array
// sorted by ID, and the save/load round trip is lossless.
func TestAgentSessionsStoredAsArray(t *testing.T) {
	dir := t.TempDir()
	s := NewWithDir(dir)
	require.NoError(t, s.EnsureDir())
	state := NewState()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, id := range []string{"claude-b", "claude-a"} {
		_, err := state.UpsertAgentSession(AgentSession{
			ID: id, Tool: "claude", Source: SourceHook, WorktreePath: "/ws/" + id, Branch: "main",
		}, now)
		require.NoError(t, err)
	}
	require.NoError(t, s.SaveState(state))

	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	require.NoError(t, err)
	var onDisk struct {
		AgentSessions []AgentSession `json:"agent_sessions"`
	}
	require.NoError(t, json.Unmarshal(raw, &onDisk), "agent_sessions is a JSON array")
	require.Len(t, onDisk.AgentSessions, 2)
	assert.Equal(t, "claude-a", onDisk.AgentSessions[0].ID, "entries are sorted by ID")
	assert.Equal(t, "claude-b", onDisk.AgentSessions[1].ID)

	loaded, err := s.LoadState()
	require.NoError(t, err)
	assert.Equal(t, state.AgentSessions, loaded.AgentSessions)
}

// TestPreV3DecodeFailsOnArray: a pre-v3 binary decodes agent_sessions into a
// map, so it cannot parse a v3 file and so never rewrites one. This is what
// keeps an old wgo from stripping v3 session fields.
func TestPreV3DecodeFailsOnArray(t *testing.T) {
	state := NewState()
	_, err := state.UpsertAgentSession(AgentSession{
		ID: "claude-a", Tool: "claude", Source: SourceHook, WorktreePath: "/ws",
	}, time.Now())
	require.NoError(t, err)
	raw, err := json.Marshal(state)
	require.NoError(t, err)

	var v2 struct {
		Version       int                        `json:"version"`
		AgentSessions map[string]json.RawMessage `json:"agent_sessions"`
	}
	err = json.Unmarshal(raw, &v2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot unmarshal array")
}

// TestLoadStateReadsObjectAndArray: both the v2-style object form (also what a
// pre-v3 binary wrote back before this format existed) and the v3 array load.
// Duplicate or missing IDs in an array are kept and renamed, not dropped.
func TestLoadStateReadsObjectAndArray(t *testing.T) {
	tests := []struct {
		name     string
		sessions string
		wantLen  int
	}{
		{"object", `{"claude-a":{"id":"claude-a","tool":"claude","worktree_path":"/ws"}}`, 1},
		{"array", `[{"id":"claude-a","tool":"claude","worktree_path":"/ws"}]`, 1},
		{"array with duplicate ID", `[{"id":"claude-a","tool":"claude","worktree_path":"/a"},{"id":"claude-a","tool":"claude","worktree_path":"/b"}]`, 2},
		{"array with missing ID", `[{"tool":"claude","worktree_path":"/a"},{"tool":"claude","worktree_path":"/b"}]`, 2},
		{"null", `null`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			s := NewWithDir(dir)
			require.NoError(t, s.EnsureDir())
			require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"),
				fmt.Appendf(nil, `{"version":%d,"agent_sessions":%s}`, StateVersion, tt.sessions), 0o644))
			state, err := s.LoadState()
			require.NoError(t, err)
			require.Len(t, state.AgentSessions, tt.wantLen)
			paths := map[string]bool{}
			for id, sess := range state.AgentSessions {
				assert.Equal(t, id, sess.ID, "sessions are keyed by their ID")
				assert.True(t, ValidAgentSessionID(id), "normalized ID %q is valid", id)
				paths[sess.WorktreePath] = true
			}
			assert.Len(t, paths, tt.wantLen, "no session was lost to a key collision")
		})
	}
}
