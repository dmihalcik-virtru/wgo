package status

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/models"
)

// mockGitClient implements git.Client for testing.
type mockGitClient struct {
	isRepo      bool
	branch      string
	status      models.GitStatus
	commit      models.CommitInfo
	repoName    string
	remoteURL   string
	commitCount int
	diffStat    models.DiffStat
	worktrees   map[string][]git.WorktreeInfo // keyed by repo path
}

func (m *mockGitClient) IsRepo(string) (bool, error)                              { return m.isRepo, nil }
func (m *mockGitClient) CurrentBranch(string) (string, error)                     { return m.branch, nil }
func (m *mockGitClient) Status(string) (models.GitStatus, error)                  { return m.status, nil }
func (m *mockGitClient) AheadBehind(string, string) (int, int, error)             { return 0, 0, nil }
func (m *mockGitClient) LastCommit(string) (models.CommitInfo, error)             { return m.commit, nil }
func (m *mockGitClient) RepoName(string) (string, error)                          { return m.repoName, nil }
func (m *mockGitClient) RemoteURL(string) (string, error)                         { return m.remoteURL, nil }
func (m *mockGitClient) RecentCommitCount(string, time.Time) (int, error)         { return m.commitCount, nil }
func (m *mockGitClient) DiffStat(string) (models.DiffStat, error)                 { return m.diffStat, nil }
func (m *mockGitClient) Clone(string, string) error                               { return nil }
func (m *mockGitClient) WorktreeAdd(string, string, string, bool, string) error   { return nil }
func (m *mockGitClient) Fetch(string) error                                       { return nil }
func (m *mockGitClient) FetchPRRef(string, int, string) error                     { return nil }
func (m *mockGitClient) DefaultBranch(string) (string, error)                     { return "main", nil }
func (m *mockGitClient) BranchExists(string, string) (bool, error)                { return false, nil }
func (m *mockGitClient) RemoveWorktree(string, string, bool) error                { return nil }
func (m *mockGitClient) DeleteBranch(string, string, bool) error                  { return nil }
func (m *mockGitClient) HasLocalOnlyCommits(string, string, string) (bool, error) { return false, nil }
func (m *mockGitClient) IsAncestor(string, string, string) (bool, error)          { return false, nil }
func (m *mockGitClient) UpstreamRef(string, string) (string, error)               { return "", nil }
func (m *mockGitClient) PruneWorktrees(string) error                              { return nil }
func (m *mockGitClient) ListLocalBranches(string) ([]string, error)               { return nil, nil }
func (m *mockGitClient) IsBranchMerged(string, string, string) (bool, error)      { return false, nil }
func (m *mockGitClient) Push(string, string) error                                { return nil }
func (m *mockGitClient) AddAndCommit(string, string, string) error                { return nil }
func (m *mockGitClient) ListWorktrees(repoPath string) ([]git.WorktreeInfo, error) {
	if m.worktrees != nil {
		if wts, ok := m.worktrees[repoPath]; ok {
			return wts, nil
		}
	}
	return []git.WorktreeInfo{{Path: repoPath, IsMain: true}}, nil
}

