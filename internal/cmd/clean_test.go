package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/cleanup"
	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/github"
)

// setupCleanRepo initialises a git repo with an initial commit on main and returns its path.
func setupCleanRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "symbolic-ref", "HEAD", "refs/heads/main"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test User"},
		{"git", "config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run())
	}
	addCleanCommit(t, dir, "initial")
	return dir
}

// addCleanCommit writes a file and creates a commit, returning the new HEAD SHA.
func addCleanCommit(t *testing.T, dir, msg string) string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, msg+".txt"), []byte(msg), 0o644))
	require.NoError(t, exec.Command("git", "-C", dir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "commit", "-m", msg).Run())
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

// setRemoteRef manually sets a remote-tracking ref without needing an actual remote.
func setRemoteRef(t *testing.T, dir, ref, sha string) {
	t.Helper()
	require.NoError(t, exec.Command("git", "-C", dir, "update-ref", ref, sha).Run())
}

// mergedPR returns a minimal PRInfo for a merged PR, optionally with HeadSHA and MergeCommit.
func mergedPR(headSHA, mergeOID string) *github.PRInfo {
	pr := &github.PRInfo{State: "MERGED", HeadSHA: headSHA}
	if mergeOID != "" {
		pr.MergeCommit = &github.PRMergeCommit{OID: mergeOID}
	}
	return pr
}

func callExecuteRemoval(dir, branch string, pr *github.PRInfo) error {
	c := cleanup.Candidate{
		Kind:     cleanup.KindLocalBranch,
		RepoPath: dir,
		Branch:   branch,
		PRInfo:   pr,
	}
	return executeRemoval(c, git.New(dir), nil, nil)
}

// TestExecuteRemoval_StandardMerge: local tip is reachable from origin/main (Check 1).
func TestExecuteRemoval_StandardMerge(t *testing.T) {
	dir := setupCleanRepo(t)

	// Create feature branch with one commit, record its SHA.
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "feat").Run())
	featSHA := addCleanCommit(t, dir, "feature work")

	// Back to main; set origin/main = feat tip (simulates fast-forward merge).
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "main").Run())
	setRemoteRef(t, dir, "refs/remotes/origin/main", featSHA)

	origForce := cleanForce
	cleanForce = false
	defer func() { cleanForce = origForce }()

	err := callExecuteRemoval(dir, "feat", mergedPR("", ""))
	assert.NoError(t, err, "standard merge should auto-delete without error")

	branches, _ := git.New(dir).ListLocalBranches(dir)
	for _, b := range branches {
		assert.NotEqual(t, "feat", b, "feat branch should have been deleted")
	}
}

// TestExecuteRemoval_SquashMerge: local tip is NOT reachable from origin/main, but
// PRInfo.MergeCommit.OID is on origin/main (Check 2).
func TestExecuteRemoval_SquashMerge(t *testing.T) {
	dir := setupCleanRepo(t)

	// Feature branch with commit C1 (squashed, not a parent of origin/main).
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "feat").Run())
	addCleanCommit(t, dir, "feature work")

	// Back to main; add a squash commit S (parent = initial, NOT C1).
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "main").Run())
	squashSHA := addCleanCommit(t, dir, "squash merge result")

	// Simulate origin/main = S (the squash commit).
	setRemoteRef(t, dir, "refs/remotes/origin/main", squashSHA)

	// Reset local main back to initial so feat's commits are definitely not reachable.
	require.NoError(t, exec.Command("git", "-C", dir, "reset", "--hard", "HEAD~1").Run())

	origForce := cleanForce
	cleanForce = false
	defer func() { cleanForce = origForce }()

	err := callExecuteRemoval(dir, "feat", mergedPR("", squashSHA))
	assert.NoError(t, err, "squash merge (MergeCommit on origin/main) should auto-delete")

	branches, _ := git.New(dir).ListLocalBranches(dir)
	for _, b := range branches {
		assert.NotEqual(t, "feat", b)
	}
}

