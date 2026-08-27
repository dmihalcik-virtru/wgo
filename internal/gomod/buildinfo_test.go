package gomod

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realBuildInfo mirrors the exact shape `go version -m` emits, including the
// trailing tab after a "(devel)" main-module version, a pseudo-versioned dep,
// a "=>" replacement record, and build settings whose values contain "=".
const realBuildInfo = "/tmp/dsp-server: go1.24.5\n" +
	"\tpath\tgithub.com/virtru-corp/data-security-platform/v2/cmd/server\n" +
	"\tmod\tgithub.com/virtru-corp/data-security-platform/v2\t(devel)\t\n" +
	"\tdep\tgithub.com/opentdf/platform/service\tv0.11.6\th1:abc=\n" +
	"\tdep\tgithub.com/opentdf/platform/sdk\tv0.10.1\th1:def=\n" +
	"\tdep\tgithub.com/sourcegraph/conc\tv0.3.1-0.20240121214520-5f936abd7ae8\th1:ghi=\n" +
	"\tdep\tgithub.com/docker/cli\tv28.0.0+incompatible\th1:jkl=\n" +
	"\t=>\tgithub.com/docker/cli\tv28.5.2+incompatible\th1:mno=\n" +
	"\tbuild\t-buildmode=exe\n" +
	"\tbuild\tCGO_ENABLED=1\n" +
	"\tbuild\tCGO_CFLAGS=\n" +
	"\tbuild\tDefaultGODEBUG=tracebacklabels=0,x509sslcertoverrideplatform=0\n" +
	"\tbuild\tvcs.revision=deadbeefcafe\n"

func TestParseBuildInfo(t *testing.T) {
	bi, err := ParseBuildInfo([]byte(realBuildInfo))
	require.NoError(t, err)

	assert.Equal(t, "go1.24.5", bi.GoVersion)
	assert.Equal(t, "github.com/virtru-corp/data-security-platform/v2/cmd/server", bi.Path)
	assert.Equal(t, "github.com/virtru-corp/data-security-platform/v2", bi.Main.Path)
	assert.Equal(t, "(devel)", bi.Main.Version)
	assert.True(t, bi.Main.Main)

	require.Len(t, bi.Deps, 4)
	assert.Equal(t, "github.com/opentdf/platform/service", bi.Deps[0].Path)
	assert.Equal(t, "v0.11.6", bi.Deps[0].Version)
	assert.Nil(t, bi.Deps[0].Replace)

	// A "=>" record attaches to the dep immediately preceding it, and
	// Effective() must then report the replacement.
	docker := bi.Deps[3]
	assert.Equal(t, "github.com/docker/cli", docker.Path)
	assert.Equal(t, "v28.0.0+incompatible", docker.Version)
	require.NotNil(t, docker.Replace)
	assert.Equal(t, "v28.5.2+incompatible", docker.Replace.Version)
	assert.Equal(t, "v28.5.2+incompatible", docker.Effective().Version)

	// Settings values may themselves contain "="; only the first splits.
	assert.Equal(t, "exe", bi.Settings["-buildmode"])
	assert.Equal(t, "1", bi.Settings["CGO_ENABLED"])
	assert.Equal(t, "", bi.Settings["CGO_CFLAGS"])
	assert.Equal(t, "tracebacklabels=0,x509sslcertoverrideplatform=0", bi.Settings["DefaultGODEBUG"])
	assert.Equal(t, "deadbeefcafe", bi.Settings["vcs.revision"])
}

// The build info of a real artifact is the pin source for `wgo rig
// --from-binary`, so each dep must round-trip through the origin mapping.
func TestParseBuildInfoFeedsOriginMapping(t *testing.T) {
	bi, err := ParseBuildInfo([]byte(realBuildInfo))
	require.NoError(t, err)

	svc := bi.Deps[0]
	o, err := ParseOrigin(svc.Path)
	require.NoError(t, err)
	assert.Equal(t, "opentdf/platform", o.Slug())
	assert.Equal(t, "service/v0.11.6", o.TagFor(svc.Version))

	// A pseudo-versioned dep resolves by commit rather than by tag.
	conc := bi.Deps[2]
	assert.Equal(t, "5f936abd7ae8", PseudoCommit(conc.Version))
}

func TestParseBuildInfoErrors(t *testing.T) {
	_, err := ParseBuildInfo([]byte(""))
	require.Error(t, err)

	_, err = ParseBuildInfo([]byte("this is not a go binary\n"))
	require.Error(t, err)
}

func TestParseBuildInfoStrippedBinaryIsAnError(t *testing.T) {
	// `go version -m` prints a header for any binary it recognises, so a header
	// alone proves nothing. With neither a main module nor a dep there is no pin
	// to resolve, and returning a zero-valued BuildInfo would send the caller on
	// to fail somewhere further away with a less useful message.
	_, err := ParseBuildInfo([]byte("/tmp/hello: go1.24.5\n\tpath\tcommand-line-arguments\n"))
	require.Error(t, err)
}

func TestParseBuildInfoMainWithNoDeps(t *testing.T) {
	// A real main module with zero dependencies is legitimate, and must not be
	// caught by the stripped-binary check above.
	bi, err := ParseBuildInfo([]byte("/tmp/hello: go1.24.5\n\tmod\tgithub.com/acme/hello\tv1.0.0\th1:abc=\n"))
	require.NoError(t, err)
	assert.Equal(t, "github.com/acme/hello", bi.Main.Path)
	assert.Empty(t, bi.Deps)
}

func TestParseModuleList(t *testing.T) {
	// `go list -m -json all` emits concatenated objects, not a JSON array.
	const stream = `{
	"Path": "github.com/virtru-corp/data-security-platform/v2",
	"Main": true,
	"Dir": "/src/dsp",
	"GoVersion": "1.24.5"
}
{
	"Path": "github.com/opentdf/platform/service",
	"Version": "v0.11.6",
	"Dir": "/cache/service@v0.11.6"
}
{
	"Path": "github.com/opentdf/platform/lib/flattening",
	"Version": "v0.1.3",
	"Indirect": true
}
{
	"Path": "github.com/virtru-corp/data-security-platform/sdk/v2",
	"Version": "v2.7.1-0.20260108153148-87305bdcc192",
	"Replace": {
		"Path": "./sdk",
		"Dir": "/src/dsp/sdk"
	}
}
`
	mods, err := ParseModuleList(strings.NewReader(stream))
	require.NoError(t, err)
	require.Len(t, mods, 4)

	assert.True(t, mods[0].Main)
	assert.Equal(t, "1.24.5", mods[0].GoVersion)

	assert.Equal(t, "v0.11.6", mods[1].Version)
	assert.False(t, mods[1].Indirect)

	assert.True(t, mods[2].Indirect)

	// A locally replaced module reports the replacement as its effective
	// source; the rig serves it from the primary checkout rather than
	// creating a checkout of its own.
	repl := mods[3]
	require.NotNil(t, repl.Replace)
	assert.Equal(t, "./sdk", repl.Effective().Path)
	assert.Equal(t, "/src/dsp/sdk", repl.Effective().Dir)
}

func TestParseModuleListEmpty(t *testing.T) {
	mods, err := ParseModuleList(strings.NewReader(""))
	require.NoError(t, err)
	assert.Empty(t, mods)
}

func TestParseModuleListMalformed(t *testing.T) {
	_, err := ParseModuleList(strings.NewReader(`{"Path": "a"} {oops}`))
	require.Error(t, err)
}
