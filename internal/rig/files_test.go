package rig

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// filesByName indexes a rendered file set for assertion.
func filesByName(t *testing.T, m *Manifest, root string) map[string]GeneratedFile {
	t.Helper()
	out := map[string]GeneratedFile{}
	for _, f := range GeneratedFiles(m, root) {
		require.NotContains(t, out, f.Name, "duplicate generated file")
		out[f.Name] = f
	}
	return out
}

func TestGeneratedFilesCoverTheWholeSet(t *testing.T) {
	files := filesByName(t, twoCheckoutManifest(), "/rigs/dsp")
	for _, name := range []string{EnvShName, EnvrcName, ClaudeMDName, ReadmeName, GitignoreName} {
		assert.Contains(t, files, name)
		assert.NotEmpty(t, files[name].Content, name)
	}
	assert.Equal(t, uint32(0o755), files[EnvShName].Mode, "env.sh is meant to be executable")
}

func TestEnvShExportsAbsoluteGoWork(t *testing.T) {
	env := filesByName(t, twoCheckoutManifest(), "/rigs/dsp")[EnvShName].Content

	assert.Contains(t, env, "GOWORK='/rigs/dsp/go.work'\n")
	assert.Contains(t, env, "export GOWORK\n")
	assert.Contains(t, env, "WGO_RIG='dsp'\n")
	assert.Contains(t, env, "export WGO_RIG\n")
	assert.True(t, strings.HasPrefix(env, "#!/bin/sh\n"))
}

func TestEnvShQuotesAwkwardPaths(t *testing.T) {
	// Rig roots come from config and land under things like
	// ~/Library/Application Support; an unquoted export would truncate.
	env := filesByName(t, twoCheckoutManifest(), "/Users/me/My Rigs/it's here")[EnvShName].Content
	assert.Contains(t, env, `GOWORK='/Users/me/My Rigs/it'\''s here/go.work'`)
}

func TestEnvrcDelegatesToEnvSh(t *testing.T) {
	// direnv and a manually sourced shell must not be able to disagree.
	envrc := filesByName(t, twoCheckoutManifest(), "/rigs/dsp")[EnvrcName].Content
	assert.Contains(t, envrc, "source_env_if_exists "+EnvShName)
	assert.NotContains(t, envrc, "GOWORK=", "the exports live in exactly one place")
}

func TestGitignoreIgnoresEverything(t *testing.T) {
	ignore := filesByName(t, twoCheckoutManifest(), "/rigs/dsp")[GitignoreName].Content
	assert.Contains(t, strings.Split(ignore, "\n"), "*")
}

func TestClaudeMDWarnsAboutTheThingsThatDestroyARig(t *testing.T) {
	md := filesByName(t, twoCheckoutManifest(), "/rigs/dsp")[ClaudeMDName].Content

	assert.Contains(t, md, "export GOWORK=/rigs/dsp/go.work")
	for _, forbidden := range []string{"go mod tidy", "go work sync", "jj bookmark set", "jj git push"} {
		assert.Contains(t, md, forbidden)
	}
	// The upward-search trap is the failure an agent hits first and diagnoses
	// last, because the wrong workspace still builds.
	assert.Contains(t, md, "walking *up*")

	// Every member appears in the layout table with the path it is used from.
	assert.Contains(t, md, "| `github.com/opentdf/platform/service` | `./src/platform-v0.9.0/service` | `service/v0.9.0` |")
	assert.Contains(t, md, "| `github.com/opentdf/otdfctl` | `./src/otdfctl-v0.3.0` | `v0.3.0` |")
}

func TestReadmeDescribesCheckoutsAndSkips(t *testing.T) {
	m := twoCheckoutManifest()
	m.Created = "2026-08-26"
	m.Skipped = []Skip{{
		Path: "github.com/virtru/gone", Version: "v1.0.0",
		Kind: SkipUnreachable, Detail: "no such repository",
	}}

	readme := filesByName(t, m, "/rigs/dsp")[ReadmeName].Content

	assert.Contains(t, readme, "# rig: dsp")
	assert.Contains(t, readme, "opentdf/platform@v0.9.0")
	assert.Contains(t, readme, "**Checkouts:** 2, serving 3 modules")
	assert.Contains(t, readme, "**Created:** 2026-08-26")
	assert.Contains(t, readme, ". /rigs/dsp/env.sh")

	assert.Contains(t, readme, "| `platform-v0.9.0` | `opentdf/platform` | `service/v0.9.0` | sparse: `lib/fixtures`, `service` |")
	assert.Contains(t, readme, "| `otdfctl-v0.3.0` | `opentdf/otdfctl` | `v0.3.0` | full |")

	assert.Contains(t, readme, "| `github.com/virtru/gone` | unreachable: no such repository |")
	// Deleting the tree by hand strands jj workspaces in the main clones, so
	// the README has to name the command that does not.
	assert.Contains(t, readme, "wgo rig rm dsp")
}

func TestReadmeOmitsSkippedSectionWhenNothingWasSkipped(t *testing.T) {
	readme := filesByName(t, twoCheckoutManifest(), "/rigs/dsp")[ReadmeName].Content
	assert.NotContains(t, readme, "## Skipped")
}

func TestPinLabelFallsBackToShortCommit(t *testing.T) {
	assert.Equal(t, "v1.2.3", pinLabel(Checkout{Tag: "v1.2.3", Commit: "abcdef0123456789"}))
	assert.Equal(t, shortCommit("abcdef0123456789"), pinLabel(Checkout{Commit: "abcdef0123456789"}))
}

func TestContentsLabel(t *testing.T) {
	assert.Equal(t, "full", contentsLabel(Checkout{Full: true}))
	// A sparse checkout with no patterns would materialise nothing, so an empty
	// set can only mean "not narrowed".
	assert.Equal(t, "full", contentsLabel(Checkout{}))
	assert.Equal(t, "sparse: `a`, `b/c`", contentsLabel(Checkout{Sparse: []string{"a", "b/c"}}))
}
