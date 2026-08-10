package spec

import (
	"io/fs"
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

// Tickets arrive uppercased from ParseTicketFromBranch, but specs for GitHub
// issues live on disk lowercase (spec/gh-9.md). The lookup must find them, and
// must hand back the on-disk name — callers render the result as a link, and
// spec/GH-9.md 404s on github.com and on case-sensitive filesystems even though
// the lookup itself would succeed on macOS.
func TestFindByTicketUsesOnDiskCase(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(tmpDir, "spec"), 0o755), "mkdir")
	onDisk := filepath.Join(tmpDir, "spec", "gh-9.md")
	require.NoError(t, os.WriteFile(onDisk, []byte("test"), 0o644), "write")

	got, err := FindByTicket(tmpDir, "GH-9")
	require.NoError(t, err, "FindByTicket")
	assert.Equal(t, onDisk, got)
}

// writeSharedClaudeMD distinguishes "no spec here, keep looking" from "this
// repo is broken, warn": a missing spec/ must be fs.ErrNotExist, an unreadable
// one must not be.
func TestFindByTicketErrorShape(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := FindByTicket(tmpDir, "WGO-1")
	assert.ErrorIs(t, err, fs.ErrNotExist, "missing spec/ dir")

	require.NoError(t, os.Mkdir(filepath.Join(tmpDir, "spec"), 0o755), "mkdir")
	_, err = FindByTicket(tmpDir, "WGO-1")
	assert.ErrorIs(t, err, fs.ErrNotExist, "empty spec/ dir")

	_, err = FindByTicket(tmpDir, "")
	assert.ErrorIs(t, err, fs.ErrNotExist, "empty ticket")

	// spec/ is a file, not a directory: a real problem, not a missing spec.
	fileRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fileRoot, "spec"), nil, 0o644), "write")
	_, err = FindByTicket(fileRoot, "WGO-1")
	require.Error(t, err)
	assert.NotErrorIs(t, err, fs.ErrNotExist, "unreadable spec/ must be distinguishable")
}
