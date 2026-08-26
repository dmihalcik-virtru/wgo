package rig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/gomod"
)

// resolvedPlan is what a re-resolution of twoCheckoutManifest's source would
// produce on its own: the same two pins, but with directory and workspace names
// derived afresh rather than read back from the manifest.
func resolvedPlan() *Manifest {
	m := twoCheckoutManifest()
	m.Created = "2026-08-26T00:00:00Z"
	m.Checkouts[0].Dir = "platform-service-v0.9.0"
	m.Checkouts[0].Workspace = "rig-dsp-aaaaaaaa"
	m.Checkouts[1].Dir = "otdfctl-v0.3.0-bbbbbbbb"
	m.Members[0].Checkout = "platform-service-v0.9.0"
	m.Members[1].Checkout = "platform-service-v0.9.0"
	m.Members[2].Checkout = "otdfctl-v0.3.0-bbbbbbbb"
	return m
}

// A checkout is matched on what it holds, not on where the plan would have put
// it. The recorded directory is where the user's editor, debugger and shell are
// pointed; a suffix that shifted because some unrelated module joined the rig is
// no reason to move it.
func TestReconcileKeepsTheRecordedLocations(t *testing.T) {
	have := twoCheckoutManifest()
	want := resolvedPlan()

	merged, diff, err := Reconcile(have, want, nil)
	require.NoError(t, err)
	assert.True(t, diff.Empty(), "same pins, so nothing to do: %s", diff.Summary())

	assert.Equal(t, []string{"platform-v0.9.0", "otdfctl-v0.3.0"},
		[]string{merged.Checkouts[0].Dir, merged.Checkouts[1].Dir})

	// Members are remapped onto the kept directories, or go.work would name
	// paths that do not exist.
	for _, mem := range merged.Members {
		assert.NotNil(t, merged.CheckoutByDir(mem.Checkout), "member %s", mem.Path)
	}
	assert.Equal(t, "platform-v0.9.0", merged.Members[0].Checkout)
	assert.Equal(t, "otdfctl-v0.3.0", merged.Members[2].Checkout)
}

// The rig was created once. A sync re-resolves its pins; it does not make a new
// rig, and the freeze state is the user's rather than the plan's.
func TestReconcileKeepsRigIdentity(t *testing.T) {
	have := twoCheckoutManifest()
	have.Created = "2026-01-01T00:00:00Z"
	have.Frozen = []string{"google.golang.org/grpc"}
	have.Name = "dsp"

	want := resolvedPlan()
	want.Name = "something-else"
	want.Frozen = nil

	merged, _, err := Reconcile(have, want, nil)
	require.NoError(t, err)
	assert.Equal(t, "dsp", merged.Name)
	assert.Equal(t, "2026-01-01T00:00:00Z", merged.Created)
	assert.Equal(t, []string{"google.golang.org/grpc"}, merged.Frozen)
}

// The plan decides what the rig holds, so a re-read baseline and a re-derived
// go directive both win.
func TestReconcileTakesTheContentFromThePlan(t *testing.T) {
	have := twoCheckoutManifest()
	have.GoVersion = "1.24.5"
	have.Baseline = map[string]string{"google.golang.org/grpc": "v1.65.0"}

	want := resolvedPlan()
	want.GoVersion = "1.25.0"
	want.Baseline = map[string]string{"google.golang.org/grpc": "v1.66.0"}

	merged, _, err := Reconcile(have, want, nil)
	require.NoError(t, err)
	assert.Equal(t, "1.25.0", merged.GoVersion)
	assert.Equal(t, map[string]string{"google.golang.org/grpc": "v1.66.0"}, merged.Baseline)
}

