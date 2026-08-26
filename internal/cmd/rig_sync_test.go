package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/gomod"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/rig"
)

func TestRigSyncFlagsAreRegistered(t *testing.T) {
	// The spec's documented surface; a flag silently missing turns a scripted
	// invocation into an "unknown flag" failure.
	for _, name := range []string{"prune", "dry-run"} {
		assert.NotNil(t, rigSyncCmd.Flags().Lookup(name), "wgo rig sync --%s", name)
	}
	for _, name := range []string{"module", "dry-run"} {
		assert.NotNil(t, rigAddCmd.Flags().Lookup(name), "wgo rig add --%s", name)
	}
	assert.NotNil(t, rigAddCmd.Flags().ShorthandLookup("m"))

	// `wgo rig add` never removes anything, so there is nothing to prune.
	assert.Nil(t, rigAddCmd.Flags().Lookup("prune"))
}

func TestRecordedSourceFromARepo(t *testing.T) {
	m := testManifest()
	m.Source.OrgPrefixes = []string{"github.com/opentdf"}
	m.Source.Modules = []string{"github.com/opentdf/platform/sdk@v0.10.1"}

	src, err := recordedSource(m)
	require.NoError(t, err)
	assert.Equal(t, "opentdf/platform@v0.9.0", src.from)
	assert.Empty(t, src.binary)
	assert.Equal(t, []string{"github.com/opentdf"}, src.orgs)
	assert.Equal(t, []string{"github.com/opentdf/platform/sdk@v0.10.1"}, src.modules)
}

func TestRecordedSourceFromABinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dsp")
	require.NoError(t, os.WriteFile(bin, []byte("ELF"), 0o755))

	m := testManifest()
	m.Source = rig.Source{Kind: "binary", Binary: bin, OrgPrefixes: []string{"github.com/opentdf"}}

	src, err := recordedSource(m)
	require.NoError(t, err)
	assert.Equal(t, bin, src.binary)
	assert.Empty(t, src.from)
}

// A source that cannot be read back downgrades a sync to a reconciliation
// rather than failing it, so every one of these paths has to be recognisable
// with errors.Is.
func TestRecordedSourceReportsWhatCannotBeReResolved(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "deleted")

	cases := []struct {
		name   string
		source rig.Source
		want   string
	}{
		{
			name:   "no org prefixes",
			source: rig.Source{Kind: "repo", Ref: "opentdf/platform@v0.9.0"},
			want:   "records no org prefixes",
		},
		{
			name:   "repo with no ref",
			source: rig.Source{Kind: "repo", OrgPrefixes: []string{"github.com/opentdf"}},
			want:   "no ref",
		},
		{
			name:   "binary with no path",
			source: rig.Source{Kind: "binary", OrgPrefixes: []string{"github.com/opentdf"}},
			want:   "no path",
		},
		{
			name:   "binary that is gone",
			source: rig.Source{Kind: "binary", Binary: gone, OrgPrefixes: []string{"github.com/opentdf"}},
			want:   "is gone",
		},
		{
			name:   "assembled by hand",
			source: rig.Source{Kind: "manual", OrgPrefixes: []string{"github.com/opentdf"}},
			want:   `assembled by hand (source kind "manual")`,
		},
		{
			name:   "no kind at all",
			source: rig.Source{OrgPrefixes: []string{"github.com/opentdf"}},
			want:   "assembled by hand",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testManifest()
			m.Source = tc.source

			_, err := recordedSource(m)
			require.Error(t, err)
			assert.ErrorIs(t, err, errSourceGone)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestCheckoutOnDisk(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, rig.SrcDir, "platform-v0.9.0"), 0o755))
	// A file where a checkout should be is not a checkout.
	require.NoError(t, os.WriteFile(filepath.Join(root, rig.SrcDir, "notadir"), nil, 0o644))

	on := checkoutOnDisk(root)
	assert.True(t, on("platform-v0.9.0"))
	assert.False(t, on("otdfctl-v0.3.0"))
	assert.False(t, on("notadir"))
}

