package pilot

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// initPilotTestRepo creates a git repo with spec commits and [no-spec] commits
// spanning the given date range for metric collection tests.
func initPilotTestRepo(t *testing.T) (string, time.Time, time.Time) {
	t.Helper()
	dir := t.TempDir()

	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local)
	until := time.Date(2026, 5, 31, 23, 59, 59, 0, time.Local)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
	}

	run("git", "init")
	run("git", "config", "user.email", "dave@example.com")
	run("git", "config", "user.name", "Dave")
	run("git", "config", "commit.gpgsign", "false")

	// Initial commit
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "README.md")
	run("git", "commit", "--date=2026-05-01T09:00:00", "-m", "init")

	// Create spec directory and add spec files
	specDir := filepath.Join(dir, "spec")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Spec 1 created
	if err := os.WriteFile(filepath.Join(specDir, "WGO-1.md"), []byte("# WGO-1\n\nspec content"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "spec/WGO-1.md")
	run("git", "commit", "--date=2026-05-02T10:00:00", "-m", "spec: add WGO-1")

	// Spec 2 created by same author
	if err := os.WriteFile(filepath.Join(specDir, "WGO-2.md"), []byte("# WGO-2\n\nspec content"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "spec/WGO-2.md")
	run("git", "commit", "--date=2026-05-03T11:00:00", "-m", "spec: add WGO-2")

	// Update spec 1 (not a creation)
	if err := os.WriteFile(filepath.Join(specDir, "WGO-1.md"), []byte("# WGO-1\n\nupdated content"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "spec/WGO-1.md")
	run("git", "commit", "--date=2026-05-04T12:00:00", "-m", "spec: update WGO-1 scope")

	// Regular commit with [no-spec]
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "main.go")
	run("git", "commit", "--date=2026-05-05T14:00:00", "-m", "fix typo [no-spec]")

	// Another no-spec override
	if err := os.WriteFile(filepath.Join(dir, "util.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "util.go")
	run("git", "commit", "--date=2026-05-06T15:00:00", "-m", "docs: update readme [no-spec]")

	return dir, since, until
}

func TestCollect_SpecsCreated(t *testing.T) {
	repoPath, since, until := initPilotTestRepo(t)

	opts := Options{
		Since: since,
		Until: until,
	}
	m, err := Collect([]string{repoPath}, "", opts)
	if err != nil {
		t.Fatal(err)
	}

	if m.SpecsCreated != 2 {
		t.Errorf("SpecsCreated = %d, want 2", m.SpecsCreated)
	}
}

func TestCollect_SpecsUpdated(t *testing.T) {
	repoPath, since, until := initPilotTestRepo(t)

	opts := Options{Since: since, Until: until}
	m, err := Collect([]string{repoPath}, "", opts)
	if err != nil {
		t.Fatal(err)
	}

	// 2 creates + 1 update = 3 total spec commits
	if m.SpecsUpdated != 3 {
		t.Errorf("SpecsUpdated = %d, want 3", m.SpecsUpdated)
	}
}

func TestCollect_NoSpecOverrides(t *testing.T) {
	repoPath, since, until := initPilotTestRepo(t)

	opts := Options{Since: since, Until: until}
	m, err := Collect([]string{repoPath}, "", opts)
	if err != nil {
		t.Fatal(err)
	}

	if m.NoSpecOverrides != 2 {
		t.Errorf("NoSpecOverrides = %d, want 2", m.NoSpecOverrides)
	}
}

func TestCollect_SpecEditsByAuthor(t *testing.T) {
	repoPath, since, until := initPilotTestRepo(t)

	opts := Options{Since: since, Until: until}
	m, err := Collect([]string{repoPath}, "", opts)
	if err != nil {
		t.Fatal(err)
	}

	// All spec creates are by dave@example.com
	count, ok := m.SpecEditsByAuthor["dave@example.com"]
	if !ok {
		t.Fatalf("expected dave@example.com in SpecEditsByAuthor, got: %v", m.SpecEditsByAuthor)
	}
	if count != 2 {
		t.Errorf("SpecEditsByAuthor[dave@example.com] = %d, want 2", count)
	}
}

func TestCollect_OutOfRange_NotCounted(t *testing.T) {
	repoPath, _, _ := initPilotTestRepo(t)

	// Use a range that excludes all commits
	since := time.Date(2025, 1, 1, 0, 0, 0, 0, time.Local)
	until := time.Date(2025, 1, 31, 0, 0, 0, 0, time.Local)
	opts := Options{Since: since, Until: until}

	m, err := Collect([]string{repoPath}, "", opts)
	if err != nil {
		t.Fatal(err)
	}

	if m.SpecsCreated != 0 {
		t.Errorf("SpecsCreated = %d, want 0 for out-of-range window", m.SpecsCreated)
	}
	if m.NoSpecOverrides != 0 {
		t.Errorf("NoSpecOverrides = %d, want 0 for out-of-range window", m.NoSpecOverrides)
	}
}

func TestCollect_NoSpecDir_GracefulZero(t *testing.T) {
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
	}
	run("git", "init")
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "Test")
	run("git", "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "README.md")
	run("git", "commit", "--date=2026-05-01T09:00:00", "-m", "init")

	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local)
	until := time.Date(2026, 5, 31, 0, 0, 0, 0, time.Local)
	opts := Options{Since: since, Until: until}

	m, err := Collect([]string{dir}, "", opts)
	if err != nil {
		t.Fatal(err)
	}
	// Should get zero, no error
	if m.SpecsCreated != 0 {
		t.Errorf("expected 0 specs in repo without spec/ dir, got %d", m.SpecsCreated)
	}
}

func TestRenderMarkdown_ContainsRequiredSections(t *testing.T) {
	m := &Metrics{
		Since:             time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local),
		Until:             time.Date(2026, 5, 31, 0, 0, 0, 0, time.Local),
		Team:              []string{"dave", "sujan"},
		Period:            "2026-05-01 to 2026-05-31",
		SpecsCreated:      5,
		SpecsUpdated:      8,
		PRsMerged:         4,
		DriftEventsCaught: 2,
		NoSpecOverrides:   1,
		SpecEditsByAuthor: map[string]int{"dave": 3, "sujan": 2},
	}
	opts := Options{
		Since: m.Since,
		Until: m.Until,
		Team:  m.Team,
	}

	out := RenderMarkdown(m, opts)

	required := []string{
		"## How we structured pairing",
		"## Spec → implementation handoff",
		"## What worked",
		"## What we'd do differently",
		"## Metrics",
		"## Pilot checklist",
		"Specs created",
		"PRs merged",
		"Drift events caught",
	}
	for _, section := range required {
		if !strings.Contains(out, section) {
			t.Errorf("markdown missing required section/metric: %q", section)
		}
	}
}

func TestRenderJSON_ValidJSON(t *testing.T) {
	m := &Metrics{
		Period:            "2026-05-01 to 2026-05-31",
		Team:              []string{"dave"},
		SpecsCreated:      3,
		SpecEditsByAuthor: map[string]int{"dave": 3},
	}

	data, err := RenderJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"specs_created"`) {
		t.Errorf("JSON missing specs_created field: %s", data)
	}
}

func TestWalkLogs_CountsMarkers(t *testing.T) {
	logsDir := t.TempDir()

	// Write a log file with drift and block markers
	content := `# 2026-05-05
## Events
- drift detected: spec/WGO-1.md out of sync
- pre-commit blocked: WGO-2-feature has no spec
- drift detected: another one
`
	logPath := filepath.Join(logsDir, "2026-05-05.md")
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local)
	until := time.Date(2026, 5, 31, 0, 0, 0, 0, time.Local)

	drift, blocks, err := walkLogs(logsDir, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if drift != 2 {
		t.Errorf("drift = %d, want 2", drift)
	}
	if blocks != 1 {
		t.Errorf("blocks = %d, want 1", blocks)
	}
}
