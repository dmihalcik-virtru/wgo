package status

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/models"
)

// mockJJClient implements jj.Client for collector tests. Each field
// supplies a deterministic answer for the corresponding method; methods
// not used by the collector return zero values.
type mockJJClient struct {
	currentChange jj.Change
	status        jj.Status
	logEntries    map[string][]jj.LogEntry            // keyed by revset
	countResults  map[string]int                      // keyed by revset
	diffAdded     int
	diffDeleted   int
	changedFiles  []string
	ahead         int
	behind        int
	remotes       map[string]string
	workspaces    map[string][]jj.Workspace           // keyed by repo path
}

func (m *mockJJClient) Root(string) (string, error)         { return "", nil }
func (m *mockJJClient) IsRepo(string) bool                  { return true }
func (m *mockJJClient) RemoteURLs(string) (map[string]string, error) {
	if m.remotes != nil {
		return m.remotes, nil
	}
	return map[string]string{}, nil
}
func (m *mockJJClient) ListWorkspaces(repo string) ([]jj.Workspace, error) {
	if m.workspaces != nil {
		if ws, ok := m.workspaces[repo]; ok {
			return ws, nil
		}
	}
	return []jj.Workspace{{Name: "default", Path: repo}}, nil
}
func (m *mockJJClient) WorkspaceAdd(string, string, string, string) error { return nil }
func (m *mockJJClient) WorkspaceForget(string, string) error              { return nil }
func (m *mockJJClient) WorkspaceRoot(p string) (string, error)            { return p, nil }
func (m *mockJJClient) MainWorkspaceRoot(p string) (string, error)        { return p, nil }
func (m *mockJJClient) UpdateStale(string) error                          { return nil }
func (m *mockJJClient) Log(_, revset string) ([]jj.LogEntry, error) {
	if m.logEntries != nil {
		if entries, ok := m.logEntries[revset]; ok {
			return entries, nil
		}
	}
	return nil, nil
}
func (m *mockJJClient) CurrentChange(string) (jj.Change, error) { return m.currentChange, nil }
func (m *mockJJClient) Resolve(string, string) (string, error)  { return "", nil }
func (m *mockJJClient) Status(string) (jj.Status, error)        { return m.status, nil }
func (m *mockJJClient) IsClean(string) (bool, []string, error)  { return m.status.Clean, nil, nil }
func (m *mockJJClient) AheadBehind(string, string) (int, int, error) {
	return m.ahead, m.behind, nil
}
func (m *mockJJClient) DiffStat(string, string) (int, int, error) {
	return m.diffAdded, m.diffDeleted, nil
}
func (m *mockJJClient) ChangedFiles(string, string) ([]string, error) { return m.changedFiles, nil }
func (m *mockJJClient) DiffSummary(string, string) ([]jj.FileChange, error) {
	return nil, nil
}
func (m *mockJJClient) CountRevset(_, revset string) (int, error) {
	if m.countResults != nil {
		if n, ok := m.countResults[revset]; ok {
			return n, nil
		}
	}
	return 0, nil
}
func (m *mockJJClient) BookmarkList(string, jj.BookmarkListOpts) ([]jj.Bookmark, error) {
	return nil, nil
}
func (m *mockJJClient) BookmarkSet(string, string, string, bool) error { return nil }
func (m *mockJJClient) BookmarkCreate(string, string, string) error    { return nil }
func (m *mockJJClient) BookmarkDelete(string, string) error            { return nil }
func (m *mockJJClient) New(string, string, string) error               { return nil }
func (m *mockJJClient) Describe(string, string) error                  { return nil }
func (m *mockJJClient) EditChange(string, string) error                { return nil }
func (m *mockJJClient) GitInit(string, jj.InitOpts) error              { return nil }
func (m *mockJJClient) GitClone(string, string) error                  { return nil }
func (m *mockJJClient) GitFetch(string, string, []string) error        { return nil }
func (m *mockJJClient) GitPush(string, jj.PushOpts) (jj.PushResult, error) {
	return jj.PushResult{}, nil
}
func (m *mockJJClient) GitRemoteAdd(string, string, string) error    { return nil }
func (m *mockJJClient) GitRemoteRemove(string, string) error         { return nil }