// The primary's checkout is re-derived by the planner, but the recorded
// directory and workspace names carry a collision suffix that the fresh plan
// need not reproduce. Leaving the derived ones in place would make Reconcile see
// the rig's own primary as a stranger and plan to check it out again.
func TestReusePrimaryAdoptsTheRecordedCheckout(t *testing.T) {
	have := testManifest()
	rigRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(rigRoot, rig.SrcDir, "platform-v0.9.0"), 0o755))

	c := &rig.Checkout{
		Dir: "platform-service-v0.9.0", Workspace: "rig-dsp-aaaaaaaa",
		Repo: "opentdf/platform", MainClone: "/mains/opentdf/platform",
		Commit: "aaaaaaaa11112222", Tag: "service/v0.9.0", Sparse: []string{"service", "sdk"},
	}
	ws := &countingWorkspaces{}
	dest, err := reusePrimary(context.Background(), have, &rig.Materializer{JJ: ws}, rigRoot)(c)
	require.NoError(t, err)

	assert.Zero(t, ws.adds, "the checkout is already on disk, with an editor possibly attached to it")
	assert.Equal(t, filepath.Join(rigRoot, rig.SrcDir, "platform-v0.9.0"), dest)
	assert.Equal(t, "platform-v0.9.0", c.Dir)
	assert.Equal(t, "rig-dsp-aaaaaaaa", c.Workspace)
	assert.Equal(t, []string{"service"}, c.Sparse, "the recorded sparse set, not the re-derived one")
}

// countingWorkspaces records how many workspaces were created, which is what
// separates "adopted the recorded checkout" from "made a new one".
type countingWorkspaces struct {
	adds    int
	forgets []string
}

func (c *countingWorkspaces) WorkspaceAdd(_, dest string, _ jj.WorkspaceAddOpts) error {
	c.adds++
	return os.MkdirAll(dest, 0o755)
}
func (c *countingWorkspaces) WorkspaceForget(repo, name string) error {
	c.forgets = append(c.forgets, repo+" "+name)
	return nil
}
func (c *countingWorkspaces) SparseSet(string, jj.SparseSetOpts) error { return nil }

// Recorded but missing: the pin matches, so this is still the rig's primary, but
// there is nothing on disk to read a build list out of.
func TestReusePrimaryReCreatesAMissingCheckout(t *testing.T) {
	rigRoot := filepath.Join(t.TempDir(), "dsp")
	c := &rig.Checkout{
		Dir: "platform-service-v0.9.0", Workspace: "rig-dsp-aaaaaaaa",
		Repo: "opentdf/platform", MainClone: "/mains/opentdf/platform",
		Commit: "aaaaaaaa11112222", Full: true,
	}

	ws := &countingWorkspaces{}
	dest, err := reusePrimary(context.Background(), testManifest(), &rig.Materializer{JJ: ws}, rigRoot)(c)
	require.NoError(t, err)

	assert.Equal(t, 1, ws.adds)
	// Re-created under the *recorded* names, not the freshly derived ones:
	// Reconcile keys on them, so a second directory for the primary is exactly
	// what a sync must not make.
	assert.Equal(t, filepath.Join(rigRoot, rig.SrcDir, "platform-v0.9.0"), dest)
	assert.Equal(t, "platform-v0.9.0", c.Dir)
	assert.Equal(t, "rig-dsp-aaaaaaaa", c.Workspace)
	// Deleting the directory did not unregister the workspace, and
	// `jj workspace add --name` fails on a duplicate.
	assert.Equal(t, []string{"/mains/opentdf/platform rig-dsp-aaaaaaaa"}, ws.forgets)
}

func TestReusePrimarySkipsADifferentCommit(t *testing.T) {
	rigRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(rigRoot, rig.SrcDir, "platform-v0.9.0"), 0o755))

	c := &rig.Checkout{
		Dir: "platform-v0.10.0", Workspace: "rig-dsp-ffffffff", Repo: "opentdf/platform",
		MainClone: "/mains/opentdf/platform", Commit: "ffffffff00000000", Full: true,
	}
	ws := &countingWorkspaces{}
	_, err := reusePrimary(context.Background(), testManifest(), &rig.Materializer{JJ: ws}, rigRoot)(c)
	require.NoError(t, err)
	assert.Equal(t, 1, ws.adds, "a different commit is a different checkout, whatever the repo")
}