func TestReconcileClassifiesAddAndObsolete(t *testing.T) {
	have := twoCheckoutManifest()
	want := resolvedPlan()
	// The re-resolved artifact dropped otdfctl and picked up policy.
	want.Checkouts[1] = Checkout{
		Dir: "policy-v1.0.0", Workspace: "rig-dsp-cccccccc",
		Repo: "opentdf/policy", MainClone: "/mains/opentdf/policy",
		Revset: "tags/v1.0.0", Commit: "cccccccc3333", Tag: "v1.0.0", Full: true,
	}
	want.Members[2] = Member{Path: "github.com/opentdf/policy", Version: "v1.0.0", Checkout: "policy-v1.0.0"}

	merged, diff, err := Reconcile(have, want, nil)
	require.NoError(t, err)

	require.Len(t, diff.Add, 1)
	assert.Equal(t, "policy-v1.0.0", diff.Add[0].Dir)
	require.Len(t, diff.Remove, 1)
	assert.Equal(t, "otdfctl-v0.3.0", diff.Remove[0].Dir)
	assert.Empty(t, diff.Restore)
	assert.Equal(t, "1 to add, 1 obsolete", diff.Summary())

	// The obsolete checkout leaves the manifest even though nothing removes it
	// from disk yet: it is out of go.work, so nothing builds against it.
	assert.Nil(t, merged.CheckoutByDir("otdfctl-v0.3.0"))
	assert.Len(t, merged.Checkouts, 2)
}

// A recorded checkout whose directory has gone is restored rather than added,
// because its workspace may still be registered in the main clone.
func TestReconcileRestoresAMissingCheckout(t *testing.T) {
	have := twoCheckoutManifest()
	gone := func(dir string) bool { return dir != "otdfctl-v0.3.0" }

	_, diff, err := Reconcile(have, resolvedPlan(), gone)
	require.NoError(t, err)

	require.Len(t, diff.Restore, 1)
	assert.Equal(t, "otdfctl-v0.3.0", diff.Restore[0].Dir)
	assert.Equal(t, "rig-dsp-bbbbbbbb", diff.Restore[0].Workspace, "the workspace to forget before re-adding")
	assert.Empty(t, diff.Add)
	assert.Equal(t, "1 to restore", diff.Summary())
}

func TestReconcileWidensASparseSet(t *testing.T) {
	have := twoCheckoutManifest()
	want := resolvedPlan()
	want.Checkouts[0].Sparse = []string{"protocol/go", "service"}

	merged, diff, err := Reconcile(have, want, nil)
	require.NoError(t, err)

	require.Len(t, diff.Widen, 1)
	assert.Equal(t, "platform-v0.9.0", diff.Widen[0].Dir)
	assert.Equal(t, []string{"protocol/go"}, diff.Widen[0].Added)
	// The whole set, not the delta: `jj sparse set --clear --add` replaces it.
	assert.Equal(t, []string{"lib/fixtures", "protocol/go", "service"}, diff.Widen[0].Set)
	assert.Equal(t, []string{"lib/fixtures", "protocol/go", "service"}, merged.Checkouts[0].Sparse)
}

// Narrowing deletes files from a working copy the user may have edited, and the
// cost of keeping a directory the rig no longer needs is disk, not correctness.
func TestReconcileNeverNarrowsASparseSet(t *testing.T) {
	have := twoCheckoutManifest()
	want := resolvedPlan()
	want.Checkouts[0].Sparse = []string{"service"} // lib/fixtures no longer wanted

	merged, diff, err := Reconcile(have, want, nil)
	require.NoError(t, err)
	assert.Empty(t, diff.Widen)
	assert.Equal(t, []string{"lib/fixtures", "service"}, merged.Checkouts[0].Sparse)
}

func TestReconcileFullBeatsSparse(t *testing.T) {
	t.Run("sparse plan cannot un-widen a full checkout", func(t *testing.T) {
		have := twoCheckoutManifest()
		want := resolvedPlan()
		want.Checkouts[1].Full = false
		want.Checkouts[1].Sparse = []string{"cmd"}

		merged, diff, err := Reconcile(have, want, nil)
		require.NoError(t, err)
		assert.True(t, merged.Checkouts[1].Full)
		assert.Empty(t, merged.Checkouts[1].Sparse)
		assert.Empty(t, diff.Widen)
	})

	t.Run("a sparse checkout going full widens to the whole tree", func(t *testing.T) {
		have := twoCheckoutManifest()
		want := resolvedPlan()
		want.Checkouts[0].Full = true
		want.Checkouts[0].Sparse = nil

		merged, diff, err := Reconcile(have, want, nil)
		require.NoError(t, err)
		assert.True(t, merged.Checkouts[0].Full)
		assert.Empty(t, merged.Checkouts[0].Sparse)
		require.Len(t, diff.Widen, 1)
		assert.Equal(t, []string{"."}, diff.Widen[0].Set, `"." is jj's spelling of a full working copy`)
	})
}

