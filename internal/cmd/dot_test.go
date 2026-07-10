package cmd

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/jjtest"
	"github.com/virtru/wgo/models"
)

// fixtureContext returns a fully-populated Context whose fields are all
// deterministic. The commit date is left zero so formatTime renders the stable
// "unknown" instead of a relative timestamp.
func fixtureContext() *models.Context {
	return &models.Context{
		SchemaVersion: models.ContextSchemaVersion,
		Repo:          "wgo",
		RepoURL:       "https://github.com/virtru/wgo",
		Branch:        "WGO-130-statusline-context-api",
		Status:        "modified",
		Changes:       models.GitStatus{Modified: 2, Added: 1},
		Dirty:         true,
		Ahead:         1,
		Behind:        0,
		Remote:        "https://github.com/virtru/wgo.git",
		BranchURL:     "https://github.com/virtru/wgo/tree/WGO-130-statusline-context-api",
		Commit: models.CommitInfo{
			Hash:    "abc1234",
			Message: "do a thing",
			Author:  "dev@example.com",
			Date:    time.Time{},
			URL:     "https://github.com/virtru/wgo/commit/abc1234",
		},
		Ticket:       "WGO-130",
		JiraStatus:   "In Review",
		JiraAssignee: "Alice Dev",
		Spec:         &models.SpecRef{Path: "spec/WGO-130.md", Status: "draft", Updated: "2026-07-08"},
		Tasks:        []models.TaskRef{{Bullet: "○", Text: "implement context"}},
		PRs: []models.PRRef{{
			Number:         42,
			Title:          "Add context",
			State:          "open",
			URL:            "https://github.com/virtru/wgo/pull/42",
			ReviewDecision: "APPROVED",
			Checks: models.CIStatus{
				State: "success", Passed: 3, Total: 3,
				URL: "https://github.com/virtru/wgo/pull/42/checks",
			},
		}},
		Siblings: []models.SiblingRef{{Name: "wgo-2", Branch: "main", Status: "clean"}},
		Agent:    &models.AgentRef{Name: "claude", Since: time.Time{}},
	}
}

// TestRenderTextGolden pins the human output byte-for-byte (tty=false, so no
// OSC8 escapes are emitted).
func TestRenderTextGolden(t *testing.T) {
	want := `repo:   wgo
branch: WGO-130-statusline-context-api
status: 2 modified, 1 added
remote: ↑1  (origin/wgo)
commit: abc1234 do a thing (unknown)
pr:     #42 Add context [OPEN ✓ CI:green]
task:   ○ implement context
spec:   📄 spec/WGO-130.md (draft, updated 2026-07-08) [In Review]
jira:   In Review · @Alice Dev
agent:  🤖 claude (since unknown)

Workspace siblings:
  wgo-2/       main   clean
`

	var buf bytes.Buffer
	renderText(&buf, fixtureContext(), false)
	assert.Equal(t, want, buf.String())
}

// TestJSONFieldParity guards against text/JSON drift: every field the text
// output shows must have a populated counterpart in the JSON output.
func TestJSONFieldParity(t *testing.T) {
	ctx := fixtureContext()

	var buf bytes.Buffer
	require.NoError(t, renderJSON(&buf, ctx))
	out := buf.String()

	// Schema version is emitted.
	assert.Contains(t, out, `"schema_version": 1`)

	// The four fields that the old --json map omitted are all present.
	for _, want := range []string{
		`"ticket": "WGO-130"`,
		`"spec"`, `"path": "spec/WGO-130.md"`, `"status": "draft"`, `"updated": "2026-07-08"`,
		`"tasks"`, `"bullet": "○"`, `"text": "implement context"`,
		`"prs"`, `"number": 42`,
		`"review_decision": "APPROVED"`, `"checks"`, `"state": "success"`,
		`"url": "https://github.com/virtru/wgo/pull/42/checks"`,
		`"siblings"`, `"name": "wgo-2"`,
		`"url": "https://github.com/virtru/wgo/commit/abc1234"`,
	} {
		assert.Contains(t, out, want, "JSON output missing %q", want)
	}

	// Reflection sweep: any non-zero exported field must appear by its json
	// key, so a future field added to the struct can't silently drop from JSON.
	v := reflect.ValueOf(*ctx)
	typ := v.Type()
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" || v.Field(i).IsZero() {
			continue
		}
		assert.Contains(t, out, `"`+name+`"`, "JSON missing field %q shown by text", name)
	}
}

