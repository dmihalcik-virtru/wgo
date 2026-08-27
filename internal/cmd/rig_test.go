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
	use, err := readWorkUse(dir)
	require.NoError(t, err)
	assert.Nil(t, use)

	require.NoError(t, os.WriteFile(filepath.Join(dir, rig.GoWorkName),
		[]byte("go 1.24.5\n\nuse (\n\t.\n\t./sdk\n)\n"), 0o644))
	use, err = readWorkUse(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{".", "./sdk"}, use)
}

func TestReadWorkUseReportsAnUnparseableWorkFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, rig.GoWorkName), []byte("this is not a go.work\n"), 0o644))

	_, err := readWorkUse(dir)
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
	for _, name := range []string{"from", "module", "org", "full", "dry-run"} {
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