func TestCollector_DetermineState(t *testing.T) {
	c := NewCollector(&mockJJClient{}, nil, nil)

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
	now := time.Now()
	mock := &mockJJClient{
		currentChange: jj.Change{Bookmarks: []string{"main"}, CommitID: "abc123"},
		status:        jj.Status{Modified: []string{"a", "b"}}, // two modified
		logEntries: map[string][]jj.LogEntry{
			"@-": {{CommitID: "abc123", Description: "test", AuthorTimestamp: now}},
		},
		diffAdded:    10,
		diffDeleted:  3,
		changedFiles: []string{"a.txt", "b.txt"},
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
	now := time.Now()
	mock := &mockJJClient{
		currentChange: jj.Change{Bookmarks: []string{"main"}, CommitID: "abc"},
		logEntries: map[string][]jj.LogEntry{
			"@-": {{CommitID: "abc", AuthorTimestamp: now}},
		},
		workspaces: map[string][]jj.Workspace{
			"/tmp/myrepo": {
				{Name: "default", Path: "/tmp/myrepo"},
				{Name: "feat", Path: "/tmp/myrepo-feat"},
			},
		},
	}

	c := NewCollector(mock, nil, nil)

	repos := []discovery.DiscoveredRepo{
		{Path: "/tmp/myrepo", Name: "myrepo"},
	}

	results := c.CollectAll(context.Background(), repos)

	require.Len(t, results, 2, "expected 2 results (main + workspace)")

	var foundWorktree bool
	for _, r := range results {
		if r.IsWorktree {
			foundWorktree = true
			assert.Equal(t, "myrepo", r.MainRepoName)
			assert.Equal(t, "/tmp/myrepo", r.MainRepoPath)
			assert.Equal(t, "myrepo-feat", r.Name)
		}
	}

	assert.True(t, foundWorktree, "expected to find a workspace entry in results")
}

func TestCollector_WorktreeDedup(t *testing.T) {
	now := time.Now()
	mock := &mockJJClient{
		currentChange: jj.Change{Bookmarks: []string{"main"}, CommitID: "abc"},
		logEntries: map[string][]jj.LogEntry{
			"@-": {{CommitID: "abc", AuthorTimestamp: now}},
		},
		workspaces: map[string][]jj.Workspace{
			"/tmp/myrepo": {
				{Name: "default", Path: "/tmp/myrepo"},
				{Name: "feat", Path: "/tmp/myrepo-feat"},
			},
		},
	}

	c := NewCollector(mock, nil, nil)

	// Both main and workspace are already in the discovery list
	repos := []discovery.DiscoveredRepo{
		{Path: "/tmp/myrepo", Name: "myrepo"},
		{Path: "/tmp/myrepo-feat", Name: "myrepo-feat"},
	}

	results := c.CollectAll(context.Background(), repos)

	// Should still get exactly 2 results (no duplicate for the workspace)
	assert.Len(t, results, 2, "expected 2 results (deduped)")
}

func TestCollector_WithSince(t *testing.T) {
	since := time.Now().Add(-1 * time.Hour)
	mock := &mockJJClient{
		currentChange: jj.Change{Bookmarks: []string{"main"}, CommitID: "abc"},
		logEntries: map[string][]jj.LogEntry{
			"@-": {{CommitID: "abc", AuthorTimestamp: time.Now()}},
		},
		// The collector builds a revset of the form `(::main) &
		// author_date(after:"<ts>")`; we don't need the exact key match,
		// just any non-zero count, so set a catch-all entry.
	}

	// Build the exact revset the collector will use so the mock returns 5.
	mock.countResults = map[string]int{}

	c := NewCollector(mock, nil, nil, WithSince(since))

	// Pre-populate the count map after constructing the revset format.
	// The collector uses time.RFC3339 in UTC, so reconstruct it here.
	const tsLayout = time.RFC3339
	revset := "(::main) & author_date(after:" + quote(since.UTC().Format(tsLayout)) + ")"
	mock.countResults[revset] = 5

	repos := []discovery.DiscoveredRepo{
		{Path: "/tmp/repo1", Name: "repo1"},
	}

	results := c.CollectAll(context.Background(), repos)
	require.Len(t, results, 1)
	assert.Equal(t, 5, results[0].RecentCommits)
}

// quote wraps s in double quotes — matches the collector's fmt.Sprintf("%q", ...) call.
func quote(s string) string { return "\"" + s + "\"" }
