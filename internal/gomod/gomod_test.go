package gomod

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/modfile"
)

func TestParseOrigin(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		want    Origin
		wantErr error
	}{
		// The trap pair: these two differ only in whether "otdfctl" is the repo
		// or a subdirectory, which prefix matching cannot distinguish.
		{
			name: "separate repo, root module",
			path: "github.com/opentdf/otdfctl",
			want: Origin{Host: "github.com", Owner: "opentdf", Repo: "otdfctl"},
		},
		{
			name: "same-named module inside a monorepo",
			path: "github.com/opentdf/platform/otdfctl",
			want: Origin{Host: "github.com", Owner: "opentdf", Repo: "platform", Subdir: "otdfctl"},
		},

		{
			name: "single-element subdir",
			path: "github.com/opentdf/platform/service",
			want: Origin{Host: "github.com", Owner: "opentdf", Repo: "platform", Subdir: "service"},
		},
		{
			name: "multi-element subdir",
			path: "github.com/opentdf/platform/protocol/go",
			want: Origin{Host: "github.com", Owner: "opentdf", Repo: "platform", Subdir: "protocol/go"},
		},
		{
			name: "nested lib subdir",
			path: "github.com/opentdf/platform/lib/ocrypto",
			want: Origin{Host: "github.com", Owner: "opentdf", Repo: "platform", Subdir: "lib/ocrypto"},
		},

		// Major-version suffixes are part of the module path, never the repo
		// layout, so they must be stripped before the two-element rule runs.
		{
			name: "root module with major suffix",
			path: "github.com/virtru-corp/data-security-platform/v2",
			want: Origin{Host: "github.com", Owner: "virtru-corp", Repo: "data-security-platform", Major: "v2"},
		},
		{
			name: "subdir module with major suffix",
			path: "github.com/virtru-corp/data-security-platform/sdk/v2",
			want: Origin{Host: "github.com", Owner: "virtru-corp", Repo: "data-security-platform", Subdir: "sdk", Major: "v2"},
		},

		{
			name:    "owner only",
			path:    "github.com/opentdf",
			wantErr: ErrNotRepoPath,
		},
		{
			name:    "host only",
			path:    "github.com",
			wantErr: ErrNotRepoPath,
		},
		// Hosts we cannot map structurally must be rejected rather than guessed.
		{
			name:    "gopkg.in vanity path",
			path:    "gopkg.in/yaml.v3",
			wantErr: ErrNotRepoPath,
		},
		{
			name:    "golang.org/x",
			path:    "golang.org/x/mod",
			wantErr: ErrUnsupportedHost,
		},
		{
			name:    "gitlab nested subgroups are not mappable",
			path:    "gitlab.com/group/subgroup/project",
			wantErr: ErrUnsupportedHost,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseOrigin(tt.path)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestOriginSlug(t *testing.T) {
	o, err := ParseOrigin("github.com/opentdf/platform/service")
	require.NoError(t, err)
	assert.Equal(t, "opentdf/platform", o.Slug())
}

func TestTagFor(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		version string
		want    string
	}{
		{"root module", "github.com/opentdf/otdfctl", "v0.26.2", "v0.26.2"},
		{"single-element subdir", "github.com/opentdf/platform/service", "v0.11.6", "service/v0.11.6"},
		{"multi-element subdir", "github.com/opentdf/platform/protocol/go", "v0.13.0", "protocol/go/v0.13.0"},
		{"nested lib", "github.com/opentdf/platform/lib/ocrypto", "v0.7.0", "lib/ocrypto/v0.7.0"},
		// The major-version element lives in the module path only. Emitting
		// "sdk/v2/v2.7.1" here would resolve to nothing.
		{"subdir with major suffix", "github.com/virtru-corp/data-security-platform/sdk/v2", "v2.7.1", "sdk/v2.7.1"},
		{"root with major suffix", "github.com/virtru-corp/data-security-platform/v2", "v2.7.1", "v2.7.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, err := ParseOrigin(tt.path)
			require.NoError(t, err)
			assert.Equal(t, tt.want, o.TagFor(tt.version))
		})
	}
}

func TestPseudoCommit(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{
			name:    "pseudo-version on a tagged base",
			version: "v2.7.1-0.20260108153148-87305bdcc192",
			want:    "87305bdcc192",
		},
		{
			name:    "pseudo-version with no tagged base",
			version: "v0.0.0-20240227190045-46a1ea645a98",
			want:    "46a1ea645a98",
		},
		{"plain release", "v1.2.3", ""},
		{"incompatible release", "v28.5.2+incompatible", ""},
		{"prerelease that is not a pseudo-version", "v1.2.3-rc.1", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, PseudoCommit(tt.version))
		})
	}
}

