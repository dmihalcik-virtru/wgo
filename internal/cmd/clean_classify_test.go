package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/cleanup"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/jjtest"
)

// The scan reads workspaces without snapshotting them, so a file edited since
// jj last looked is invisible to it. Removal snapshots first and must refuse a
// workspace whose verdict that changes.
func TestRecheckWorkspaceCatchesUnsnapshottedEdits(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	ws := jjtest.NewWorkspace(t, repo, "scratchpad")
	classifier := cleanup.NewClassifier(c.ReadOnly(), nil, cleanup.ClassifyOptions{
		StaleAfter: 14 * 24 * time.Hour,
		LastUsed:   func(string) time.Time { return time.Now().Add(-90 * 24 * time.Hour) },
	})
	cl := classifier.Classify(cleanup.WorkspaceRef{Name: "scratchpad", Path: ws, RepoPath: repo, DefaultBranch: "main"})
	require.Equal(t, cleanup.TypeIdle, cl.Type, "evidence: %q", cl.Evidence)
	cand := Candidate{Kind: cleanup.KindWorktree, Path: ws, RepoPath: repo, WorkspaceName: "scratchpad", Class: &cl}

	assert.Empty(t, recheckWorkspace(cand, c, classifier), "an untouched workspace passes")

	require.NoError(t, os.WriteFile(filepath.Join(ws, "notes.txt"), []byte("not yet snapshotted\n"), 0o644))
	why := recheckWorkspace(cand, c, classifier)
	assert.Contains(t, why, "changed since the scan")
	assert.Contains(t, why, "unpushed")
}

// A workspace that needs a decision is removed on the evidence the person
// confirmed. Edits that appear afterwards are not in that evidence, so the
// removal is skipped even when they leave the verdict unchanged.
func TestRecheckWorkspaceSkipsAChangedDecision(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	ws := jjtest.NewWorkspace(t, repo, "draft")
	jjtest.Commit(t, ws, "feat: draft", map[string]string{"draft.txt": "draft\n"})
	classifier := cleanup.NewClassifier(c.ReadOnly(), nil, cleanup.ClassifyOptions{
		StaleAfter: 14 * 24 * time.Hour,
		LastUsed:   func(string) time.Time { return time.Now().Add(-90 * 24 * time.Hour) },
	})
	ref := cleanup.WorkspaceRef{Name: "draft", Path: ws, RepoPath: repo, DefaultBranch: "main"}
	cl := classifier.Classify(ref)
	require.Equal(t, cleanup.TypeUnpushed, cl.Type, "evidence: %q", cl.Evidence)
	require.Equal(t, cleanup.TierDecide, cl.Tier)
	cand := Candidate{Kind: cleanup.KindWorktree, Path: ws, RepoPath: repo, WorkspaceName: "draft", Class: &cl}

	assert.Empty(t, recheckWorkspace(cand, c, classifier), "an untouched workspace passes")

	require.NoError(t, os.WriteFile(filepath.Join(ws, "more.txt"), []byte("written after the prompt\n"), 0o644))
	assert.Contains(t, recheckWorkspace(cand, c, classifier), "changed after you confirmed it")
	now := classifier.Classify(ref)
	assert.Equal(t, cleanup.TypeUnpushed, now.Type, "the verdict alone would not catch the new file")
	assert.Equal(t, cleanup.TierDecide, now.Tier)
}

func TestSplitByTier(t *testing.T) {
	decide := func(typ cleanup.WorkspaceType, path string) Candidate {
		return Candidate{Kind: cleanup.KindWorktree, Path: path, Class: &cleanup.Classification{Type: typ, Tier: cleanup.TierDecide}}
	}
	remove := Candidate{Kind: cleanup.KindWorktree, Path: "/w/idle", Class: &cleanup.Classification{Type: cleanup.TypeIdle, Tier: cleanup.TierRemove}}
	branch := Candidate{Kind: cleanup.KindLocalBranch, Branch: "old"}

	gotRemove, gotDecide := splitByTier([]Candidate{
		decide(cleanup.TypeUnpushed, "/w/b"), remove, decide(cleanup.TypeScratch, "/w/a"), branch, decide(cleanup.TypeUnpushed, "/w/a"),
	})
	assert.Equal(t, []Candidate{remove, branch}, gotRemove)
	var order []string
	for _, c := range gotDecide {
		order = append(order, string(c.Class.Type)+" "+c.Path)
	}
	assert.Equal(t, []string{"scratch /w/a", "unpushed /w/a", "unpushed /w/b"}, order)
}

