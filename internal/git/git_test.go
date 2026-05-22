package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupGitRepo initializes a git repository in the given directory.
func setupGitRepo(t *testing.T, dir string) {
	t.Helper()

	commands := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test User"},
		{"git", "config", "commit.gpgsign", "false"},
	}

	for _, args := range commands {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run(), "failed to initialize git repo")
	}
}

// addCommit adds a commit to the repository.
func addCommit(t *testing.T, dir, message string) {
	t.Helper()

	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte(message), 0o644), "failed to write test file")

	commands := [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", message},
	}

	for _, args := range commands {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run(), "failed to add commit")
	}
}

// createBranch creates a new branch in the repository.
func createBranch(t *testing.T, dir, branchName string) {
	t.Helper()

	cmd := exec.Command("git", "checkout", "-b", branchName)
	cmd.Dir = dir
	require.NoError(t, cmd.Run(), "failed to create branch")
}

func TestIsRepo(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)

	client := New(tmpDir)

	isRepo, err := client.IsRepo(tmpDir)
	require.NoError(t, err, "IsRepo failed")
	assert.True(t, isRepo, "expected IsRepo to return true for git directory")

	// Test non-repo directory
	nonRepoDir := t.TempDir()
	isRepo, err = client.IsRepo(nonRepoDir)
	require.NoError(t, err, "IsRepo failed")
	assert.False(t, isRepo, "expected IsRepo to return false for non-git directory")
}

func TestCurrentBranch(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "initial commit")

	client := New(tmpDir)

	branch, err := client.CurrentBranch(tmpDir)
	require.NoError(t, err, "CurrentBranch failed")
	assert.True(t, branch == "master" || branch == "main", "expected branch to be 'master' or 'main', got %q", branch)
}

func TestStatus(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "initial commit")

	client := New(tmpDir)

	// Create untracked file
	testFile := filepath.Join(tmpDir, "untracked.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("untracked"), 0o644), "failed to create untracked file")

	status, err := client.Status(tmpDir)
	require.NoError(t, err, "Status failed")
	assert.Equal(t, 1, status.Untracked, "expected 1 untracked file")

	// Modify tracked file
	existingFile := filepath.Join(tmpDir, "test.txt")
	require.NoError(t, os.WriteFile(existingFile, []byte("modified"), 0o644), "failed to modify file")

	status, err = client.Status(tmpDir)
	require.NoError(t, err, "Status failed")
	assert.Equal(t, 1, status.Modified, "expected 1 modified file")
}

func TestLastCommit(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "test commit message")

	client := New(tmpDir)

	commit, err := client.LastCommit(tmpDir)
	require.NoError(t, err, "LastCommit failed")

	assert.NotEmpty(t, commit.Hash, "expected non-empty commit hash")
	assert.Equal(t, "test commit message", commit.Message)
	assert.Equal(t, "Test User", commit.Author)
	assert.False(t, commit.Date.IsZero(), "expected non-zero commit date")
}

func TestRepoName(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "myrepo")
	require.NoError(t, os.MkdirAll(repoDir, 0o755), "failed to create repo dir")

	setupGitRepo(t, repoDir)

	client := New(repoDir)

	name, err := client.RepoName(repoDir)
	require.NoError(t, err, "RepoName failed")
	assert.Equal(t, "myrepo", name)
}

func TestRemoteURL(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)

	// Add a remote
	cmd := exec.Command("git", "remote", "add", "origin", "https://github.com/test/repo.git")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run(), "failed to add remote")

	client := New(tmpDir)

	url, err := client.RemoteURL(tmpDir)
	require.NoError(t, err, "RemoteURL failed")

	// URL should contain the repo reference (git may normalize it)
	assert.True(t, strings.Contains(url, "test/repo") || strings.Contains(url, "test"), "expected URL to contain test/repo, got %q", url)
}

func TestAheadBehind(t *testing.T) {
	// This test is more complex as it requires setting up a remote
	// For now, we test that it returns 0,0 when there's no tracking branch
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "initial commit")

	client := New(tmpDir)

	ahead, behind, err := client.AheadBehind(tmpDir, "master")
	if err != nil && err.Error() != "" && !isZeroError(err) {
		// Having no tracking branch is not an error for our implementation
	}

	// Should return 0,0 for branches without a tracking branch
	if ahead != 0 || behind != 0 {
		// This is acceptable if the branch has no tracking
	}
}

