package cleanup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/jj"
)

// A workspace jj still lists after its directory was deleted (jj reports no
// root), or whose root lost its .jj directory, is offered as an orphan to
// forget, never as a worktree to delete.
func TestFindRepoCandidateOrphanedWorkspaces(t *testing.T) {
	home := t.TempDir()
	repoPath := liveWorkspace(t, home, "mains/wgo")
	noJJ := filepath.Join(home, "worktrees", "no-jj")
	require.NoError(t, os.MkdirAll(noJJ, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(noJJ, "notes.txt"), []byte("keep\n"), 0o644))

	jjc := &stubJJ{
		workspaces: []jj.Workspace{
			{Name: "default", Path: repoPath},
			{Name: "gone", Path: ""},
			{Name: "no-jj", Path: noJJ},
			{Name: "rig-dsp-2.7.1-sdk", Path: ""},
		},
	}

	got, _, _, err := findRepoCandidate(repoPath, jjc, nil, repoPass{workspaces: true, bookmarks: true, classify: removeAll})
	require.NoError(t, err)
	require.Len(t, got, 2, "rig workspaces stay owned by `wgo rig rm`, even orphaned")

	assert.Equal(t, KindOrphanWorkspace, got[0].Kind)
	assert.Equal(t, "gone", got[0].WorkspaceName)
	assert.Equal(t, "its directory is gone", got[0].Reason)
	assert.Equal(t, repoPath+" [workspace gone]", got[0].DisplayPath())

	assert.Equal(t, KindOrphanWorkspace, got[1].Kind)
	assert.Equal(t, "no-jj", got[1].WorkspaceName)
	assert.Equal(t, noJJ, got[1].Path)
	assert.Contains(t, got[1].Reason, "has no .jj directory")
}

// Reading a workspace at "" runs jj in the process's cwd, so a gone workspace
// used to borrow the bookmark of whatever repo `wgo clean` was run from and
// could be offered as a merged worktree. An orphan must never reach the
// classifier.
func TestFindRepoCandidateOrphanIsNeverClassified(t *testing.T) {
	home := t.TempDir()
	repoPath := liveWorkspace(t, home, "mains/wgo")

	jjc := &stubJJ{
		workspaces: []jj.Workspace{
			{Name: "default", Path: repoPath},
			{Name: "gone", Path: ""},
		},
	}
	classify := func(ref WorkspaceRef) Classification {
		t.Errorf("orphaned workspace %q was classified", ref.Name)
		return removeAll(ref)
	}

	got, _, _, err := findRepoCandidate(repoPath, jjc, nil, repoPass{workspaces: true, bookmarks: true, classify: classify})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, KindOrphanWorkspace, got[0].Kind)
	assert.Empty(t, got[0].Branch)
}

func TestFindRepoCandidateOrphanDoesNotHideLocalBookmark(t *testing.T) {
	repoPath := liveWorkspace(t, t.TempDir(), "main")
	jjc := &stubJJ{
		workspaces: []jj.Workspace{
			{Name: "default", Path: repoPath},
			{Name: "gone", CommitID: "same-commit"},
		},
		local: []jj.Bookmark{{Name: "feature", CommitID: "same-commit"}},
	}

	for _, pass := range []repoPass{
		{workspaces: true, bookmarks: true, classify: removeAll},
		{bookmarks: true},
	} {
		got, _, warnings, err := findRepoCandidate(repoPath, jjc, nil, pass)
		require.NoError(t, err)
		assert.Empty(t, warnings)
		assert.Equal(t, cleanupKinds(pass.workspaces), candidateKinds(got))
	}
}

func cleanupKinds(withWorkspaces bool) []CandidateKind {
	if withWorkspaces {
		return []CandidateKind{KindOrphanWorkspace, KindLocalBranch}
	}
	return []CandidateKind{KindLocalBranch}
}

func candidateKinds(candidates []Candidate) []CandidateKind {
	kinds := make([]CandidateKind, len(candidates))
	for i, c := range candidates {
		kinds[i] = c.Kind
	}
	return kinds
}

func TestFindRepoCandidateLiveWorkspaceCoversBookmark(t *testing.T) {
	home := t.TempDir()
	repoPath := liveWorkspace(t, home, "main")
	wsPath := liveWorkspace(t, home, "feature")
	jjc := &stubJJ{
		workspaces: []jj.Workspace{
			{Name: "default", Path: repoPath},
			{Name: "feature", Path: wsPath, CommitID: "same-commit"},
		},
		local: []jj.Bookmark{{Name: "feature", CommitID: "same-commit"}},
	}

	got, _, _, err := findRepoCandidate(repoPath, jjc, nil, repoPass{workspaces: true, bookmarks: true, classify: removeAll})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, KindWorktree, got[0].Kind)
}

func TestFindRepoCandidateUncheckableWorkspaceWarnsAndKeepsBookmark(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	home := t.TempDir()
	repoPath := liveWorkspace(t, home, "main")
	locked := liveWorkspace(t, home, "locked")
	require.NoError(t, os.Chmod(locked, 0))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	jjc := &stubJJ{
		workspaces: []jj.Workspace{
			{Name: "default", Path: repoPath},
			{Name: "locked", Path: locked, CommitID: "same-commit"},
		},
		local: []jj.Bookmark{{Name: "feature", CommitID: "same-commit"}},
	}

	got, _, warnings, err := findRepoCandidate(repoPath, jjc, nil, repoPass{workspaces: true, bookmarks: true, classify: removeAll})
	require.NoError(t, err)
	assert.Empty(t, got)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], `skipped workspace "locked"`)
}
