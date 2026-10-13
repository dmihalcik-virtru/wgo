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

	got, err := findRepoCandidate(repoPath, jjc, nil, 30, "")
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

// CurrentChange("") runs jj in the process's cwd, so a gone workspace used to
// borrow the bookmark of whatever repo `wgo clean` was run from and could be
// offered as a merged worktree. The stub maps "" to a merged bookmark to stand
// in for that cwd.
func TestFindRepoCandidateOrphanIgnoresCwdBookmark(t *testing.T) {
	home := t.TempDir()
	repoPath := liveWorkspace(t, home, "mains/wgo")

	jjc := &stubJJ{
		workspaces: []jj.Workspace{
			{Name: "default", Path: repoPath},
			{Name: "gone", Path: ""},
		},
		bookmarks: map[string]string{"": "feature-from-cwd"},
	}

	got, err := findRepoCandidate(repoPath, jjc, nil, 30, "")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, KindOrphanWorkspace, got[0].Kind)
	assert.Empty(t, got[0].Branch)
}