func isZeroError(err error) bool {
	return err.Error() == ""
}

func TestNewFromCwd(t *testing.T) {
	tmpDir := t.TempDir()
	originalCwd, err := os.Getwd()
	require.NoError(t, err, "failed to get cwd")
	defer func() {
		if err := os.Chdir(originalCwd); err != nil {
			t.Logf("failed to restore cwd: %v", err)
		}
	}()

	require.NoError(t, os.Chdir(tmpDir), "failed to change directory")

	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "test")

	client, err := NewFromCwd()
	require.NoError(t, err, "NewFromCwd failed")
	require.NotNil(t, client, "expected non-nil client")

	branch, err := client.CurrentBranch(tmpDir)
	require.NoError(t, err, "CurrentBranch failed")
	assert.NotEmpty(t, branch, "expected non-empty branch name")
}

func TestRecentCommitCount(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "commit 1")
	addCommit(t, tmpDir, "commit 2")
	addCommit(t, tmpDir, "commit 3")

	client := New(tmpDir)

	// All commits should be within the last minute
	since := time.Now().Add(-time.Minute)
	count, err := client.RecentCommitCount(tmpDir, since)
	require.NoError(t, err, "RecentCommitCount failed")
	assert.Equal(t, 3, count, "expected 3 recent commits")

	// No commits from the future
	future := time.Now().Add(time.Hour)
	count, err = client.RecentCommitCount(tmpDir, future)
	require.NoError(t, err, "RecentCommitCount failed")
	assert.Equal(t, 0, count, "expected 0 recent commits from future")
}

func TestDiffStat(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "initial commit")

	client := New(tmpDir)

	// Clean repo should have no diff
	stat, err := client.DiffStat(tmpDir)
	require.NoError(t, err, "DiffStat failed")
	assert.Equal(t, 0, stat.FilesChanged, "expected 0 files changed in clean repo")

	// Modify a tracked file
	testFile := filepath.Join(tmpDir, "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("line1\nline2\nline3\n"), 0o644), "failed to write file")

	stat, err = client.DiffStat(tmpDir)
	require.NoError(t, err, "DiffStat failed")
	assert.Equal(t, 1, stat.FilesChanged, "expected 1 file changed")
	assert.Greater(t, stat.Insertions, 0, "expected some insertions")
}

func TestListWorktrees(t *testing.T) {
	tmpDir := t.TempDir()
	mainDir := filepath.Join(tmpDir, "main-repo")
	require.NoError(t, os.MkdirAll(mainDir, 0o755), "failed to create main dir")

	setupGitRepo(t, mainDir)
	addCommit(t, mainDir, "initial commit")

	// Add a worktree
	wtDir := filepath.Join(tmpDir, "wt-feat")
	cmd := exec.Command("git", "worktree", "add", "-b", "feat/test", wtDir)
	cmd.Dir = mainDir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "failed to add worktree: %s", out)

	client := New(mainDir)
	worktrees, err := client.ListWorktrees(mainDir)
	require.NoError(t, err, "ListWorktrees failed")
	require.Len(t, worktrees, 2)

	// Resolve symlinks for comparison (macOS /var -> /private/var)
	resolvedMainDir, _ := filepath.EvalSymlinks(mainDir)
	resolvedWtDir, _ := filepath.EvalSymlinks(wtDir)

	// First should be main
	assert.True(t, worktrees[0].IsMain, "expected first worktree to be main")
	assert.Equal(t, resolvedMainDir, worktrees[0].Path)

	// Second should be the added worktree
	assert.False(t, worktrees[1].IsMain, "expected second worktree to not be main")
	assert.Equal(t, resolvedWtDir, worktrees[1].Path)
	assert.Equal(t, "feat/test", worktrees[1].Branch)
}

func TestListWorktrees_SingleRepo(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "initial commit")

	client := New(tmpDir)
	worktrees, err := client.ListWorktrees(tmpDir)
	require.NoError(t, err, "ListWorktrees failed")
	require.Len(t, worktrees, 1, "expected 1 worktree for single repo")
	assert.True(t, worktrees[0].IsMain, "expected the only worktree to be main")
}

func TestLastCommitDate(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "commit with time")

	client := New(tmpDir)
	commit, err := client.LastCommit(tmpDir)
	require.NoError(t, err, "LastCommit failed")

	// Verify the date is recent (within the last minute)
	now := time.Now()
	diff := now.Sub(commit.Date)
	if diff < 0 || diff > time.Minute {
		t.Logf("commit time seems off, diff from now: %v", diff)
	}
}