func TestRevset(t *testing.T) {
	svc, err := ParseOrigin("github.com/opentdf/platform/service")
	require.NoError(t, err)

	// Tags resolve through present(tags(exact:…)) so that a same-named bookmark
	// cannot win, and a missing tag yields an empty set rather than an error.
	assert.Equal(t, `present(tags(exact:"service/v0.11.6"))`, svc.Revset("v0.11.6"))

	// Pseudo-versions carry their own commit, so no tag lookup is needed.
	dsp, err := ParseOrigin("github.com/virtru-corp/data-security-platform/sdk/v2")
	require.NoError(t, err)
	assert.Equal(t, "87305bdcc192", dsp.Revset("v2.7.1-0.20260108153148-87305bdcc192"))
}

func TestInOrg(t *testing.T) {
	prefixes := []string{"github.com/opentdf", "github.com/virtru-corp/"}

	assert.True(t, InOrg("github.com/opentdf/platform/service", prefixes))
	assert.True(t, InOrg("github.com/opentdf/otdfctl", prefixes))
	assert.True(t, InOrg("github.com/virtru-corp/data-security-platform/v2", prefixes))
	// Exact match on the prefix itself.
	assert.True(t, InOrg("github.com/opentdf", prefixes))

	// Element-boundary matching: a longer owner name that merely starts with
	// the prefix must not match.
	assert.False(t, InOrg("github.com/opentdfx/thing", prefixes))
	assert.False(t, InOrg("github.com/opentdf-fork/platform", prefixes))
	assert.False(t, InOrg("github.com/spf13/cobra", prefixes))

	assert.False(t, InOrg("github.com/opentdf/platform", nil))
	assert.False(t, InOrg("github.com/opentdf/platform", []string{"", "  ", "/"}))
}

func TestMaxGoVersion(t *testing.T) {
	assert.Equal(t, "1.25.6", MaxGoVersion("1.24.0", "1.25.6", "1.21"))
	assert.Equal(t, "1.24.5", MaxGoVersion("1.24.5"))
	// A bare major.minor compares correctly against a full patch version.
	assert.Equal(t, "1.25", MaxGoVersion("1.24.9", "1.25"))
	assert.Equal(t, "1.24.1", MaxGoVersion("1.24", "1.24.1"))
	assert.Equal(t, "", MaxGoVersion())
	assert.Equal(t, "", MaxGoVersion("", "  "))
	assert.Equal(t, "1.22.0", MaxGoVersion("", "1.22.0"))
}

func TestLocalReplaceTargets(t *testing.T) {
	tests := []struct {
		name        string
		subdir      string
		gomod       string
		wantInRepo  []string
		wantEscaped []string
	}{
		{
			name:   "root module replacing a sibling subdir",
			subdir: "",
			gomod: `module github.com/virtru-corp/data-security-platform/v2
go 1.24.5
replace github.com/virtru-corp/data-security-platform/sdk/v2 => ./sdk
`,
			wantInRepo: []string{"sdk"},
		},
		{
			name:   "nested module replacing upward",
			subdir: "test/integration",
			gomod: `module github.com/opentdf/platform/test/integration
go 1.24.5
replace (
	github.com/opentdf/platform/lib/fixtures => ../../lib/fixtures
	github.com/opentdf/platform/lib/ocrypto => ../../lib/ocrypto
	github.com/opentdf/platform/protocol/go => ../../protocol/go
	github.com/opentdf/platform/sdk => ../../sdk
)
`,
			wantInRepo: []string{"lib/fixtures", "lib/ocrypto", "protocol/go", "sdk"},
		},
		{
			name:   "replacement with a version is not a local path",
			subdir: "",
			gomod: `module github.com/acme/app
go 1.24.5
replace github.com/docker/cli => github.com/docker/cli v28.5.2+incompatible
`,
		},
		{
			name:   "target escaping the repo root",
			subdir: "service",
			gomod: `module github.com/acme/app/service
go 1.24.5
replace github.com/other/thing => ../../../other-repo/thing
`,
			wantEscaped: []string{"../../../other-repo/thing"},
		},
		{
			name:   "absolute target",
			subdir: "",
			gomod: `module github.com/acme/app
go 1.24.5
replace github.com/other/thing => /opt/checkouts/thing
`,
			wantEscaped: []string{"/opt/checkouts/thing"},
		},
		{
			name:   "duplicate targets are collapsed",
			subdir: "",
			gomod: `module github.com/acme/app
go 1.24.5
replace (
	github.com/acme/app/a => ./shared
	github.com/acme/app/b => ./shared
)
`,
			wantInRepo: []string{"shared"},
		},
		{
			name:   "replacement pointing at the repo root itself",
			subdir: "sub",
			gomod: `module github.com/acme/app/sub
go 1.24.5
replace github.com/acme/app => ../
`,
			wantInRepo: []string{""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := modfile.Parse("go.mod", []byte(tt.gomod), nil)
			require.NoError(t, err)

			inRepo, escaped := LocalReplaceTargets(f, tt.subdir)
			assert.Equal(t, tt.wantInRepo, inRepo)
			assert.Equal(t, tt.wantEscaped, escaped)
		})
	}
}

func TestLocalReplaceTargetsNilFile(t *testing.T) {
	inRepo, escaped := LocalReplaceTargets(nil, "")
	assert.Nil(t, inRepo)
	assert.Nil(t, escaped)
}
