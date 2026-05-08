package discovery

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupGitRepo(t *testing.T, dir string) {
	t.Helper()

	commands := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test User"},
	}

	for _, args := range commands {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run(), "failed to initialize git repo")
	}
}

func TestDiscoveryBasic(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a repo
	repoDir := filepath.Join(tmpDir, "repo1")
	require.NoError(t, os.MkdirAll(repoDir, 0o755))
	setupGitRepo(t, repoDir)

	discovery := New([]string{tmpDir}, 2, []string{})
	repos, err := discovery.DiscoverAll()
	require.NoError(t, err, "DiscoverAll failed")

	require.Len(t, repos, 1)
	assert.Equal(t, "repo1", repos[0].Name)
	assert.False(t, repos[0].IsWorktree)
}

func TestDiscoveryMultipleRepos(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple repos
	for i := 1; i <= 3; i++ {
		repoDir := filepath.Join(tmpDir, "repo"+string(rune(48+i)))
		require.NoError(t, os.MkdirAll(repoDir, 0o755))
		setupGitRepo(t, repoDir)
	}

	discovery := New([]string{tmpDir}, 2, []string{})
	repos, err := discovery.DiscoverAll()
	require.NoError(t, err, "DiscoverAll failed")

	assert.Len(t, repos, 3)
}

func TestDiscoveryScanDepth(t *testing.T) {
	tmpDir := t.TempDir()

	// Create nested repos
	nestedDir := filepath.Join(tmpDir, "level1", "level2", "level3")
	require.NoError(t, os.MkdirAll(nestedDir, 0o755))
	setupGitRepo(t, nestedDir)

	// With depth 2, should not find deeply nested repo
	discovery := New([]string{tmpDir}, 2, []string{})
	repos, err := discovery.DiscoverAll()
	require.NoError(t, err, "DiscoverAll failed")
	assert.Empty(t, repos, "expected 0 repos with depth 2")

	// With depth 4, should find it
	discovery = New([]string{tmpDir}, 4, []string{})
	repos, err = discovery.DiscoverAll()
	require.NoError(t, err, "DiscoverAll failed")
	assert.Len(t, repos, 1, "expected 1 repo with depth 4")
}

func TestDiscoveryExcludePatterns(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a normal repo and one in an excluded dir
	repoDir1 := filepath.Join(tmpDir, "included")
	require.NoError(t, os.MkdirAll(repoDir1, 0o755))
	setupGitRepo(t, repoDir1)

	excludedDir := filepath.Join(tmpDir, "node_modules")
	repoDir2 := filepath.Join(excludedDir, "package")
	require.NoError(t, os.MkdirAll(repoDir2, 0o755))
	setupGitRepo(t, repoDir2)

	// Discovery with node_modules excluded
	discovery := New([]string{tmpDir}, 3, []string{"node_modules"})
	repos, err := discovery.DiscoverAll()
	require.NoError(t, err, "DiscoverAll failed")

	require.Len(t, repos, 1, "expected 1 repo (excluded node_modules)")
	assert.Equal(t, "included", repos[0].Name)
}

func TestIsRepo(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a git repo
	repoDir := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(repoDir, 0o755))
	setupGitRepo(t, repoDir)

	assert.True(t, IsRepo(repoDir), "expected IsRepo to return true for git directory")

	// Non-repo directory
	nonRepoDir := filepath.Join(tmpDir, "nonrepo")
	require.NoError(t, os.MkdirAll(nonRepoDir, 0o755))
	assert.False(t, IsRepo(nonRepoDir), "expected IsRepo to return false for non-git directory")
}

func TestMultipleBaseDirs(t *testing.T) {
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	// Create repos in both dirs
	repoDir1 := filepath.Join(tmpDir1, "repo1")
	require.NoError(t, os.MkdirAll(repoDir1, 0o755))
	setupGitRepo(t, repoDir1)

	repoDir2 := filepath.Join(tmpDir2, "repo2")
	require.NoError(t, os.MkdirAll(repoDir2, 0o755))
	setupGitRepo(t, repoDir2)

	discovery := New([]string{tmpDir1, tmpDir2}, 2, []string{})
	repos, err := discovery.DiscoverAll()
	require.NoError(t, err, "DiscoverAll failed")

	assert.Len(t, repos, 2)
}
