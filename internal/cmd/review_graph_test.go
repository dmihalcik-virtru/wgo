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

	var buf bytes.Buffer
	require.NoError(t, runReviewGraph(&buf, "", ""))
	var g struct {
		Label string           `json:"label"`
		Nodes []map[string]any `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &g))
	assert.Equal(t, "2026-test", g.Label)
	assert.Len(t, g.Nodes, 3)

	out := filepath.Join(t.TempDir(), "g.json")
	buf.Reset()
	require.NoError(t, runReviewGraph(&buf, "2026-test", out))
	assert.Contains(t, buf.String(), "wrote 3 nodes")
	_, err := os.Stat(out)
	require.NoError(t, err)
}

func TestRunReviewGraphNoRuns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := runReviewGraph(&bytes.Buffer{}, "", "")
	require.ErrorContains(t, err, "/year-in-review")
}
