package rig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/jj"
)

type addCall struct {
	repo string
	dest string
	opts jj.WorkspaceAddOpts
}

type sparseCall struct {
	path string
	opts jj.SparseSetOpts
}

type forgetCall struct {
	repo string
	name string
}

// fakeWorkspaces stands in for jj. WorkspaceAdd materialises the files listed
// in content so verifyMembers and widen have real go.mod files to read, which
// is the only part of the checkout this package looks at.
type fakeWorkspaces struct {
	// content maps a workspace name to the workspace-relative files it should
	// appear to contain.
	content map[string]map[string]string

	addErr    map[string]error
	forgetErr map[string]error
	sparseErr map[string]error

	adds    []addCall
	sparses []sparseCall
	forgets []forgetCall
}

func (f *fakeWorkspaces) WorkspaceAdd(repo, dest string, opts jj.WorkspaceAddOpts) error {
	f.adds = append(f.adds, addCall{repo: repo, dest: dest, opts: opts})
	if err := f.addErr[opts.Name]; err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for rel, body := range f.content[opts.Name] {
		p := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeWorkspaces) WorkspaceForget(repo, name string) error {
	f.forgets = append(f.forgets, forgetCall{repo: repo, name: name})
	return f.forgetErr[name]
}

func (f *fakeWorkspaces) SparseSet(workspacePath string, opts jj.SparseSetOpts) error {
	f.sparses = append(f.sparses, sparseCall{path: workspacePath, opts: opts})
	return f.sparseErr[filepath.Base(workspacePath)]
}

// dirtyWorkspaces is a fakeWorkspaces that can also answer for a working copy,
// which is what a prune consults before it removes one.
type dirtyWorkspaces struct {
	fakeWorkspaces
	// dirty maps an absolute checkout path to its uncommitted entries.
	dirty map[string][]string
	// err maps an absolute checkout path to a status read that fails.
	err map[string]error
}

func (d *dirtyWorkspaces) IsClean(workspacePath string) (bool, []string, error) {
	if err := d.err[workspacePath]; err != nil {
		return false, nil, err
	}
	changed := d.dirty[workspacePath]
	return len(changed) == 0, changed, nil
}

// sparseFor returns every sparse set applied to the named checkout dir, in
// order, so a test can assert both the initial narrowing and any widening.
func (f *fakeWorkspaces) sparseFor(dir string) []jj.SparseSetOpts {
	var out []jj.SparseSetOpts
	for _, c := range f.sparses {
		if filepath.Base(c.path) == dir {
			out = append(out, c.opts)
		}
	}
	return out
}

// twoCheckoutManifest is a rig with one sparse monorepo checkout serving two
// modules and one full single-module checkout.
func twoCheckoutManifest() *Manifest {
	return &Manifest{
		Name:      "dsp",
		GoVersion: "1.24.5",
		Sparse:    true,
		Source:    Source{Kind: "repo", Ref: "opentdf/platform@v0.9.0"},
		Primary:   "github.com/opentdf/platform/service",
		Checkouts: []Checkout{
			{
				Dir: "platform-v0.9.0", Workspace: "rig-dsp-aaaaaaaa",
				Repo: "opentdf/platform", MainClone: "/mains/opentdf/platform",
				Revset: "tags/service/v0.9.0", Commit: "aaaaaaaa1111", Tag: "service/v0.9.0",
				Sparse: []string{"lib/fixtures", "service"},
			},
			{
				Dir: "otdfctl-v0.3.0", Workspace: "rig-dsp-bbbbbbbb",
				Repo: "opentdf/otdfctl", MainClone: "/mains/opentdf/otdfctl",
				Revset: "tags/v0.3.0", Commit: "bbbbbbbb2222", Tag: "v0.3.0",
				Full: true,
			},
		},
		Members: []Member{
			{Path: "github.com/opentdf/platform/service", Version: "v0.9.0",
				Checkout: "platform-v0.9.0", Subdir: "service"},
			{Path: "github.com/opentdf/platform/lib/fixtures", Version: "v0.1.0",
				Checkout: "platform-v0.9.0", Subdir: "lib/fixtures", Indirect: true},
			{Path: "github.com/opentdf/otdfctl", Version: "v0.3.0",
				Checkout: "otdfctl-v0.3.0"},
		},
	}
}

// twoCheckoutContent gives every member a go.mod, which is the minimum
// verifyMembers accepts.
func twoCheckoutContent() map[string]map[string]string {
	return map[string]map[string]string{
		"rig-dsp-aaaaaaaa": {
			"service/go.mod":      "module github.com/opentdf/platform/service\n\ngo 1.24.5\n",
			"lib/fixtures/go.mod": "module github.com/opentdf/platform/lib/fixtures\n\ngo 1.24.5\n",
		},
		"rig-dsp-bbbbbbbb": {
			"go.mod": "module github.com/opentdf/otdfctl\n\ngo 1.24.5\n",
		},
	}
}

func TestMaterializeCreatesCheckoutsAndFiles(t *testing.T) {
	ws := &fakeWorkspaces{content: twoCheckoutContent()}
	mz := &Materializer{JJ: ws}
	m := twoCheckoutManifest()
	root := filepath.Join(t.TempDir(), "dsp")

	require.NoError(t, mz.Materialize(context.Background(), m, root))

	require.Len(t, ws.adds, 2)

	// The sparse checkout starts empty so jj never materialises the whole
	// monorepo, and is pinned by commit rather than by tag.
	platform := ws.adds[0]
	assert.Equal(t, "/mains/opentdf/platform", platform.repo)
	assert.Equal(t, filepath.Join(root, SrcDir, "platform-v0.9.0"), platform.dest)
	assert.Equal(t, "rig-dsp-aaaaaaaa", platform.opts.Name)
	assert.Equal(t, "aaaaaaaa1111", platform.opts.Revset)
	assert.Equal(t, jj.SparseEmpty, platform.opts.SparsePatterns)
	assert.Equal(t, "rig checkout: opentdf/platform @ service/v0.9.0", platform.opts.Message)

	// The full checkout skips the narrowing round trip entirely.
	otdfctl := ws.adds[1]
	assert.Equal(t, jj.SparseFull, otdfctl.opts.SparsePatterns)
	assert.Empty(t, ws.sparseFor("otdfctl-v0.3.0"))

	sparse := ws.sparseFor("platform-v0.9.0")
	require.Len(t, sparse, 1, "no replaces to cover, so no widening pass")
	assert.Equal(t, jj.SparseSetOpts{Clear: true, Add: []string{"lib/fixtures", "service"}}, sparse[0])

	for _, name := range []string{GoWorkName, ManifestName, EnvShName, EnvrcName, ClaudeMDName, ReadmeName, GitignoreName} {
		assert.FileExists(t, filepath.Join(root, name), name)
	}

	// env.sh is sourced but also meant to be runnable directly.
	info, err := os.Stat(filepath.Join(root, EnvShName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	// The manifest must round-trip, since `rig rm` reads it to find the
	// workspaces it has to forget.
	loaded, err := Load(root)
	require.NoError(t, err)
	assert.Equal(t, m.Checkouts, loaded.Checkouts)

	work, err := os.ReadFile(filepath.Join(root, GoWorkName))
	require.NoError(t, err)
	assert.Contains(t, string(work), "./src/platform-v0.9.0/service")
	assert.Contains(t, string(work), "./src/otdfctl-v0.3.0")
}

func TestMaterializeWidensSparseForLocalReplace(t *testing.T) {
	content := twoCheckoutContent()
	// service replaces a sibling that the planner had no way to know about: it
	// only appears in the go.mod, which does not exist until the checkout does.
	content["rig-dsp-aaaaaaaa"]["service/go.mod"] = strings.Join([]string{
		"module github.com/opentdf/platform/service",
		"",
		"go 1.24.5",
		"",
		"replace github.com/opentdf/platform/protocol/go => ../protocol/go",
		"",
	}, "\n")

	ws := &fakeWorkspaces{content: content}
	mz := &Materializer{JJ: ws}
	m := twoCheckoutManifest()
	root := filepath.Join(t.TempDir(), "dsp")

	require.NoError(t, mz.Materialize(context.Background(), m, root))

	sparse := ws.sparseFor("platform-v0.9.0")
	require.Len(t, sparse, 2, "narrowed once, then widened once the go.mod was readable")
	assert.Equal(t, []string{"lib/fixtures", "service"}, sparse[0].Add)
	assert.Equal(t, []string{"lib/fixtures", "protocol/go", "service"}, sparse[1].Add)
	assert.True(t, sparse[1].Clear, "widening replaces the set rather than appending to it")

	// The widened set is what gets persisted, so a later `rig verify` compares
	// against what is actually on disk.
	loaded, err := Load(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"lib/fixtures", "protocol/go", "service"}, loaded.Checkouts[0].Sparse)
}

func TestMaterializeWidensToFullForRootReplace(t *testing.T) {
	content := twoCheckoutContent()
	content["rig-dsp-aaaaaaaa"]["service/go.mod"] = "module github.com/opentdf/platform/service\n\ngo 1.24.5\n\nreplace github.com/opentdf/platform => ..\n"

	ws := &fakeWorkspaces{content: content}
	mz := &Materializer{JJ: ws}
	m := twoCheckoutManifest()
	root := filepath.Join(t.TempDir(), "dsp")

	require.NoError(t, mz.Materialize(context.Background(), m, root))

	sparse := ws.sparseFor("platform-v0.9.0")
	require.Len(t, sparse, 2)
	assert.Equal(t, []string{"."}, sparse[1].Add, "sparse cannot express less than the whole tree")

	loaded, err := Load(root)
	require.NoError(t, err)
	assert.True(t, loaded.Checkouts[0].Full)
	assert.Empty(t, loaded.Checkouts[0].Sparse)
}

func TestMaterializeRecordsEscapedReplaceAsSkip(t *testing.T) {
	content := twoCheckoutContent()
	content["rig-dsp-aaaaaaaa"]["service/go.mod"] = "module github.com/opentdf/platform/service\n\ngo 1.24.5\n\nreplace example.com/other => ../../../elsewhere\n"

	var logged []string
	ws := &fakeWorkspaces{content: content}
	mz := &Materializer{JJ: ws, Logf: func(f string, a ...any) { logged = append(logged, f) }}
	m := twoCheckoutManifest()
	root := filepath.Join(t.TempDir(), "dsp")

	require.NoError(t, mz.Materialize(context.Background(), m, root), "an unsatisfiable replace warns, it does not abort the rig")

	loaded, err := Load(root)
	require.NoError(t, err)
	require.Len(t, loaded.Skipped, 1)
	assert.Equal(t, SkipEscapedReplace, loaded.Skipped[0].Kind)
	assert.Equal(t, "github.com/opentdf/platform/service", loaded.Skipped[0].Path)
	assert.Contains(t, strings.Join(logged, "\n"), "warning")
}

func TestMaterializeMissingGoModIsFatal(t *testing.T) {
	content := twoCheckoutContent()
	// The mapping put lib/fixtures in the wrong place: nothing is there.
	delete(content["rig-dsp-aaaaaaaa"], "lib/fixtures/go.mod")

	ws := &fakeWorkspaces{content: content}
	mz := &Materializer{JJ: ws}
	root := filepath.Join(t.TempDir(), "dsp")

	err := mz.Materialize(context.Background(), twoCheckoutManifest(), root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github.com/opentdf/platform/lib/fixtures")
	assert.Contains(t, err.Error(), "lib/fixtures")
	assert.Contains(t, err.Error(), "does not map to this repository")

	assert.NoDirExists(t, root, "a rig that cannot be built leaves nothing behind")
}

func TestMaterializeRollsBackInReverseOrder(t *testing.T) {
	ws := &fakeWorkspaces{
		content: twoCheckoutContent(),
		addErr:  map[string]error{"rig-dsp-bbbbbbbb": errors.New("workspace already exists")},
	}
	mz := &Materializer{JJ: ws}
	root := filepath.Join(t.TempDir(), "dsp")

	err := mz.Materialize(context.Background(), twoCheckoutManifest(), root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "opentdf/otdfctl")

	// Only the workspace that was actually registered is forgotten; the one
	// whose add failed was never ours to forget.
	require.Len(t, ws.forgets, 1)
	assert.Equal(t, forgetCall{repo: "/mains/opentdf/platform", name: "rig-dsp-aaaaaaaa"}, ws.forgets[0])
	assert.NoDirExists(t, root)
}

func TestMaterializeRollbackKeepsPreexistingRoot(t *testing.T) {
	root := t.TempDir() // already exists, and holds something of the user's
	keep := filepath.Join(root, "notes.md")
	require.NoError(t, os.WriteFile(keep, []byte("mine"), 0o644))

	ws := &fakeWorkspaces{
		content: twoCheckoutContent(),
		addErr:  map[string]error{"rig-dsp-aaaaaaaa": errors.New("boom")},
	}
	mz := &Materializer{JJ: ws}

	require.Error(t, mz.Materialize(context.Background(), twoCheckoutManifest(), root))

	assert.FileExists(t, keep, "rollback removes only what this run created")
	// src/ is ours even when the root is not, and leaving it behind makes the
	// retry fail: checkRigRootFree refuses a non-empty directory.
	assert.NoDirExists(t, filepath.Join(root, SrcDir))
}

// A src/ that predates the run is the user's, and an unexpected file inside it
// is a reason to stop rather than to delete harder.
func TestMaterializeRollbackKeepsAPreexistingSrc(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, SrcDir, "scratch.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(keep), 0o755))
	require.NoError(t, os.WriteFile(keep, []byte("mine"), 0o644))

	ws := &fakeWorkspaces{
		content: twoCheckoutContent(),
		addErr:  map[string]error{"rig-dsp-aaaaaaaa": errors.New("boom")},
	}
	mz := &Materializer{JJ: ws}

	require.Error(t, mz.Materialize(context.Background(), twoCheckoutManifest(), root))

	assert.FileExists(t, keep)
}

func TestMaterializeRollsBackOnSparseFailure(t *testing.T) {
	ws := &fakeWorkspaces{
		content:   twoCheckoutContent(),
		sparseErr: map[string]error{"platform-v0.9.0": errors.New("no such path")},
	}
	mz := &Materializer{JJ: ws}
	root := filepath.Join(t.TempDir(), "dsp")

	err := mz.Materialize(context.Background(), twoCheckoutManifest(), root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lib/fixtures, service")

	require.Len(t, ws.forgets, 1, "the workspace created just before the failure is undone")
	assert.Equal(t, "rig-dsp-aaaaaaaa", ws.forgets[0].name)
	assert.NoDirExists(t, root)
}

func TestMaterializeRejectsRelativeRoot(t *testing.T) {
	mz := &Materializer{JJ: &fakeWorkspaces{content: twoCheckoutContent()}}
	err := mz.Materialize(context.Background(), twoCheckoutManifest(), "rigs/dsp")
	require.Error(t, err)
	// GOWORK must be absolute or every go command in the rig fails.
	assert.Contains(t, err.Error(), "must be absolute")
}

func TestMaterializeRejectsInvalidManifest(t *testing.T) {
	ws := &fakeWorkspaces{content: twoCheckoutContent()}
	mz := &Materializer{JJ: ws}
	m := twoCheckoutManifest()
	m.Name = ""

	require.Error(t, mz.Materialize(context.Background(), m, filepath.Join(t.TempDir(), "dsp")))
	assert.Empty(t, ws.adds, "validation runs before anything touches the disk")
}

// stubValidator reports whether the rendered go.work parsed.
type stubValidator struct {
	err    error
	called bool
}

func (s *stubValidator) WorkEditFmt() error {
	s.called = true
	return s.err
}

func TestMaterializeValidatesGoWork(t *testing.T) {
	v := &stubValidator{}
	ws := &fakeWorkspaces{content: twoCheckoutContent()}
	mz := &Materializer{JJ: ws, Validate: v}

	require.NoError(t, mz.Materialize(context.Background(), twoCheckoutManifest(), filepath.Join(t.TempDir(), "dsp")))
	assert.True(t, v.called)
}

func TestMaterializeRollsBackWhenGoWorkIsRejected(t *testing.T) {
	ws := &fakeWorkspaces{content: twoCheckoutContent()}
	mz := &Materializer{JJ: ws, Validate: &stubValidator{err: errors.New("unknown directive")}}
	root := filepath.Join(t.TempDir(), "dsp")

	err := mz.Materialize(context.Background(), twoCheckoutManifest(), root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "go.work does not parse")
	assert.Len(t, ws.forgets, 2, "both checkouts are undone")
	assert.NoDirExists(t, root)
}

func TestRemoveForgetsWorkspacesThenDeletesTree(t *testing.T) {
	ws := &fakeWorkspaces{content: twoCheckoutContent()}
	mz := &Materializer{JJ: ws}
	m := twoCheckoutManifest()
	root := filepath.Join(t.TempDir(), "dsp")
	require.NoError(t, mz.Materialize(context.Background(), m, root))

	require.NoError(t, Remove(ws, m, root, nil))

	assert.Equal(t, []forgetCall{
		{repo: "/mains/opentdf/platform", name: "rig-dsp-aaaaaaaa"},
		{repo: "/mains/opentdf/otdfctl", name: "rig-dsp-bbbbbbbb"},
	}, ws.forgets)
	assert.NoDirExists(t, root)
}

func TestRemoveReportsUnforgettableWorkspaces(t *testing.T) {
	ws := &fakeWorkspaces{content: twoCheckoutContent()}
	mz := &Materializer{JJ: ws}
	m := twoCheckoutManifest()
	root := filepath.Join(t.TempDir(), "dsp")
	require.NoError(t, mz.Materialize(context.Background(), m, root))

	ws.forgetErr = map[string]error{"rig-dsp-bbbbbbbb": errors.New("workspace is dirty")}

	err := Remove(ws, m, root, nil)
	require.Error(t, err)
	// The tree is gone either way; the error has to name the command that
	// cleans up the stale registration, since nothing on disk names it now.
	assert.NoDirExists(t, root)
	assert.Contains(t, err.Error(), "jj -R /mains/opentdf/otdfctl workspace forget rig-dsp-bbbbbbbb")
}

func TestWriteFileIsAtomicAndSetsMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.sh")

	require.NoError(t, writeFile(path, "first\n", 0o755))
	require.NoError(t, writeFile(path, "second\n", 0o755))

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "second\n", string(body))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	// No temp file survives either write.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestCheckoutPreWarmsAndMaterializeAdoptsIt(t *testing.T) {
	ws := &fakeWorkspaces{content: twoCheckoutContent()}
	mz := &Materializer{JJ: ws}
	m := twoCheckoutManifest()
	root := filepath.Join(t.TempDir(), "dsp")

	// Phase one: `rig new` needs the primary on disk to compute the build list.
	dest, err := mz.Checkout(context.Background(), root, &m.Checkouts[0])
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, SrcDir, "platform-v0.9.0"), dest)
	assert.FileExists(t, filepath.Join(dest, "service", "go.mod"))

	// Phase two: the rest of the rig, without touching the primary again.
	require.NoError(t, mz.Materialize(context.Background(), m, root))

	require.Len(t, ws.adds, 2, "the pre-warmed checkout is not created twice")
	assert.Equal(t, "rig-dsp-aaaaaaaa", ws.adds[0].opts.Name)
	assert.Equal(t, "rig-dsp-bbbbbbbb", ws.adds[1].opts.Name)
	assert.FileExists(t, filepath.Join(root, ManifestName))
}

