package cleanup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/jj"
)

// stubJJ implements only the handful of jj.Client methods findRepoCandidate
// touches. The embedded nil interface makes any other call panic, so the test
// fails loudly rather than silently exercising a different code path if
// findRepoCandidate grows a new dependency.
type stubJJ struct {
	jj.Client
	workspaces []jj.Workspace
}

func (s *stubJJ) RemoteURLs(string) (map[string]string, error) { return nil, nil }

func (s *stubJJ) ListWorkspaces(string) ([]jj.Workspace, error) { return s.workspaces, nil }

// Every bookmark reads as fully merged into the default branch.
func (s *stubJJ) CountRevset(string, string) (int, error) { return 0, nil }

func (s *stubJJ) BookmarkList(string, jj.BookmarkListOpts) ([]jj.Bookmark, error) {
	return nil, nil
}

// removeAll classifies every workspace as removable, so a test sees exactly
// which workspaces reach the classifier.
func removeAll(WorkspaceRef) Classification {
	return Classification{Type: TypeIdle, Tier: TierRemove, Evidence: []string{"test"}}
}

// liveWorkspace creates path under root with a .jj directory, so the orphan
// check reads it as a workspace that still exists, and returns path.
func liveWorkspace(t *testing.T, root, rel string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Join(path, ".jj"), 0o755))
	return path
}

// A rig workspace pinned to a released tag can end up with a local bookmark on
// its @ — someone runs `jj bookmark set` there, or an old bookmark already
// points at the tagged commit. At that moment it looks exactly like an
// abandoned, fully-merged worktree, which is what `wgo clean` offers to delete.
func TestFindRepoCandidateSkipsRigWorkspaces(t *testing.T) {
	home := t.TempDir()
	repoPath := liveWorkspace(t, home, "mains/opentdf/platform")
	rigDir := filepath.Join(home, "rigs")

	rigWS := liveWorkspace(t, rigDir, "dsp-2.7.1/src/platform-service-v0.11.6")
	strayWS := liveWorkspace(t, home, "scratch/platform-sdk-v0.10.1")
	realWS := liveWorkspace(t, home, "worktrees/WGO-136/platform")

	jjc := &stubJJ{
		workspaces: []jj.Workspace{
			{Name: "default", Path: repoPath},
			{Name: "rig-dsp-2.7.1-service", Path: rigWS},
			{Name: "rig-dsp-2.7.1-sdk", Path: strayWS},
			{Name: "WGO-136-add-rig", Path: realWS},
		},
	}

	got, _, err := findRepoCandidate(repoPath, jjc, nil, repoPass{workspaces: true, bookmarks: true, classify: removeAll, rigDir: rigDir})
	require.NoError(t, err)

	require.Len(t, got, 1, "only the ordinary worktree is a candidate")
	assert.Equal(t, realWS, got[0].Path)
	assert.Equal(t, KindWorktree, got[0].Kind)
}

// Without the exclusion every rig checkout is offered for deletion. This
// asserts the guard is what is doing the work, not some unrelated filter.
func TestFindRepoCandidateWithoutRigExclusion(t *testing.T) {
	home := t.TempDir()
	repoPath := liveWorkspace(t, home, "mains/opentdf/platform")
	rigWS := liveWorkspace(t, home, "rigs/dsp-2.7.1/src/platform-service-v0.11.6")

	jjc := &stubJJ{
		workspaces: []jj.Workspace{
			{Name: "default", Path: repoPath},
			{Name: "service-v0-11-6", Path: rigWS},
		},
	}

	got, _, err := findRepoCandidate(repoPath, jjc, nil, repoPass{workspaces: true, bookmarks: true, classify: removeAll})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, rigWS, got[0].Path)
}
