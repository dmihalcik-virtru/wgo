package cleanup

import (
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
	bookmarks  map[string]string // workspace path -> bookmark on @
}

func (s *stubJJ) RemoteURLs(string) (map[string]string, error) { return nil, nil }

func (s *stubJJ) ListWorkspaces(string) ([]jj.Workspace, error) { return s.workspaces, nil }

func (s *stubJJ) CurrentChange(wsPath string) (jj.Change, error) {
	bm := s.bookmarks[wsPath]
	if bm == "" {
		return jj.Change{}, nil
	}
	return jj.Change{Bookmarks: []string{bm}}, nil
}

func (s *stubJJ) Status(string) (jj.Status, error) { return jj.Status{Clean: true}, nil }

// Every bookmark reads as fully merged into the default branch — the worst
// case for a rig, since that is what makes a workspace a cleanup candidate.
func (s *stubJJ) CountRevset(string, string) (int, error) { return 0, nil }

func (s *stubJJ) BookmarkList(string, jj.BookmarkListOpts) ([]jj.Bookmark, error) {
	return nil, nil
}

// A rig workspace pinned to a released tag can end up with a local bookmark on
// its @ — someone runs `jj bookmark set` there, or an old bookmark already
// points at the tagged commit. At that moment it looks exactly like an
// abandoned, fully-merged worktree, which is what `wgo clean` offers to delete.
func TestFindRepoCandidateSkipsRigWorkspaces(t *testing.T) {
	repoPath := filepath.FromSlash("/home/u/mains/opentdf/platform")
	rigDir := filepath.FromSlash("/home/u/rigs")

	rigWS := filepath.Join(rigDir, "dsp-2.7.1", "src", "platform-service-v0.11.6")
	strayWS := filepath.FromSlash("/tmp/scratch/platform-sdk-v0.10.1")
	realWS := filepath.FromSlash("/home/u/worktrees/WGO-136/platform")

	jjc := &stubJJ{
		workspaces: []jj.Workspace{
			{Name: "default", Path: repoPath},
			{Name: "rig-dsp-2.7.1-service", Path: rigWS},
			{Name: "rig-dsp-2.7.1-sdk", Path: strayWS},
			{Name: "WGO-136-add-rig", Path: realWS},
		},
		bookmarks: map[string]string{
			rigWS:   "service/v0.11.6",
			strayWS: "sdk/v0.10.1",
			realWS:  "WGO-136-add-rig",
		},
	}

	got, err := findRepoCandidate(repoPath, jjc, nil, 30, rigDir)
	require.NoError(t, err)

	require.Len(t, got, 1, "only the ordinary worktree is a candidate")
	assert.Equal(t, realWS, got[0].Path)
	assert.Equal(t, KindWorktree, got[0].Kind)
}

// Without the exclusion every rig checkout is offered for deletion. This
// asserts the guard is what is doing the work, not some unrelated filter.
func TestFindRepoCandidateWithoutRigExclusion(t *testing.T) {
	repoPath := filepath.FromSlash("/home/u/mains/opentdf/platform")
	rigWS := filepath.FromSlash("/home/u/rigs/dsp-2.7.1/src/platform-service-v0.11.6")

	jjc := &stubJJ{
		workspaces: []jj.Workspace{
			{Name: "default", Path: repoPath},
			{Name: "service-v0-11-6", Path: rigWS},
		},
		bookmarks: map[string]string{rigWS: "service/v0.11.6"},
	}

	got, err := findRepoCandidate(repoPath, jjc, nil, 30, "")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, rigWS, got[0].Path)
}
