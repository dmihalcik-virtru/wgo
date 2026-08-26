package rig

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/gomod"
	"golang.org/x/mod/modfile"
)

// fakeLocator resolves owner/repo to a canned clone path.
type fakeLocator struct {
	clones map[string]string
	calls  []string
	err    map[string]error
}

func (f *fakeLocator) Locate(owner, repo string) (string, error) {
	slug := owner + "/" + repo
	f.calls = append(f.calls, slug)
	if err, ok := f.err[slug]; ok {
		return "", err
	}
	p, ok := f.clones[slug]
	if !ok {
		return "", errors.New("no such repo")
	}
	return p, nil
}

// fakeResolver maps a revset to a commit. A revset with no entry resolves to
// the empty string, which is what jj returns for a tag that was never fetched.
type fakeResolver struct {
	commits map[string]string
	err     error
}

func (f *fakeResolver) Resolve(_, revset string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.commits[revset], nil
}

const (
	dspClone      = "/mains/virtru-corp/data-security-platform"
	platformClone = "/mains/opentdf/platform"
	otdfctlClone  = "/mains/opentdf/otdfctl"
)

// dspRequest reproduces virtru-corp/data-security-platform@v2.7.1: seven
// opentdf/platform modules spanning seven distinct commits, plus a second
// opentdf repo, plus a locally replaced sibling module.
func dspRequest() (Request, *Planner) {
	primary := gomod.Module{
		Path: "github.com/virtru-corp/data-security-platform/v2", Version: "v2.7.1",
		Main: true, GoVersion: "1.24.5",
	}
	buildList := []gomod.Module{
		primary,
		{Path: "github.com/opentdf/platform/service", Version: "v0.11.6", GoVersion: "1.23.0"},
		{Path: "github.com/opentdf/platform/sdk", Version: "v0.10.1"},
		{Path: "github.com/opentdf/platform/protocol/go", Version: "v0.13.0"},
		{Path: "github.com/opentdf/platform/lib/ocrypto", Version: "v0.7.0"},
		{Path: "github.com/opentdf/platform/lib/identifier", Version: "v0.2.0"},
		{Path: "github.com/opentdf/platform/lib/fixtures", Version: "v0.3.0"},
		{Path: "github.com/opentdf/platform/lib/flattening", Version: "v0.1.3", Indirect: true},
		{Path: "github.com/opentdf/otdfctl", Version: "v0.26.2"},
		// Pinned at a pseudo-version but replaced with a directory inside the
		// primary's own tree, so it must not get a checkout of its own.
		{
			Path:    "github.com/virtru-corp/data-security-platform/sdk/v2",
			Version: "v2.7.1-0.20260801120000-abcdefabcdef",
			Replace: &gomod.Module{Path: "./sdk"},
		},
		// Out of org: left to the module cache.
		{Path: "google.golang.org/grpc", Version: "v1.65.0"},
		{Path: "github.com/stretchr/testify", Version: "v1.9.0", Indirect: true},
	}

	req := Request{
		Name:        "dsp-2.7.1",
		Source:      Source{Kind: "repo", Ref: "virtru-corp/data-security-platform@v2.7.1"},
		OrgPrefixes: []string{"github.com/opentdf", "github.com/virtru-corp"},
		Sparse:      true,
		Primary:     primary,
		// data-security-platform ships `use (. ./sdk)` at v2.7.1.
		PrimaryUse: []string{".", "./sdk"},
		BuildList:  buildList,
		GoVersion:  "1.24.5",
		Created:    "2026-08-25T00:00:00Z",
		WgoVersion: "test",
	}

	p := &Planner{
		Locator: &fakeLocator{clones: map[string]string{
			"virtru-corp/data-security-platform": dspClone,
			"opentdf/platform":                   platformClone,
			"opentdf/otdfctl":                    otdfctlClone,
		}},
		Resolver: &fakeResolver{commits: map[string]string{
			`present(tags(exact:"v2.7.1"))`:                "dsp0000000000000000000000000000000000000",
			`present(tags(exact:"service/v0.11.6"))`:       "6a5827d7000000000000000000000000000000aa",
			`present(tags(exact:"sdk/v0.10.1"))`:           "a9262237000000000000000000000000000000aa",
			`present(tags(exact:"protocol/go/v0.13.0"))`:   "22faa49a000000000000000000000000000000aa",
			`present(tags(exact:"lib/ocrypto/v0.7.0"))`:    "a2fa7a98000000000000000000000000000000aa",
			`present(tags(exact:"lib/identifier/v0.2.0"))`: "7fab0940000000000000000000000000000000aa",
			`present(tags(exact:"lib/fixtures/v0.3.0"))`:   "237edab0000000000000000000000000000000aa",
			`present(tags(exact:"lib/flattening/v0.1.3"))`: "a2323482000000000000000000000000000000aa",
			`present(tags(exact:"v0.26.2"))`:               "0000000000000000000000000000000000ctl002",
		}},
	}
	return req, p
}