// TestRenderTextSpecStates covers the three spec branches of the text renderer.
func TestRenderTextSpecStates(t *testing.T) {
	tests := []struct {
		name         string
		mutate       func(*models.Context)
		wantContains string
		wantAbsent   string
	}{
		{
			name:         "spec found",
			mutate:       func(*models.Context) {},
			wantContains: "spec:   📄 spec/WGO-130.md (draft, updated 2026-07-08)",
		},
		{
			name:         "spec missing",
			mutate:       func(c *models.Context) { c.Spec = nil; c.SpecMissing = true },
			wantContains: "spec:   ⚠ no spec (run: wgo spec new WGO-130)",
		},
		{
			name:         "spec unreadable",
			mutate:       func(c *models.Context) { c.Spec = nil; c.SpecUnreadable = true },
			wantContains: "spec:   ⚠ spec/WGO-130.md present but unreadable (malformed frontmatter)",
			wantAbsent:   "no spec",
		},
		{
			name:       "no ticket",
			mutate:     func(c *models.Context) { c.Ticket = ""; c.Spec = nil; c.SpecMissing = false },
			wantAbsent: "spec:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := fixtureContext()
			tt.mutate(ctx)

			var buf bytes.Buffer
			renderText(&buf, ctx, false)
			out := buf.String()

			if tt.wantContains != "" {
				assert.Contains(t, out, tt.wantContains)
			}
			if tt.wantAbsent != "" {
				assert.NotContains(t, out, tt.wantAbsent)
			}
		})
	}
}

// TestRenderTextRemoteStates covers the remote line, including the failure
// states that must not be rendered as a confidently-wrong "↑0 ↓0"/"(no remote)".
func TestRenderTextRemoteStates(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*models.Context)
		wantRemote string
	}{
		{
			name:       "ahead only",
			mutate:     func(*models.Context) {},
			wantRemote: "remote: ↑1  (origin/wgo)",
		},
		{
			name:       "no remote",
			mutate:     func(c *models.Context) { c.Ahead = 0; c.Remote = "(no remote)" },
			wantRemote: "remote: ↑0 ↓0 (no remote)",
		},
		{
			name:       "remote lookup failed",
			mutate:     func(c *models.Context) { c.Ahead = 0; c.Remote = "(remote lookup failed)" },
			wantRemote: "remote: ↑0 ↓0 (remote lookup failed)",
		},
		{
			name:       "ahead/behind unknown",
			mutate:     func(c *models.Context) { c.Ahead = 0; c.SyncUnknown = true },
			wantRemote: "remote: ↑? ↓? (origin/wgo)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := fixtureContext()
			tt.mutate(ctx)

			var buf bytes.Buffer
			renderText(&buf, ctx, false)
			assert.Contains(t, buf.String(), tt.wantRemote)
		})
	}
}

// TestRenderTextTicketLineNoSpec: a ticket with a live Jira status but no spec
// file still surfaces the status on a dedicated ticket line.
func TestRenderTextTicketLineNoSpec(t *testing.T) {
	ctx := fixtureContext()
	ctx.Spec = nil
	ctx.SpecMissing = false
	ctx.JiraStatus = "In Progress"
	ctx.JiraAssignee = ""

	var buf bytes.Buffer
	renderText(&buf, ctx, false)
	out := buf.String()
	assert.Contains(t, out, "ticket: WGO-130 [In Progress]")
	assert.NotContains(t, out, "jira:", "no assignee line without an assignee")
}

const specSmokeContent = `---
ticket: WGO-130
title: Smoke
status: draft
authors: [tester]
created: 2026-07-08
updated: 2026-07-08
---
# body
`

// specMalformedContent has valid --- delimiters but frontmatter that fails YAML
// unmarshal (a non-date value in a time.Time field), so spec.Parse errors while
// the file itself exists.
const specMalformedContent = `---
ticket: WGO-130
status: draft
created: not-a-date
updated: 2026-07-08
---
# body
`