func TestMaterializeRollsBackThePreWarmedCheckoutToo(t *testing.T) {
	ws := &fakeWorkspaces{
		content: twoCheckoutContent(),
		addErr:  map[string]error{"rig-dsp-bbbbbbbb": errors.New("boom")},
	}
	mz := &Materializer{JJ: ws}
	m := twoCheckoutManifest()
	root := filepath.Join(t.TempDir(), "dsp")

	_, err := mz.Checkout(context.Background(), root, &m.Checkouts[0])
	require.NoError(t, err)
	require.Error(t, mz.Materialize(context.Background(), m, root))

	// The primary was created in an earlier phase but is still ours to undo.
	require.Len(t, ws.forgets, 1)
	assert.Equal(t, "rig-dsp-aaaaaaaa", ws.forgets[0].name)
	assert.NoDirExists(t, root)
}

func TestRollbackIsCallableByTheCallerAndOnlyOnce(t *testing.T) {
	ws := &fakeWorkspaces{content: twoCheckoutContent()}
	mz := &Materializer{JJ: ws}
	m := twoCheckoutManifest()
	root := filepath.Join(t.TempDir(), "dsp")

	_, err := mz.Checkout(context.Background(), root, &m.Checkouts[0])
	require.NoError(t, err)

	// This is the window `rig new` fails in when `go list` fails: the primary
	// exists and no manifest records it, so only the caller can clean up.
	mz.Rollback(root)
	require.Len(t, ws.forgets, 1)
	assert.NoDirExists(t, root)

	mz.Rollback(root)
	assert.Len(t, ws.forgets, 1, "a second rollback must not forget the same workspace again")
}

