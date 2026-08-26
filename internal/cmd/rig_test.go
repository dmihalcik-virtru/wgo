package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/gomod"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/rig"
)

// cloneFailClient is a jj.Client whose clone always fails — an unreachable,
// renamed or unauthorised repository.
type cloneFailClient struct {
	jj.Client
	inits []string
}

func (c *cloneFailClient) IsRepo(string) bool { return false }

func (c *cloneFailClient) GitClone(string, string) error {
	return errors.New("remote: Repository not found")
}

func (c *cloneFailClient) GitInit(dest string, _ jj.InitOpts) error {
	c.inits = append(c.inits, dest)
	return os.MkdirAll(dest, 0o755)
}

func (c *cloneFailClient) GitRemoteAdd(string, string, string) error { return nil }
func (c *cloneFailClient) GitFetch(string, string, []string) error   { return nil }

// A rig resolves every pin through a tag revset, so an empty repo is worse than
// no repo: its pins fail as "no such tag", which reads as a bad --from rather
// than an unreachable dependency, and the planner aborts instead of skipping.
// The initialised directory also outlives the failure and short-circuits every
// later lookup for the slug.
func TestFindOrCloneRepoNoInitLeavesNothingBehind(t *testing.T) {
	mains := t.TempDir()
	cfg := &config.Config{}
	cfg.Worktree.MainsDir = mains
	c := &cloneFailClient{}

	_, err := findOrCloneRepoNoInit(c, cfg, "acme", "private")
	require.Error(t, err)
	assert.Empty(t, c.inits, "no fallback repo for a caller that needs history")
	assert.NoDirExists(t, filepath.Join(mains, "acme", "private"))

	// The permissive caller keeps its fallback: `wgo to` against a GitHub repo
	// with no commits yet is a supported flow.
	dest, err := findOrCloneRepo(c, cfg, "acme", "brand-new")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(mains, "acme", "brand-new"), dest)
	assert.Equal(t, []string{dest}, c.inits)
}

func TestParseRigFrom(t *testing.T) {
	owner, repo, ref, err := parseRigFrom("virtru-corp/data-security-platform@v2.7.1")
	require.NoError(t, err)
	assert.Equal(t, "virtru-corp", owner)
	assert.Equal(t, "data-security-platform", repo)
	assert.Equal(t, "v2.7.1", ref)

	// A subdirectory module's tag carries slashes, and so may a ref.
	_, _, ref, err = parseRigFrom("opentdf/platform@service/v0.11.6")
	require.NoError(t, err)
	assert.Equal(t, "service/v0.11.6", ref)

	for _, bad := range []string{"", "  ", "owner/repo", "owner/repo@", "@v1.0.0", "owner@v1.0.0", "/repo@v1.0.0"} {
		_, _, _, err := parseRigFrom(bad)
		assert.Error(t, err, "should reject %q", bad)
	}
}

func TestParseRigFromErrorShowsTheShape(t *testing.T) {
	_, _, _, err := parseRigFrom("data-security-platform")
	require.Error(t, err)
	// The mistake is easy to make and impossible to guess a fix for from
	// "invalid argument".
	assert.Contains(t, err.Error(), "<owner/repo>@<ref>")
	assert.Contains(t, err.Error(), "--from virtru-corp/data-security-platform@v2.7.1")
}

func TestParseModulePins(t *testing.T) {
	mods, err := parseModulePins([]string{
		"github.com/opentdf/platform/sdk@v0.10.1",
		"  github.com/opentdf/otdfctl@v0.26.2  ",
	})
	require.NoError(t, err)
	require.Len(t, mods, 2)
	assert.Equal(t, gomod.Module{Path: "github.com/opentdf/platform/sdk", Version: "v0.10.1"}, mods[0])
	assert.Equal(t, "github.com/opentdf/otdfctl", mods[1].Path)

	none, err := parseModulePins(nil)
	require.NoError(t, err)
	assert.Empty(t, none)

	for _, bad := range []string{"github.com/opentdf/platform/sdk", "@v1.0.0", "path@", ""} {
		_, err := parseModulePins([]string{bad})
		assert.Error(t, err, "should reject %q", bad)
	}
}

