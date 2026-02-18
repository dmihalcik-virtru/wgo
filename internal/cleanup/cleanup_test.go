package cleanup

import (
	"testing"
	"time"

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
		if got := tt.kind.String(); got != tt.want {
			t.Errorf("CandidateKind(%d).String() = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

func TestDisplayPath(t *testing.T) {
	c := Candidate{Kind: KindLocalBranch, RepoPath: "/repos/foo", Branch: "feat"}
	if got := c.DisplayPath(); got != "/repos/foo [feat]" {
		t.Errorf("DisplayPath() = %q", got)
	}
	c2 := Candidate{Kind: KindWorktree, Path: "/worktrees/foo", RepoPath: "/repos/foo"}
	if got := c2.DisplayPath(); got != "/worktrees/foo" {
		t.Errorf("DisplayPath() = %q", got)
	}
}

func TestFilterKind(t *testing.T) {
	cs := []Candidate{
		{Kind: KindWorktree},
		{Kind: KindLocalBranch},
		{Kind: KindRemoteBranch},
		{Kind: KindWorktree},
	}
	got := FilterKind(cs, KindWorktree)
	if len(got) != 2 {
		t.Errorf("expected 2 worktree candidates, got %d", len(got))
	}
}

func TestFilterKinds(t *testing.T) {
	cs := []Candidate{
		{Kind: KindWorktree},
		{Kind: KindLocalBranch},
		{Kind: KindRemoteBranch},
	}
	got := FilterKinds(cs, KindWorktree, KindLocalBranch)
	if len(got) != 2 {
		t.Errorf("expected 2, got %d", len(got))
	}
}

func TestSenescenceReason(t *testing.T) {
	merged := time.Now().Add(-48 * time.Hour)
	pr := &github.PRInfo{
		Number:   42,
		State:    "merged",
		MergedAt: &merged,
	}
	reason := SenescenceReason(pr)
	if reason == "" {
		t.Errorf("expected non-empty reason for merged PR")
	}

	pr2 := (*github.PRInfo)(nil)
	if SenescenceReason(pr2) != "" {
		t.Errorf("expected empty reason for nil PR")
	}
}

func TestGroupByRepo(t *testing.T) {
	cs := []Candidate{
		{Kind: KindLocalBranch, RepoPath: "/repos/a", Branch: "b1"},
		{Kind: KindLocalBranch, RepoPath: "/repos/a", Branch: "b2"},
		{Kind: KindLocalBranch, RepoPath: "/repos/b", Branch: "b1"},
	}
	groups := GroupByRepo(cs)
	if len(groups["/repos/a"]) != 2 {
		t.Errorf("expected 2 for /repos/a, got %d", len(groups["/repos/a"]))
	}
	if len(groups["/repos/b"]) != 1 {
		t.Errorf("expected 1 for /repos/b, got %d", len(groups["/repos/b"]))
	}
}

func TestPRInfoMethods(t *testing.T) {
	now := time.Now()
	merged := &github.PRInfo{State: "merged", MergedAt: &now}
	if !merged.IsMerged() {
		t.Error("expected IsMerged true")
	}
	if merged.IsClosed() {
		t.Error("expected IsClosed false for merged PR")
	}

	closed := &github.PRInfo{State: "closed"}
	if closed.IsMerged() {
		t.Error("expected IsMerged false for closed PR")
	}
	if !closed.IsClosed() {
		t.Error("expected IsClosed true")
	}
}
