package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/config"
	gh "github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jjtest"
)

// The re-entry block in createWorktree decides whether an existing directory
// is a usable checkout of the requested target. It prints a path on stdout
// that the user's shell cds into, so "I could not tell" must not be spelled
// "here you go".
func TestCreateWorktreeReentry(t *testing.T) {
	jjtest.RequireJJ(t)

	t.Run("already on the bookmark returns the same path", func(t *testing.T) {
		repo, jjc := jjtest.NewRepo(t)
		jjtest.Bookmark(t, repo, "feature", "@")
		cfg := &config.Config{Worktree: config.WorktreeConfig{WorktreesDir: t.TempDir()}}
		parsed := &gh.ParsedURL{Owner: "acme", Repo: "widget", Type: gh.URLTypeBranch}

		first, err := createWorktree(jjc, repo, cfg, parsed, "feature")
		require.NoError(t, err)

		// Second run takes the re-entry path: @ is already on the bookmark, so
		// it is a hit, not a reason to go moving things.
		second, err := createWorktree(jjc, repo, cfg, parsed, "feature")
		require.NoError(t, err, "re-running wgo to on an existing workspace must not fail")
		assert.Equal(t, first, second)
		assert.Equal(t, "feature", currentBookmark(jjc, second))
	})

	// The husk case: an interrupted run can leave a directory that is not a
	// checkout of anything. Handing its path back at exit 0 walks the user
	// into an empty tree, which is the one outcome `wgo to` exists to prevent.
	t.Run("no bookmark resolves locally is an error", func(t *testing.T) {
		repo, jjc := jjtest.NewRepo(t)
		wtDir := t.TempDir()
		cfg := &config.Config{Worktree: config.WorktreeConfig{WorktreesDir: wtDir}}
		parsed := &gh.ParsedURL{Owner: "acme", Repo: "widget", Type: gh.URLTypeBranch}

		// No "feature" bookmark exists anywhere in this repo.
		wtPath := filepath.Join(wtDir, "feature", "widget")
		require.NoError(t, os.MkdirAll(wtPath, 0o755))

		got, err := createWorktree(jjc, repo, cfg, parsed, "feature")

		require.Error(t, err)
		assert.Empty(t, got, "a path the caller would cd into must not be returned alongside the error")
		assert.ErrorContains(t, err, wtPath)
		assert.ErrorContains(t, err, "feature", "the error should name the bookmarks it looked for")
	})
}

// `jj workspace add` creates the destination and registers the workspace
// before it resolves the revset, so an unresolvable revset leaves a husk:
// a registered workspace whose directory holds nothing but .jj/. That husk is
// what sends the next run down the re-entry path above, so it is cleaned up
// at the source.
func TestAddWorkspaceCleansUpUnresolvableRevset(t *testing.T) {
	jjtest.RequireJJ(t)
	repo, jjc := jjtest.NewRepo(t)
	wtPath := filepath.Join(t.TempDir(), "ws")

	err := addWorkspace(jjc, repo, wtPath, "ws", "nosuchbookmark")

	require.Error(t, err)
	assert.ErrorContains(t, err, "nosuchbookmark")
	assert.NoDirExists(t, wtPath, "a failed add must not leave a husk for the next run to trip over")
	assert.False(t, workspaceRegistered(jjc, repo, "ws"),
		"the abandoned workspace must not stay in `jj workspace list`")
}

// The other way an add fails is a name that is already taken — and there the
// registration belongs to someone else. Forgetting it would turn one failed
// command into a lost workspace.
func TestAddWorkspaceKeepsWorkspaceThatOwnsTheName(t *testing.T) {
	jjtest.RequireJJ(t)
	repo, jjc := jjtest.NewRepo(t)
	jjtest.Bookmark(t, repo, "feature", "@")
	base := t.TempDir()

	first := filepath.Join(base, "first")
	require.NoError(t, addWorkspace(jjc, repo, first, "feature", "feature"))

	err := addWorkspace(jjc, repo, filepath.Join(base, "second"), "feature", "feature")

	require.Error(t, err)
	assert.True(t, workspaceRegistered(jjc, repo, "feature"),
		"cleanup must not unregister the workspace that already held the name")
	assert.DirExists(t, first, "the pre-existing workspace must survive a later clashing add")
}