func TestPlanDSP(t *testing.T) {
	req, p := dspRequest()
	m, err := p.Plan(req)
	require.NoError(t, err)

	// One dsp checkout, seven platform checkouts at seven distinct commits, one
	// otdfctl checkout. The locally replaced sdk/v2 adds none.
	require.Len(t, m.Checkouts, 9)

	var dirs []string
	byRepo := map[string]int{}
	for _, c := range m.Checkouts {
		dirs = append(dirs, c.Dir)
		byRepo[c.Repo]++
	}
	assert.Equal(t, 7, byRepo["opentdf/platform"])
	assert.Equal(t, 1, byRepo["opentdf/otdfctl"])
	assert.Equal(t, 1, byRepo["virtru-corp/data-security-platform"])

	assert.ElementsMatch(t, []string{
		"data-security-platform-v2.7.1",
		"platform-service-v0.11.6",
		"platform-sdk-v0.10.1",
		"platform-protocol-go-v0.13.0",
		"platform-lib-ocrypto-v0.7.0",
		"platform-lib-identifier-v0.2.0",
		"platform-lib-fixtures-v0.3.0",
		"platform-lib-flattening-v0.1.3",
		"otdfctl-v0.26.2",
	}, dirs)

	// go.work `use` entries. Order is asserted separately by
	// TestPlanOrdersCheckoutsForReading.
	var uses []string
	for _, mem := range m.Members {
		uses = append(uses, mem.UseDir())
	}
	assert.ElementsMatch(t, []string{
		"./src/data-security-platform-v2.7.1",
		"./src/data-security-platform-v2.7.1/sdk",
		"./src/otdfctl-v0.26.2",
		"./src/platform-lib-fixtures-v0.3.0/lib/fixtures",
		"./src/platform-lib-flattening-v0.1.3/lib/flattening",
		"./src/platform-lib-identifier-v0.2.0/lib/identifier",
		"./src/platform-lib-ocrypto-v0.7.0/lib/ocrypto",
		"./src/platform-protocol-go-v0.13.0/protocol/go",
		"./src/platform-sdk-v0.10.1/sdk",
		"./src/platform-service-v0.11.6/service",
	}, uses)

	// The go directive is the highest across the workspace.
	assert.Equal(t, "1.24.5", m.GoVersion)
	assert.True(t, m.Sparse)
}

// Checkouts are ordered for someone reading go.work or rig.toml: the artifact
// being reproduced first, then each repo's checkouts by tag. Grouping by
// (repo, commit) makes the commit hash an obvious sort key, but sorting on it
// interleaves the repos in an order that means nothing to a reader.
func TestPlanOrdersCheckoutsForReading(t *testing.T) {
	req, p := dspRequest()
	m, err := p.Plan(req)
	require.NoError(t, err)

	var dirs []string
	for _, c := range m.Checkouts {
		dirs = append(dirs, c.Dir)
	}
	assert.Equal(t, []string{
		"data-security-platform-v2.7.1", // the primary leads
		"otdfctl-v0.26.2",
		"platform-lib-fixtures-v0.3.0", // then opentdf/platform, by tag
		"platform-lib-flattening-v0.1.3",
		"platform-lib-identifier-v0.2.0",
		"platform-lib-ocrypto-v0.7.0",
		"platform-protocol-go-v0.13.0",
		"platform-sdk-v0.10.1",
		"platform-service-v0.11.6",
	}, dirs)

	// Members follow their checkout, so rig.toml reads in the same order.
	var members []string
	for _, mem := range m.Members {
		members = append(members, mem.Checkout)
	}
	assert.Equal(t, []string{
		"data-security-platform-v2.7.1",
		"data-security-platform-v2.7.1",
		"otdfctl-v0.26.2",
		"platform-lib-fixtures-v0.3.0",
		"platform-lib-flattening-v0.1.3",
		"platform-lib-identifier-v0.2.0",
		"platform-lib-ocrypto-v0.7.0",
		"platform-protocol-go-v0.13.0",
		"platform-sdk-v0.10.1",
		"platform-service-v0.11.6",
	}, members)
}

