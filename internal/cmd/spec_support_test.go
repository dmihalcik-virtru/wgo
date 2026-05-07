package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindBranchSpec(t *testing.T) {
	repoRoot := t.TempDir()
	specDir := filepath.Join(repoRoot, "spec")
	require.NoError(t, os.MkdirAll(specDir, 0o755))

	content := `---
ticket: WGO-101
title: Spec scaffold and plan integration
status: draft
authors: [dmihalcik]
branches: [virtru/wgo:WGO-101-spec-scaffold]
prs: []
created: 2026-05-06
updated: 2026-05-06
---
`
	require.NoError(t, os.WriteFile(filepath.Join(specDir, "WGO-101.md"), []byte(content), 0o644))

	info, err := findBranchSpec(repoRoot, "WGO-101-spec-scaffold")
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, "spec/WGO-101.md", info.RelPath)
	assert.Equal(t, "draft", info.Status)
	assert.Equal(t, "2026-05-06", info.Updated.Format(time.DateOnly))
}

func TestFormatSpecInfo(t *testing.T) {
	missing := formatSpecInfo(&branchSpecInfo{
		Ticket:  "WGO-101",
		Missing: true,
	})
	assert.Equal(t, "⚠ no spec (run: wgo spec new WGO-101)", missing)

	present := formatSpecInfo(&branchSpecInfo{
		RelPath: "spec/WGO-101.md",
		Status:  "draft",
		Updated: time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC),
	})
	assert.Equal(t, "📄 spec/WGO-101.md (draft, updated 2026-05-06)", present)
}