// TestExecuteRemoval_PushedHeadMatch: local tip equals PRInfo.HeadSHA — no extra commits
// were added after the last push (Check 3).
func TestExecuteRemoval_PushedHeadMatch(t *testing.T) {
	dir := setupCleanRepo(t)

	// Feature branch at C1; remote branch was deleted post-merge (no upstream).
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "feat").Run())
	c1 := addCleanCommit(t, dir, "feature work")

	// Back to main; origin/main stays at initial (does NOT contain C1).
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "main").Run())
	initialSHA, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	setRemoteRef(t, dir, "refs/remotes/origin/main", strings.TrimSpace(string(initialSHA)))

	origForce := cleanForce
	cleanForce = false
	defer func() { cleanForce = origForce }()

	// HeadSHA = C1 (what was pushed); local tip == pushed tip → no extra commits.
	err = callExecuteRemoval(dir, "feat", mergedPR(c1, ""))
	assert.NoError(t, err, "local tip matching pushed HeadSHA should auto-delete")

	branches, _ := git.New(dir).ListLocalBranches(dir)
	for _, b := range branches {
		assert.NotEqual(t, "feat", b)
	}
}

// TestExecuteRemoval_UpstreamRef: local tip matches upstream ref with no extra commits (Check 4).
func TestExecuteRemoval_UpstreamRef(t *testing.T) {
	dir := setupCleanRepo(t)

	// Feature branch at C1.
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "feat").Run())
	c1 := addCleanCommit(t, dir, "feature work")

	// Back to main; origin/main stays at initial.
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "main").Run())
	initialSHA, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	setRemoteRef(t, dir, "refs/remotes/origin/main", strings.TrimSpace(string(initialSHA)))

	// Set up a fake remote "origin" in config so @{upstream} resolution works.
	require.NoError(t, exec.Command("git", "-C", dir, "config", "remote.origin.url", "https://example.com/repo.git").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*").Run())
	// Set up upstream tracking: origin/feat = C1, branch config tracking origin.
	setRemoteRef(t, dir, "refs/remotes/origin/feat", c1)
	require.NoError(t, exec.Command("git", "-C", dir, "config", "branch.feat.remote", "origin").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "config", "branch.feat.merge", "refs/heads/feat").Run())

	origForce := cleanForce
	cleanForce = false
	defer func() { cleanForce = origForce }()

	err = callExecuteRemoval(dir, "feat", mergedPR("", ""))
	assert.NoError(t, err, "local tip at upstream ref should auto-delete")

	branches, _ := git.New(dir).ListLocalBranches(dir)
	for _, b := range branches {
		assert.NotEqual(t, "feat", b)
	}
}

// TestExecuteRemoval_UnpushedCommits: all four checks fail, yielding a detailed error.
func TestExecuteRemoval_UnpushedCommits(t *testing.T) {
	dir := setupCleanRepo(t)

	// Feature branch: C1 was pushed (HeadSHA), then C2 added locally (unpushed).
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "feat").Run())
	c1 := addCleanCommit(t, dir, "pushed work")
	addCleanCommit(t, dir, "unpushed extra commit")

	// Back to main; origin/main stays at initial (C1 and C2 not reachable from it).
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "main").Run())
	initialSHA, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	setRemoteRef(t, dir, "refs/remotes/origin/main", strings.TrimSpace(string(initialSHA)))

	origForce := cleanForce
	cleanForce = false
	defer func() { cleanForce = origForce }()

	// HeadSHA = C1 (pushed), but feat is now at C2 (extra unpushed commit).
	// No MergeCommit, no upstream ref → all checks fail.
	err = callExecuteRemoval(dir, "feat", mergedPR(c1, ""))
	require.Error(t, err, "unpushed commits should block deletion")
	assert.Contains(t, err.Error(), "cannot verify branch is safe to delete")
	assert.Contains(t, err.Error(), "use --force to delete anyway")
	assert.Contains(t, err.Error(), "local commits exist beyond pushed PR head")
}

// TestExecuteRemoval_ForceFlag: --force bypasses all safety checks.
func TestExecuteRemoval_ForceFlag(t *testing.T) {
	dir := setupCleanRepo(t)

	// Feature branch with genuinely unpushed commits.
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "feat").Run())
	c1 := addCleanCommit(t, dir, "pushed")
	addCleanCommit(t, dir, "unpushed extra")
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "main").Run())

	initialSHA, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	setRemoteRef(t, dir, "refs/remotes/origin/main", strings.TrimSpace(string(initialSHA)))

	origForce := cleanForce
	cleanForce = true
	defer func() { cleanForce = origForce }()

	err = callExecuteRemoval(dir, "feat", mergedPR(c1, ""))
	assert.NoError(t, err, "--force should bypass all checks and delete the branch")

	branches, _ := git.New(dir).ListLocalBranches(dir)
	for _, b := range branches {
		assert.NotEqual(t, "feat", b)
	}
}
