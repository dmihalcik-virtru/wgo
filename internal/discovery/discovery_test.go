package discovery

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to initialize git repo: %v", err)
		}
	}
}

func TestDiscoveryBasic(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a repo
	repoDir := filepath.Join(tmpDir, "repo1")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}
	setupGitRepo(t, repoDir)

	discovery := New([]string{tmpDir}, 2, []string{})
	repos, err := discovery.DiscoverAll()
	if err != nil {
		t.Fatalf("DiscoverAll failed: %v", err)
	}

	if len(repos) != 1 {
		t.Errorf("expected 1 repo, got %d", len(repos))
	}

	if repos[0].Name != "repo1" {
		t.Errorf("expected repo name 'repo1', got %q", repos[0].Name)
	}

	if repos[0].IsWorktree {
		t.Errorf("expected repo to not be a worktree")
	}
}

func TestDiscoveryMultipleRepos(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple repos
	for i := 1; i <= 3; i++ {
		repoDir := filepath.Join(tmpDir, "repo"+string(rune(48+i)))
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			t.Fatalf("failed to create repo dir: %v", err)
		}
		setupGitRepo(t, repoDir)
	}

	discovery := New([]string{tmpDir}, 2, []string{})
	repos, err := discovery.DiscoverAll()
	if err != nil {
		t.Fatalf("DiscoverAll failed: %v", err)
	}

	if len(repos) != 3 {
		t.Errorf("expected 3 repos, got %d", len(repos))
	}
}

func TestDiscoveryScanDepth(t *testing.T) {
	tmpDir := t.TempDir()

	// Create nested repos
	nestedDir := filepath.Join(tmpDir, "level1", "level2", "level3")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("failed to create nested dirs: %v", err)
	}
	setupGitRepo(t, nestedDir)

	// With depth 2, should not find deeply nested repo
	discovery := New([]string{tmpDir}, 2, []string{})
	repos, err := discovery.DiscoverAll()
	if err != nil {
		t.Fatalf("DiscoverAll failed: %v", err)
	}

	if len(repos) != 0 {
		t.Errorf("expected 0 repos with depth 2, got %d", len(repos))
	}

	// With depth 4, should find it
	discovery = New([]string{tmpDir}, 4, []string{})
	repos, err = discovery.DiscoverAll()
	if err != nil {
		t.Fatalf("DiscoverAll failed: %v", err)
	}

	if len(repos) != 1 {
		t.Errorf("expected 1 repo with depth 4, got %d", len(repos))
	}
}

func TestDiscoveryExcludePatterns(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a normal repo and one in an excluded dir
	repoDir1 := filepath.Join(tmpDir, "included")
	if err := os.MkdirAll(repoDir1, 0o755); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}
	setupGitRepo(t, repoDir1)

	excludedDir := filepath.Join(tmpDir, "node_modules")
	repoDir2 := filepath.Join(excludedDir, "package")
	if err := os.MkdirAll(repoDir2, 0o755); err != nil {
		t.Fatalf("failed to create excluded repo dir: %v", err)
	}
	setupGitRepo(t, repoDir2)

	// Discovery with node_modules excluded
	discovery := New([]string{tmpDir}, 3, []string{"node_modules"})
	repos, err := discovery.DiscoverAll()
	if err != nil {
		t.Fatalf("DiscoverAll failed: %v", err)
	}

	if len(repos) != 1 {
		t.Errorf("expected 1 repo (excluded node_modules), got %d", len(repos))
	}

	if repos[0].Name != "included" {
		t.Errorf("expected repo name 'included', got %q", repos[0].Name)
	}
}

func TestIsRepo(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a git repo
	repoDir := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}
	setupGitRepo(t, repoDir)

	if !IsRepo(repoDir) {
		t.Errorf("expected IsRepo to return true for git directory")
	}

	// Non-repo directory
	nonRepoDir := filepath.Join(tmpDir, "nonrepo")
	if err := os.MkdirAll(nonRepoDir, 0o755); err != nil {
		t.Fatalf("failed to create non-repo dir: %v", err)
	}

	if IsRepo(nonRepoDir) {
		t.Errorf("expected IsRepo to return false for non-git directory")
	}
}

func TestMultipleBaseDirs(t *testing.T) {
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	// Create repos in both dirs
	repoDir1 := filepath.Join(tmpDir1, "repo1")
	if err := os.MkdirAll(repoDir1, 0o755); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}
	setupGitRepo(t, repoDir1)

	repoDir2 := filepath.Join(tmpDir2, "repo2")
	if err := os.MkdirAll(repoDir2, 0o755); err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}
	setupGitRepo(t, repoDir2)

	discovery := New([]string{tmpDir1, tmpDir2}, 2, []string{})
	repos, err := discovery.DiscoverAll()
	if err != nil {
		t.Fatalf("DiscoverAll failed: %v", err)
	}

	if len(repos) != 2 {
		t.Errorf("expected 2 repos, got %d", len(repos))
	}
}