// ApplyDiff appends the widening pass's skips to the merged manifest, so it must
// not be sharing a slice with the plan the caller still holds.
func TestReconcileCopiesSkips(t *testing.T) {
	want := resolvedPlan()
	want.Skipped = []Skip{{Path: "google.golang.org/grpc", Version: "v1.65.0", Kind: SkipOutOfOrg}}

	merged, _, err := Reconcile(twoCheckoutManifest(), want, nil)
	require.NoError(t, err)

	merged.Skipped = append(merged.Skipped, Skip{Path: "other", Kind: SkipUnreachable})
	assert.Len(t, want.Skipped, 1, "the plan's skip list is untouched")
}

func TestReconcileRejectsAnInvalidResult(t *testing.T) {
	want := resolvedPlan()
	want.Members = nil // leaves both checkouts serving nothing

	_, _, err := Reconcile(twoCheckoutManifest(), want, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no members")
}

func TestReconcileNeedsBothSides(t *testing.T) {
	_, _, err := Reconcile(nil, resolvedPlan(), nil)
	require.Error(t, err)
	_, _, err = Reconcile(twoCheckoutManifest(), nil, nil)
	require.Error(t, err)
}

func TestDiffSummary(t *testing.T) {
	assert.Equal(t, "no changes", (&Diff{}).Summary())
	assert.Equal(t, "no changes", (*Diff)(nil).Summary())
	d := &Diff{
		Add:     []Checkout{{Dir: "a"}, {Dir: "b"}},
		Restore: []Checkout{{Dir: "c"}},
		Widen:   []Widening{{Dir: "d"}},
		Remove:  []Checkout{{Dir: "e"}},
	}
	assert.Equal(t, "2 to add, 1 to restore, 1 to widen, 1 obsolete", d.Summary())
}

func TestDedupeSkips(t *testing.T) {
	dup := Skip{Path: "github.com/opentdf/platform/service", Kind: SkipEscapedReplace, Detail: "../../elsewhere"}
	out := dedupeSkips([]Skip{
		dup,
		{Path: "google.golang.org/grpc", Version: "v1.65.0", Kind: SkipOutOfOrg},
		dup,
	})
	require.Len(t, out, 2)
	assert.Equal(t, dup, out[0], "sorted by path, and the repeat is gone")
}

// materializedRig builds a rig on disk and hands back its manifest as loaded,
// which is what a sync starts from.
func materializedRig(t *testing.T, content map[string]map[string]string) (string, *Manifest) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "dsp")
	mz := &Materializer{JJ: &fakeWorkspaces{content: content}}
	require.NoError(t, mz.Materialize(twoCheckoutManifest(), root))
	have, err := Load(root)
	require.NoError(t, err)
	return root, have
}

// onDiskUnder is the same test as the sync command's, kept local so the rig
// package does not depend on the command layer.
func onDiskUnder(root string) func(string) bool {
	return func(dir string) bool {
		info, err := os.Stat(filepath.Join(root, SrcDir, dir))
		return err == nil && info.IsDir()
	}
}

// policyPlan is a re-resolution that additionally pulls in opentdf/policy.
func policyPlan(have *Manifest) *Manifest {
	want := *have
	want.Checkouts = append(append([]Checkout(nil), have.Checkouts...), Checkout{
		Dir: "policy-v1.0.0", Workspace: "rig-dsp-cccccccc",
		Repo: "opentdf/policy", MainClone: "/mains/opentdf/policy",
		Revset: "tags/v1.0.0", Commit: "cccccccc3333", Tag: "v1.0.0", Full: true,
	})
	want.Members = append(append([]Member(nil), have.Members...),
		Member{Path: "github.com/opentdf/policy", Version: "v1.0.0", Checkout: "policy-v1.0.0"})
	return &want
}

func policyContent() map[string]string {
	return map[string]string{"go.mod": "module github.com/opentdf/policy\n\ngo 1.24.5\n"}
}