func TestBaselineOfSkipsVersionlessModules(t *testing.T) {
	base := baselineOf([]gomod.Module{
		{Path: "github.com/opentdf/platform/sdk", Version: "v0.10.1"},
		// The main module has no version in `go list` output.
		{Path: "github.com/virtru-corp/dsp/v2", Main: true},
		{Path: "", Version: "v1.0.0"},
	})
	assert.Equal(t, map[string]string{"github.com/opentdf/platform/sdk": "v0.10.1"}, base)
}

// `rig verify` measures the build's *effective* versions, so the baseline has
// to be recorded the same way. A declared version left behind by a replace was
// never built from, and comparing against it makes every replaced module read
// as drift on the very first verify.
func TestBaselineOfRecordsEffectiveVersions(t *testing.T) {
	base := baselineOf([]gomod.Module{
		// A fork replace: what shipped is v0.10.2 from the fork, not v0.10.1.
		{
			Path: "github.com/opentdf/platform/sdk", Version: "v0.10.1",
			Replace: &gomod.Module{Path: "github.com/virtru-corp/platform/sdk", Version: "v0.10.2"},
		},
		// A directory replace has no version at all. The stale pseudo-version
		// on the left is not what was built.
		{
			Path: "github.com/virtru-corp/dsp/sdk/v2", Version: "v2.7.1-0.20260801120000-abcdefabcdef",
			Replace: &gomod.Module{Path: "./sdk"},
		},
		{Path: "github.com/opentdf/otdfctl", Version: "v0.3.0"},
	})
	assert.Equal(t, map[string]string{
		"github.com/opentdf/platform/sdk": "v0.10.2",
		"github.com/opentdf/otdfctl":      "v0.3.0",
	}, base)
}

func TestCheckRigRootFree(t *testing.T) {
	dir := t.TempDir()

	// Nothing there yet.
	require.NoError(t, checkRigRootFree(filepath.Join(dir, "absent"), "absent"))

	// An empty directory the user made ahead of time is not in the way.
	empty := filepath.Join(dir, "empty")
	require.NoError(t, os.MkdirAll(empty, 0o755))
	require.NoError(t, checkRigRootFree(empty, "empty"))

	// Occupied by something that is not a rig.
	occupied := filepath.Join(dir, "occupied")
	require.NoError(t, os.MkdirAll(occupied, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(occupied, "notes.md"), []byte("mine"), 0o644))
	err := checkRigRootFree(occupied, "occupied")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a rig")
}

func TestCheckRigRootFreeRecognisesAnExistingRig(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "dsp")
	require.NoError(t, rig.Save(root, testManifest()))

	err := checkRigRootFree(root, "dsp")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	// Errors name the command that resolves them.
	assert.Contains(t, err.Error(), "wgo rig rm dsp")
}

// testManifest is a minimal valid rig with two checkouts.
func testManifest() *rig.Manifest {
	return &rig.Manifest{
		Name:      "dsp",
		GoVersion: "1.24.5",
		Source:    rig.Source{Kind: "repo", Ref: "opentdf/platform@v0.9.0"},
		Primary:   "github.com/opentdf/platform/service",
		Created:   "2026-08-26T00:00:00Z",
		Checkouts: []rig.Checkout{
			{
				Dir: "platform-v0.9.0", Workspace: "rig-dsp-aaaaaaaa",
				Repo: "opentdf/platform", MainClone: "/mains/opentdf/platform",
				Commit: "aaaaaaaa11112222", Tag: "service/v0.9.0",
				Sparse: []string{"service"},
			},
			{
				Dir: "otdfctl-v0.3.0", Workspace: "rig-dsp-bbbbbbbb",
				Repo: "opentdf/otdfctl", MainClone: "/mains/opentdf/otdfctl",
				Commit: "bbbbbbbb33334444", Tag: "v0.3.0", Full: true,
			},
		},
		Members: []rig.Member{
			{Path: "github.com/opentdf/platform/service", Version: "v0.9.0",
				Checkout: "platform-v0.9.0", Subdir: "service"},
			{Path: "github.com/opentdf/otdfctl", Version: "v0.3.0", Checkout: "otdfctl-v0.3.0"},
		},
	}
}