// The ordering must not depend on map iteration, which Go randomises per run.
func TestPlanIsDeterministic(t *testing.T) {
	req, p := dspRequest()
	first, err := p.Plan(req)
	require.NoError(t, err)

	for i := 0; i < 20; i++ {
		req, p := dspRequest()
		got, err := p.Plan(req)
		require.NoError(t, err)
		require.Equal(t, first, got, "plan %d differs", i)
	}
}

// A repository-root module gains nothing from sparse; a subdirectory module
// materialises only its own subtree.
func TestPlanSparseSets(t *testing.T) {
	req, p := dspRequest()
	m, err := p.Plan(req)
	require.NoError(t, err)

	for _, c := range m.Checkouts {
		switch c.Dir {
		case "data-security-platform-v2.7.1", "otdfctl-v0.26.2":
			assert.True(t, c.Full, "%s is a repo-root module", c.Dir)
			assert.Empty(t, c.Sparse)
		case "platform-service-v0.11.6":
			assert.False(t, c.Full)
			assert.Equal(t, []string{"service"}, c.Sparse)
		case "platform-protocol-go-v0.13.0":
			assert.Equal(t, []string{"protocol/go"}, c.Sparse)
		}
	}
}

func TestPlanFullCheckouts(t *testing.T) {
	req, p := dspRequest()
	req.Sparse = false
	m, err := p.Plan(req)
	require.NoError(t, err)

	for _, c := range m.Checkouts {
		assert.True(t, c.Full, "%s", c.Dir)
		assert.Empty(t, c.Sparse, "%s", c.Dir)
	}
	assert.False(t, m.Sparse)
}

func TestPlanWorkspaceNames(t *testing.T) {
	req, p := dspRequest()
	m, err := p.Plan(req)
	require.NoError(t, err)

	seen := map[string]bool{}
	for _, c := range m.Checkouts {
		assert.Truef(t, len(c.Workspace) > len(WorkspacePrefix), "%q", c.Workspace)
		assert.Equal(t, WorkspacePrefix, c.Workspace[:len(WorkspacePrefix)])
		assert.NotContains(t, c.Workspace, "/", "workspace names must be slash-free")
		assert.False(t, seen[c.Workspace], "duplicate workspace %q", c.Workspace)
		seen[c.Workspace] = true
	}
	assert.Equal(t, "rig-dsp-2.7.1-platform-service-v0.11.6",
		mustCheckout(t, m, "platform-service-v0.11.6").Workspace)
}

func TestPlanSkips(t *testing.T) {
	req, p := dspRequest()
	m, err := p.Plan(req)
	require.NoError(t, err)

	kinds := map[string][]string{}
	for _, s := range m.Skipped {
		kinds[s.SkipKind()] = append(kinds[s.SkipKind()], s.Path)
	}
	assert.ElementsMatch(t,
		[]string{"google.golang.org/grpc", "github.com/stretchr/testify"},
		kinds[SkipOutOfOrg])
	assert.Equal(t,
		[]string{"github.com/virtru-corp/data-security-platform/sdk/v2"},
		kinds[SkipLocalReplace])
	assert.Empty(t, kinds[SkipUnreachable])
}

// A private or deleted repo must cost only its own modules, not the whole rig.
func TestPlanUnreachableRepoIsSkipped(t *testing.T) {
	req, p := dspRequest()
	p.Locator.(*fakeLocator).err = map[string]error{
		"opentdf/otdfctl": errors.New("repository not found"),
	}

	m, err := p.Plan(req)
	require.NoError(t, err)

	assert.Len(t, m.Checkouts, 8, "the other eight checkouts still get planned")
	var unreachable []string
	for _, s := range m.Skipped {
		if s.SkipKind() == SkipUnreachable {
			unreachable = append(unreachable, s.Path)
			assert.Contains(t, s.Reason, "repository not found")
		}
	}
	assert.Equal(t, []string{"github.com/opentdf/otdfctl"}, unreachable)
}

