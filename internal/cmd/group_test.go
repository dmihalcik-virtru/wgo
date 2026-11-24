package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/jjtest"
	"github.com/virtru/wgo/internal/store"
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
	t.Setenv("ANTIGRAVITY_AGENT", "")
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

// TestLookupWorkspaceGroupFolder resolves a folder of workspaces to itself,
// with no repository or bookmark, and any other non-workspace to an error.
func TestLookupWorkspaceGroupFolder(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "api/.jj", "web/.jj", "docs")

	ws, err := lookupWorkspace(root)
	require.NoError(t, err)
	assert.Equal(t, workspaceInfo{Root: root}, ws)

	_, err = lookupWorkspace(filepath.Join(root, "docs"))
	assert.ErrorIs(t, err, errNotJJRepo)
}

// TestAgentHookInGroupFolder records a session started in a folder of
// workspaces on that folder, where it used to be skipped.
func TestAgentHookInGroupFolder(t *testing.T) {
	realLookup := lookupWorkspace
	newAgentEnv(t)
	lookupWorkspace = realLookup
	root := t.TempDir()
	mkdirs(t, root, "api/.jj", "web/.jj")

	payload := fmt.Sprintf(`{"session_id":"grp-1","hook_event_name":"SessionStart","cwd":%q}`, root)
	require.NoError(t, runAgentHook(strings.NewReader(payload), "", "claude"))
	sess := loadState(t).GetAgentSession("grp-1")
	require.NotNil(t, sess)
	assert.Equal(t, root, sess.WorktreePath)
	assert.Empty(t, sess.Branch)
	assert.Equal(t, store.AgentWaiting, sess.Status)
}

// TestEditedWorkspace narrows a path under a folder of workspaces to the
// workspace it lies in, and finds nothing outside the folder or when the
// folder is a workspace itself.
func TestEditedWorkspace(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "api/.jj", "api/pkg/deep", "web/.jj", "docs")
	api := filepath.Join(root, "api")

	assert.Equal(t, api, editedWorkspace(root, filepath.Join(api, "main.go")))
	assert.Equal(t, api, editedWorkspace(root, filepath.Join(api, "pkg", "deep", "x.go")))
	assert.Empty(t, editedWorkspace(root, filepath.Join(root, "docs", "notes.md")), "not in a workspace")
	assert.Empty(t, editedWorkspace(root, filepath.Join(root, "CLAUDE.md")))
	assert.Empty(t, editedWorkspace(root, root+"-other/api/main.go"), "a sibling sharing the prefix is outside")
	assert.Empty(t, editedWorkspace(api, filepath.Join(api, "main.go")), "the root is the workspace")
	assert.Empty(t, editedWorkspace("", filepath.Join(api, "main.go")))
}

// TestHookPayloadEditedPath reads the path only from file edits, and never
// fails on a tool_input it does not expect.
func TestHookPayloadEditedPath(t *testing.T) {
	for _, tc := range []struct {
		name, tool, input, want string
	}{
		{"edit", "Edit", `{"file_path":"/w/api/x.go","old_string":"a"}`, "/w/api/x.go"},
		{"write", "Write", `{"file_path":"/w/api/../web/y.go"}`, "/w/web/y.go"},
		{"notebook", "NotebookEdit", `{"notebook_path":"/w/api/n.ipynb"}`, "/w/api/n.ipynb"},
		{"read is not an edit", "Read", `{"file_path":"/w/api/x.go"}`, ""},
		{"bash", "Bash", `{"command":"vi /w/api/x.go"}`, ""},
		{"relative path", "Edit", `{"file_path":"api/x.go"}`, ""},
		{"unexpected shape", "Edit", `"x.go"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p hookPayload
			raw := fmt.Sprintf(`{"session_id":"s","tool_name":%q,"tool_input":%s}`, tc.tool, tc.input)
			require.NoError(t, json.Unmarshal([]byte(raw), &p))
			assert.Equal(t, tc.want, p.editedPath())
		})
	}

	for _, tc := range []struct {
		name, call, want string
	}{
		{"antigravity edit_file", `{"name":"edit_file","args":{"Path":"/w/api/x.go"}}`, "/w/api/x.go"},
		{"antigravity write_file", `{"name":"write_file","args":{"Path":"/w/api/../web/y.go"}}`, "/w/web/y.go"},
		{"antigravity read_file ignored", `{"name":"read_file","args":{"Path":"/w/api/x.go"}}`, ""},
		{"antigravity relative path", `{"name":"edit_file","args":{"Path":"api/x.go"}}`, ""},
		{"antigravity unexpected shape", `"x.go"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p hookPayload
			raw := fmt.Sprintf(`{"conversationId":"s","toolCall":%s}`, tc.call)
			require.NoError(t, json.Unmarshal([]byte(raw), &p))
			assert.Equal(t, tc.want, p.editedPath())
		})
	}
}

