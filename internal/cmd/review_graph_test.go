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

	var buf bytes.Buffer
	require.NoError(t, runReviewGraph(&buf, reviewGraphOpts{}))
	var g struct {
		Label string           `json:"label"`
		Nodes []map[string]any `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &g))
	assert.Equal(t, "2026-test", g.Label)
	assert.Len(t, g.Nodes, 3)

	out := filepath.Join(t.TempDir(), "g.json")
	buf.Reset()
	require.NoError(t, runReviewGraph(&buf, reviewGraphOpts{Arg: "2026-test", Out: out}))
	assert.Contains(t, buf.String(), "wrote 3 nodes")
	_, err := os.Stat(out)
	require.NoError(t, err)
}

func TestRunReviewGraphNoRuns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := runReviewGraph(&bytes.Buffer{}, reviewGraphOpts{})
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
			require.NoError(t, runReviewGraph(&bytes.Buffer{}, reviewGraphOpts{Out: out, Format: tt.format}))
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
	err := runReviewGraph(&bytes.Buffer{}, reviewGraphOpts{Format: "pdf"})
	require.ErrorContains(t, err, "--format json or --format html")
}

func TestRunReviewGraphHTMLDefaultPath(t *testing.T) {
	home := seedReviewRun(t)
	var buf bytes.Buffer
	require.NoError(t, runReviewGraph(&buf, reviewGraphOpts{Format: "html"}))
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
	require.NoError(t, runReviewGraph(&bytes.Buffer{}, reviewGraphOpts{Out: out, Open: true}))
	assert.Equal(t, []string{out}, *opened)
}

func TestRunReviewGraphOpenRequiresHTML(t *testing.T) {
	seedReviewRun(t)
	opened := stubOpener(t)
	err := runReviewGraph(&bytes.Buffer{}, reviewGraphOpts{Format: "json", Open: true})
	require.ErrorContains(t, err, "--open only works with the html format")

	// Bare --open implies html and writes the default explorer path.
	require.NoError(t, runReviewGraph(&bytes.Buffer{}, reviewGraphOpts{Open: true}))
	require.Len(t, *opened, 1)
	assert.True(t, strings.HasSuffix((*opened)[0], ".graph.html"))
	*opened = nil
	err = runReviewGraph(&bytes.Buffer{}, reviewGraphOpts{Out: "-", Format: "html", Open: true})
	require.ErrorContains(t, err, "--open needs a file")
	assert.Empty(t, *opened)
}

func TestRunReviewGraphHTMLToWriter(t *testing.T) {
	seedReviewRun(t)
	var buf bytes.Buffer
	require.NoError(t, runReviewGraph(&buf, reviewGraphOpts{Out: "-", Format: "html"}))
	assert.True(t, strings.HasPrefix(buf.String(), "<!doctype html>"))
}
