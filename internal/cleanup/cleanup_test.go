package cleanup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/github"
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
