package spec

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderTemplateAndParse(t *testing.T) {
	now := time.Date(2026, 5, 6, 9, 30, 0, 0, time.UTC)

	data, err := RenderTemplate(TemplateData{
		Ticket:      "WGO-101",
		Title:       "Spec scaffold and plan integration",
		Description: "Add spec scaffolding to wgo add.",
		Authors:     []string{"dmihalcik", "sujan"},
		Branches:    []string{"virtru/wgo:WGO-101-spec-scaffold"},
		Now:         now,
	})
	require.NoError(t, err)

	specFile, err := ParseBytes(data)
	require.NoError(t, err)

	assert.Equal(t, "WGO-101", specFile.Frontmatter.Ticket)
	assert.Equal(t, "Spec scaffold and plan integration", specFile.Frontmatter.Title)
	assert.Equal(t, StatusDraft, specFile.Frontmatter.Status)
	assert.Equal(t, "2026-05-06", specFile.Frontmatter.Created.Format(time.DateOnly))
	assert.Equal(t, "Add spec scaffolding to wgo add.", specFile.Sections["Summary"])
}

func TestFindByTicketAndBranch(t *testing.T) {
	repoRoot := t.TempDir()
	specDir := filepath.Join(repoRoot, "spec")
	require.NoError(t, os.MkdirAll(specDir, 0o755))

	specPath := filepath.Join(specDir, "WGO-101.md")
	require.NoError(t, os.WriteFile(specPath, []byte("---\nticket: WGO-101\nstatus: draft\nauthors: []\nbranches: []\nprs: []\ncreated: 2026-05-06\nupdated: 2026-05-06\n---\n"), 0o644))

	foundByTicket, err := FindByTicket(repoRoot, "WGO-101")
	require.NoError(t, err)
	assert.Equal(t, specPath, foundByTicket)

	foundByBranch, err := FindByBranch(repoRoot, "WGO-101-spec-scaffold")
	require.NoError(t, err)
	assert.Equal(t, specPath, foundByBranch)
	assert.Equal(t, "WGO-101", ParseTicketFromBranch("WGO-101-spec-scaffold"))
	assert.Empty(t, ParseTicketFromBranch("feature/spec-scaffold"))
}

func TestUpdateFrontmatterPreservesBody(t *testing.T) {
	repoRoot := t.TempDir()
	path := filepath.Join(repoRoot, "spec.md")

	original := []byte(`---
ticket: WGO-101
title: Old title
status: draft
authors: [dmihalcik]
branches: [virtru/wgo:WGO-101-spec-scaffold]
prs: []
created: 2026-05-06
updated: 2026-05-06
estimate: 1d
---

# Heading

## Summary
This body must stay byte-for-byte identical.
`)
	require.NoError(t, os.WriteFile(path, original, 0o644))

	originalFrontmatter, originalBody, err := splitFrontmatter(original)
	require.NoError(t, err)
	assert.NotEmpty(t, originalFrontmatter)

	err = UpdateFrontmatter(path, func(fm *Frontmatter) error {
		fm.Status = StatusInProgress
		fm.Title = "New title"
		fm.Updated = time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
		return nil
	})
	require.NoError(t, err)

	updated, err := os.ReadFile(path)
	require.NoError(t, err)

	_, updatedBody, err := splitFrontmatter(updated)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(originalBody, updatedBody))
	assert.True(t, bytes.Contains(updated, []byte("estimate: 1d")))

	parsed, err := Parse(path)
	require.NoError(t, err)
	assert.Equal(t, StatusInProgress, parsed.Frontmatter.Status)
	assert.Equal(t, "New title", parsed.Frontmatter.Title)
}