func TestPrintRigDiff(t *testing.T) {
	t.Cleanup(func() { rigSyncPrune = false })
	rigSyncPrune = false

	m := testManifest()
	d := &rig.Diff{
		Add: []rig.Checkout{{Dir: "policy-v1.0.0", Repo: "opentdf/policy", Tag: "v1.0.0"}},
		Restore: []rig.Checkout{
			{Dir: "otdfctl-v0.3.0", Repo: "opentdf/otdfctl", Tag: "v0.3.0"},
		},
		Widen:  []rig.Widening{{Dir: "platform-v0.9.0", Added: []string{"lib/fixtures", "sdk"}}},
		Remove: []rig.Checkout{{Dir: "old-v0.1.0", Repo: "opentdf/old", Commit: "cccccccc3333"}},
	}

	out := captureStdout(t, func() { printRigDiff(m, d, "/rigs/dsp") })

	assert.Contains(t, out, "rig dsp (/rigs/dsp)")
	assert.Contains(t, out, "+ policy-v1.0.0")
	assert.Contains(t, out, "~ otdfctl-v0.3.0")
	assert.Contains(t, out, "(missing, would be restored)")
	assert.Contains(t, out, "> platform-v0.9.0")
	assert.Contains(t, out, "widen to cover lib/fixtures, sdk")
	assert.Contains(t, out, "- old-v0.1.0")
	assert.Contains(t, out, "(obsolete)")
	assert.Contains(t, out, "1 to add, 1 to restore, 1 to widen, 1 obsolete")
	assert.Contains(t, out, "unless you pass --prune")
	assert.Contains(t, out, "rig.toml", "the tombstone is why the workspace stays findable")
}

func TestPrintRigDiffWithNothingToDo(t *testing.T) {
	out := captureStdout(t, func() { printRigDiff(testManifest(), &rig.Diff{}, "/rigs/dsp") })
	assert.Contains(t, out, "no changes: 2 checkouts, 2 modules")
	assert.NotContains(t, out, "--prune")
}

// A dry run that would prune says nothing about keeping the obsolete checkouts,
// because it is not going to keep them.
func TestPrintRigDiffUnderPrune(t *testing.T) {
	t.Cleanup(func() { rigSyncPrune = false })
	rigSyncPrune = true

	d := &rig.Diff{Remove: []rig.Checkout{{Dir: "old-v0.1.0", Repo: "opentdf/old", Commit: "cccccccc3333"}}}
	out := captureStdout(t, func() { printRigDiff(testManifest(), d, "/rigs/dsp") })
	assert.Contains(t, out, "- old-v0.1.0")
	assert.NotContains(t, out, "kept unless")
}

func TestReportObsoleteNamesTheCommandThatResolvesIt(t *testing.T) {
	remove := []rig.Checkout{
		{Dir: "otdfctl-v0.3.0", Repo: "opentdf/otdfctl", Tag: "v0.3.0"},
		{Dir: "old-abc", Repo: "opentdf/old", Commit: "cccccccc33334444"},
	}
	err := captureStderr(t, func() { reportObsolete("dsp", remove) })

	assert.Contains(t, err, "2 checkout(s)")
	// Tagged pins read as their tag; an untagged one falls back to the commit.
	assert.Contains(t, err, "otdfctl-v0.3.0 (opentdf/otdfctl @ v0.3.0)")
	assert.Contains(t, err, "old-abc (opentdf/old @ cccccccc3333)")
	assert.Contains(t, err, "wgo rig sync dsp --prune")
	assert.Contains(t, err, "go.work")
}

// Naming the module is the request, so an out-of-org pin is checked out with a
// warning rather than being silently dropped by the rig's own filter.
func TestWarnOutOfOrg(t *testing.T) {
	m := testManifest()
	m.Source.OrgPrefixes = []string{"github.com/opentdf"}

	out := captureStderr(t, func() {
		warnOutOfOrg(m, []gomod.Module{
			{Path: "google.golang.org/grpc", Version: "v1.65.0"},
			{Path: "github.com/opentdf/platform/sdk", Version: "v0.10.1"},
		})
	})
	assert.Contains(t, out, "google.golang.org/grpc is outside dsp's org prefixes")
	assert.NotContains(t, out, "github.com/opentdf/platform/sdk")
}
