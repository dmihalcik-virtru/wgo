package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	require.NoError(t, runReviewGraph(&buf, &errBuf, reviewGraphOpts{}))
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
	require.NoError(t, runReviewGraph(&buf, &errBuf, reviewGraphOpts{Arg: "2026-test", Out: out}))
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

	err := runReviewGraph(&bytes.Buffer{}, &bytes.Buffer{}, reviewGraphOpts{Out: filepath.Join(home, "missing-dir", "g.json")})
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
	err := runReviewGraph(&bytes.Buffer{}, &bytes.Buffer{}, reviewGraphOpts{Out: out})
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
	require.NoError(t, runReviewGraph(&bytes.Buffer{}, &errBuf, reviewGraphOpts{Format: "json"}))
	assert.Contains(t, errBuf.String(), "issue_filed=1, pr_commented=2")
}

func TestRunReviewGraphNoRuns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := runReviewGraph(&bytes.Buffer{}, &bytes.Buffer{}, reviewGraphOpts{})
	require.ErrorContains(t, err, "/year-in-review")
}

// seedReviewRun creates a one-PR run under a fresh HOME and returns HOME.
func seedReviewRun(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	run := filepath.Join(home, ".wgo", "cache", "review", "runs", "2026-test")
	require.NoError(t, os.MkdirAll(run, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(run, "ledger.jsonl"),
		[]byte(`{"kind":"pr_authored","url":"https://github.com/o/r/pull/1","repo":"o/r","number":1,"title":"t"}`+"\n"), 0o644))
	return home
}

// stubOpener replaces the browser opener and returns the paths it was given.
func stubOpener(t *testing.T) *[]string {
	t.Helper()
	var opened []string
	orig := openInBrowser
	openInBrowser = func(p string) error { opened = append(opened, p); return nil }
	t.Cleanup(func() { openInBrowser = orig })
	return &opened
}

func TestRunReviewGraphFormatSelection(t *testing.T) {
	tests := []struct {
		name   string
		format string
		out    string
		want   string
	}{
		{"default json", "", "g.out", "json"},
		{"html extension", "", "g.html", "html"},
		{"html extension upper", "", "G.HTML", "html"},
		{"flag html", "html", "g.out", "html"},
		{"flag json beats extension", "json", "g.html", "json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seedReviewRun(t)
			out := filepath.Join(t.TempDir(), tt.out)
			require.NoError(t, runReviewGraph(&bytes.Buffer{}, &bytes.Buffer{}, reviewGraphOpts{Out: out, Format: tt.format}))
			b, err := os.ReadFile(out)
			require.NoError(t, err)
			if tt.want == "html" {
				assert.True(t, strings.HasPrefix(string(b), "<!doctype html>"))
			} else {
				assert.True(t, json.Valid(b))
			}
		})
	}
}

func TestRunReviewGraphUnknownFormat(t *testing.T) {
	seedReviewRun(t)
	err := runReviewGraph(&bytes.Buffer{}, &bytes.Buffer{}, reviewGraphOpts{Format: "pdf"})
	require.ErrorContains(t, err, "--format json or --format html")
}

func TestRunReviewGraphHTMLDefaultPath(t *testing.T) {
	home := seedReviewRun(t)
	var buf bytes.Buffer
	require.NoError(t, runReviewGraph(&buf, &bytes.Buffer{}, reviewGraphOpts{Format: "html"}))
	want := filepath.Join(home, ".wgo", "reviews", "2026-test.graph.html")
	assert.Contains(t, buf.String(), want)
	assert.NotContains(t, buf.String(), "<html")
	b, err := os.ReadFile(want)
	require.NoError(t, err)
	assert.Contains(t, string(b), "<title>wgo review — 2026-test</title>")
}

func TestRunReviewGraphOpen(t *testing.T) {
	seedReviewRun(t)
	opened := stubOpener(t)
	out := filepath.Join(t.TempDir(), "e.html")
	require.NoError(t, runReviewGraph(&bytes.Buffer{}, &bytes.Buffer{}, reviewGraphOpts{Out: out, Open: true}))
	assert.Equal(t, []string{out}, *opened)
}

func TestRunReviewGraphOpenRequiresHTML(t *testing.T) {
	seedReviewRun(t)
	opened := stubOpener(t)
	err := runReviewGraph(&bytes.Buffer{}, &bytes.Buffer{}, reviewGraphOpts{Format: "json", Open: true})
	require.ErrorContains(t, err, "--open only works with the html format")

	// Bare --open implies html and writes the default explorer path.
	require.NoError(t, runReviewGraph(&bytes.Buffer{}, &bytes.Buffer{}, reviewGraphOpts{Open: true}))
	require.Len(t, *opened, 1)
	assert.True(t, strings.HasSuffix((*opened)[0], ".graph.html"))
	*opened = nil
	err = runReviewGraph(&bytes.Buffer{}, &bytes.Buffer{}, reviewGraphOpts{Out: "-", Format: "html", Open: true})
	require.ErrorContains(t, err, "--open needs a file")
	assert.Empty(t, *opened)
}

func TestRunReviewGraphHTMLToWriter(t *testing.T) {
	seedReviewRun(t)
	var buf bytes.Buffer
	require.NoError(t, runReviewGraph(&buf, &bytes.Buffer{}, reviewGraphOpts{Out: "-", Format: "html"}))
	assert.True(t, strings.HasPrefix(buf.String(), "<!doctype html>"))
}
