package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/cleanup"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/jjtest"
)

// setupCleanRepo creates a jj repo with an "origin" remote backed by a bare
// git repo, seeds an initial commit on the "main" bookmark, and pushes
// "main" to origin so `main@origin` resolves. Returns (repoPath, jjClient,
// originURL).
func setupCleanRepo(t *testing.T) (string, *jj.CLIClient, string) {
	t.Helper()
	jjtest.RequireJJ(t)

	originDir := filepath.Join(t.TempDir(), "origin.git")
	require.NoError(t, os.MkdirAll(originDir, 0o755))
	out, err := exec.Command("git", "init", "--bare", originDir).CombinedOutput()
	require.NoError(t, err, "git init bare failed: %s", string(out))

	repo, c := jjtest.NewRepo(t)
	mustRun(t, repo, "jj", "git", "remote", "add", "origin", originDir)

	// Seed an initial described commit and bookmark it as "main".
	jjtest.Commit(t, repo, "initial", map[string]string{"README.md": "init\n"})
	require.NoError(t, c.BookmarkCreate(repo, "main", "@-"))
	_, err = c.GitPush(repo, jj.PushOpts{Bookmarks: []string{"main"}, AllowNew: true})
	require.NoError(t, err, "GitPush main")

	return repo, c, originDir
}

// addBookmarkOnNewCommit creates a child commit of @- on the given branch,
// sets the bookmark to point at the new commit, and returns the new commit
// id. Leaves the workspace on a fresh empty @ so subsequent calls compose.
func addBookmarkOnNewCommit(t *testing.T, repo string, c *jj.CLIClient, branch, msg, file string) string {
	t.Helper()
	jjtest.Commit(t, repo, msg, map[string]string{file: msg + "\n"})
	cur, err := c.Log(repo, "@-")
	require.NoError(t, err)
	require.NotEmpty(t, cur)
	commitID := cur[0].CommitID
	if err := c.BookmarkSet(repo, branch, commitID, true); err != nil {
		require.NoError(t, c.BookmarkCreate(repo, branch, commitID))
	}
	return commitID
}

// mergedPR returns a minimal PRInfo for a merged PR. Sets MergedAt so
// IsMerged() returns true regardless of the State string casing.
func mergedPR(headSHA, mergeOID string) *github.PRInfo {
	merged := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	pr := &github.PRInfo{State: "merged", HeadSHA: headSHA, MergedAt: &merged}
	if mergeOID != "" {
		pr.MergeCommit = &github.PRMergeCommit{OID: mergeOID}
	}
	return pr
}

func callExecuteRemoval(repo string, c *jj.CLIClient, branch string, pr *github.PRInfo) error {
	cand := cleanup.Candidate{
		Kind:     cleanup.KindLocalBranch,
		RepoPath: repo,
		Branch:   branch,
		PRInfo:   pr,
	}
	return executeRemoval(cand, c, nil, nil)
}

// TestExecuteRemoval_StandardMerge: local feat bookmark is fully reachable
// from main@origin (origin was advanced to include feat's commits).
func TestExecuteRemoval_StandardMerge(t *testing.T) {
	repo, c, _ := setupCleanRepo(t)

	featSHA := addBookmarkOnNewCommit(t, repo, c, "feat", "feature work", "feat.txt")
	require.NoError(t, c.BookmarkSet(repo, "main", featSHA, true))
	_, err := c.GitPush(repo, jj.PushOpts{Bookmarks: []string{"main"}, AllowNew: true})
	require.NoError(t, err)

	origForce := cleanForce
	cleanForce = false
	defer func() { cleanForce = origForce }()

	err = callExecuteRemoval(repo, c, "feat", mergedPR("", ""))
	assert.NoError(t, err, "standard merge should auto-delete without error")

	for _, b := range presentBookmarks(t, c, repo) {
		assert.NotEqual(t, "feat", b.Name, "feat bookmark should have been deleted")
	}
}

// TestExecuteRemoval_SquashMerge: local feat bookmark's commits are NOT
// reachable from main@origin, but PRInfo.MergeCommit.OID *is* on
// main@origin (a fresh squash commit replaced feat's history).
func TestExecuteRemoval_SquashMerge(t *testing.T) {
	repo, c, _ := setupCleanRepo(t)

	addBookmarkOnNewCommit(t, repo, c, "feat", "feature work", "feat.txt")

	squashSHA := addBookmarkOnNewCommit(t, repo, c, "main", "squash merge result", "squash.txt")
	_, err := c.GitPush(repo, jj.PushOpts{Bookmarks: []string{"main"}, AllowNew: true})
	require.NoError(t, err)

	origForce := cleanForce
	cleanForce = false
	defer func() { cleanForce = origForce }()

	err = callExecuteRemoval(repo, c, "feat", mergedPR("", squashSHA))
	assert.NoError(t, err, "squash merge (MergeCommit on main@origin) should auto-delete")

	for _, b := range presentBookmarks(t, c, repo) {
		assert.NotEqual(t, "feat", b.Name)
	}
}

