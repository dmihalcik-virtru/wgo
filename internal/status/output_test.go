package status

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/models"
)

func sampleActivities() []models.RepoActivity {
	now := time.Now()
	return []models.RepoActivity{
		{
			Name:          "wgo",
			Branch:        "feat/status",
			State:         models.StateModified,
			Status:        models.GitStatus{Modified: 3, Untracked: 1},
			RecentCommits: 12,
			DiffStat:      models.DiffStat{FilesChanged: 3, Insertions: 50, Deletions: 10},
			LastActivity:  now.Add(-5 * time.Minute),
			Annotation:    "status dashboard",
			Path:          "/home/user/repos/wgo",
		},
		{
			Name:          "other",
			Branch:        "main",
			State:         models.StateClean,
			RecentCommits: 0,
			LastActivity:  now.Add(-48 * time.Hour),
			Path:          "/home/user/repos/other",
		},
	}
}

func TestRenderTable(t *testing.T) {
	var buf bytes.Buffer
	activities := sampleActivities()

	RenderTable(&buf, activities, false)
	output := buf.String()

	assert.Contains(t, output, "REPO")
	assert.Contains(t, output, "wgo")
	assert.Contains(t, output, "feat/status")
	assert.Contains(t, output, "modified")
}

func TestRenderTable_Verbose(t *testing.T) {
	var buf bytes.Buffer
	activities := sampleActivities()

	RenderTable(&buf, activities, true)
	output := buf.String()

	assert.Contains(t, output, "WHY", "verbose table should contain WHY column")
	assert.Contains(t, output, "PATH", "verbose table should contain PATH column")
	assert.Contains(t, output, "status dashboard", "verbose table should show annotation")
}

func TestRenderJSON(t *testing.T) {
	var buf bytes.Buffer
	activities := sampleActivities()

	err := RenderJSON(&buf, activities)
	require.NoError(t, err, "RenderJSON failed")

	// Verify valid JSON
	var parsed []models.RepoActivity
	err = json.Unmarshal(buf.Bytes(), &parsed)
	require.NoError(t, err, "output is not valid JSON")

	assert.Len(t, parsed, 2)
}

func TestRenderCSV(t *testing.T) {
	var buf bytes.Buffer
	activities := sampleActivities()

	err := RenderCSV(&buf, activities, false)
	require.NoError(t, err, "RenderCSV failed")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	assert.Len(t, lines, 3, "expected 3 CSV lines (header + 2 rows)")
	assert.Contains(t, lines[0], "repo", "CSV header should contain 'repo'")
}

func TestRenderTable_Worktrees(t *testing.T) {
	now := time.Now()
	activities := []models.RepoActivity{
		{
			Name:          "wgo",
			Branch:        "main",
			State:         models.StateModified,
			Status:        models.GitStatus{Modified: 3},
			RecentCommits: 12,
			LastActivity:  now.Add(-5 * time.Minute),
			Path:          "/home/user/repos/wgo",
		},
		{
			Name:          "feat-status",
			Branch:        "feat/status",
			State:         models.StateModified,
			Status:        models.GitStatus{Modified: 2},
			RecentCommits: 3,
			LastActivity:  now.Add(-10 * time.Minute),
			Path:          "/home/user/repos/wgo-feat",
			IsWorktree:    true,
			MainRepoName:  "wgo",
			MainRepoPath:  "/home/user/repos/wgo",
		},
	}

	var buf bytes.Buffer
	RenderTable(&buf, activities, false)
	output := buf.String()

	assert.Contains(t, output, " +- ", "expected worktree prefix ' +- ' in output")
	assert.Contains(t, output, "feat-status", "expected worktree name in output")
}

func TestRenderJSON_Worktrees(t *testing.T) {
	activities := []models.RepoActivity{
		{Name: "main-repo", Path: "/tmp/main"},
		{Name: "wt", Path: "/tmp/wt", IsWorktree: true, MainRepoName: "main-repo", MainRepoPath: "/tmp/main"},
	}

	var buf bytes.Buffer
	err := RenderJSON(&buf, activities)
	require.NoError(t, err, "RenderJSON failed")

	output := buf.String()
	assert.Contains(t, output, `"is_worktree": true`)
	assert.Contains(t, output, `"main_repo_name": "main-repo"`)
}

func TestFormatChanges(t *testing.T) {
	tests := []struct {
		status models.GitStatus
		want   string
	}{
		{models.GitStatus{}, "-"},
		{models.GitStatus{Modified: 3}, "3M"},
		{models.GitStatus{Modified: 2, Untracked: 1}, "2M 1U"},
		{models.GitStatus{Added: 1, Deleted: 2}, "1A 2D"},
	}

	for _, tt := range tests {
		got := formatChanges(tt.status)
		assert.Equal(t, tt.want, got, "formatChanges(%+v)", tt.status)
	}
}

func TestFormatTimeSince(t *testing.T) {
	now := time.Now()

	tests := []struct {
		input time.Time
		want  string
	}{
		{time.Time{}, "unknown"},
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5 mins ago"},
		{now.Add(-1 * time.Minute), "1 min ago"},
		{now.Add(-3 * time.Hour), "3 hours ago"},
		{now.Add(-1 * time.Hour), "1 hour ago"},
		{now.Add(-48 * time.Hour), "2 days ago"},
		{now.Add(-24 * time.Hour), "1 day ago"},
	}

	for _, tt := range tests {
		got := formatTimeSince(tt.input)
		assert.Equal(t, tt.want, got, "formatTimeSince(%v)", tt.input)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is way too long", 10, "this is w…"},
	}

	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		assert.Equal(t, tt.want, got, "truncate(%q, %d)", tt.input, tt.maxLen)
	}
}