func TestResolveRigRootByName(t *testing.T) {
	rigDir := t.TempDir()
	require.NoError(t, rig.Save(filepath.Join(rigDir, "dsp"), testManifest()))

	root, err := resolveRigRoot(rigDir, []string{"dsp"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(rigDir, "dsp"), root)

	_, err = resolveRigRoot(rigDir, []string{"nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wgo rig ls")
}

// A rig name is a directory name, never a path. Joining an unvalidated one onto
// rig.dir puts `rig rm`'s RemoveAll — and every command that resolves a root —
// anywhere on the filesystem.
func TestResolveRigRootRejectsPathsDisguisedAsNames(t *testing.T) {
	rigDir := t.TempDir()
	outside := filepath.Join(filepath.Dir(rigDir), "not-a-rig")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	require.NoError(t, rig.Save(outside, testManifest()))

	for _, name := range []string{
		"../" + filepath.Base(outside),
		"nested/dsp",
		"/etc",
		".",
	} {
		_, err := resolveRigRoot(rigDir, []string{name})
		assert.Error(t, err, "should reject %q", name)
	}
}

func TestRigRootContaining(t *testing.T) {
	rigDir := t.TempDir()
	root := filepath.Join(rigDir, "dsp")
	deep := filepath.Join(root, rig.SrcDir, "platform-v0.9.0", "service")
	require.NoError(t, os.MkdirAll(deep, 0o755))
	require.NoError(t, rig.Save(root, testManifest()))

	// From anywhere inside a checkout, `wgo rig show` should know which rig
	// it is in — that is the whole point of omitting the name.
	assert.Equal(t, root, rigRootContaining(rigDir, deep))
	assert.Equal(t, root, rigRootContaining(rigDir, root))

	// A sibling directory under rig.dir that is not a rig.
	other := filepath.Join(rigDir, "scratch")
	require.NoError(t, os.MkdirAll(other, 0o755))
	assert.Empty(t, rigRootContaining(rigDir, other))

	// The walk must not escape rig.dir looking for a manifest above it.
	assert.Empty(t, rigRootContaining(rigDir, t.TempDir()))
}

// fakeCleanClient reports per-path cleanliness for checkRigCheckoutsClean.
type fakeCleanClient struct {
	jj.Client
	dirty map[string][]string
	errs  map[string]error
}

func (f *fakeCleanClient) IsClean(workspacePath string) (bool, []string, error) {
	base := filepath.Base(workspacePath)
	if err := f.errs[base]; err != nil {
		return false, nil, err
	}
	changed := f.dirty[base]
	return len(changed) == 0, changed, nil
}

// materialiseCheckoutDirs creates the on-disk directories the manifest claims,
// since checkRigCheckoutsClean skips checkouts that are not there.
func materialiseCheckoutDirs(t *testing.T, rigRoot string, m *rig.Manifest) {
	t.Helper()
	for _, c := range m.Checkouts {
		require.NoError(t, os.MkdirAll(filepath.Join(rigRoot, rig.SrcDir, c.Dir), 0o755))
	}
}

func TestCheckRigCheckoutsCleanPasses(t *testing.T) {
	root := t.TempDir()
	m := testManifest()
	materialiseCheckoutDirs(t, root, m)

	require.NoError(t, checkRigCheckoutsClean(&fakeCleanClient{}, m, root))
}

func TestCheckRigCheckoutsCleanReportsEveryDirtyCheckout(t *testing.T) {
	root := t.TempDir()
	m := testManifest()
	materialiseCheckoutDirs(t, root, m)

	c := &fakeCleanClient{dirty: map[string][]string{
		"platform-v0.9.0": {"service/main.go"},
		"otdfctl-v0.3.0":  {"cmd/root.go", "go.mod"},
	}}
	err := checkRigCheckoutsClean(c, m, root)
	require.Error(t, err)

	// Both, in one message: fixing them one at a time means re-running rm
	// once per checkout.
	assert.Contains(t, err.Error(), "platform-v0.9.0")
	assert.Contains(t, err.Error(), "otdfctl-v0.3.0")
	assert.Contains(t, err.Error(), "--force")
	// Rig checkouts carry no bookmark, so the edits are not recoverable from
	// anywhere else — the message has to say so.
	assert.Contains(t, err.Error(), "no bookmark")
}

func TestCheckRigCheckoutsCleanTreatsUnreadableAsUnsafe(t *testing.T) {
	root := t.TempDir()
	m := testManifest()
	materialiseCheckoutDirs(t, root, m)

	c := &fakeCleanClient{errs: map[string]error{"platform-v0.9.0": errors.New("stale working copy")}}
	err := checkRigCheckoutsClean(c, m, root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale working copy")
	assert.Contains(t, err.Error(), "--force")
}

func TestCheckRigCheckoutsCleanSkipsAbsentCheckouts(t *testing.T) {
	// A checkout the user already deleted by hand should not block `rig rm` —
	// removing the rig is exactly how they clean up after that.
	root := t.TempDir()
	m := testManifest()

	require.NoError(t, checkRigCheckoutsClean(&fakeCleanClient{}, m, root))
}

func TestReadWorkUse(t *testing.T) {
	dir := t.TempDir()

	// A repo with no go.work is the common case and not an error.
	use, err := readWorkUse(dir, dir)
	require.NoError(t, err)
	assert.Nil(t, use)

	require.NoError(t, os.WriteFile(filepath.Join(dir, rig.GoWorkName),
		[]byte("go 1.24.5\n\nuse (\n\t.\n\t./sdk\n)\n"), 0o644))
	use, err = readWorkUse(dir, dir)
	require.NoError(t, err)
	assert.Equal(t, []string{".", "sdk"}, use)
}

func TestReadWorkUseFindsARepoRootWorkFileFromASubdirModule(t *testing.T) {
	// What a monorepo primary looks like: the artifact is ./otdfctl, but the
	// go.work covering it sits at the repo root and names its siblings
	// relative to *that*. Rebasing is what keeps them lining up with
	// Member.Subdir, which is checkout-relative.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "otdfctl"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, rig.GoWorkName),
		[]byte("go 1.24.5\n\nuse (\n\t./otdfctl\n\t./sdk\n)\n"), 0o644))

	use, err := readWorkUse(dir, filepath.Join(dir, "otdfctl"))
	require.NoError(t, err)
	assert.Equal(t, []string{"otdfctl", "sdk"}, use)
}

func TestReadWorkUseDropsAUseOutsideTheCheckout(t *testing.T) {
	// A go.work in a subdirectory can name a sibling of the checkout itself.
	// No single repository holds that source, so a member for it would render
	// a go.work that does not resolve.
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, rig.GoWorkName),
		[]byte("go 1.24.5\n\nuse (\n\t.\n\t../../elsewhere\n)\n"), 0o644))

	use, err := readWorkUse(dir, sub)
	require.NoError(t, err)
	assert.Equal(t, []string{"sub"}, use)
}