func TestApplyDiffAddsOnlyTheNewCheckout(t *testing.T) {
	root, have := materializedRig(t, twoCheckoutContent())

	merged, diff, err := Reconcile(have, policyPlan(have), onDiskUnder(root))
	require.NoError(t, err)
	require.Len(t, diff.Add, 1)

	content := twoCheckoutContent()
	content["rig-dsp-cccccccc"] = policyContent()
	ws := &fakeWorkspaces{content: content}
	mz := &Materializer{JJ: ws}
	require.NoError(t, mz.ApplyDiff(merged, diff, root, false))

	require.Len(t, ws.adds, 1, "the two checkouts already on disk are left alone")
	assert.Equal(t, "rig-dsp-cccccccc", ws.adds[0].opts.Name)
	assert.Empty(t, ws.forgets)

	loaded, err := Load(root)
	require.NoError(t, err)
	assert.Len(t, loaded.Checkouts, 3)

	work, err := os.ReadFile(filepath.Join(root, GoWorkName))
	require.NoError(t, err)
	assert.Contains(t, string(work), "./src/policy-v1.0.0")
}

// go.work and the helper files are generated from the manifest, and repairing a
// clobbered one is half of what a sync is for.
func TestApplyDiffRegeneratesFilesWithNothingToChange(t *testing.T) {
	root, have := materializedRig(t, twoCheckoutContent())
	require.NoError(t, os.Remove(filepath.Join(root, GoWorkName)))
	require.NoError(t, os.Remove(filepath.Join(root, EnvShName)))

	merged, diff, err := Reconcile(have, have, onDiskUnder(root))
	require.NoError(t, err)
	require.True(t, diff.Empty())

	ws := &fakeWorkspaces{content: twoCheckoutContent()}
	require.NoError(t, (&Materializer{JJ: ws}).ApplyDiff(merged, diff, root, false))

	assert.FileExists(t, filepath.Join(root, GoWorkName))
	assert.FileExists(t, filepath.Join(root, EnvShName))
	assert.Empty(t, ws.adds)
}

// A restored checkout's directory is gone but its workspace may still be
// registered in the main clone, and `jj workspace add --name` fails on a
// duplicate.
func TestApplyDiffForgetsBeforeRestoring(t *testing.T) {
	root, have := materializedRig(t, twoCheckoutContent())
	require.NoError(t, os.RemoveAll(filepath.Join(root, SrcDir, "otdfctl-v0.3.0")))

	merged, diff, err := Reconcile(have, have, onDiskUnder(root))
	require.NoError(t, err)
	require.Len(t, diff.Restore, 1)

	ws := &fakeWorkspaces{content: twoCheckoutContent()}
	require.NoError(t, (&Materializer{JJ: ws}).ApplyDiff(merged, diff, root, false))

	require.Len(t, ws.forgets, 1)
	assert.Equal(t, forgetCall{repo: "/mains/opentdf/otdfctl", name: "rig-dsp-bbbbbbbb"}, ws.forgets[0])
	require.Len(t, ws.adds, 1)
	assert.Equal(t, "rig-dsp-bbbbbbbb", ws.adds[0].opts.Name)
	assert.DirExists(t, filepath.Join(root, SrcDir, "otdfctl-v0.3.0"))
}

// A stale registration is the expected case, so a forget that fails must not
// stop the restore: the add that follows reports the problem far more precisely.
func TestApplyDiffRestoresDespiteAFailedForget(t *testing.T) {
	root, have := materializedRig(t, twoCheckoutContent())
	require.NoError(t, os.RemoveAll(filepath.Join(root, SrcDir, "otdfctl-v0.3.0")))

	merged, diff, err := Reconcile(have, have, onDiskUnder(root))
	require.NoError(t, err)

	ws := &fakeWorkspaces{
		content:   twoCheckoutContent(),
		forgetErr: map[string]error{"rig-dsp-bbbbbbbb": errors.New("no such workspace")},
	}
	var logged []string
	mz := &Materializer{JJ: ws, Logf: func(f string, a ...any) { logged = append(logged, f) }}
	require.NoError(t, mz.ApplyDiff(merged, diff, root, false))

	require.Len(t, ws.adds, 1)
	assert.Contains(t, strings.Join(logged, "\n"), "was not registered in")
}

