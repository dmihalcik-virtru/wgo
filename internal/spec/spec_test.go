package spec

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseBytes(t *testing.T) {
	tests := []struct {
		name string
		data string
		want struct {
			ticket string
			status Status
		}
	}{
		{
			name: "valid frontmatter",
			data: `---
ticket: WGO-101
title: Test Spec
status: draft
authors: [alice]
branches: []
prs: []
created: 2026-05-06
updated: 2026-05-06
---
# Body`,
			want: struct {
				ticket string
				status Status
			}{
				ticket: "WGO-101",
				status: StatusDraft,
			},
		},
		{
			name: "no frontmatter",
			data: `# Just body
Some content`,
			want: struct {
				ticket string
				status Status
			}{
				ticket: "",
				status: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sf, err := ParseBytes([]byte(tt.data))
			require.NoError(t, err, "ParseBytes failed")
			assert.Equal(t, tt.want.ticket, sf.Frontmatter.Ticket)
			assert.Equal(t, tt.want.status, sf.Frontmatter.Status)
		})
	}
}

func TestUpdateFrontmatter(t *testing.T) {
	tmpDir := t.TempDir()
	specPath := filepath.Join(tmpDir, "test.md")

	original := `---
ticket: WGO-101
status: draft
authors: [alice]
branches: []
prs: []
created: 2026-05-06
updated: 2026-05-06
---
# Original Body

This is the body.`

	require.NoError(t, os.WriteFile(specPath, []byte(original), 0o644), "write file")

	err := UpdateFrontmatter(specPath, func(fm *Frontmatter) error {
		fm.Status = StatusInProgress
		fm.Updated = time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
		return nil
	})
	require.NoError(t, err, "UpdateFrontmatter")

	updated, err := os.ReadFile(specPath)
	require.NoError(t, err, "read updated file")

	sf, err := ParseBytes(updated)
	require.NoError(t, err, "parse updated file")

	assert.Equal(t, StatusInProgress, sf.Frontmatter.Status)
	assert.Contains(t, sf.Body, "Original Body")
}

func TestParseTicketFromBranch(t *testing.T) {
	tests := []struct {
		branch string
		want   string
	}{
		{"WGO-101", "WGO-101"},
		{"WGO-101-foo", "WGO-101"},
		{"feature-WGO-101", ""},
		{"not-a-ticket", ""},
		{"WGO-101-long-branch-name", "WGO-101"},
	}

	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			got := ParseTicketFromBranch(tt.branch)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFindByTicket(t *testing.T) {
	tmpDir := t.TempDir()
	specDir := filepath.Join(tmpDir, "spec")
	require.NoError(t, os.Mkdir(specDir, 0o755), "mkdir")

	specFile := filepath.Join(specDir, "WGO-101.md")
	require.NoError(t, os.WriteFile(specFile, []byte("test"), 0o644), "write")

	got, err := FindByTicket(tmpDir, "WGO-101")
	require.NoError(t, err, "FindByTicket")
	assert.Equal(t, specFile, got)

	_, err = FindByTicket(tmpDir, "WGO-999")
	assert.Error(t, err, "FindByTicket should return error for non-existent ticket")
}