func TestReadWorkUseReportsAnUnparseableWorkFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, rig.GoWorkName), []byte("this is not a go.work\n"), 0o644))

	_, err := readWorkUse(dir, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), rig.GoWorkName)
}

func TestRigPinAndContents(t *testing.T) {
	assert.Equal(t, "service/v0.9.0", rigPin(rig.Checkout{Tag: "service/v0.9.0", Commit: "aaaaaaaa11112222"}))
	assert.Equal(t, "aaaaaaaa1111", rigPin(rig.Checkout{Commit: "aaaaaaaa11112222"}))

	assert.Equal(t, "full", rigContents(rig.Checkout{Full: true}))
	assert.Equal(t, "sparse: lib/fixtures service",
		rigContents(rig.Checkout{Sparse: []string{"lib/fixtures", "service"}}))
}

func TestShortDate(t *testing.T) {
	assert.Equal(t, "2026-08-26", shortDate("2026-08-26T12:00:00Z"))
	assert.Equal(t, "", shortDate(""))
}

func TestRigNewFlagsAreRegistered(t *testing.T) {
	// The spec's documented surface; a flag silently missing turns a scripted
	// invocation into an "unknown flag" failure.
	for _, name := range []string{"from", "from-binary", "module", "org", "full", "dry-run"} {
		assert.NotNil(t, rigNewCmd.Flags().Lookup(name), "wgo rig new --%s", name)
	}
	assert.NotNil(t, rigNewCmd.Flags().ShorthandLookup("m"))
	assert.NotNil(t, rigLsCmd.Flags().Lookup("format"))
	assert.NotNil(t, rigShowCmd.Flags().Lookup("env"))
	assert.NotNil(t, rigRmCmd.Flags().Lookup("force"))
}

