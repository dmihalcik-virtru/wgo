package discovery

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupJJRepo initializes a fresh jj repo in dir. Tests are skipped if jj is
// not on PATH (CI must install it).
func setupJJRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj binary not found on PATH; skipping discovery test")
	}
	cmd := exec.Command("jj", "git", "init", "--colocate")
	cmd.Dir = dir
	require.NoError(t, cmd.Run(), "failed to initialize jj repo")
}

func TestDiscoveryBasic(t *testing.T) {
	tmpDir := t.TempDir()

	repoDir := filepath.Join(tmpDir, "repo1")
	require.NoError(t, os.MkdirAll(repoDir, 0o755))
	setupJJRepo(t, repoDir)

	discovery := New([]string{tmpDir}, 2, []string{})
	repos, err := discovery.DiscoverAll()
	require.NoError(t, err, "DiscoverAll failed")

	require.Len(t, repos, 1)
	assert.Equal(t, "repo1", repos[0].Name)
	assert.False(t, repos[0].IsWorktree)
}

func TestDiscoveryMultipleRepos(t *testing.T) {
	tmpDir := t.TempDir()

	for i := 1; i <= 3; i++ {
		repoDir := filepath.Join(tmpDir, "repo"+string(rune(48+i)))
		require.NoError(t, os.MkdirAll(repoDir, 0o755))
		setupJJRepo(t, repoDir)
	}

	discovery := New([]string{tmpDir}, 2, []string{})
	repos, err := discovery.DiscoverAll()
	require.NoError(t, err, "DiscoverAll failed")

	assert.Len(t, repos, 3)
}

func TestDiscoveryScanDepth(t *testing.T) {
	tmpDir := t.TempDir()

	nestedDir := filepath.Join(tmpDir, "level1", "level2", "level3")
	require.NoError(t, os.MkdirAll(nestedDir, 0o755))
	setupJJRepo(t, nestedDir)

	discovery := New([]string{tmpDir}, 2, []string{})
	repos, err := discovery.DiscoverAll()
	require.NoError(t, err, "DiscoverAll failed")
	assert.Empty(t, repos, "expected 0 repos with depth 2")

	discovery = New([]string{tmpDir}, 4, []string{})
	repos, err = discovery.DiscoverAll()
	require.NoError(t, err, "DiscoverAll failed")
	assert.Len(t, repos, 1, "expected 1 repo with depth 4")
}

func TestDiscoveryExcludePatterns(t *testing.T) {
	tmpDir := t.TempDir()

	repoDir1 := filepath.Join(tmpDir, "included")
	require.NoError(t, os.MkdirAll(repoDir1, 0o755))
	setupJJRepo(t, repoDir1)

	excludedDir := filepath.Join(tmpDir, "node_modules")
	repoDir2 := filepath.Join(excludedDir, "package")
	require.NoError(t, os.MkdirAll(repoDir2, 0o755))
	setupJJRepo(t, repoDir2)

	discovery := New([]string{tmpDir}, 3, []string{"node_modules"})
	repos, err := discovery.DiscoverAll()
	require.NoError(t, err, "DiscoverAll failed")

	require.Len(t, repos, 1, "expected 1 repo (excluded node_modules)")
	assert.Equal(t, "included", repos[0].Name)
}

func TestIsRepo(t *testing.T) {
	tmpDir := t.TempDir()

	repoDir := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(repoDir, 0o755))
	setupJJRepo(t, repoDir)

	assert.True(t, IsRepo(repoDir), "expected IsRepo to return true for jj directory")

	nonRepoDir := filepath.Join(tmpDir, "nonrepo")
	require.NoError(t, os.MkdirAll(nonRepoDir, 0o755))
	assert.False(t, IsRepo(nonRepoDir), "expected IsRepo to return false for non-jj directory")
}

func TestMultipleBaseDirs(t *testing.T) {
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	repoDir1 := filepath.Join(tmpDir1, "repo1")
	require.NoError(t, os.MkdirAll(repoDir1, 0o755))
	setupJJRepo(t, repoDir1)

	repoDir2 := filepath.Join(tmpDir2, "repo2")
	require.NoError(t, os.MkdirAll(repoDir2, 0o755))
	setupJJRepo(t, repoDir2)

	discovery := New([]string{tmpDir1, tmpDir2}, 2, []string{})
	repos, err := discovery.DiscoverAll()
	require.NoError(t, err, "DiscoverAll failed")

	assert.Len(t, repos, 2)
}

func TestDiscoverySecondaryWorkspace(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj binary not found on PATH; skipping")
	}
	tmpDir := t.TempDir()

	mainDir := filepath.Join(tmpDir, "main")
	require.NoError(t, os.MkdirAll(mainDir, 0o755))
	setupJJRepo(t, mainDir)

	// Add a secondary workspace at <tmpDir>/secondary.
	addWS := exec.Command("jj", "workspace", "add", "--name", "secondary", filepath.Join(tmpDir, "secondary"))
	addWS.Dir = mainDir
	require.NoError(t, addWS.Run(), "failed to add secondary workspace")

	discovery := New([]string{tmpDir}, 3, []string{})
	repos, err := discovery.DiscoverAll()
	require.NoError(t, err, "DiscoverAll failed")
	require.Len(t, repos, 2)

	var main, secondary DiscoveredRepo
	for _, r := range repos {
		if r.IsWorktree {
			secondary = r
		} else {
			main = r
		}
	}
	assert.Equal(t, "main", main.Name)
	assert.Equal(t, "secondary", secondary.Name)
	assert.Equal(t, mainDir, secondary.MainRepoPath)
}