func TestCollector_DetermineState(t *testing.T) {
	c := NewCollector(&mockGitClient{}, nil, nil)

	tests := []struct {
		name     string
		status   models.GitStatus
		activity time.Time
		want     models.RepoState
	}{
		{
			name:   "conflict takes priority",
			status: models.GitStatus{Conflicts: 1, Modified: 1, Staged: 1},
			want:   models.StateConflict,
		},
		{
			name:   "staged over modified",
			status: models.GitStatus{Staged: 2, Modified: 1},
			want:   models.StateStaged,
		},
		{
			name:   "modified files",
			status: models.GitStatus{Modified: 3},
			want:   models.StateModified,
		},
		{
			name:   "untracked counts as modified",
			status: models.GitStatus{Untracked: 1},
			want:   models.StateModified,
		},
		{
			name:     "stale repo",
			status:   models.GitStatus{},
			activity: time.Now().Add(-30 * 24 * time.Hour),
			want:     models.StateStale,
		},
		{
			name:     "clean recent repo",
			status:   models.GitStatus{},
			activity: time.Now().Add(-1 * time.Hour),
			want:     models.StateClean,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.determineState(tt.status, tt.activity)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCollector_CollectAll(t *testing.T) {
	mock := &mockGitClient{
		isRepo:   true,
		branch:   "main",
		status:   models.GitStatus{Modified: 2},
		commit:   models.CommitInfo{Hash: "abc123", Message: "test", Date: time.Now()},
		repoName: "test-repo",
		diffStat: models.DiffStat{FilesChanged: 2, Insertions: 10, Deletions: 3},
	}

	c := NewCollector(mock, nil, nil)

	repos := []discovery.DiscoveredRepo{
		{Path: "/tmp/repo1", Name: "repo1"},
		{Path: "/tmp/repo2", Name: "repo2"},
	}

	results := c.CollectAll(context.Background(), repos)

	require.Len(t, results, 2)

	for _, r := range results {
		assert.Equal(t, "main", r.Branch)
		assert.Equal(t, models.StateModified, r.State)
	}
}

func TestCollector_WorktreeExpansion(t *testing.T) {
	mock := &mockGitClient{
		isRepo:   true,
		branch:   "main",
		commit:   models.CommitInfo{Date: time.Now()},
		repoName: "myrepo",
		worktrees: map[string][]git.WorktreeInfo{
			"/tmp/myrepo": {
				{Path: "/tmp/myrepo", Branch: "main", IsMain: true},
				{Path: "/tmp/myrepo-feat", Branch: "feat/new", IsMain: false},
			},
		},
	}

	c := NewCollector(mock, nil, nil)

	repos := []discovery.DiscoveredRepo{
		{Path: "/tmp/myrepo", Name: "myrepo"},
	}

	results := c.CollectAll(context.Background(), repos)

	require.Len(t, results, 2, "expected 2 results (main + worktree)")

	// Find the worktree entry
	var foundWorktree bool
	for _, r := range results {
		if r.IsWorktree {
			foundWorktree = true
			assert.Equal(t, "myrepo", r.MainRepoName)
			assert.Equal(t, "/tmp/myrepo", r.MainRepoPath)
			assert.Equal(t, "myrepo-feat", r.Name)
		}
	}

	assert.True(t, foundWorktree, "expected to find a worktree entry in results")
}

func TestCollector_WorktreeDedup(t *testing.T) {
	mock := &mockGitClient{
		isRepo:   true,
		branch:   "main",
		commit:   models.CommitInfo{Date: time.Now()},
		repoName: "myrepo",
		worktrees: map[string][]git.WorktreeInfo{
			"/tmp/myrepo": {
				{Path: "/tmp/myrepo", Branch: "main", IsMain: true},
				{Path: "/tmp/myrepo-feat", Branch: "feat/new", IsMain: false},
			},
		},
	}

	c := NewCollector(mock, nil, nil)

	// Both main and worktree are already in the discovery list
	repos := []discovery.DiscoveredRepo{
		{Path: "/tmp/myrepo", Name: "myrepo"},
		{Path: "/tmp/myrepo-feat", Name: "myrepo-feat"},
	}

	results := c.CollectAll(context.Background(), repos)

	// Should still get exactly 2 results (no duplicate for the worktree)
	assert.Len(t, results, 2, "expected 2 results (deduped)")
}

func TestCollector_WithSince(t *testing.T) {
	since := time.Now().Add(-1 * time.Hour)
	mock := &mockGitClient{
		branch:      "main",
		commit:      models.CommitInfo{Date: time.Now()},
		commitCount: 5,
	}

	c := NewCollector(mock, nil, nil, WithSince(since))

	repos := []discovery.DiscoveredRepo{
		{Path: "/tmp/repo1", Name: "repo1"},
	}

	results := c.CollectAll(context.Background(), repos)
	require.Len(t, results, 1)
	assert.Equal(t, 5, results[0].RecentCommits)
}