func TestRigShowEnvIsEvalable(t *testing.T) {
	// `eval "$(wgo rig show <name> --env)"` must set GOWORK to the rig's own
	// go.work; the checkouts ship their own and Go's upward search finds those
	// first.
	rigRoot := filepath.Join(t.TempDir(), "dsp")
	m := testManifest()

	var env string
	for _, f := range rig.GeneratedFiles(m, rigRoot) {
		if f.Name == rig.EnvShName {
			env = f.Content
		}
	}
	require.NotEmpty(t, env)
	assert.Contains(t, env, "GOWORK='"+filepath.Join(rigRoot, rig.GoWorkName)+"'")
	assert.Contains(t, env, "export GOWORK")
	for _, line := range strings.Split(env, "\n") {
		assert.False(t, strings.HasPrefix(line, "echo "), "env output must be pure exports: %q", line)
	}
}

func TestCheckRigSourceFlags(t *testing.T) {
	t.Cleanup(func() { rigNewFrom, rigNewFromBinary = "", "" })

	rigNewFrom, rigNewFromBinary = "", ""
	err := checkRigSourceFlags()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--from <owner/repo>@<ref>")
	assert.Contains(t, err.Error(), "--from-binary <path>")

	rigNewFrom, rigNewFromBinary = "owner/repo@v1.0.0", "/tmp/app"
	err = checkRigSourceFlags()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "alternatives")

	rigNewFrom, rigNewFromBinary = "owner/repo@v1.0.0", ""
	assert.NoError(t, checkRigSourceFlags())

	rigNewFrom, rigNewFromBinary = "", "/tmp/app"
	assert.NoError(t, checkRigSourceFlags())

	// Whitespace is not a source.
	rigNewFrom, rigNewFromBinary = "   ", "  "
	assert.Error(t, checkRigSourceFlags())
}

func TestBuildInfoModulesNormalisesDirectoryReplacements(t *testing.T) {
	bi := &gomod.BuildInfo{
		Main: gomod.Module{Path: "github.com/acme/app", Version: "v1.2.3", Main: true},
		Deps: []gomod.Module{
			{Path: "github.com/acme/lib", Version: "v0.4.0"},
			// A directory replacement: build info spells the replacement
			// "(devel)" where `go list` leaves the version empty.
			{Path: "github.com/acme/sibling", Version: gomod.DevelVersion,
				Replace: &gomod.Module{Path: "./sibling", Version: gomod.DevelVersion}},
			// A module-to-module replacement keeps its version.
			{Path: "github.com/other/pkg", Version: "v1.0.0",
				Replace: &gomod.Module{Path: "github.com/other/fork", Version: "v1.0.1"}},
		},
	}

	mods := buildInfoModules(bi)
	require.Len(t, mods, 3)

	assert.Nil(t, mods[0].Replace)

	// The shape the planner recognises as "served from another checkout".
	require.NotNil(t, mods[1].Replace)
	assert.Equal(t, "./sibling", mods[1].Replace.Path)
	assert.Empty(t, mods[1].Replace.Version)

	require.NotNil(t, mods[2].Replace)
	assert.Equal(t, "v1.0.1", mods[2].Replace.Version)
}

