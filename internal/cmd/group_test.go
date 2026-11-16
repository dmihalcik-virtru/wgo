package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/jjtest"
	"github.com/virtru/wgo/models"
)

// mkdirs creates each path (and its parents) under root.
func mkdirs(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		require.NoError(t, os.MkdirAll(filepath.Join(root, p), 0o755))
	}
}

// TestGroupWorkspaces finds the jj workspaces directly inside a folder, and
// under src/ in a rig root, skipping hidden folders and plain ones.
func TestGroupWorkspaces(t *testing.T) {
	t.Run("shared root", func(t *testing.T) {
		root := t.TempDir()
		mkdirs(t, root, "api/.jj", "web/.jj", "docs", ".cache/.jj", "deep/nested/.jj")
		require.NoError(t, os.WriteFile(filepath.Join(root, "CLAUDE.md"), nil, 0o644))
		assert.Equal(t, []string{filepath.Join(root, "api"), filepath.Join(root, "web")}, groupWorkspaces(root))
	})
	t.Run("rig root", func(t *testing.T) {
		root := t.TempDir()
		mkdirs(t, root, "src/platform-v1/.jj", "src/otdfctl-v2/.jj")
		require.NoError(t, os.WriteFile(filepath.Join(root, "rig.toml"), nil, 0o644))
		assert.Equal(t, []string{filepath.Join(root, "src", "otdfctl-v2"), filepath.Join(root, "src", "platform-v1")}, groupWorkspaces(root))
	})
	t.Run("no workspaces", func(t *testing.T) {
		root := t.TempDir()
		mkdirs(t, root, "docs")
		assert.Empty(t, groupWorkspaces(root))
		assert.Empty(t, groupWorkspaces(filepath.Join(root, "missing")))
	})
}

func fixtureGroupContext() *models.Context {
	return &models.Context{
		SchemaVersion: models.ContextSchemaVersion,
		Ticket:        "WGO-140",
		JiraStatus:    "In Progress",
		Group: &models.GroupRef{
			Name: "WGO-140-multi-repo",
			Path: "/w/WGO-140-multi-repo",
			Workspaces: []models.GroupWorkspace{
				{Name: "api", RepoURL: "https://github.com/acme/api", Branch: "WGO-140-api",
					PRs: []models.PRRef{{Number: 7, State: "open", Checks: models.CIStatus{State: "success"}}}},
				{Name: "web"},
			},
			Overflow: 2,
		},
		Agent: &models.AgentRef{Name: "claude"},
	}
}

// TestStatuslineGroupGolden pins the plain group line: name and ticket, each
// workspace (one without a bookmark shows its name alone), overflow, agent.
func TestStatuslineGroupGolden(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderStatuslineLine(&buf, fixtureGroupContext(), false))
	assert.Equal(t, "⧉ WGO-140-multi-repo [WGO-140 In Progress] · api WGO-140-api #7 open ● · web · +2 more 🤖 claude\n", buf.String())
}

// TestStatuslineGroupRig leads a rig root's line with the rig, not the folder.
func TestStatuslineGroupRig(t *testing.T) {
	ctx := fixtureGroupContext()
	ctx.Ticket = ""
	ctx.Rig = &models.RigRef{Name: "dsp-2.7.1"}
	var buf bytes.Buffer
	require.NoError(t, renderStatuslineLine(&buf, ctx, false))
	assert.Equal(t, "⚓ dsp-2.7.1 · api WGO-140-api #7 open ● · web · +2 more 🤖 claude\n", buf.String())
}

// TestStatuslineInGroupFolder renders the summary from a real folder holding
// two workspaces, where the statusline used to fail with "not a jj
// repository".
func TestStatuslineInGroupFolder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDECODE", "")
	origRepo, origNoColor := repoFlag, statuslineNoColor
	t.Cleanup(func() { repoFlag, statuslineNoColor = origRepo, origNoColor })
	statuslineNoColor = true

	repo, _ := jjtest.NewRepo(t)
	jjtest.Bookmark(t, repo, "feat-x", "@")
	ws := jjtest.NewWorkspace(t, repo, "second")
	repoFlag = filepath.Dir(repo)

	var buf bytes.Buffer
	require.NoError(t, runStatusline(&buf))
	assert.Equal(t, "⧉ "+filepath.Base(repoFlag)+" · "+filepath.Base(repo)+" feat-x · "+filepath.Base(ws)+"\n",
		buf.String())
}