// fakeOrphans adds the two lookups orphan recovery needs to fakeWorkspaces:
// which clone a checkout belongs to, and what that clone has registered.
type fakeOrphans struct {
	fakeWorkspaces
	// mainClone maps a checkout directory to the clone backing it. A missing
	// entry stands for a directory that is not a readable jj workspace.
	mainClone map[string]string
	// registered maps a clone to the workspaces it still lists.
	registered map[string][]jj.Workspace
	rootErr    error
	listErr    error
}

func (f *fakeOrphans) MainWorkspaceRoot(path string) (string, error) {
	if f.rootErr != nil {
		return "", f.rootErr
	}
	clone, ok := f.mainClone[filepath.Base(path)]
	if !ok {
		return "", errors.New("not a jj workspace")
	}
	return clone, nil
}

func (f *fakeOrphans) ListWorkspaces(repo string) ([]jj.Workspace, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.registered[repo], nil
}

// orphanRig builds the state an interrupted `rig new` leaves: checkout
// directories under src/ and no manifest.
func orphanRig(t *testing.T, dirs ...string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "dsp")
	for _, d := range dirs {
		require.NoError(t, os.MkdirAll(filepath.Join(root, SrcDir, d), 0o755))
	}
	return root
}

func TestFindOrphansTracesCheckoutsBackToTheirClones(t *testing.T) {
	root := orphanRig(t, "platform-v0.9.0", "otdfctl-v0.3.0")
	js := &fakeOrphans{
		mainClone: map[string]string{
			"platform-v0.9.0": "/mains/opentdf/platform",
			"otdfctl-v0.3.0":  "/mains/opentdf/otdfctl",
		},
		registered: map[string][]jj.Workspace{
			"/mains/opentdf/platform": {
				{Name: "default", Path: "/mains/opentdf/platform"},
				{Name: "rig-dsp-aaaaaaaa", Path: filepath.Join(root, SrcDir, "platform-v0.9.0")},
			},
			"/mains/opentdf/otdfctl": {
				{Name: "rig-dsp-bbbbbbbb", Path: filepath.Join(root, SrcDir, "otdfctl-v0.3.0")},
			},
		},
	}

	got, err := FindOrphans(js, root)
	require.NoError(t, err)
	require.Len(t, got, 2)

	byDir := map[string]Orphan{}
	for _, o := range got {
		byDir[filepath.Base(o.Dir)] = o
	}
	assert.Equal(t, "rig-dsp-aaaaaaaa", byDir["platform-v0.9.0"].Workspace)
	assert.Equal(t, "/mains/opentdf/platform", byDir["platform-v0.9.0"].MainClone)
	assert.Equal(t, "rig-dsp-bbbbbbbb", byDir["otdfctl-v0.3.0"].Workspace)
}