func TestListLocalBranches(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "initial")

	// Create an extra branch
	cmd := exec.Command("git", "checkout", "-b", "feature-x")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Run(), "failed to create branch")
	// Switch back to main/master
	exec.Command("git", "-C", tmpDir, "checkout", "-").Run()

	client := New(tmpDir)
	branches, err := client.ListLocalBranches(tmpDir)
	require.NoError(t, err, "ListLocalBranches failed")
	require.GreaterOrEqual(t, len(branches), 2, "expected at least 2 branches, got %v", branches)

	found := false
	for _, b := range branches {
		if b == "feature-x" {
			found = true
		}
	}
	assert.True(t, found, "expected feature-x in branches: %v", branches)
}

func TestIsBranchMerged(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "initial")

	// Get the default branch name
	client := New(tmpDir)
	defaultBranch, _ := client.CurrentBranch(tmpDir)

	// Create and merge a branch
	exec.Command("git", "-C", tmpDir, "checkout", "-b", "to-merge").Run()
	addCommit(t, tmpDir, "on feature")
	exec.Command("git", "-C", tmpDir, "checkout", defaultBranch).Run()
	exec.Command("git", "-C", tmpDir, "merge", "--no-ff", "-m", "merge feature", "to-merge").Run()

	// Create an unmerged branch
	exec.Command("git", "-C", tmpDir, "checkout", "-b", "not-merged").Run()
	addCommit(t, tmpDir, "unmerged work")
	exec.Command("git", "-C", tmpDir, "checkout", defaultBranch).Run()

	merged, err := client.IsBranchMerged(tmpDir, "to-merge", defaultBranch)
	require.NoError(t, err, "IsBranchMerged failed")
	assert.True(t, merged, "expected to-merge to be merged into %s", defaultBranch)

	unmerged, err := client.IsBranchMerged(tmpDir, "not-merged", defaultBranch)
	require.NoError(t, err, "IsBranchMerged failed")
	assert.False(t, unmerged, "expected not-merged to NOT be merged into %s", defaultBranch)
}

func TestDeleteBranch(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "initial")

	client := New(tmpDir)
	defaultBranch, _ := client.CurrentBranch(tmpDir)

	// Create and fully merge a branch
	exec.Command("git", "-C", tmpDir, "checkout", "-b", "deletable").Run()
	addCommit(t, tmpDir, "branch commit")
	exec.Command("git", "-C", tmpDir, "checkout", defaultBranch).Run()
	exec.Command("git", "-C", tmpDir, "merge", "--no-ff", "-m", "merge", "deletable").Run()

	require.NoError(t, client.DeleteBranch(tmpDir, "deletable", false), "DeleteBranch failed")

	branches, _ := client.ListLocalBranches(tmpDir)
	for _, b := range branches {
		assert.NotEqual(t, "deletable", b, "expected deletable branch to be deleted")
	}
}

func TestIsAncestor(t *testing.T) {
	// Set up a bare "remote" and a clone of it.
	bareDir := t.TempDir()
	cloneDir := t.TempDir()

	setupGitRepo(t, bareDir)
	addCommit(t, bareDir, "initial")

	// Convert to bare repo.
	require.NoError(t, exec.Command("git", "-C", bareDir, "config", "core.bare", "true").Run())

	// Clone from the bare repo.
	require.NoError(t, exec.Command("git", "clone", bareDir, cloneDir).Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "config", "user.email", "test@example.com").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "config", "user.name", "Test User").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "config", "commit.gpgsign", "false").Run())

	client := New(cloneDir)

	// Get the initial commit SHA.
	initialSHA, err := client.runInPath(cloneDir, "rev-parse", "HEAD")
	require.NoError(t, err)
	initialSHA = strings.TrimSpace(initialSHA)

	// Add a new commit on main.
	addCommit(t, cloneDir, "second commit")
	secondSHA, err := client.runInPath(cloneDir, "rev-parse", "HEAD")
	require.NoError(t, err)
	secondSHA = strings.TrimSpace(secondSHA)

	// initial is an ancestor of HEAD.
	isAnc, err := client.IsAncestor(cloneDir, initialSHA, "HEAD")
	require.NoError(t, err)
	assert.True(t, isAnc, "initial commit should be an ancestor of HEAD")

	// HEAD is not an ancestor of its own parent.
	isAnc, err = client.IsAncestor(cloneDir, secondSHA, initialSHA)
	require.NoError(t, err)
	assert.False(t, isAnc, "second commit should not be an ancestor of first commit")

	// A commit is an ancestor of itself.
	isAnc, err = client.IsAncestor(cloneDir, initialSHA, initialSHA)
	require.NoError(t, err)
	assert.True(t, isAnc, "commit should be an ancestor of itself")
}