// Locate is expensive (it may clone), so a monorepo contributing seven modules
// must only be located once.
func TestPlanLocatesEachRepoOnce(t *testing.T) {
	req, p := dspRequest()
	_, err := p.Plan(req)
	require.NoError(t, err)

	counts := map[string]int{}
	for _, slug := range p.Locator.(*fakeLocator).calls {
		counts[slug]++
	}
	assert.Equal(t, 1, counts["opentdf/platform"])
	assert.Equal(t, 1, counts["virtru-corp/data-security-platform"])
	assert.Equal(t, 1, counts["opentdf/otdfctl"])
}

// A repo we can reach but a tag we cannot resolve means the pin is wrong, not
// merely unavailable — that has to stop the rig rather than silently omit a
// module the build needs.
func TestPlanUnresolvableTagIsFatal(t *testing.T) {
	req, p := dspRequest()
	delete(p.Resolver.(*fakeResolver).commits, `present(tags(exact:"service/v0.11.6"))`)

	_, err := p.Plan(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github.com/opentdf/platform/service@v0.11.6")
	assert.Contains(t, err.Error(), "jj git fetch", "the error names the fix")
}

func TestPlanResolverError(t *testing.T) {
	req, p := dspRequest()
	p.Resolver.(*fakeResolver).err = errors.New("jj exploded")

	_, err := p.Plan(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jj exploded")
}

// A pseudo-version carries its own commit, so it resolves without a tag and the
// checkout is named after the commit.
func TestPlanPseudoVersion(t *testing.T) {
	primary := gomod.Module{Path: "github.com/acme/app", Version: "v1.0.0", Main: true, GoVersion: "1.24"}
	req := Request{
		Name:        "pseudo",
		OrgPrefixes: []string{"github.com/acme"},
		Sparse:      true,
		Primary:     primary,
		BuildList: []gomod.Module{
			primary,
			{Path: "github.com/acme/lib/thing", Version: "v0.0.0-20260101000000-0123456789ab"},
		},
		Created: "2026-08-25T00:00:00Z",
	}
	p := &Planner{
		Locator: &fakeLocator{clones: map[string]string{
			"acme/app": "/mains/acme/app",
			"acme/lib": "/mains/acme/lib",
		}},
		Resolver: &fakeResolver{commits: map[string]string{
			`present(tags(exact:"v1.0.0"))`: "aaaa111100000000000000000000000000000000",
			"0123456789ab":                  "0123456789abcdef000000000000000000000000",
		}},
	}

	m, err := p.Plan(req)
	require.NoError(t, err)

	// Named after the commit, since there is no tag to name it after.
	c := mustCheckout(t, m, "lib-01234567")
	assert.Equal(t, "", c.Tag, "a pseudo-version has no tag")
	assert.Equal(t, "0123456789ab", c.Revset)
	assert.Equal(t, []string{"thing"}, c.Sparse)
}

// Two modules released from one commit share one checkout with a unioned
// sparse set, rather than getting byte-identical working copies.
func TestPlanSharesCheckoutAcrossModulesAtOneCommit(t *testing.T) {
	primary := gomod.Module{Path: "github.com/acme/app", Version: "v1.0.0", Main: true}
	req := Request{
		Name:        "shared",
		OrgPrefixes: []string{"github.com/acme"},
		Sparse:      true,
		Primary:     primary,
		BuildList: []gomod.Module{
			primary,
			{Path: "github.com/acme/mono/a", Version: "v0.1.0"},
			{Path: "github.com/acme/mono/b", Version: "v0.2.0"},
			{Path: "github.com/acme/mono/deep/c", Version: "v0.3.0"},
		},
		Created: "2026-08-25T00:00:00Z",
	}
	release := "cccc333300000000000000000000000000000000"
	p := &Planner{
		Locator: &fakeLocator{clones: map[string]string{
			"acme/app": "/mains/acme/app", "acme/mono": "/mains/acme/mono",
		}},
		Resolver: &fakeResolver{commits: map[string]string{
			`present(tags(exact:"v1.0.0"))`:        "aaaa111100000000000000000000000000000000",
			`present(tags(exact:"a/v0.1.0"))`:      release,
			`present(tags(exact:"b/v0.2.0"))`:      release,
			`present(tags(exact:"deep/c/v0.3.0"))`: release,
		}},
	}

	m, err := p.Plan(req)
	require.NoError(t, err)
	require.Len(t, m.Checkouts, 2, "app plus one shared mono checkout")

	mono := mustCheckoutForRepo(t, m, "acme/mono")
	assert.Equal(t, []string{"a", "b", "deep/c"}, mono.Sparse)
	// Named after the module closest to the root, tie-broken by path.
	assert.Equal(t, "a/v0.1.0", mono.Tag)
	assert.Equal(t, "mono-a-v0.1.0", mono.Dir)

	var served []string
	for _, mem := range m.Members {
		if mem.Checkout == mono.Dir {
			served = append(served, mem.Path)
		}
	}
	assert.Len(t, served, 3)
}

// Two repos releasing the same tag name must not land on one directory.
func TestPlanDirCollision(t *testing.T) {
	primary := gomod.Module{Path: "github.com/one/platform", Version: "v1.0.0", Main: true}
	req := Request{
		Name:        "collide",
		OrgPrefixes: []string{"github.com/one", "github.com/two"},
		Sparse:      true,
		Primary:     primary,
		BuildList: []gomod.Module{
			primary,
			{Path: "github.com/two/platform", Version: "v1.0.0"},
		},
		Created: "2026-08-25T00:00:00Z",
	}
	p := &Planner{
		Locator: &fakeLocator{clones: map[string]string{
			"one/platform": "/mains/one/platform", "two/platform": "/mains/two/platform",
		}},
		Resolver: &fakeResolver{commits: map[string]string{
			`present(tags(exact:"v1.0.0"))`: "", // set per-clone below
		}},
	}
	// Distinct commits per clone so the two group separately.
	p.Resolver = &perCloneResolver{commits: map[string]string{
		"/mains/one/platform": "1111111100000000000000000000000000000000",
		"/mains/two/platform": "2222222200000000000000000000000000000000",
	}}

	m, err := p.Plan(req)
	require.NoError(t, err)
	require.Len(t, m.Checkouts, 2)

	dirs := []string{m.Checkouts[0].Dir, m.Checkouts[1].Dir}
	assert.NotEqual(t, dirs[0], dirs[1], "collision must be disambiguated")
	assert.Contains(t, dirs, "platform-v1.0.0")
	assert.Contains(t, dirs, "platform-v1.0.0-22222222")
}

type perCloneResolver struct{ commits map[string]string }

func (r *perCloneResolver) Resolve(clone, _ string) (string, error) {
	return r.commits[clone], nil
}

func TestPlanRequiresNameAndPrimary(t *testing.T) {
	p := &Planner{Locator: &fakeLocator{}, Resolver: &fakeResolver{}}

	_, err := p.Plan(Request{Primary: gomod.Module{Path: "github.com/a/b"}})
	assert.ErrorContains(t, err, "requires a name")

	_, err = p.Plan(Request{Name: "x"})
	assert.ErrorContains(t, err, "requires a primary module")
}

// The primary repo's own go.work `use` list has to be mirrored: at v2.7.1
// data-security-platform's sdk submodule pins platform/service v0.7.2 against
// the root's v0.11.6, so a rig that used only the root module would compute a
// different build list than the artifact.
func TestPlanMirrorsPrimaryGoWork(t *testing.T) {
	req, p := dspRequest()
	m, err := p.Plan(req)
	require.NoError(t, err)

	assert.Equal(t, []string{"", "sdk"}, m.PrimaryUse)

	primaryDir := "data-security-platform-v2.7.1"
	var subdirs []string
	for _, mem := range m.Members {
		if mem.Checkout == primaryDir {
			subdirs = append(subdirs, mem.Subdir)
		}
	}
	assert.ElementsMatch(t, []string{"", "sdk"}, subdirs)
}

func TestPlanWithoutPrimaryGoWork(t *testing.T) {
	req, p := dspRequest()
	req.PrimaryUse = nil
	m, err := p.Plan(req)
	require.NoError(t, err)

	for _, mem := range m.Members {
		assert.NotEqual(t, "./src/data-security-platform-v2.7.1/sdk", mem.UseDir())
	}
}

func TestWidenSparse(t *testing.T) {
	// platform/test/integration replaces ../../lib/fixtures and ../ocrypto.
	f, err := modfile.Parse("go.mod", []byte(`
module github.com/opentdf/platform/test/integration

go 1.23

require github.com/opentdf/platform/lib/fixtures v0.3.0

replace github.com/opentdf/platform/lib/fixtures => ../../lib/fixtures

replace github.com/opentdf/platform/lib/ocrypto => ../../lib/ocrypto
`), nil)
	require.NoError(t, err)

	c := Checkout{Dir: "platform-x", Repo: "opentdf/platform", Sparse: []string{"test/integration"}}
	members := []Member{{Path: "github.com/opentdf/platform/test/integration", Checkout: "platform-x", Subdir: "test/integration"}}

	skips := WidenSparse(&c, members, map[string]*modfile.File{"test/integration": f})
	assert.Empty(t, skips)
	assert.Equal(t, []string{"lib/fixtures", "lib/ocrypto", "test/integration"}, c.Sparse)
	assert.False(t, c.Full)
}

// A replace that resolves to the repository root means the whole tree is
// needed; sparse cannot express less than everything.
func TestWidenSparseToRoot(t *testing.T) {
	f, err := modfile.Parse("go.mod", []byte(`
module github.com/acme/repo/sub

go 1.23

replace github.com/acme/repo => ../
`), nil)
	require.NoError(t, err)

	c := Checkout{Dir: "repo-x", Repo: "acme/repo", Sparse: []string{"sub"}}
	members := []Member{{Path: "github.com/acme/repo/sub", Checkout: "repo-x", Subdir: "sub"}}

	skips := WidenSparse(&c, members, map[string]*modfile.File{"sub": f})
	assert.Empty(t, skips)
	assert.True(t, c.Full)
	assert.Empty(t, c.Sparse)
}

// A replace pointing outside the repository cannot be satisfied by any single
// checkout, so it is reported rather than silently producing a broken tree.
func TestWidenSparseEscapingTarget(t *testing.T) {
	f, err := modfile.Parse("go.mod", []byte(`
module github.com/acme/repo/sub

go 1.23

replace github.com/other/thing => ../../../elsewhere

replace github.com/other/abs => /opt/vendored
`), nil)
	require.NoError(t, err)

	c := Checkout{Dir: "repo-x", Repo: "acme/repo", Sparse: []string{"sub"}}
	members := []Member{{Path: "github.com/acme/repo/sub", Version: "v1.0.0", Checkout: "repo-x", Subdir: "sub"}}

	skips := WidenSparse(&c, members, map[string]*modfile.File{"sub": f})
	require.Len(t, skips, 2)
	for _, s := range skips {
		assert.Equal(t, SkipEscapedReplace, s.SkipKind())
		assert.Contains(t, s.Reason, "leaves acme/repo")
	}
	assert.Equal(t, []string{"sub"}, c.Sparse, "the base set survives")
}

func TestWidenSparseSkipsFullCheckouts(t *testing.T) {
	c := Checkout{Dir: "repo-x", Full: true}
	assert.Nil(t, WidenSparse(&c, nil, nil))
	assert.True(t, c.Full)
	assert.Empty(t, c.Sparse)
}

func TestNormaliseUse(t *testing.T) {
	assert.Equal(t, []string{"", "sdk"}, normaliseUse([]string{".", "./sdk"}))
	assert.Equal(t, []string{"", "a/b"}, normaliseUse([]string{"./a/b", ".", "a/b", "  "}))
	assert.Empty(t, normaliseUse(nil))
}

func mustCheckout(t *testing.T, m *Manifest, dir string) Checkout {
	t.Helper()
	c, ok := m.CheckoutByDir(dir)
	require.Truef(t, ok, "no checkout %q in %v", dir, m.Checkouts)
	return c
}

func mustCheckoutForRepo(t *testing.T, m *Manifest, repo string) Checkout {
	t.Helper()
	for _, c := range m.Checkouts {
		if c.Repo == repo {
			return c
		}
	}
	t.Fatalf("no checkout for repo %q", repo)
	return Checkout{}
}
