package spec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testFrontmatter = `---
ticket: WGO-201
title: Test Spec
status: draft
authors: [alice]
branches: []
prs: []
created: 2026-05-01T00:00:00Z
updated: 2026-05-01T00:00:00Z
---
# Body
`

func writeSpec(t *testing.T, dir, ticket, content string) string {
	t.Helper()
	specDir := filepath.Join(dir, "spec")
	require.NoError(t, os.MkdirAll(specDir, 0o755), "mkdir spec")
	p := filepath.Join(specDir, ticket+".md")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644), "write spec")
	return p
}

func TestDetectForBranch_Untracked(t *testing.T) {
	dir := t.TempDir()
	// No spec file; branch has a ticket prefix.
	reports, err := DetectForBranch(dir, "WGO-201-some-feature")
	require.NoError(t, err)
	require.Len(t, reports, 1)
	assert.Equal(t, DriftUntracked, reports[0].Kind)
	assert.Equal(t, "WGO-201-some-feature", reports[0].Branch)
}

func TestDetectForBranch_NoTicket(t *testing.T) {
	dir := t.TempDir()
	reports, err := DetectForBranch(dir, "main")
	require.NoError(t, err)
	assert.Empty(t, reports, "expected 0 reports for non-ticket branch")
}

func TestDetectForBranch_ValidSpec(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "WGO-201", testFrontmatter)
	// Not a real git repo, so commit counting will fail gracefully — no stale report.
	reports, err := DetectForBranch(dir, "WGO-201-some-feature")
	require.NoError(t, err)
	// No stale report (git command fails gracefully in tmpdir) and not untracked.
	for _, r := range reports {
		assert.NotEqual(t, DriftUntracked, r.Kind, "should not be untracked when spec exists")
	}
}

func TestDetectOrphaned(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "WGO-201", testFrontmatter)

	// No live branches that reference WGO-201.
	reports, err := detectOrphaned(dir, []string{"main", "feature-branch"})
	require.NoError(t, err)
	require.Len(t, reports, 1)
	assert.Equal(t, DriftOrphaned, reports[0].Kind)
}

func TestDetectOrphaned_LiveBranch(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "WGO-201", testFrontmatter)

	// WGO-201-feature starts with the ticket prefix — should not be orphaned.
	reports, err := detectOrphaned(dir, []string{"main", "WGO-201-feature"})
	require.NoError(t, err)
	assert.Empty(t, reports, "expected 0 orphaned reports when live branch exists")
}

func TestDetectOrphaned_Terminal(t *testing.T) {
	dir := t.TempDir()
	shipped := `---
ticket: WGO-202
title: Shipped
status: shipped
authors: [alice]
branches: []
prs: []
created: 2026-05-01T00:00:00Z
updated: 2026-05-01T00:00:00Z
---
`
	writeSpec(t, dir, "WGO-202", shipped)
	// Shipped spec should not appear as orphaned.
	reports, err := detectOrphaned(dir, []string{"main"})
	require.NoError(t, err)
	assert.Empty(t, reports, "terminal spec should not be orphaned")
}