// The match is on path, not on the rig-<name>-<hash> convention, so a rig
// whose directory was renamed is still traced correctly.
func TestFindOrphansMatchesOnPathNotWorkspaceName(t *testing.T) {
	root := orphanRig(t, "platform-v0.9.0")
	js := &fakeOrphans{
		mainClone: map[string]string{"platform-v0.9.0": "/mains/opentdf/platform"},
		registered: map[string][]jj.Workspace{
			"/mains/opentdf/platform": {
				{Name: "nothing-like-a-rig-name", Path: filepath.Join(root, SrcDir, "platform-v0.9.0")},
			},
		},
	}

	got, err := FindOrphans(js, root)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "nothing-like-a-rig-name", got[0].Workspace)
}

func TestFindOrphansReportsUntraceableCheckouts(t *testing.T) {
	root := orphanRig(t, "half-written")
	got, err := FindOrphans(&fakeOrphans{}, root)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Empty(t, got[0].Workspace, "nothing to forget")
	assert.Error(t, got[0].Err, "but the reason is reported rather than swallowed")
}

// A checkout whose workspace the user already forgot by hand leaves a
// directory with no registration. Removal still has to delete the tree.
func TestFindOrphansHandlesAnAlreadyForgottenWorkspace(t *testing.T) {
	root := orphanRig(t, "platform-v0.9.0")
	js := &fakeOrphans{
		mainClone:  map[string]string{"platform-v0.9.0": "/mains/opentdf/platform"},
		registered: map[string][]jj.Workspace{"/mains/opentdf/platform": {{Name: "default", Path: "/mains/opentdf/platform"}}},
	}

	got, err := FindOrphans(js, root)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Empty(t, got[0].Workspace)
	assert.NoError(t, got[0].Err, "a forgotten workspace is an expected state, not a failure")
}