func TestApplyDiffWidensAnExistingCheckout(t *testing.T) {
	root, have := materializedRig(t, twoCheckoutContent())

	want := *have
	want.Checkouts = append([]Checkout(nil), have.Checkouts...)
	want.Checkouts[0].Sparse = []string{"lib/fixtures", "protocol/go", "service"}

	merged, diff, err := Reconcile(have, &want, onDiskUnder(root))
	require.NoError(t, err)
	require.Len(t, diff.Widen, 1)

	ws := &fakeWorkspaces{content: twoCheckoutContent()}
	require.NoError(t, (&Materializer{JJ: ws}).ApplyDiff(merged, diff, root, false))

	sparse := ws.sparseFor("platform-v0.9.0")
	require.Len(t, sparse, 1)
	assert.Equal(t, []string{"lib/fixtures", "protocol/go", "service"}, sparse[0].Add)
	assert.True(t, sparse[0].Clear)

	loaded, err := Load(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"lib/fixtures", "protocol/go", "service"}, loaded.Checkouts[0].Sparse)
}

// Removing a working copy is the one irreversible thing a sync can do, and a rig
// checkout is somewhere a user may have uncommitted debugging edits.
func TestApplyDiffPrunesOnlyWhenAsked(t *testing.T) {
	drop := func(have *Manifest) *Manifest {
		want := *have
		want.Checkouts = []Checkout{have.Checkouts[0]}
		want.Members = []Member{have.Members[0], have.Members[1]}
		return &want
	}

	t.Run("kept by default", func(t *testing.T) {
		root, have := materializedRig(t, twoCheckoutContent())
		merged, diff, err := Reconcile(have, drop(have), onDiskUnder(root))
		require.NoError(t, err)
		require.Len(t, diff.Remove, 1)

		ws := &fakeWorkspaces{content: twoCheckoutContent()}
		require.NoError(t, (&Materializer{JJ: ws}).ApplyDiff(merged, diff, root, false))

		assert.Empty(t, ws.forgets)
		assert.DirExists(t, filepath.Join(root, SrcDir, "otdfctl-v0.3.0"))
		// Out of the manifest and out of go.work all the same, so nothing builds
		// against the directory that was left behind.
		loaded, err := Load(root)
		require.NoError(t, err)
		assert.Len(t, loaded.Checkouts, 1)
		work, err := os.ReadFile(filepath.Join(root, GoWorkName))
		require.NoError(t, err)
		assert.NotContains(t, string(work), "otdfctl")
	})

	t.Run("removed with --prune", func(t *testing.T) {
		root, have := materializedRig(t, twoCheckoutContent())
		merged, diff, err := Reconcile(have, drop(have), onDiskUnder(root))
		require.NoError(t, err)

		ws := &fakeWorkspaces{content: twoCheckoutContent()}
		require.NoError(t, (&Materializer{JJ: ws}).ApplyDiff(merged, diff, root, true))

		require.Len(t, ws.forgets, 1)
		assert.Equal(t, "rig-dsp-bbbbbbbb", ws.forgets[0].name)
		assert.NoDirExists(t, filepath.Join(root, SrcDir, "otdfctl-v0.3.0"))
	})
}

// The manifest that no longer names the pruned checkouts is saved first, so a
// failed teardown leaves recoverable orphans rather than a failed sync.
func TestApplyDiffSurvivesAFailedPrune(t *testing.T) {
	root, have := materializedRig(t, twoCheckoutContent())
	want := *have
	want.Checkouts = []Checkout{have.Checkouts[0]}
	want.Members = []Member{have.Members[0], have.Members[1]}

	merged, diff, err := Reconcile(have, &want, onDiskUnder(root))
	require.NoError(t, err)

	var logged []string
	ws := &fakeWorkspaces{
		content:   twoCheckoutContent(),
		forgetErr: map[string]error{"rig-dsp-bbbbbbbb": errors.New("locked")},
	}
	mz := &Materializer{JJ: ws, Logf: func(f string, a ...any) { logged = append(logged, f) }}
	require.NoError(t, mz.ApplyDiff(merged, diff, root, true))

	joined := strings.Join(logged, "\n")
	assert.Contains(t, joined, "jj -R %s workspace forget %s", "the user needs the exact command")
	loaded, err := Load(root)
	require.NoError(t, err)
	assert.Len(t, loaded.Checkouts, 1)
}

