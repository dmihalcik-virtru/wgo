package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunReviewGraphWritesJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	run := filepath.Join(home, ".wgo", "cache", "review", "runs", "2026-test")
	require.NoError(t, os.MkdirAll(run, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(run, "ledger.jsonl"),
		[]byte(`{"kind":"pr_authored","url":"https://github.com/o/r/pull/1","repo":"o/r","number":1,"title":"t"}`+"\n"), 0o644))

	var buf, errBuf bytes.Buffer
	require.NoError(t, runReviewGraph(&buf, &errBuf, "", ""))
	var g struct {
		Version int              `json:"version"`
		Label   string           `json:"label"`
		Nodes   []map[string]any `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &g))
	assert.Equal(t, 1, g.Version)
	assert.Equal(t, "2026-test", g.Label)
	assert.Len(t, g.Nodes, 3)
	assert.Contains(t, errBuf.String(), "using run 2026-test", "an implicit run choice is named")
	assert.Contains(t, errBuf.String(), "has no coverage.json, people.json, cards/")

	outDir := t.TempDir()
	out := filepath.Join(outDir, "g.json")
	require.NoError(t, os.WriteFile(out, []byte("stale"), 0o644))
	buf.Reset()
	errBuf.Reset()
	require.NoError(t, runReviewGraph(&buf, &errBuf, "2026-test", out))
	assert.Contains(t, buf.String(), "wrote 3 nodes")
	assert.NotContains(t, errBuf.String(), "using run", "an explicit run needs no note")
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.True(t, json.Valid(b), "the stale file is replaced")
	entries, err := os.ReadDir(outDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp file is left behind")
}

func TestRunReviewGraphOutWriteError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	run := filepath.Join(home, ".wgo", "cache", "review", "runs", "2026-test")
	require.NoError(t, os.MkdirAll(run, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(run, "ledger.jsonl"), []byte("\n"), 0o644))

	err := runReviewGraph(&bytes.Buffer{}, &bytes.Buffer{}, "", filepath.Join(home, "missing-dir", "g.json"))
	require.ErrorContains(t, err, "write ")
}

func TestRunReviewGraphOutRenameFailureLeavesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	run := filepath.Join(home, ".wgo", "cache", "review", "runs", "2026-test")
	require.NoError(t, os.MkdirAll(run, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(run, "ledger.jsonl"), []byte("\n"), 0o644))

	// CreateTemp succeeds but renaming a file over a directory fails.
	outDir := t.TempDir()
	out := filepath.Join(outDir, "g.json")
	require.NoError(t, os.Mkdir(out, 0o755))
	err := runReviewGraph(&bytes.Buffer{}, &bytes.Buffer{}, "", out)
	require.ErrorContains(t, err, "write ")
	entries, err := os.ReadDir(outDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the temp file is cleaned up")
}

func TestRunReviewGraphReportsSkippedRows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	run := filepath.Join(home, ".wgo", "cache", "review", "runs", "2026-test")
	require.NoError(t, os.MkdirAll(run, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(run, "ledger.jsonl"),
		[]byte(`{"kind":"pr_commented"}`+"\n"+`{"kind":"issue_filed"}`+"\n"+`{"kind":"pr_commented"}`+"\n"), 0o644))
	var errBuf bytes.Buffer
	require.NoError(t, runReviewGraph(&bytes.Buffer{}, &errBuf, "", ""))
	assert.Contains(t, errBuf.String(), "issue_filed=1, pr_commented=2")
}

func TestRunReviewGraphNoRuns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := runReviewGraph(&bytes.Buffer{}, &bytes.Buffer{}, "", "")
	require.ErrorContains(t, err, "/year-in-review")
}
