package cleanup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/store"
)

func TestCandidateKindString(t *testing.T) {
	tests := []struct {
		kind CandidateKind
		want string
	}{
		{KindWorktree, "worktree"},
		{KindLocalBranch, "local branch"},
		{KindRemoteBranch, "remote branch"},
		{KindRepo, "repo"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.kind.String(), "CandidateKind(%d).String()", tt.kind)
	}
}

func TestDisplayPath(t *testing.T) {
	c := Candidate{Kind: KindLocalBranch, RepoPath: "/repos/foo", Branch: "feat"}
	assert.Equal(t, "/repos/foo [feat]", c.DisplayPath())

	c2 := Candidate{Kind: KindWorktree, Path: "/worktrees/foo", RepoPath: "/repos/foo"}
	assert.Equal(t, "/worktrees/foo", c2.DisplayPath())
}

func TestFilterKind(t *testing.T) {
	cs := []Candidate{
		{Kind: KindWorktree},
		{Kind: KindLocalBranch},
		{Kind: KindRemoteBranch},
		{Kind: KindWorktree},
	}
	got := FilterKind(cs, KindWorktree)
	assert.Len(t, got, 2, "expected 2 worktree candidates")
}

func TestFilterKinds(t *testing.T) {
	cs := []Candidate{
		{Kind: KindWorktree},
		{Kind: KindLocalBranch},
		{Kind: KindRemoteBranch},
	}
	got := FilterKinds(cs, KindWorktree, KindLocalBranch)
	assert.Len(t, got, 2)
}

func TestSenescenceReason(t *testing.T) {
	merged := time.Now().Add(-48 * time.Hour)
	pr := &github.PRInfo{
		Number:   42,
		State:    "merged",
		MergedAt: &merged,
	}
	reason := SenescenceReason(pr)
	assert.NotEmpty(t, reason, "expected non-empty reason for merged PR")

	pr2 := (*github.PRInfo)(nil)
	assert.Empty(t, SenescenceReason(pr2), "expected empty reason for nil PR")
}

func TestGroupByRepo(t *testing.T) {
	cs := []Candidate{
		{Kind: KindLocalBranch, RepoPath: "/repos/a", Branch: "b1"},
		{Kind: KindLocalBranch, RepoPath: "/repos/a", Branch: "b2"},
		{Kind: KindLocalBranch, RepoPath: "/repos/b", Branch: "b1"},
	}
	groups := GroupByRepo(cs)
	require.Len(t, groups["/repos/a"], 2)
	require.Len(t, groups["/repos/b"], 1)
}

func TestPRInfoMethods(t *testing.T) {
	now := time.Now()
	merged := &github.PRInfo{State: "merged", MergedAt: &now}
	assert.True(t, merged.IsMerged(), "expected IsMerged true")
	assert.False(t, merged.IsClosed(), "expected IsClosed false for merged PR")

	closed := &github.PRInfo{State: "closed"}
	assert.False(t, closed.IsMerged(), "expected IsMerged false for closed PR")
	assert.True(t, closed.IsClosed(), "expected IsClosed true")
}

func TestFilterStackParentsBlocksParentsWithChildren(t *testing.T) {
	state := store.NewState()
	state.AddAnnotation("/repo", "parent", "")
	state.AddAnnotation("/repo", "child", "")
	state.SetParents("/repo", "child", []string{store.AnnotationKey("/repo", "parent")})

	candidates := []Candidate{
		{Kind: KindLocalBranch, RepoPath: "/repo", Branch: "parent"},
		{Kind: KindLocalBranch, RepoPath: "/repo", Branch: "leaf"},
	}

	safe, blocked := FilterStackParents(candidates, state)
	require.Len(t, safe, 1)
	assert.Equal(t, "leaf", safe[0].Branch, "leaf has no children, must remain a candidate")
	require.Len(t, blocked, 1)
	assert.Equal(t, "parent", blocked[0].Candidate.Branch)
	assert.Equal(t, []string{"/repo:child"}, blocked[0].Children)
}

func TestFilterStackParentsNilStateIsPassthrough(t *testing.T) {
	candidates := []Candidate{{Kind: KindLocalBranch, RepoPath: "/r", Branch: "b"}}
	safe, blocked := FilterStackParents(candidates, nil)
	assert.Equal(t, candidates, safe)
	assert.Empty(t, blocked)
}

func TestFilterStackParentsIgnoresCandidatesWithoutBranch(t *testing.T) {
	state := store.NewState()
	state.AddAnnotation("/repo", "x", "")
	state.SetParents("/repo", "x", []string{store.AnnotationKey("/repo", "missing-branch")})

	// A candidate with no Branch (e.g. KindRepo) should never be filtered.
	candidates := []Candidate{{Kind: KindRepo, RepoPath: "/repo"}}
	safe, blocked := FilterStackParents(candidates, state)
	assert.Len(t, safe, 1)
	assert.Empty(t, blocked)
}