// Every sync re-runs the widening pass, so a manifest that already records an
// escaped replace must not grow a second copy of the same skip.
func TestApplyDiffDoesNotAccumulateSkips(t *testing.T) {
	content := twoCheckoutContent()
	content["rig-dsp-aaaaaaaa"]["service/go.mod"] =
		"module github.com/opentdf/platform/service\n\ngo 1.24.5\n\nreplace example.com/other => ../../../elsewhere\n"

	root, have := materializedRig(t, content)
	require.Len(t, have.Skipped, 1)

	for range 2 {
		merged, diff, err := Reconcile(have, have, onDiskUnder(root))
		require.NoError(t, err)
		require.NoError(t, (&Materializer{JJ: &fakeWorkspaces{content: content}}).ApplyDiff(merged, diff, root, false))

		have, err = Load(root)
		require.NoError(t, err)
		require.Len(t, have.Skipped, 1, "the same escaped replace, recorded once")
	}
}

// A sync that fails partway takes its own new checkouts back down and leaves the
// rig exactly as it was.
func TestApplyDiffRollsBackWhatItCreated(t *testing.T) {
	root, have := materializedRig(t, twoCheckoutContent())

	content := twoCheckoutContent()
	content["rig-dsp-cccccccc"] = policyContent()
	ws := &fakeWorkspaces{content: content}
	mz := &Materializer{JJ: ws, Validate: &stubValidator{err: errors.New("unknown directive")}}

	merged, diff, err := Reconcile(have, policyPlan(have), onDiskUnder(root))
	require.NoError(t, err)

	require.Error(t, mz.ApplyDiff(merged, diff, root, false))

	require.Len(t, ws.forgets, 1, "only the checkout this run added is undone")
	assert.Equal(t, "rig-dsp-cccccccc", ws.forgets[0].name)
	assert.NoDirExists(t, filepath.Join(root, SrcDir, "policy-v1.0.0"))
	assert.DirExists(t, filepath.Join(root, SrcDir, "platform-v0.9.0"), "the rig that was already here survives")

	loaded, err := Load(root)
	require.NoError(t, err)
	assert.Len(t, loaded.Checkouts, 2, "the manifest is written last, so it still describes the old rig")
}

// The primary is re-materialised before the build list can be read, so its
// checkout is already on disk and on the rollback list by the time ApplyDiff
// sees it in the diff.
func TestApplyDiffSkipsCheckoutsThisRunAlreadyMade(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dsp")
	m := twoCheckoutManifest()

	ws := &fakeWorkspaces{content: twoCheckoutContent()}
	mz := &Materializer{JJ: ws}
	_, err := mz.Checkout(root, &m.Checkouts[0])
	require.NoError(t, err)
	require.Len(t, ws.adds, 1)

	require.NoError(t, mz.ApplyDiff(m, &Diff{Add: m.Checkouts}, root, false))
	require.Len(t, ws.adds, 2, "the pre-warmed checkout is not added twice")
	assert.Equal(t, "rig-dsp-bbbbbbbb", ws.adds[1].opts.Name)
}

