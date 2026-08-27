package rig

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/gotool/gotooltest"
)

// update rewrites the golden files instead of comparing against them:
// `go test ./internal/rig -update`. The generated go.work is the file a
// confused reader opens first, so its exact shape — comments included — is
// worth reviewing as a diff rather than asserting line by line.
var update = flag.Bool("update", false, "rewrite testdata golden files")

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden file; re-run with -update")
	assert.Equal(t, string(want), got)
}

func TestRenderGoWorkDSP(t *testing.T) {
	req, p := dspRequest()
	m, err := p.Plan(req)
	require.NoError(t, err)

	assertGolden(t, "dsp-2.7.1.go.work", RenderGoWork(m))
}

// A frozen module renders a replace pinning it back to its baseline version.
func TestRenderGoWorkFrozen(t *testing.T) {
	req, p := dspRequest()
	req.Baseline = map[string]string{
		"google.golang.org/grpc":      "v1.65.0",
		"github.com/stretchr/testify": "v1.9.0",
		"golang.org/x/net":            "v0.27.0",
	}
	m, err := p.Plan(req)
	require.NoError(t, err)
	m.Frozen = []string{"golang.org/x/net", "google.golang.org/grpc"}

	assertGolden(t, "dsp-2.7.1-frozen.go.work", RenderGoWork(m))
}

// A frozen module with no baseline version cannot be pinned; rendering an empty
// version would produce a go.work that does not parse.
func TestRenderGoWorkFrozenWithoutBaseline(t *testing.T) {
	req, p := dspRequest()
	m, err := p.Plan(req)
	require.NoError(t, err)
	m.Frozen = []string{"golang.org/x/net"}

	out := RenderGoWork(m)
	assert.NotContains(t, out, "replace")
	assert.NotContains(t, out, "golang.org/x/net")
}

// Without the primary's own go.work the rig uses only its root module, so the
// sdk `use` line disappears but nothing else changes shape.
func TestRenderGoWorkWithoutPrimaryUse(t *testing.T) {
	req, p := dspRequest()
	req.PrimaryUse = nil
	m, err := p.Plan(req)
	require.NoError(t, err)

	out := RenderGoWork(m)
	assert.Contains(t, out, "\t./src/data-security-platform-v2.7.1\n")
	assert.NotContains(t, out, "./src/data-security-platform-v2.7.1/sdk")
}

// Every use path must be relative to the rig root, and stay inside it.
//
// Not because the toolchain objects — `go work use ../outside` is perfectly
// legal, and an absolute path is too. The requirement is that a rig be a
// self-contained directory: `wgo rig rm` deletes the rig root and expects that
// to be the whole rig, and the whole layout survives being moved or copied only
// while nothing points outside it.
func TestRenderGoWorkUsePathsAreRelative(t *testing.T) {
	req, p := dspRequest()
	m, err := p.Plan(req)
	require.NoError(t, err)

	var uses int
	for _, line := range strings.Split(RenderGoWork(m), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") || line == "use (" || line == ")" {
			continue
		}
		if !strings.HasPrefix(line, "./src/") {
			continue
		}
		uses++
		assert.NotContains(t, line, "..", "use path must not escape the rig root")
	}
	assert.Equal(t, len(m.Members), uses)
}

// The warning about `go mod tidy` and `go work sync` is load-bearing: both
// write the workspace build list back into the checked-out go.mod files, which
// silently destroys the pins the rig exists to preserve.
func TestRenderGoWorkWarnsAgainstTidy(t *testing.T) {
	req, p := dspRequest()
	m, err := p.Plan(req)
	require.NoError(t, err)

	out := RenderGoWork(m)
	assert.Contains(t, out, "go mod tidy")
	assert.Contains(t, out, "go work sync")
	assert.Contains(t, out, "do not edit")
}

// The materialiser writes this file and then runs `go work edit -fmt` over it,
// which doubles as validation. Rendering something the toolchain reformats
// would make every `wgo rig sync` show spurious churn, so the output has to be
// already-formatted, not merely parseable.
func TestRenderGoWorkIsToolchainFormatted(t *testing.T) {
	gotooltest.RequireGo(t)

	for _, name := range []string{"dsp-2.7.1.go.work", "dsp-2.7.1-frozen.go.work"} {
		t.Run(name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", name))
			require.NoError(t, err)

			dir := t.TempDir()
			work := filepath.Join(dir, GoWorkName)
			require.NoError(t, os.WriteFile(work, want, 0o644))

			// `go work edit -fmt` parses and rewrites the file without
			// resolving the `use` directories, so the checkouts need not exist.
			//
			// GOWORK is set explicitly rather than left to the upward search:
			// this command rewrites whatever go.work it finds, and the search
			// does not stop at the temp dir. Naming the target keeps a test that
			// misplaces its fixture failing instead of reformatting the
			// developer's own workspace file.
			cmd := exec.Command("go", "work", "edit", "-fmt")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOWORK="+work)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "generated go.work does not parse: %s", out)

			got, err := os.ReadFile(work)
			require.NoError(t, err)
			assert.Equal(t, string(want), string(got))
		})
	}
}

func TestSourceSummary(t *testing.T) {
	tests := []struct {
		name string
		src  Source
		want string
	}{
		{"repo", Source{Kind: "repo", Ref: "virtru-corp/dsp@v2.7.1"}, "virtru-corp/dsp@v2.7.1"},
		{"binary", Source{Kind: "binary", Binary: "/dist/dsp-server"}, "/dist/dsp-server"},
		{"manual", Source{Kind: "manual", Modules: []string{"a@v1", "b@v2"}}, "a@v1, b@v2"},
		{"manual without modules", Source{Kind: "manual"}, "explicit modules"},
		{"unknown", Source{Kind: "wat"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sourceSummary(tt.src))
		})
	}
}
