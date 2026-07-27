package pilot

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/jjtest"
)

// initPilotTestRepo creates a jj repo with spec commits and [no-spec]
// commits spanning the given date range for metric collection tests.
//
// Each commit is created with JJ_TIMESTAMP set to a specific moment in the
// window so author_date revsets resolve deterministically.
func initPilotTestRepo(t *testing.T) (string, time.Time, time.Time) {
	t.Helper()
	jjtest.RequireJJ(t)

	dir := t.TempDir()

	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 5, 31, 23, 59, 59, 0, time.UTC)

	t.Setenv("JJ_USER", "Dave")
	t.Setenv("JJ_EMAIL", "dave@example.com")

	mustRunAt(t, dir, "2026-05-01T09:00:00Z", "jj", "git", "init", "--colocate")
	mustRunAt(t, dir, "2026-05-01T09:00:00Z", "jj", "config", "set", "--repo", "user.name", "Dave")
	mustRunAt(t, dir, "2026-05-01T09:00:00Z", "jj", "config", "set", "--repo", "user.email", "dave@example.com")

	// initial commit on the auto-created @
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0o644))
	mustRunAt(t, dir, "2026-05-01T09:00:00Z", "jj", "describe", "-m", "init")

	specDir := filepath.Join(dir, "spec")
	require.NoError(t, os.MkdirAll(specDir, 0o755))

	// Spec 1 created — first do `jj new` to give the new change a clean
	// author identity from the env vars (the initial change still has the
	// git config identity used by `jj git init`).
	mustRunAt(t, dir, "2026-05-02T10:00:00Z", "jj", "new")
	require.NoError(t, os.WriteFile(filepath.Join(specDir, "WGO-1.md"), []byte("# WGO-1\n\nspec content"), 0o644))
	mustRunAt(t, dir, "2026-05-02T10:00:00Z", "jj", "describe", "-m", "spec: add WGO-1")

	// Spec 2 created
	mustRunAt(t, dir, "2026-05-03T11:00:00Z", "jj", "new")
	require.NoError(t, os.WriteFile(filepath.Join(specDir, "WGO-2.md"), []byte("# WGO-2\n\nspec content"), 0o644))
	mustRunAt(t, dir, "2026-05-03T11:00:00Z", "jj", "describe", "-m", "spec: add WGO-2")

	// Update spec 1 (not a creation)
	mustRunAt(t, dir, "2026-05-04T12:00:00Z", "jj", "new")
	require.NoError(t, os.WriteFile(filepath.Join(specDir, "WGO-1.md"), []byte("# WGO-1\n\nupdated content"), 0o644))
	mustRunAt(t, dir, "2026-05-04T12:00:00Z", "jj", "describe", "-m", "spec: update WGO-1 scope")

	// Two [no-spec] commits that touch non-spec files.
	mustRunAt(t, dir, "2026-05-05T14:00:00Z", "jj", "new")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	mustRunAt(t, dir, "2026-05-05T14:00:00Z", "jj", "describe", "-m", "fix typo [no-spec]")

	mustRunAt(t, dir, "2026-05-06T15:00:00Z", "jj", "new")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "util.go"), []byte("package main\n"), 0o644))
	mustRunAt(t, dir, "2026-05-06T15:00:00Z", "jj", "describe", "-m", "docs: update readme [no-spec]")

	// Leave the workspace on a fresh empty @ so no later mutation drifts
	// the previous commit's timestamps.
	mustRunAt(t, dir, "2026-05-06T15:00:01Z", "jj", "new")

	return dir, since, until
}

// mustRunAt runs jj at the given JJ_TIMESTAMP. Used to pin commit author
// timestamps to specific moments in the test window.
func mustRunAt(t *testing.T, dir, timestamp string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "JJ_TIMESTAMP="+timestamp)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "command %v failed: %s", args, out)
}

func TestCollect_SpecsCreated(t *testing.T) {
	repoPath, since, until := initPilotTestRepo(t)

	opts := Options{Since: since, Until: until}
	m, err := Collect([]string{repoPath}, "", opts)
	require.NoError(t, err)
	assert.Equal(t, 2, m.SpecsCreated)
}

func TestCollect_SpecsUpdated(t *testing.T) {
	repoPath, since, until := initPilotTestRepo(t)

	opts := Options{Since: since, Until: until}
	m, err := Collect([]string{repoPath}, "", opts)
	require.NoError(t, err)

	// 2 creates + 1 update = 3 total spec commits
	assert.Equal(t, 3, m.SpecsUpdated)
}

func TestCollect_NoSpecOverrides(t *testing.T) {
	repoPath, since, until := initPilotTestRepo(t)

	opts := Options{Since: since, Until: until}
	m, err := Collect([]string{repoPath}, "", opts)
	require.NoError(t, err)
	assert.Equal(t, 2, m.NoSpecOverrides)
}

func TestCollect_SpecEditsByAuthor(t *testing.T) {
	repoPath, since, until := initPilotTestRepo(t)

	opts := Options{Since: since, Until: until}
	m, err := Collect([]string{repoPath}, "", opts)
	require.NoError(t, err)

	// All spec creates are by dave@example.com
	count, ok := m.SpecEditsByAuthor["dave@example.com"]
	require.True(t, ok, "expected dave@example.com in SpecEditsByAuthor, got: %v", m.SpecEditsByAuthor)
	assert.Equal(t, 2, count)
}

func TestCollect_OutOfRange_NotCounted(t *testing.T) {
	repoPath, _, _ := initPilotTestRepo(t)

	// Use a range that excludes all commits
	since := time.Date(2025, 1, 1, 0, 0, 0, 0, time.Local)
	until := time.Date(2025, 1, 31, 0, 0, 0, 0, time.Local)
	opts := Options{Since: since, Until: until}

	m, err := Collect([]string{repoPath}, "", opts)
	require.NoError(t, err)
	assert.Equal(t, 0, m.SpecsCreated, "expected 0 specs for out-of-range window")
	assert.Equal(t, 0, m.NoSpecOverrides, "expected 0 overrides for out-of-range window")
}

func TestCollect_NoSpecDir_GracefulZero(t *testing.T) {
	jjtest.RequireJJ(t)
	dir := t.TempDir()

	mustRunAt(t, dir, "2026-05-01T09:00:00Z", "jj", "git", "init", "--colocate")
	mustRunAt(t, dir, "2026-05-01T09:00:00Z", "jj", "config", "set", "--repo", "user.name", "Test")
	mustRunAt(t, dir, "2026-05-01T09:00:00Z", "jj", "config", "set", "--repo", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0o644))
	mustRunAt(t, dir, "2026-05-01T09:00:00Z", "jj", "describe", "-m", "init")

	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)
	opts := Options{Since: since, Until: until}

	m, err := Collect([]string{dir}, "", opts)
	require.NoError(t, err)
	// Should get zero, no error
	assert.Equal(t, 0, m.SpecsCreated, "expected 0 specs in repo without spec/ dir")
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
		assert.True(t, strings.Contains(out, section), "markdown missing required section/metric: %q", section)
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
	require.NoError(t, err)
	assert.Contains(t, string(data), `"specs_created"`)
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
	require.NoError(t, os.WriteFile(logPath, []byte(content), 0o644))

	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local)
	until := time.Date(2026, 5, 31, 0, 0, 0, 0, time.Local)

	drift, blocks, err := walkLogs(logsDir, since, until)
	require.NoError(t, err)
	assert.Equal(t, 2, drift)
	assert.Equal(t, 1, blocks)
}