func TestApplyDiffRejectsARelativeRoot(t *testing.T) {
	mz := &Materializer{JJ: &fakeWorkspaces{content: twoCheckoutContent()}}
	err := mz.ApplyDiff(twoCheckoutManifest(), &Diff{}, "rigs/dsp", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be absolute")
}

// addPlanner resolves the pins `wgo rig add` is given in these tests. sdk/v0.9.0
// and the platform root both land on the commit twoCheckoutManifest already
// holds, so they exercise joining an existing checkout.
func addPlanner() *Planner {
	return &Planner{
		Locator: &fakeLocator{clones: map[string]string{
			"opentdf/platform": platformClone,
			"opentdf/otdfctl":  otdfctlClone,
		}},
		Resolver: &fakeResolver{commits: map[string]string{
			`present(tags(exact:"lib/flattening/v0.1.3"))`: "dddddddd4444",
			`present(tags(exact:"sdk/v0.9.0"))`:            "aaaaaaaa1111",
			`present(tags(exact:"v0.9.0"))`:                "aaaaaaaa1111",
			`present(tags(exact:"v0.4.0"))`:                "eeeeeeee5555",
		}},
	}
}

func TestAddModulesCreatesACheckout(t *testing.T) {
	m := twoCheckoutManifest()
	m.Source.Modules = []string{"github.com/opentdf/platform/sdk@v0.9.0"}

	updated, diff, err := addPlanner().AddModules(m, []gomod.Module{
		{Path: "github.com/opentdf/platform/lib/flattening", Version: "v0.1.3", GoVersion: "1.25.0"},
	})
	require.NoError(t, err)

	require.Len(t, updated.Checkouts, 3)
	c := updated.Checkouts[2]
	assert.Equal(t, "platform-lib-flattening-v0.1.3", c.Dir)
	assert.Equal(t, "rig-dsp-dddddddd", c.Workspace)
	assert.Equal(t, platformClone, c.MainClone)
	assert.Equal(t, []string{"lib/flattening"}, c.Sparse, "a sparse rig gets a sparse checkout")

	require.Len(t, diff.Add, 1)
	assert.Equal(t, "platform-lib-flattening-v0.1.3", diff.Add[0].Dir)
	assert.Empty(t, diff.Widen)

	mem := updated.Members[len(updated.Members)-1]
	assert.Equal(t, "github.com/opentdf/platform/lib/flattening", mem.Path)
	assert.Equal(t, "lib/flattening", mem.Subdir)

	// The go directive cannot be older than any member.
	assert.Equal(t, "1.25.0", updated.GoVersion)

	// Recorded on the source, or the next sync would plan a rig without it and
	// report the checkout it brought as obsolete.
	assert.Equal(t, []string{
		"github.com/opentdf/platform/sdk@v0.9.0",
		"github.com/opentdf/platform/lib/flattening@v0.1.3",
	}, updated.Source.Modules)
	assert.Len(t, m.Source.Modules, 1, "the manifest passed in is not mutated")
}

// Modules released from one commit of a monorepo share a working copy rather
// than getting byte-identical copies of it.
func TestAddModulesJoinsAnExistingCheckout(t *testing.T) {
	updated, diff, err := addPlanner().AddModules(twoCheckoutManifest(), []gomod.Module{
		{Path: "github.com/opentdf/platform/sdk", Version: "v0.9.0"},
	})
	require.NoError(t, err)

	assert.Len(t, updated.Checkouts, 2, "no new checkout: same repo, same commit")
	assert.Equal(t, []string{"lib/fixtures", "sdk", "service"}, updated.Checkouts[0].Sparse)

	assert.Empty(t, diff.Add)
	require.Len(t, diff.Widen, 1)
	assert.Equal(t, "platform-v0.9.0", diff.Widen[0].Dir)
	assert.Equal(t, []string{"sdk"}, diff.Widen[0].Added)

	// Members read in checkout order, so the new one sorts in beside its
	// siblings rather than being appended at the end.
	var uses []string
	for _, mem := range updated.Members {
		uses = append(uses, mem.UseDir())
	}
	assert.Equal(t, []string{
		"./src/platform-v0.9.0/lib/fixtures",
		"./src/platform-v0.9.0/sdk",
		"./src/platform-v0.9.0/service",
		"./src/otdfctl-v0.3.0",
	}, uses)
}

// A module at the repository root needs the whole tree, so a sparse checkout
// that acquires one has to go full.
func TestAddModulesAtTheRepoRootGoesFull(t *testing.T) {
	updated, diff, err := addPlanner().AddModules(twoCheckoutManifest(), []gomod.Module{
		{Path: "github.com/opentdf/platform", Version: "v0.9.0"},
	})
	require.NoError(t, err)

	assert.Len(t, updated.Checkouts, 2)
	assert.True(t, updated.Checkouts[0].Full)
	assert.Empty(t, updated.Checkouts[0].Sparse)
	require.Len(t, diff.Widen, 1)
	assert.Equal(t, []string{"."}, diff.Widen[0].Set)
}

// A rig is deliberately frozen; moving a pin means a different commit, a
// different checkout, and a build list that no longer matches the artifact.
func TestAddModulesRefusesToMoveAPin(t *testing.T) {
	_, _, err := addPlanner().AddModules(twoCheckoutManifest(), []gomod.Module{
		{Path: "github.com/opentdf/otdfctl", Version: "v0.4.0"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already in dsp at v0.3.0")
	assert.Contains(t, err.Error(), "does not move a pin")
}

func TestAddModulesIsANoOpForAPinAlreadyHeld(t *testing.T) {
	m := twoCheckoutManifest()
	updated, diff, err := addPlanner().AddModules(m, []gomod.Module{
		{Path: "github.com/opentdf/otdfctl", Version: "v0.3.0"},
	})
	require.NoError(t, err)
	assert.True(t, diff.Empty())
	assert.Same(t, m, updated)
}

func TestAddModulesRejectsAnUnpinnableVersion(t *testing.T) {
	for _, version := range []string{gomod.DevelVersion, ""} {
		_, _, err := addPlanner().AddModules(twoCheckoutManifest(), []gomod.Module{
			{Path: "github.com/opentdf/platform/lib/flattening", Version: version},
		})
		require.Error(t, err, "version %q", version)
		assert.Contains(t, err.Error(), "names no release")
	}
}

func TestAddModulesNeedsAModulePath(t *testing.T) {
	_, _, err := addPlanner().AddModules(twoCheckoutManifest(), []gomod.Module{{Version: "v1.0.0"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs a module path")
}

// A skip is the right answer for a module that merely turned up in a build list.
// It is the wrong answer for one the user named by hand.
func TestAddModulesFailsOnAModuleItCannotCheckOut(t *testing.T) {
	p := addPlanner()
	p.Locator = &fakeLocator{clones: map[string]string{}, err: map[string]error{
		"opentdf/policy": errors.New("no clone and no remote"),
	}}

	_, _, err := p.AddModules(twoCheckoutManifest(), []gomod.Module{
		{Path: "github.com/opentdf/policy", Version: "v1.0.0"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot check out github.com/opentdf/policy@v1.0.0")
	assert.Contains(t, err.Error(), "unreachable")
}

func TestAddModulesNeedsARig(t *testing.T) {
	_, _, err := addPlanner().AddModules(nil, []gomod.Module{{Path: "x", Version: "v1.0.0"}})
	require.Error(t, err)
}

// The rig on disk has to end up matching what AddModules planned, including the
// sparse set the joined checkout grew.
func TestAddModulesAppliesEndToEnd(t *testing.T) {
	root, have := materializedRig(t, twoCheckoutContent())

	updated, diff, err := addPlanner().AddModules(have, []gomod.Module{
		{Path: "github.com/opentdf/platform/sdk", Version: "v0.9.0"},
	})
	require.NoError(t, err)

	content := twoCheckoutContent()
	content["rig-dsp-aaaaaaaa"]["sdk/go.mod"] = "module github.com/opentdf/platform/sdk\n\ngo 1.24.5\n"
	// The widening pass reads the go.mod through the checkout that is already on
	// disk, so the new module's file has to be put there too.
	require.NoError(t, os.MkdirAll(filepath.Join(root, SrcDir, "platform-v0.9.0", "sdk"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, SrcDir, "platform-v0.9.0", "sdk", "go.mod"),
		[]byte(content["rig-dsp-aaaaaaaa"]["sdk/go.mod"]), 0o644))

	ws := &fakeWorkspaces{content: content}
	require.NoError(t, (&Materializer{JJ: ws}).ApplyDiff(updated, diff, root, false))

	assert.Empty(t, ws.adds)
	sparse := ws.sparseFor("platform-v0.9.0")
	require.Len(t, sparse, 1)
	assert.Equal(t, []string{"lib/fixtures", "sdk", "service"}, sparse[0].Add)

	work, err := os.ReadFile(filepath.Join(root, GoWorkName))
	require.NoError(t, err)
	assert.Contains(t, string(work), "./src/platform-v0.9.0/sdk")
}