// TestBuildContextResolvesTicketAndSpec is a light integration check that
// buildContext wires ticket + spec resolution end-to-end against a real jj
// repo. Task/PR/sibling field mapping is covered by the deterministic renderer
// tests above.
func TestBuildContextResolvesTicketAndSpec(t *testing.T) {
	jjtest.RequireJJ(t)
	jjtest.SetIdentity(t)
	// Isolate ~/.wgo so the agent heartbeat and Jira cache write to a temp HOME
	// rather than the developer's real state.
	t.Setenv("HOME", t.TempDir())

	repo, _ := jjtest.NewRepo(t)
	jjtest.Commit(t, repo, "initial", map[string]string{
		"README.md":       "hi\n",
		"spec/WGO-130.md": specSmokeContent,
	})
	jjtest.Bookmark(t, repo, "WGO-130-smoke", "@-")

	ctx, err := buildContext(repo)
	require.NoError(t, err)

	assert.Equal(t, models.ContextSchemaVersion, ctx.SchemaVersion)
	assert.Equal(t, "WGO-130", ctx.Ticket)
	require.NotNil(t, ctx.Spec, "spec should be resolved")
	assert.Equal(t, "draft", ctx.Spec.Status)
	assert.Contains(t, ctx.Spec.Path, "WGO-130.md")
}

// TestBuildContextResolvesSpecFromSubdir guards against the regression where
// spec lookup was rooted at cwd: running from a subdirectory must still find
// spec/<TICKET>.md at the workspace root.
func TestBuildContextResolvesSpecFromSubdir(t *testing.T) {
	jjtest.RequireJJ(t)
	jjtest.SetIdentity(t)

	repo, _ := jjtest.NewRepo(t)
	jjtest.Commit(t, repo, "initial", map[string]string{
		"spec/WGO-130.md":  specSmokeContent,
		"internal/keep.go": "package internal\n",
	})
	jjtest.Bookmark(t, repo, "WGO-130-smoke", "@-")

	ctx, err := buildContext(filepath.Join(repo, "internal"))
	require.NoError(t, err)

	require.NotNil(t, ctx.Spec, "spec should resolve from a subdirectory")
	assert.Equal(t, "spec/WGO-130.md", ctx.Spec.Path)
}

// TestBuildContextSpecUnreadable guards against the black hole where a spec file
// that exists but fails to parse produced neither a spec line nor a warning.
func TestBuildContextSpecUnreadable(t *testing.T) {
	jjtest.RequireJJ(t)
	jjtest.SetIdentity(t)

	repo, _ := jjtest.NewRepo(t)
	jjtest.Commit(t, repo, "initial", map[string]string{
		"spec/WGO-130.md": specMalformedContent,
	})
	jjtest.Bookmark(t, repo, "WGO-130-smoke", "@-")

	ctx, err := buildContext(repo)
	require.NoError(t, err)

	assert.Equal(t, "WGO-130", ctx.Ticket)
	assert.Nil(t, ctx.Spec, "malformed spec must not populate Spec")
	assert.True(t, ctx.SpecUnreadable, "malformed spec must set SpecUnreadable")
	assert.False(t, ctx.SpecMissing, "SpecMissing is for absent specs, not unreadable ones")
}

// TestBuildContextOptsLocalOnly verifies the statusline mode skips the sibling
// walk and the network PR fetch: on a cold cache it yields no PRs and no
// siblings without error.
func TestBuildContextOptsLocalOnly(t *testing.T) {
	jjtest.RequireJJ(t)
	jjtest.SetIdentity(t)
	// Isolate the on-disk PR cache so a cold cache is guaranteed.
	t.Setenv("HOME", t.TempDir())

	repo, _ := jjtest.NewRepo(t)
	jjtest.Commit(t, repo, "initial", map[string]string{"README.md": "hi\n"})
	jjtest.Bookmark(t, repo, "WGO-130-smoke", "@-")

	ctx, err := buildContextOpts(repo, contextOptions{LocalOnly: true, Siblings: false})
	require.NoError(t, err)
	assert.Nil(t, ctx.Siblings, "siblings walk should be skipped")
	assert.Empty(t, ctx.PRs, "cold cache in local-only mode yields no PRs")
}
