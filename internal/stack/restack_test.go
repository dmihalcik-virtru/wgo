package stack

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/store"
)

// fakeGit records every call so tests can assert on the sequence and supply
// canned responses for things like ListWorktrees and ResolveRef.
type fakeGit struct {
	worktrees  map[string][]git.WorktreeInfo // repoPath -> worktrees
	refs       map[string]string             // repoPath+":"+ref -> OID
	dirty      map[string][]string           // worktreePath -> porcelain entries
	rebaseFail map[string]error              // worktreePath -> error to return
	mergeFail  map[string]error
	pushFail   map[string]error
	calls      []string
	pushedRefs map[string][]git.ForceLeaseRef
	fetchCalls []string
}

func newFakeGit() *fakeGit {
	return &fakeGit{
		worktrees:  map[string][]git.WorktreeInfo{},
		refs:       map[string]string{},
		dirty:      map[string][]string{},
		rebaseFail: map[string]error{},
		mergeFail:  map[string]error{},
		pushFail:   map[string]error{},
		pushedRefs: map[string][]git.ForceLeaseRef{},
	}
}

func (f *fakeGit) Fetch(repoPath string) error {
	f.fetchCalls = append(f.fetchCalls, repoPath)
	return nil
}
func (f *fakeGit) IsClean(wt string) (bool, []string, error) {
	if dirty, ok := f.dirty[wt]; ok {
		return false, dirty, nil
	}
	return true, nil, nil
}
func (f *fakeGit) ListWorktrees(repoPath string) ([]git.WorktreeInfo, error) {
	return f.worktrees[repoPath], nil
}
func (f *fakeGit) ResolveRef(repoPath, ref string) (string, error) {
	if oid, ok := f.refs[repoPath+":"+ref]; ok {
		return oid, nil
	}
	return "", fmt.Errorf("unknown ref %s in %s", ref, repoPath)
}
func (f *fakeGit) Rebase(wt, onto string) error {
	f.calls = append(f.calls, fmt.Sprintf("rebase %s %s", wt, onto))
	if err, ok := f.rebaseFail[wt]; ok {
		return err
	}
	return nil
}
func (f *fakeGit) Merge(wt, ref string, noFF bool) error {
	f.calls = append(f.calls, fmt.Sprintf("merge %s %s noff=%v", wt, ref, noFF))
	if err, ok := f.mergeFail[wt]; ok {
		return err
	}
	return nil
}
func (f *fakeGit) PushForceWithLease(repoPath string, refs []git.ForceLeaseRef) error {
	f.pushedRefs[repoPath] = append(f.pushedRefs[repoPath], refs...)
	if err, ok := f.pushFail[repoPath]; ok {
		return err
	}
	return nil
}

type fakeGitHub struct {
	available   bool
	prsByBranch map[string]*github.PRInfo // "repoPath:branch" -> PR
	bodies      map[int]string
	baseUpdates map[int]string
	bodyUpdates map[int]string
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{
		available:   true,
		prsByBranch: map[string]*github.PRInfo{},
		bodies:      map[int]string{},
		baseUpdates: map[int]string{},
		bodyUpdates: map[int]string{},
	}
}

func (f *fakeGitHub) Available() bool { return f.available }
func (f *fakeGitHub) GetPRStatus(repoPath, branch string) (*github.PRInfo, error) {
	if pr, ok := f.prsByBranch[repoPath+":"+branch]; ok {
		return pr, nil
	}
	return nil, nil
}
func (f *fakeGitHub) GetPRBody(_ string, n int) (string, error) {
	return f.bodies[n], nil
}
func (f *fakeGitHub) UpdatePRBody(_ string, n int, body string) error {
	f.bodyUpdates[n] = body
	f.bodies[n] = body
	return nil
}
func (f *fakeGitHub) UpdatePRBase(_ string, n int, base string) error {
	f.baseUpdates[n] = base
	return nil
}

// stackStateLinear builds a state with three branches a, b, c all in /repo,
// with b parent=a and c parent=b. Returns the state and the corresponding graph
// (already verified to build cleanly).
func stackStateLinear(t *testing.T) *store.State {
	t.Helper()
	s := store.NewState()
	s.AddStack(store.Stack{ID: "s1", Name: "linear"})
	for _, b := range []string{"a", "b", "c"} {
		s.AddAnnotation("/repo", b, "")
		s.SetStackID("/repo", b, "s1")
	}
	s.SetParents("/repo", "b", []string{"/repo:a"})
	s.SetParents("/repo", "c", []string{"/repo:b"})
	return s
}