func TestBuildInfoModulesDoesNotMutateTheBuildInfo(t *testing.T) {
	replaced := &gomod.Module{Path: "./sibling", Version: gomod.DevelVersion}
	bi := &gomod.BuildInfo{Deps: []gomod.Module{
		{Path: "github.com/acme/sibling", Version: gomod.DevelVersion, Replace: replaced},
	}}

	_ = buildInfoModules(bi)

	// The caller still holds the parsed build info; rewriting it in place would
	// make a second read of the same struct disagree with the first.
	assert.Equal(t, gomod.DevelVersion, replaced.Version)
	assert.Same(t, replaced, bi.Deps[0].Replace)
}

func TestRigSourceLabel(t *testing.T) {
	assert.Equal(t, "opentdf/platform@v0.9.0",
		rigSourceLabel(rig.Source{Kind: "repo", Ref: "opentdf/platform@v0.9.0"}))
	// A binary rig has no ref; an empty column would read as missing data.
	assert.Equal(t, "otdfctl",
		rigSourceLabel(rig.Source{Kind: "binary", Binary: "/opt/dist/otdfctl"}))
	assert.Empty(t, rigSourceLabel(rig.Source{Kind: "manual"}))
}

func TestReadPrimaryModule(t *testing.T) {
	dest := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dest, "go.mod"),
		[]byte("module github.com/acme/app/v2\n\ngo 1.24.5\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dest, rig.GoWorkName),
		[]byte("go 1.24.5\n\nuse (\n\t.\n\t./sdk\n)\n"), 0o644))

	mod, use, err := readPrimaryModule(dest, "", "github.com/acme/app/v2")
	require.NoError(t, err)
	assert.Equal(t, "github.com/acme/app/v2", mod.Path)
	assert.Equal(t, "1.24.5", mod.GoVersion)
	assert.True(t, mod.Main)
	assert.Equal(t, []string{".", "sdk"}, use)
}

func TestReadPrimaryModuleReadsASubdirModule(t *testing.T) {
	// A monorepo publishes each of its modules separately, so a binary's main
	// module need not be the repo root. Reading the root would report the
	// checkout as holding no Go module at all.
	dest := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dest, "otdfctl"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dest, "otdfctl", "go.mod"),
		[]byte("module github.com/opentdf/platform/otdfctl\n\ngo 1.25.5\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dest, rig.GoWorkName),
		[]byte("go 1.25.5\n\nuse (\n\t./otdfctl\n\t./sdk\n)\n"), 0o644))

	mod, use, err := readPrimaryModule(dest, "otdfctl", "github.com/opentdf/platform/otdfctl")
	require.NoError(t, err)
	assert.Equal(t, "github.com/opentdf/platform/otdfctl", mod.Path)
	assert.Equal(t, "1.25.5", mod.GoVersion)
	assert.Equal(t, []string{"otdfctl", "sdk"}, use)
}

func TestReadPrimaryModuleToleratesAMovedModulePath(t *testing.T) {
	// A repo that renamed its module between the pinned commit and today. The
	// checkout is still the right source, so this warns rather than failing.
	dest := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dest, "go.mod"),
		[]byte("module github.com/acme/app\n\ngo 1.24.5\n"), 0o644))

	mod, _, err := readPrimaryModule(dest, "", "github.com/acme/app/v2")
	require.NoError(t, err)
	assert.Equal(t, "github.com/acme/app", mod.Path)
}