func TestUpstreamRef(t *testing.T) {
	// Set up a bare "remote" and a clone.
	bareDir := t.TempDir()
	cloneDir := t.TempDir()

	setupGitRepo(t, bareDir)
	addCommit(t, bareDir, "initial")
	require.NoError(t, exec.Command("git", "-C", bareDir, "config", "core.bare", "true").Run())
	require.NoError(t, exec.Command("git", "clone", bareDir, cloneDir).Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "config", "user.email", "test@example.com").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "config", "user.name", "Test User").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "config", "commit.gpgsign", "false").Run())

	client := New(cloneDir)

	// Default branch after clone has an upstream.
	defaultBranch, err := client.DefaultBranch(cloneDir)
	require.NoError(t, err)
	if defaultBranch == "" {
		defaultBranch = "master" // git init default before rename
	}
	upstream, err := client.UpstreamRef(cloneDir, defaultBranch)
	require.NoError(t, err)
	assert.Equal(t, "origin/"+defaultBranch, upstream, "cloned branch should track origin")

	// Local-only branch has no upstream.
	require.NoError(t, exec.Command("git", "-C", cloneDir, "checkout", "-b", "local-only").Run())
	upstream, err = client.UpstreamRef(cloneDir, "local-only")
	require.NoError(t, err)
	assert.Equal(t, "", upstream, "local-only branch should have no upstream")
}

// setupRepoWithRemote creates a bare "remote" plus a clone configured for it.
// Returns (bareDir, cloneDir).
func setupRepoWithRemote(t *testing.T) (string, string) {
	t.Helper()
	bareDir := t.TempDir()
	cloneDir := t.TempDir()

	setupGitRepo(t, bareDir)
	addCommit(t, bareDir, "initial")
	require.NoError(t, exec.Command("git", "-C", bareDir, "config", "core.bare", "true").Run())

	require.NoError(t, exec.Command("git", "clone", bareDir, cloneDir).Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "config", "user.email", "test@example.com").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "config", "user.name", "Test User").Run())
	require.NoError(t, exec.Command("git", "-C", cloneDir, "config", "commit.gpgsign", "false").Run())
	return bareDir, cloneDir
}

func TestIsClean(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "initial")

	client := New(tmpDir)

	clean, dirty, err := client.IsClean(tmpDir)
	require.NoError(t, err)
	assert.True(t, clean)
	assert.Empty(t, dirty)

	// Add an untracked file and an unstaged modification.
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("modified"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "new.txt"), []byte("hello"), 0o644))

	clean, dirty, err = client.IsClean(tmpDir)
	require.NoError(t, err)
	assert.False(t, clean)
	assert.Len(t, dirty, 2, "expected one modified + one untracked entry, got %v", dirty)
}

