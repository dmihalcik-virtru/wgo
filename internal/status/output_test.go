package status

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/virtru/wgo/pkg/models"
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

	if !strings.Contains(output, "REPO") {
		t.Error("expected header to contain REPO")
	}
	if !strings.Contains(output, "wgo") {
		t.Error("expected output to contain 'wgo'")
	}
	if !strings.Contains(output, "feat/status") {
		t.Error("expected output to contain 'feat/status'")
	}
	if !strings.Contains(output, "modified") {
		t.Error("expected output to contain 'modified'")
	}
}

func TestRenderTable_Verbose(t *testing.T) {
	var buf bytes.Buffer
	activities := sampleActivities()

	RenderTable(&buf, activities, true)
	output := buf.String()

	if !strings.Contains(output, "WHY") {
		t.Error("verbose table should contain WHY column")
	}
	if !strings.Contains(output, "PATH") {
		t.Error("verbose table should contain PATH column")
	}
	if !strings.Contains(output, "status dashboard") {
		t.Error("verbose table should show annotation")
	}
}

func TestRenderJSON(t *testing.T) {
	var buf bytes.Buffer
	activities := sampleActivities()

	if err := RenderJSON(&buf, activities); err != nil {
		t.Fatalf("RenderJSON failed: %v", err)
	}

	// Verify valid JSON
	var parsed []models.RepoActivity
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	if len(parsed) != 2 {
		t.Errorf("expected 2 items in JSON, got %d", len(parsed))
	}
}

func TestRenderCSV(t *testing.T) {
	var buf bytes.Buffer
	activities := sampleActivities()

	if err := RenderCSV(&buf, activities, false); err != nil {
		t.Fatalf("RenderCSV failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 { // header + 2 rows
		t.Errorf("expected 3 CSV lines (header + 2 rows), got %d", len(lines))
	}

	if !strings.Contains(lines[0], "repo") {
		t.Error("CSV header should contain 'repo'")
	}
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

	if !strings.Contains(output, " +- ") {
		t.Error("expected worktree prefix ' +- ' in output")
	}
	if !strings.Contains(output, "feat-status") {
		t.Error("expected worktree name in output")
	}
}

func TestRenderJSON_Worktrees(t *testing.T) {
	activities := []models.RepoActivity{
		{Name: "main-repo", Path: "/tmp/main"},
		{Name: "wt", Path: "/tmp/wt", IsWorktree: true, MainRepoName: "main-repo", MainRepoPath: "/tmp/main"},
	}

	var buf bytes.Buffer
	if err := RenderJSON(&buf, activities); err != nil {
		t.Fatalf("RenderJSON failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, `"is_worktree": true`) {
		t.Error("expected is_worktree in JSON output")
	}
	if !strings.Contains(output, `"main_repo_name": "main-repo"`) {
		t.Error("expected main_repo_name in JSON output")
	}
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
		if got != tt.want {
			t.Errorf("formatChanges(%+v) = %q, want %q", tt.status, got, tt.want)
		}
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
		if got != tt.want {
			t.Errorf("formatTimeSince(%v) = %q, want %q", tt.input, got, tt.want)
		}
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
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}
