package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/cleanup"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/jjtest"
)

func workspaceNames(t *testing.T, c *jj.CLIClient, repo string) []string {
	t.Helper()
	wss, err := c.ListWorkspaces(repo)
	require.NoError(t, err)
	var names []string
	for _, w := range wss {
		names = append(names, w.Name)
	}
	return names
}

func orphanCandidate(repo, name, path string) cleanup.Candidate {
	return cleanup.Candidate{Kind: cleanup.KindOrphanWorkspace, RepoPath: repo, WorkspaceName: name, Path: path}
}

// Every gone workspace has an empty path, so looking the name up by path
// forgot whichever gone workspace jj listed first. Removal must forget the
// one that was chosen.
func TestExecuteRemoval_OrphanForgetsByName(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	require.NoError(t, os.RemoveAll(jjtest.NewWorkspace(t, repo, "alpha")))
	require.NoError(t, os.RemoveAll(jjtest.NewWorkspace(t, repo, "beta")))

	require.NoError(t, executeRemoval(orphanCandidate(repo, "beta", ""), c, nil, nil))

	names := workspaceNames(t, c, repo)
	assert.Contains(t, names, "alpha")
	assert.NotContains(t, names, "beta")
}

// A root that lost its .jj directory may hold files that matter; removal
// forgets the workspace and leaves the directory alone.
func TestExecuteRemoval_OrphanKeepsDirectory(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	ws := jjtest.NewWorkspace(t, repo, "nojj")
	require.NoError(t, os.RemoveAll(filepath.Join(ws, ".jj")))
	notes := filepath.Join(ws, "notes.txt")
	require.NoError(t, os.WriteFile(notes, []byte("keep\n"), 0o644))

	require.NoError(t, executeRemoval(orphanCandidate(repo, "nojj", ws), c, nil, nil))

	assert.NotContains(t, workspaceNames(t, c, repo), "nojj")
	assert.FileExists(t, notes)
}

// A workspace that is whole again by the time the user answers the prompt is
// not forgotten.
func TestExecuteRemoval_OrphanThatCameBack(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	ws := jjtest.NewWorkspace(t, repo, "back")

	err := executeRemoval(orphanCandidate(repo, "back", ws), c, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is back")
	assert.Contains(t, workspaceNames(t, c, repo), "back")
}

// Forgetting a workspace must not lose edits in its working-copy commit: jj
// keeps a non-empty one visible after the workspace is gone.
func TestExecuteRemoval_OrphanKeepsWorkingCopyChange(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	ws := jjtest.NewWorkspace(t, repo, "edits")
	require.NoError(t, os.WriteFile(filepath.Join(ws, "wip.txt"), []byte("wip\n"), 0o644))
	cur, err := c.CurrentChange(ws) // snapshots wip.txt into @
	require.NoError(t, err)
	require.False(t, cur.Empty, "wip.txt should be in the working-copy change")
	require.NoError(t, os.RemoveAll(ws))

	require.NoError(t, executeRemoval(orphanCandidate(repo, "edits", ""), c, nil, nil))

	assert.NotContains(t, workspaceNames(t, c, repo), "edits")
	got, err := c.Log(repo, cur.ChangeID)
	require.NoError(t, err, "the working-copy change should still resolve")
	require.Len(t, got, 1)
	assert.False(t, got[0].Empty)
}

// --worktrees covers orphans too: they are still workspaces.
func TestCleanWorktreesFilterIncludesOrphans(t *testing.T) {
	cands := []cleanup.Candidate{
		{Kind: cleanup.KindWorktree},
		{Kind: cleanup.KindOrphanWorkspace},
		{Kind: cleanup.KindLocalBranch},
	}
	got := cleanup.FilterKinds(cands, worktreeKinds...)
	require.Len(t, got, 2)
}