// staleWorkspace returns a workspace whose @ sits on a bookmarked commit with
// a file of its own, classified while it is fresh, plus a function that makes
// it stale the way a fetch does: abandoning that commit from another
// workspace, which rebases the workspace's @ and changes its tree.
func staleWorkspace(t *testing.T) (Candidate, *cleanup.Classifier, func()) {
	t.Helper()
	remote := jjtest.NewBareRemote(t)
	repo, c := jjtest.NewRepo(t)
	require.NoError(t, c.GitRemoteAdd(repo, "origin", remote))
	jjtest.Commit(t, repo, "base", map[string]string{"a.txt": "a\n"})
	require.NoError(t, c.BookmarkCreate(repo, "main", "@-"))
	_, err := c.GitPush(repo, jj.PushOpts{Bookmarks: []string{"main"}, AllowNew: true})
	require.NoError(t, err)

	ws := jjtest.NewWorkspace(t, repo, "parked")
	mustRun(t, ws, "jj", "new", "main@origin")
	jjtest.Commit(t, ws, "feature", map[string]string{"f.txt": "f\n"})
	jjtest.Bookmark(t, ws, "feature", "@-")
	classifier := cleanup.NewClassifier(c.ReadOnly(), nil, cleanup.ClassifyOptions{
		StaleAfter: 14 * 24 * time.Hour,
		LastUsed:   func(string) time.Time { return time.Now().Add(-90 * 24 * time.Hour) },
	})
	cl := classifier.Classify(cleanup.WorkspaceRef{Name: "parked", Path: ws, RepoPath: repo, DefaultBranch: "main"})
	require.Equal(t, cleanup.TypeActive, cl.Type, "evidence: %q", cl.Evidence)
	cand := Candidate{Kind: cleanup.KindWorktree, Path: ws, RepoPath: repo, WorkspaceName: "parked", Class: &cl}
	return cand, classifier, func() { mustRun(t, repo, "jj", "abandon", "feature") }
}

func TestRecheckWorkspaceStaleWithUnrecordedEdits(t *testing.T) {
	cand, classifier, makeStale := staleWorkspace(t)
	require.NoError(t, os.WriteFile(filepath.Join(cand.Path, "notes.txt"), []byte("unrecorded\n"), 0o644))
	makeStale()

	why := recheckWorkspace(cand, jj.NewCLI(), classifier)
	assert.Contains(t, why, "kept unrecorded edits in commit")
}

// Without unrecorded edits the stale workspace is updated and classified
// afresh; abandoning its only work makes it idle, a different verdict.
func TestRecheckWorkspaceStaleWithoutEdits(t *testing.T) {
	cand, classifier, makeStale := staleWorkspace(t)
	makeStale()

	why := recheckWorkspace(cand, jj.NewCLI(), classifier)
	assert.Contains(t, why, "changed since the scan; now idle")
}

// idleCandidate returns a fresh repo's idle workspace, classified, and the
// classifier.
func idleCandidate(t *testing.T) (Candidate, *cleanup.Classifier) {
	t.Helper()
	repo, c := jjtest.NewRepo(t)
	ws := jjtest.NewWorkspace(t, repo, "idle")
	classifier := cleanup.NewClassifier(c.ReadOnly(), nil, cleanup.ClassifyOptions{
		LastUsed: func(string) time.Time { return time.Now().Add(-90 * 24 * time.Hour) },
	})
	cl := classifier.Classify(cleanup.WorkspaceRef{Name: "idle", Path: ws, RepoPath: repo, DefaultBranch: "main"})
	require.Equal(t, cleanup.TypeIdle, cl.Type, "evidence: %q", cl.Evidence)
	return Candidate{Kind: cleanup.KindWorktree, Path: ws, RepoPath: repo, WorkspaceName: "idle", Class: &cl}, classifier
}

// A new file jj refuses to snapshot is in no commit; deleting the directory
// would lose it.
func TestRecheckWorkspaceRefusesUntrackedFiles(t *testing.T) {
	cand, classifier := idleCandidate(t)
	mustRun(t, cand.Path, "jj", "config", "set", "--repo", "snapshot.max-new-file-size", "1KiB")
	require.NoError(t, os.WriteFile(filepath.Join(cand.Path, "big.bin"), make([]byte, 4096), 0o644))

	assert.Contains(t, recheckWorkspace(cand, jj.NewCLI(), classifier), "jj does not track 1 file (e.g. big.bin)")
}

// Files outside a sparse workspace's patterns are invisible to jj.
func TestRecheckWorkspaceRefusesFilesOutsideSparsePatterns(t *testing.T) {
	cand, classifier := idleCandidate(t)
	mustRun(t, cand.Path, "jj", "sparse", "set", "--clear", "--add", "src")
	cl := classifier.Classify(cand.Class.Ref)
	cand.Class = &cl
	require.NoError(t, os.MkdirAll(filepath.Join(cand.Path, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cand.Path, "src", "tracked.txt"), []byte("in\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cand.Path, "notes.txt"), []byte("out\n"), 0o644))

	why := recheckWorkspace(cand, jj.NewCLI(), classifier)
	assert.Contains(t, why, "outside its sparse patterns")
	assert.Contains(t, why, "notes.txt")
}