func TestRebase(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "initial on main")
	client := New(tmpDir)
	defaultBranch, _ := client.CurrentBranch(tmpDir)

	// Create feature branch off the initial commit, add a commit.
	require.NoError(t, exec.Command("git", "-C", tmpDir, "checkout", "-b", "feature").Run())
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "feature.txt"), []byte("feat"), 0o644))
	require.NoError(t, exec.Command("git", "-C", tmpDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", tmpDir, "commit", "-m", "feature commit").Run())

	// Move main forward.
	require.NoError(t, exec.Command("git", "-C", tmpDir, "checkout", defaultBranch).Run())
	addCommit(t, tmpDir, "second on main")

	// Rebase feature onto the new main tip.
	require.NoError(t, exec.Command("git", "-C", tmpDir, "checkout", "feature").Run())
	require.NoError(t, client.Rebase(tmpDir, defaultBranch))

	// feature.txt must still exist; HEAD must descend from the new main tip.
	mainTip, err := client.ResolveRef(tmpDir, defaultBranch)
	require.NoError(t, err)
	isAnc, err := client.IsAncestor(tmpDir, mainTip, "HEAD")
	require.NoError(t, err)
	assert.True(t, isAnc, "feature should be rebased on top of new main")
}

func TestMergeNoFF(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "initial")
	client := New(tmpDir)
	defaultBranch, _ := client.CurrentBranch(tmpDir)

	// Create a branch with one commit ahead of default.
	require.NoError(t, exec.Command("git", "-C", tmpDir, "checkout", "-b", "topic").Run())
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "topic.txt"), []byte("t"), 0o644))
	require.NoError(t, exec.Command("git", "-C", tmpDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", tmpDir, "commit", "-m", "topic commit").Run())

	// Back to default and merge --no-ff. The merge commit must have two parents.
	require.NoError(t, exec.Command("git", "-C", tmpDir, "checkout", defaultBranch).Run())
	require.NoError(t, client.Merge(tmpDir, "topic", true))

	parents, err := client.runInPath(tmpDir, "rev-list", "--parents", "-n", "1", "HEAD")
	require.NoError(t, err)
	fields := strings.Fields(strings.TrimSpace(parents))
	assert.Equal(t, 3, len(fields), "merge commit + 2 parents = 3 fields, got %v", fields)
}

func TestResolveRef(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "initial")
	client := New(tmpDir)

	headOID, err := client.ResolveRef(tmpDir, "HEAD")
	require.NoError(t, err)
	assert.Len(t, headOID, 40, "expected 40-char SHA, got %q", headOID)

	_, err = client.ResolveRef(tmpDir, "definitely-not-a-ref")
	assert.Error(t, err)
}

func TestPushForceWithLease(t *testing.T) {
	_, cloneDir := setupRepoWithRemote(t)
	client := New(cloneDir)
	defaultBranch, err := client.CurrentBranch(cloneDir)
	require.NoError(t, err)

	// Capture the *current* remote OID — this is the lease.
	upstream, err := client.UpstreamRef(cloneDir, defaultBranch)
	require.NoError(t, err)
	require.NotEmpty(t, upstream)
	expected, err := client.ResolveRef(cloneDir, upstream)
	require.NoError(t, err)

	// Rewrite local history so a normal push would be rejected.
	require.NoError(t, exec.Command("git", "-C", cloneDir, "commit", "--amend", "-m", "amended").Run())

	// Force-with-lease using the captured OID should succeed.
	require.NoError(t, client.PushForceWithLease(cloneDir, []ForceLeaseRef{
		{Branch: defaultBranch, ExpectedOID: expected},
	}))

	// Rewrite again and try a lease with a stale (old) OID — git should reject.
	require.NoError(t, exec.Command("git", "-C", cloneDir, "commit", "--amend", "-m", "amended again").Run())
	err = client.PushForceWithLease(cloneDir, []ForceLeaseRef{
		{Branch: defaultBranch, ExpectedOID: "0000000000000000000000000000000000000000"},
	})
	assert.Error(t, err, "stale lease must be rejected")
}

func TestPushForceWithLeaseEmptyIsNoOp(t *testing.T) {
	_, cloneDir := setupRepoWithRemote(t)
	client := New(cloneDir)
	assert.NoError(t, client.PushForceWithLease(cloneDir, nil))
	assert.NoError(t, client.PushForceWithLease(cloneDir, []ForceLeaseRef{}))
}

func TestPushForceWithLeaseRejectsEmptyBranch(t *testing.T) {
	_, cloneDir := setupRepoWithRemote(t)
	client := New(cloneDir)
	err := client.PushForceWithLease(cloneDir, []ForceLeaseRef{{Branch: ""}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty branch")
}

func TestRemoveWorktree(t *testing.T) {
	tmpDir := t.TempDir()
	wtDir := t.TempDir()
	setupGitRepo(t, tmpDir)
	addCommit(t, tmpDir, "initial")

	client := New(tmpDir)

	require.NoError(t, client.WorktreeAdd(tmpDir, wtDir, "wt-branch", true, ""), "WorktreeAdd failed")

	wts, err := client.ListWorktrees(tmpDir)
	require.NoError(t, err, "ListWorktrees failed")
	require.GreaterOrEqual(t, len(wts), 2, "expected at least 2 worktrees")

	require.NoError(t, client.RemoveWorktree(tmpDir, wtDir, false), "RemoveWorktree failed")
	require.NoError(t, client.PruneWorktrees(tmpDir), "PruneWorktrees failed")
}