// TestExecuteRemoval_PushedHeadMatch: PRInfo.HeadSHA matches feat's local
// commit, so feat has no commits beyond what was pushed (Check 3).
func TestExecuteRemoval_PushedHeadMatch(t *testing.T) {
	repo, c, _ := setupCleanRepo(t)

	featSHA := addBookmarkOnNewCommit(t, repo, c, "feat", "feature work", "feat.txt")

	origForce := cleanForce
	cleanForce = false
	defer func() { cleanForce = origForce }()

	err := callExecuteRemoval(repo, c, "feat", mergedPR(featSHA, ""))
	assert.NoError(t, err, "local tip matching pushed HeadSHA should auto-delete")

	for _, b := range presentBookmarks(t, c, repo) {
		assert.NotEqual(t, "feat", b.Name)
	}
}

// TestExecuteRemoval_UpstreamRef: feat is also pushed to origin, so
// feat@origin exists and matches feat (Check 4).
func TestExecuteRemoval_UpstreamRef(t *testing.T) {
	repo, c, _ := setupCleanRepo(t)

	addBookmarkOnNewCommit(t, repo, c, "feat", "feature work", "feat.txt")
	_, err := c.GitPush(repo, jj.PushOpts{Bookmarks: []string{"feat"}, AllowNew: true})
	require.NoError(t, err)

	origForce := cleanForce
	cleanForce = false
	defer func() { cleanForce = origForce }()

	err = callExecuteRemoval(repo, c, "feat", mergedPR("", ""))
	assert.NoError(t, err, "local tip at feat@origin should auto-delete")

	for _, b := range presentBookmarks(t, c, repo) {
		assert.NotEqual(t, "feat", b.Name)
	}
}

// TestExecuteRemoval_UnpushedCommits: feat has extra commits beyond what
// was pushed AND beyond main@origin — all four safety checks fail.
func TestExecuteRemoval_UnpushedCommits(t *testing.T) {
	repo, c, _ := setupCleanRepo(t)

	c1 := addBookmarkOnNewCommit(t, repo, c, "feat", "pushed work", "feat1.txt")
	addBookmarkOnNewCommit(t, repo, c, "feat", "unpushed extra", "feat2.txt")

	origForce := cleanForce
	cleanForce = false
	defer func() { cleanForce = origForce }()

	err := callExecuteRemoval(repo, c, "feat", mergedPR(c1, ""))
	require.Error(t, err, "unpushed commits should block deletion")
	assert.Contains(t, err.Error(), "cannot verify bookmark is safe to delete")
	assert.Contains(t, err.Error(), "use --force to delete anyway")
	assert.Contains(t, err.Error(), "local commits exist beyond pushed PR head")
}

// TestExecuteRemoval_ForceFlag: --force bypasses all safety checks.
func TestExecuteRemoval_ForceFlag(t *testing.T) {
	repo, c, _ := setupCleanRepo(t)

	c1 := addBookmarkOnNewCommit(t, repo, c, "feat", "pushed", "feat1.txt")
	addBookmarkOnNewCommit(t, repo, c, "feat", "unpushed extra", "feat2.txt")

	origForce := cleanForce
	cleanForce = true
	defer func() { cleanForce = origForce }()

	err := callExecuteRemoval(repo, c, "feat", mergedPR(c1, ""))
	assert.NoError(t, err, "--force should bypass all checks and delete the bookmark")

	for _, b := range presentBookmarks(t, c, repo) {
		assert.NotEqual(t, "feat", b.Name)
	}
}

// presentBookmarks returns the local bookmarks that resolve to a commit,
// filtering out tombstone entries that jj retains after `bookmark delete`
// of a previously-tracked bookmark.
func presentBookmarks(t *testing.T, c *jj.CLIClient, repo string) []jj.Bookmark {
	t.Helper()
	bms, err := c.BookmarkList(repo, jj.BookmarkListOpts{Local: true})
	require.NoError(t, err)
	out := bms[:0]
	for _, b := range bms {
		if b.Present {
			out = append(out, b)
		}
	}
	return out
}

// mustRun executes name+args from dir, failing the test verbatim on error.
func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s (in %s): %v\nstderr: %s", name, strings.Join(args, " "), dir, err, stderr.String())
	}
}
