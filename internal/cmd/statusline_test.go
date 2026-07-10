package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStatuslineDefaultGolden pins the plain (rich=false) default line.
func TestStatuslineDefaultGolden(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderStatuslineLine(&buf, fixtureContext(), false))
	assert.Equal(t, "wgo WGO-130-statusline-context-api* [WGO-130 In Review] ↑1 #42 open ✓ ● 🤖 claude\n", buf.String())
}

// TestStatuslineNoJiraNoAgent drops the Jira status from the ticket segment and
// the agent segment entirely when absent (graceful degradation).
func TestStatuslineNoJiraNoAgent(t *testing.T) {
	ctx := fixtureContext()
	ctx.JiraStatus = ""
	ctx.Agent = nil

	var buf bytes.Buffer
	require.NoError(t, renderStatuslineLine(&buf, ctx, false))
	out := buf.String()
	assert.Contains(t, out, "[WGO-130]")
	assert.NotContains(t, out, "In Review")
	assert.NotContains(t, out, "🤖")
}

// TestStatuslineRichHasColorAndLinks asserts the rich line emits ANSI color and
// OSC8 hyperlink escapes.
func TestStatuslineRichHasColorAndLinks(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderStatuslineLine(&buf, fixtureContext(), true))
	out := buf.String()
	assert.Contains(t, out, "\033[", "expected ANSI color codes")
	assert.Contains(t, out, "\033]8;;", "expected OSC8 hyperlinks")
	assert.Contains(t, out, "https://github.com/virtru/wgo/pull/42", "PR should be linked")
}

// TestStatuslineGracefulDegradation drops the PR segment when there are no PRs
// (the cache-miss case).
func TestStatuslineGracefulDegradation(t *testing.T) {
	ctx := fixtureContext()
	ctx.PRs = nil

	var buf bytes.Buffer
	require.NoError(t, renderStatuslineLine(&buf, ctx, false))
	out := buf.String()
	assert.NotContains(t, out, "#")
	assert.Contains(t, out, "WGO-130-statusline-context-api*")
}

// TestStatuslineFormat renders a custom template.
func TestStatuslineFormat(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderStatuslineFormat(&buf, fixtureContext(), "{{.Branch}}{{if .Dirty}}*{{end}}", false))
	assert.Equal(t, "WGO-130-statusline-context-api*\n", buf.String())
}

// TestStatuslineFormatBadTemplate reports a clear error for an invalid template.
func TestStatuslineFormatBadTemplate(t *testing.T) {
	var buf bytes.Buffer
	err := renderStatuslineFormat(&buf, fixtureContext(), "{{.Nope", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --format template")
}

// TestStatuslineFormatColorFunc honors the rich flag in the color template func.
func TestStatuslineFormatColorFunc(t *testing.T) {
	var plain bytes.Buffer
	require.NoError(t, renderStatuslineFormat(&plain, fixtureContext(), `{{color "green" .Repo}}`, false))
	assert.Equal(t, "wgo\n", plain.String())

	var rich bytes.Buffer
	require.NoError(t, renderStatuslineFormat(&rich, fixtureContext(), `{{color "green" .Repo}}`, true))
	assert.Equal(t, "\033[32mwgo\033[0m\n", rich.String())
}

// TestColorize is a no-op unless rich and a code are given.
func TestColorize(t *testing.T) {
	assert.Equal(t, "x", colorize("x", colGreen, false))
	assert.Equal(t, "x", colorize("x", "", true))
	assert.Equal(t, "\033[32mx\033[0m", colorize("x", colGreen, true))
}

// TestTicketURL resolves GitHub-issue tickets against the remote; non-numeric
// GH tickets yield no link.
func TestTicketURL(t *testing.T) {
	assert.Equal(t, "https://github.com/acme/widgets/issues/9",
		ticketURL("GH-9", "https://github.com/acme/widgets.git"))
	assert.Equal(t, "", ticketURL("GH-x", "https://github.com/acme/widgets.git"))
}

// TestResolveCwd covers the -C/--repo flag resolution.
func TestResolveCwd(t *testing.T) {
	orig := repoFlag
	t.Cleanup(func() { repoFlag = orig })

	t.Run("unset falls back to getwd", func(t *testing.T) {
		repoFlag = ""
		got, err := resolveCwd()
		require.NoError(t, err)
		wd, _ := os.Getwd()
		assert.Equal(t, wd, got)
	})

	t.Run("existing dir", func(t *testing.T) {
		dir := t.TempDir()
		repoFlag = dir
		got, err := resolveCwd()
		require.NoError(t, err)
		assert.Equal(t, dir, got)
	})

	t.Run("a file is rejected", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "file.txt")
		require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))
		repoFlag = f
		_, err := resolveCwd()
		assert.Error(t, err)
	})

	t.Run("missing path is rejected", func(t *testing.T) {
		repoFlag = filepath.Join(t.TempDir(), "does-not-exist")
		_, err := resolveCwd()
		assert.Error(t, err)
	})
}
