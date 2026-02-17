package status

import (
	"context"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/pkg/models"
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
}

func (m *mockGitClient) IsRepo(string) (bool, error)                             { return m.isRepo, nil }
func (m *mockGitClient) CurrentBranch(string) (string, error)                    { return m.branch, nil }
func (m *mockGitClient) Status(string) (models.GitStatus, error)                 { return m.status, nil }
func (m *mockGitClient) AheadBehind(string, string) (int, int, error)            { return 0, 0, nil }
func (m *mockGitClient) LastCommit(string) (models.CommitInfo, error)            { return m.commit, nil }
func (m *mockGitClient) RepoName(string) (string, error)                         { return m.repoName, nil }
func (m *mockGitClient) RemoteURL(string) (string, error)                        { return m.remoteURL, nil }
func (m *mockGitClient) RecentCommitCount(string, time.Time) (int, error)        { return m.commitCount, nil }
func (m *mockGitClient) DiffStat(string) (models.DiffStat, error)               { return m.diffStat, nil }

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
			if got != tt.want {
				t.Errorf("determineState() = %q, want %q", got, tt.want)
			}
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

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	for _, r := range results {
		if r.Branch != "main" {
			t.Errorf("expected branch 'main', got %q", r.Branch)
		}
		if r.State != models.StateModified {
			t.Errorf("expected state 'modified', got %q", r.State)
		}
	}
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
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if results[0].RecentCommits != 5 {
		t.Errorf("expected 5 recent commits, got %d", results[0].RecentCommits)
	}
}
