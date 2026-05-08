package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStoreNew(t *testing.T) {
	s, err := New()
	require.NoError(t, err, "New failed")
	assert.NotNil(t, s)
}

func TestFileStoreEnsureDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Temporarily change home
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	s, err := New()
	require.NoError(t, err, "New failed")

	require.NoError(t, s.EnsureDir(), "EnsureDir failed")

	storeDir := filepath.Join(tmpDir, ".wgo")
	_, err = os.Stat(storeDir)
	require.NoError(t, err, "expected store directory to exist")
}

func TestSaveLoadState(t *testing.T) {
	tmpDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

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
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

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
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

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
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

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
