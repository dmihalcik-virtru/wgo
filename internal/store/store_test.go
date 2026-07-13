package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

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