func gitForLinearStack() *fakeGit {
	g := newFakeGit()
	g.worktrees["/repo"] = []git.WorktreeInfo{
		{Path: "/wt/a", Branch: "a", IsMain: true},
		{Path: "/wt/b", Branch: "b"},
		{Path: "/wt/c", Branch: "c"},
	}
	// Local branch tips (used when resolving in-stack parents).
	g.refs["/repo:a"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	g.refs["/repo:b"] = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	g.refs["/repo:c"] = "cccccccccccccccccccccccccccccccccccccccc"
	// Remote tips for lease capture.
	g.refs["/repo:origin/a"] = "a1111111111111111111111111111111111111111"[:40]
	g.refs["/repo:origin/b"] = "b1111111111111111111111111111111111111111"[:40]
	g.refs["/repo:origin/c"] = "c1111111111111111111111111111111111111111"[:40]
	return g
}

func TestRestackLinearHappyPath(t *testing.T) {
	state := stackStateLinear(t)
	g := gitForLinearStack()
	gh := newFakeGitHub()

	tmp := t.TempDir()
	res, err := Restack(g, gh, state, Options{
		WgoBaseDir: tmp,
		StackID:    "s1",
		StartFrom:  "/repo:a",
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	// b and c should be rebased in order; a is the starting node and is skipped.
	assert.Equal(t, []string{"/repo:b", "/repo:c"}, res.PlannedNodes)
	assert.Equal(t, []string{"/repo:b", "/repo:c"}, res.Completed)

	// Exactly one atomic push to /repo containing both branches with captured leases.
	require.Len(t, g.pushedRefs["/repo"], 2)
	assert.Equal(t, "b", g.pushedRefs["/repo"][0].Branch)
	assert.Equal(t, g.refs["/repo:origin/b"], g.pushedRefs["/repo"][0].ExpectedOID)
	assert.Equal(t, "c", g.pushedRefs["/repo"][1].Branch)
	assert.Equal(t, g.refs["/repo:origin/c"], g.pushedRefs["/repo"][1].ExpectedOID)

	// Checkpoint cleaned up.
	cp, err := LoadCheckpoint(tmp, "s1")
	require.NoError(t, err)
	assert.Nil(t, cp, "checkpoint should be deleted on success")
}

func TestRestackDirtyWorktreeHalts(t *testing.T) {
	state := stackStateLinear(t)
	g := gitForLinearStack()
	g.dirty["/wt/b"] = []string{" M unrelated.txt"}

	tmp := t.TempDir()
	res, err := Restack(g, newFakeGitHub(), state, Options{
		WgoBaseDir: tmp,
		StackID:    "s1",
		StartFrom:  "/repo:a",
	})
	require.NoError(t, err)
	require.Len(t, res.RebaseConflicts, 1)
	assert.Equal(t, "/repo:b", res.RebaseConflicts[0].Node)
	assert.Equal(t, "precheck", res.RebaseConflicts[0].Operation)
	assert.Equal(t, []string{" M unrelated.txt"}, res.RebaseConflicts[0].DirtyPaths)

	// Nothing pushed since the walk halted.
	assert.Empty(t, g.pushedRefs)

	// Checkpoint persists for `--continue`.
	cp, err := LoadCheckpoint(tmp, "s1")
	require.NoError(t, err)
	require.NotNil(t, cp)
	assert.Equal(t, 0, cp.CurrentIndex, "halted at first descendant")
}

func TestRestackConflictWritesCheckpointAndResumes(t *testing.T) {
	state := stackStateLinear(t)
	g := gitForLinearStack()
	g.rebaseFail["/wt/b"] = errors.New("CONFLICT in foo.go")

	tmp := t.TempDir()
	res, err := Restack(g, newFakeGitHub(), state, Options{
		WgoBaseDir: tmp,
		StackID:    "s1",
		StartFrom:  "/repo:a",
	})
	require.NoError(t, err)
	require.Len(t, res.RebaseConflicts, 1)
	assert.Equal(t, "rebase", res.RebaseConflicts[0].Operation)
	assert.Contains(t, res.RebaseConflicts[0].Err.Error(), "CONFLICT")

	// User "resolves" — clear the canned failure — and resumes.
	g.rebaseFail = map[string]error{}
	res2, err := Restack(g, newFakeGitHub(), state, Options{
		WgoBaseDir: tmp,
		StackID:    "s1",
		Continue:   true,
	})
	require.NoError(t, err)
	assert.Empty(t, res2.RebaseConflicts)
	assert.Equal(t, []string{"/repo:b", "/repo:c"}, res2.Completed,
		"resume should re-run from the conflicted node and finish")

	cp, _ := LoadCheckpoint(tmp, "s1")
	assert.Nil(t, cp, "checkpoint cleaned up after successful resume")
}

func TestRestackMergeNode(t *testing.T) {
	s := store.NewState()
	s.AddStack(store.Stack{ID: "s2"})
	for _, b := range []string{"a", "b", "c"} {
		s.AddAnnotation("/repo", b, "")
		s.SetStackID("/repo", b, "s2")
	}
	// c has two parents (merge node).
	s.SetParents("/repo", "c", []string{"/repo:a", "/repo:b"})

	g := newFakeGit()
	g.worktrees["/repo"] = []git.WorktreeInfo{
		{Path: "/wt/a", Branch: "a"},
		{Path: "/wt/b", Branch: "b"},
		{Path: "/wt/c", Branch: "c"},
	}
	g.refs["/repo:a"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	g.refs["/repo:b"] = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	g.refs["/repo:origin/a"] = "a1111111111111111111111111111111111111111"[:40]
	g.refs["/repo:origin/b"] = "b1111111111111111111111111111111111111111"[:40]
	g.refs["/repo:origin/c"] = "c1111111111111111111111111111111111111111"[:40]

	tmp := t.TempDir()
	res, err := Restack(g, newFakeGitHub(), s, Options{
		WgoBaseDir: tmp,
		StackID:    "s2",
		StartFrom:  "/repo:a",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"/repo:c"}, res.Completed,
		"changing a should affect c (b is independent of a)")

	// Must have done one rebase onto a, then one merge --no-ff of b's tip.
	require.Len(t, g.calls, 2)
	assert.Contains(t, g.calls[0], "rebase /wt/c")
	assert.Contains(t, g.calls[1], "merge /wt/c")
	assert.Contains(t, g.calls[1], "noff=true")
}

func TestRestackDryRun(t *testing.T) {
	state := stackStateLinear(t)
	g := gitForLinearStack()

	tmp := t.TempDir()
	res, err := Restack(g, newFakeGitHub(), state, Options{
		WgoBaseDir: tmp,
		StackID:    "s1",
		StartFrom:  "/repo:a",
		DryRun:     true,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"/repo:b", "/repo:c"}, res.PlannedNodes)
	assert.Empty(t, g.calls, "dry run must not invoke rebase/merge")
	assert.Empty(t, g.pushedRefs)
	assert.Empty(t, g.fetchCalls)
}

func TestRestackUpdatesPRBody(t *testing.T) {
	state := stackStateLinear(t)
	g := gitForLinearStack()
	gh := newFakeGitHub()
	gh.prsByBranch["/repo:a"] = &github.PRInfo{Number: 10, Branch: "a"}
	gh.prsByBranch["/repo:b"] = &github.PRInfo{Number: 11, Branch: "b"}
	gh.prsByBranch["/repo:c"] = &github.PRInfo{Number: 12, Branch: "c"}
	gh.bodies[10] = "PR for a"
	gh.bodies[11] = "PR for b"
	gh.bodies[12] = "PR for c"

	tmp := t.TempDir()
	_, err := Restack(g, gh, state, Options{
		WgoBaseDir: tmp,
		StackID:    "s1",
		StartFrom:  "/repo:a",
	})
	require.NoError(t, err)

	for _, num := range []int{10, 11, 12} {
		assert.Contains(t, gh.bodyUpdates[num], "<!-- wgo-stack:s1 -->",
			"PR #%d body should be updated with marker", num)
		assert.Contains(t, gh.bodyUpdates[num], "<!-- /wgo-stack -->")
	}
	// Each PR's marker should mark itself.
	assert.Contains(t, gh.bodyUpdates[11], "**#11 b ↳ on #10** ← this PR")
}

func TestRestackContinueWithoutCheckpointErrors(t *testing.T) {
	state := stackStateLinear(t)
	g := gitForLinearStack()

	tmp := t.TempDir()
	_, err := Restack(g, newFakeGitHub(), state, Options{
		WgoBaseDir: tmp,
		StackID:    "s1",
		Continue:   true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no checkpoint")
}

func TestRestackNoDescendantsIsNoOp(t *testing.T) {
	state := stackStateLinear(t)
	g := gitForLinearStack()

	tmp := t.TempDir()
	res, err := Restack(g, newFakeGitHub(), state, Options{
		WgoBaseDir: tmp,
		StackID:    "s1",
		StartFrom:  "/repo:c", // leaf — nothing downstream
	})
	require.NoError(t, err)
	assert.Empty(t, res.Completed)
	assert.Empty(t, g.pushedRefs)
}