func TestFindOrphansOnARootWithNoSrc(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dsp")
	require.NoError(t, os.MkdirAll(root, 0o755))
	got, err := FindOrphans(&fakeOrphans{}, root)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestRemoveOrphansForgetsThenDeletes(t *testing.T) {
	root := orphanRig(t, "platform-v0.9.0", "otdfctl-v0.3.0")
	js := &fakeOrphans{}
	orphans := []Orphan{
		{Dir: filepath.Join(root, SrcDir, "platform-v0.9.0"), MainClone: "/mains/opentdf/platform", Workspace: "rig-dsp-aaaaaaaa"},
		{Dir: filepath.Join(root, SrcDir, "otdfctl-v0.3.0"), MainClone: "/mains/opentdf/otdfctl", Workspace: "rig-dsp-bbbbbbbb"},
	}

	require.NoError(t, RemoveOrphans(js, orphans, root, nil))

	assert.Equal(t, []forgetCall{
		{repo: "/mains/opentdf/platform", name: "rig-dsp-aaaaaaaa"},
		{repo: "/mains/opentdf/otdfctl", name: "rig-dsp-bbbbbbbb"},
	}, js.forgets)
	assert.NoDirExists(t, root)
}

// A clone that cannot be reached must not silently lose its registration to
// the tree deletion: the user needs the command to run by hand.
func TestRemoveOrphansReportsForgetFailuresButStillDeletes(t *testing.T) {
	root := orphanRig(t, "platform-v0.9.0")
	js := &fakeOrphans{fakeWorkspaces: fakeWorkspaces{
		forgetErr: map[string]error{"rig-dsp-aaaaaaaa": errors.New("repo is locked")},
	}}
	orphans := []Orphan{
		{Dir: filepath.Join(root, SrcDir, "platform-v0.9.0"), MainClone: "/mains/opentdf/platform", Workspace: "rig-dsp-aaaaaaaa"},
	}

	err := RemoveOrphans(js, orphans, root, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jj -R /mains/opentdf/platform workspace forget rig-dsp-aaaaaaaa")
	assert.NoDirExists(t, root, "the tree still goes; only the registration is left to the user")
}

// A cancelled build must roll back, not stop where it stands: the half-built
// rig it would otherwise leave blocks `rig new` and strands workspaces.
func TestMaterializeRollsBackWhenTheContextIsCancelled(t *testing.T) {
	ws := &fakeWorkspaces{content: twoCheckoutContent()}
	mz := &Materializer{JJ: ws}
	root := filepath.Join(t.TempDir(), "dsp")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := mz.Materialize(ctx, twoCheckoutManifest(), root)

	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, ws.adds, "cancelled before the first checkout")
	assert.NoDirExists(t, root)
}

// Cancelling between checkouts is the realistic case — the first is on disk
// and registered by the time the user reaches for Ctrl-C.
func TestMaterializeRollsBackAWorkspaceAlreadyCreated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ws := &fakeWorkspaces{content: twoCheckoutContent()}
	mz := &Materializer{JJ: ws}
	root := filepath.Join(t.TempDir(), "dsp")

	// Cancel once the primary is down, so the loop stops at the second.
	dest, err := mz.Checkout(ctx, root, &twoCheckoutManifest().Checkouts[0])
	require.NoError(t, err)
	require.DirExists(t, dest)
	cancel()

	err = mz.Materialize(ctx, twoCheckoutManifest(), root)

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []forgetCall{{repo: "/mains/opentdf/platform", name: "rig-dsp-aaaaaaaa"}}, ws.forgets,
		"the workspace registered before the cancellation is forgotten")
	assert.NoDirExists(t, root, "and the tree this run created goes with it")
}

// Rollback runs after the cancellation that triggered it, so it must not use
// the cancelled client — every forget would fail and strand the workspace.
func TestRollbackUsesTheCleanupClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	work := &fakeWorkspaces{content: twoCheckoutContent()}
	cleanup := &fakeWorkspaces{}
	mz := &Materializer{JJ: work, Cleanup: cleanup}
	root := filepath.Join(t.TempDir(), "dsp")

	_, err := mz.Checkout(ctx, root, &twoCheckoutManifest().Checkouts[0])
	require.NoError(t, err)
	cancel()
	require.Error(t, mz.Materialize(ctx, twoCheckoutManifest(), root))

	assert.Empty(t, work.forgets, "the cancelled client is not used for cleanup")
	assert.Equal(t, []forgetCall{{repo: "/mains/opentdf/platform", name: "rig-dsp-aaaaaaaa"}}, cleanup.forgets)
}