func TestReadPrimaryModuleRejectsANonModuleCheckout(t *testing.T) {
	dest := t.TempDir()
	_, _, err := readPrimaryModule(dest, "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "may not point at a Go module's root")

	require.NoError(t, os.WriteFile(filepath.Join(dest, "go.mod"), []byte("go 1.24.5\n"), 0o644))
	_, _, err = readPrimaryModule(dest, "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declares no module path")
}

func TestRigNewRegistersFromBinary(t *testing.T) {
	assert.NotNil(t, rigNewCmd.Flags().Lookup("from-binary"))
}

// fakeRigLocator / fakeRigResolver stand in for the clone-and-fetch machinery
// so the binary-source logic can be exercised without a repository.
type fakeRigLocator struct{ clone string }

func (f fakeRigLocator) Locate(owner, repo string) (string, error) {
	if f.clone == "" {
		return "", errors.New("no clone")
	}
	return f.clone, nil
}

type fakeRigResolver struct{ commits map[string]string }

func (f fakeRigResolver) Resolve(_, revset string) (string, error) { return f.commits[revset], nil }

func TestResolveBinaryPrimaryPrefersAReleasedVersion(t *testing.T) {
	p := &rig.Planner{
		Locator: fakeRigLocator{clone: "/mains/acme/app"},
		Resolver: fakeRigResolver{commits: map[string]string{
			`present(tags(exact:"v1.2.3"))`: "1111111111111111111111111111111111111111",
		}},
	}
	bi := &gomod.BuildInfo{Main: gomod.Module{Path: "github.com/acme/app", Version: "v1.2.3", Main: true}}

	c, err := resolveBinaryPrimary(p, "app", "/opt/app", bi)
	require.NoError(t, err)
	assert.Equal(t, "v1.2.3", c.Tag)
	assert.Equal(t, "1111111111111111111111111111111111111111", c.Commit)
}

func TestResolveBinaryPrimaryFallsBackToVCSRevision(t *testing.T) {
	// The CI-built case: no release behind the binary, but the commit it came
	// from is recorded — and that is precisely when a rig is most wanted.
	const rev = "2222222222222222222222222222222222222222"
	p := &rig.Planner{
		Locator:  fakeRigLocator{clone: "/mains/acme/app"},
		Resolver: fakeRigResolver{commits: map[string]string{rev: rev}},
	}
	bi := &gomod.BuildInfo{
		Main:     gomod.Module{Path: "github.com/acme/app", Version: gomod.DevelVersion, Main: true},
		Settings: map[string]string{"vcs.revision": rev, "vcs.modified": "false"},
	}

	c, err := resolveBinaryPrimary(p, "app", "/opt/app", bi)
	require.NoError(t, err)
	assert.Equal(t, rev, c.Commit)
	assert.Equal(t, rev, c.Revset, "a commit resolves directly, not through a tag")
	assert.Empty(t, c.Tag)
}

func TestResolveBinaryPrimaryWithoutAnyPin(t *testing.T) {
	p := &rig.Planner{Locator: fakeRigLocator{clone: "/mains/acme/app"}, Resolver: fakeRigResolver{}}
	bi := &gomod.BuildInfo{
		Main:     gomod.Module{Path: "github.com/acme/app", Version: gomod.DevelVersion, Main: true},
		Settings: map[string]string{},
	}

	_, err := resolveBinaryPrimary(p, "app", "/opt/app", bi)
	require.Error(t, err)
	// The two ways out, both named.
	assert.Contains(t, err.Error(), "-buildvcs=false")
	assert.Contains(t, err.Error(), "--from <owner/repo>@<ref>")
}

func TestBinaryRigGoVersionFloorsOnTheToolchain(t *testing.T) {
	// The primary's own directive is old, but the artifact was linked with a
	// newer toolchain — which is at least as high as every member requires.
	// Build info carries no per-dependency directives to raise it otherwise.
	assert.Equal(t, "1.27.0", gomod.MaxGoVersion("1.21", gomod.ToolchainVersion("go1.27.0")))
	// And the primary still wins when it is the higher of the two.
	assert.Equal(t, "1.27.0", gomod.MaxGoVersion("1.27.0", gomod.ToolchainVersion("go1.24")))
}