// TestAgentHookFocusInGroupFolder: a session started in a folder of
// workspaces takes the repository and bookmark of the workspace it edits, so
// it conflicts with another session on that bookmark; it keeps them across
// events from the folder, and drops the focus once it moves into a workspace.
func TestAgentHookFocusInGroupFolder(t *testing.T) {
	e := newAgentEnv(t)
	root := t.TempDir()
	mkdirs(t, root, "api/.jj", "web/.jj")
	api, web := filepath.Join(root, "api"), filepath.Join(root, "web")
	e.workspaces[root] = workspaceInfo{Root: root}
	e.workspaces[api] = workspaceInfo{Root: api, Repo: api, Branch: "feat", BranchKnown: true}
	e.workspaces[web] = workspaceInfo{Root: web, Repo: web, Branch: "other", BranchKnown: true}
	send := func(id, event, cwd, tool, input string) {
		t.Helper()
		e.advance(time.Second)
		if input == "" {
			input = "{}"
		}
		raw := fmt.Sprintf(`{"session_id":%q,"hook_event_name":%q,"cwd":%q,"tool_name":%q,"tool_input":%s}`, id, event, cwd, tool, input)
		require.NoError(t, runAgentHook(strings.NewReader(raw), "", "claude"))
	}
	editIn := func(path string) string { return fmt.Sprintf(`{"file_path":%q}`, path) }

	send("grp", "SessionStart", root, "", "")
	send("grp", "PostToolUse", root, "Read", editIn(filepath.Join(web, "a.go")))
	assert.Empty(t, loadState(t).GetAgentSession("grp").Focus, "reading is not editing")

	send("grp", "PostToolUse", root, "Edit", editIn(filepath.Join(api, "main.go")))
	sess := loadState(t).GetAgentSession("grp")
	assert.Equal(t, root, sess.WorktreePath)
	assert.Equal(t, api, sess.Focus)
	assert.Equal(t, api, sess.RepoPath)
	assert.Equal(t, "feat", sess.Branch)

	send("solo", "SessionStart", api, "", "")
	observed := loadState(t).ObserveAgentSessions(e.now, agentPolicy())
	assert.Equal(t, []string{"solo"}, store.AgentConflicts(observed)["grp"])
	assert.Contains(t, agentStatusOut(t), "editing in api")

	send("grp", "Stop", root, "", "")
	sess = loadState(t).GetAgentSession("grp")
	assert.Equal(t, "feat", sess.Branch, "an event from the folder keeps the edited workspace's bookmark")
	assert.Equal(t, api, sess.Focus)

	e.workspaces[api] = workspaceInfo{Root: api, Repo: api, Branch: "feat-2", BranchKnown: true}
	send("grp", "UserPromptSubmit", root, "", "")
	assert.Equal(t, "feat-2", loadState(t).GetAgentSession("grp").Branch, "a refresh re-reads the edited workspace's bookmark")
	e.workspaces[api] = workspaceInfo{Root: api, Repo: api, Branch: "feat", BranchKnown: true}

	send("grp", "PostToolUse", root, "Write", editIn(filepath.Join(web, "new.go")))
	sess = loadState(t).GetAgentSession("grp")
	assert.Equal(t, web, sess.Focus)
	assert.Equal(t, "other", sess.Branch)

	send("grp", "UserPromptSubmit", api, "", "")
	sess = loadState(t).GetAgentSession("grp")
	assert.Equal(t, api, sess.WorktreePath)
	assert.Empty(t, sess.Focus, "a session inside a workspace records it directly")
	assert.Equal(t, "feat", sess.Branch)
}
