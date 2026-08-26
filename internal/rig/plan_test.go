package rig

import (
	"errors"
	"fmt"
	"strings"
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

// fakeResolver maps a revset to a commit. A revset with no entry fails, which
// is what jj does for a tag that was never fetched.
type fakeResolver struct {
	commits map[string]string
	err     error
}

func (f *fakeResolver) Resolve(_, revset string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	commit, ok := f.commits[revset]
	if !ok {
		// Mirror jj: a revset that matches nothing is an error, not an empty
		// string. A fake that returns ("", nil) sends every test down a branch
		// production never takes.
		return "", fmt.Errorf("jj resolve %q: no matching commit", revset)
	}
	return commit, nil
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

	// The go directive is the highest across the workspace. Only weakly checked
	// here — every input in this fixture is <= 1.24.5, so the assertion holds
	// even if the maximum were computed wrongly. See TestPlanGoVersion.
	assert.Equal(t, "1.24.5", m.GoVersion)
	assert.True(t, m.Sparse)
}

// go.work's `go` directive is the maximum over the modules the workspace
// actually contains — and only those.
//
// A `use` directive forces the workspace version up; a dependency left to the
// module cache compiles under its own go.mod and constrains nothing. Letting
// the whole build list in meant one out-of-org dependency could raise the rig
// above what any checked-out module needs, demanding a newer toolchain than the
// artifact was ever built with.
func TestPlanGoVersion(t *testing.T) {
	req, p := dspRequest()
	req.GoVersion = ""
	req.Primary.GoVersion = "1.22"

	for i := range req.BuildList {
		switch req.BuildList[i].Path {
		case "github.com/opentdf/platform/service":
			req.BuildList[i].GoVersion = "1.25rc1" // a member: must win
		case "google.golang.org/grpc":
			req.BuildList[i].GoVersion = "1.26" // out of org: must not count
		}
	}

	m, err := p.Plan(req)
	require.NoError(t, err)
	assert.Equal(t, "1.25rc1", m.GoVersion)
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
	// The name is rig + commit, not rig + checkout dir: the commit is short and
	// fixed-width, so it cannot be pushed out by a long directory name.
	c := mustCheckout(t, m, "platform-service-v0.11.6")
	assert.Equal(t, "rig-dsp-2.7.1-"+c.Commit[:8], c.Workspace)
}

// Building the workspace name by sanitising rig+dir as one string let
// SanitizeBranch's 60-character truncation cut off the part that made it
// unique, so two checkouts were handed one name. `jj workspace add --name`
// rejects the second, aborting materialisation with the manifest unwritten and
// the workspaces already created stranded in the main clone.
func TestPlanWorkspaceNamesSurviveTruncation(t *testing.T) {
	req, p := dspRequest()
	req.Name = strings.Repeat("x", MaxNameLen)

	m, err := p.Plan(req)
	require.NoError(t, err)
	require.Greater(t, len(m.Checkouts), 1)

	seen := map[string]string{}
	for _, c := range m.Checkouts {
		if prev, dup := seen[c.Workspace]; dup {
			t.Fatalf("checkouts %q and %q share workspace %q", prev, c.Dir, c.Workspace)
		}
		seen[c.Workspace] = c.Dir
		assert.Truef(t, strings.HasSuffix(c.Workspace, "-"+c.Commit[:8]),
			"workspace %q lost its commit discriminator", c.Workspace)
	}
}

// The cap is what makes the guarantee above hold, so it is enforced rather
// than documented.
func TestPlanRejectsOverlongName(t *testing.T) {
	req, p := dspRequest()
	req.Name = strings.Repeat("x", MaxNameLen+1)

	_, err := p.Plan(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "limit is")
}

func TestPlanSkips(t *testing.T) {
	req, p := dspRequest()
	m, err := p.Plan(req)
	require.NoError(t, err)

	kinds := map[SkipKind][]string{}
	for _, s := range m.Skipped {
		kinds[s.Kind] = append(kinds[s.Kind], s.Path)
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
		if s.Kind == SkipUnreachable {
			unreachable = append(unreachable, s.Path)
			assert.Contains(t, s.Detail, "repository not found")
		}
	}
	assert.Equal(t, []string{"github.com/opentdf/otdfctl"}, unreachable)
}

// The primary is the one module the rig exists to provide. Skipping it left
// Plan returning a valid nine-checkout manifest with no copy of the artifact
// under debug — Validate passed, because a manifest missing an *expected*
// member is structurally indistinguishable from one that never wanted it.
//
// opentdf/platform rather than the primary's own repo is deliberate: it is the
// repo whose Locate result is memoised across seven modules, so this also
// covers the cached-failure path where the primary is not the first module to
// hit it.
func TestPlanUnreachablePrimaryIsFatal(t *testing.T) {
	req, p := dspRequest()
	req.Primary = gomod.Module{
		Path: "github.com/opentdf/platform/service", Version: "v0.11.6", Main: true,
	}
	p.Locator.(*fakeLocator).err = map[string]error{
		"opentdf/platform": errors.New("repository not found"),
	}

	_, err := p.Plan(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "primary module")
	assert.Contains(t, err.Error(), "github.com/opentdf/platform/service")
	assert.Contains(t, err.Error(), "repository not found")
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

// A use dir is a directory in the primary's repo, so its module path is built
// from the repo root — not by joining the dir onto the primary's module path,
// which carries a /vN suffix that is part of the path and not a directory.
// Joining produced ".../data-security-platform/v2/sdk"; the source is at
// ".../data-security-platform/sdk", published as ".../sdk/v2".
//
// Nor does it carry a version: nothing in the build list pinned it, and copying
// the primary's version asserts a pin that does not exist.
func TestPlanPrimaryUseMemberIdentity(t *testing.T) {
	req, p := dspRequest()
	m, err := p.Plan(req)
	require.NoError(t, err)

	// Scoped to the primary's own checkout: opentdf/platform/sdk also sits at
	// subdir "sdk", in a different one.
	var sdk *Member
	for i := range m.Members {
		if m.Members[i].Subdir == "sdk" && m.Members[i].Checkout == "data-security-platform-v2.7.1" {
			sdk = &m.Members[i]
		}
	}
	require.NotNil(t, sdk, "the primary's go.work use of ./sdk must become a member")

	// The build list serves this directory through `replace …/sdk/v2 => ./sdk`,
	// and that entry is the only thing that knows the submodule's own major
	// suffix: it is not "/v2/" spliced in from the primary, and it is not
	// absent either.
	assert.Equal(t, "github.com/virtru-corp/data-security-platform/sdk/v2", sdk.Path)
	assert.NotContains(t, sdk.Path, "/v2/", "the primary's major suffix is not a directory")
	assert.Empty(t, sdk.Version, "a use dir is not a pinned module")
}

// A use dir with no build-list entry — a sibling module the primary does not
// import — has no recorded path to borrow, so the guess is all there is.
func TestPlanPrimaryUseMemberFallsBackToTheGuessedPath(t *testing.T) {
	req, p := dspRequest()
	req.PrimaryUse = append(req.PrimaryUse, "./tools")

	m, err := p.Plan(req)
	require.NoError(t, err)

	var tools *Member
	for i := range m.Members {
		if m.Members[i].Subdir == "tools" {
			tools = &m.Members[i]
		}
	}
	require.NotNil(t, tools)
	assert.Equal(t, "github.com/virtru-corp/data-security-platform/tools", tools.Path)
}

// The regression this guards: members are looked up in the build list by path,
// so a member whose path was synthesised rather than resolved contributed
// nothing, and a go.work that has to be 1.25 to build was written as 1.24.
func TestPlanGoVersionCountsLocallyReplacedMembers(t *testing.T) {
	req, p := dspRequest()
	req.GoVersion = ""
	req.Primary.GoVersion = "1.24.5"
	for i := range req.BuildList {
		if req.BuildList[i].Path == "github.com/virtru-corp/data-security-platform/sdk/v2" {
			req.BuildList[i].GoVersion = "1.25.0"
		}
	}

	m, err := p.Plan(req)
	require.NoError(t, err)
	assert.Equal(t, "1.25.0", m.GoVersion,
		"the primary's ./sdk is a workspace member; go.work cannot be older than it")
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

// An unresolvable pin is a hard error, and the message has to point somewhere
// useful. The two causes need different advice: a tag that was never fetched
// (jj does not fetch tags by default) versus a pseudo-version, whose commit no
// amount of tag fetching will produce.
func TestPlanUnresolvableRevisionHint(t *testing.T) {
	t.Run("missing tag names the tag to fetch", func(t *testing.T) {
		req, p := dspRequest()
		delete(p.Resolver.(*fakeResolver).commits, `present(tags(exact:"service/v0.11.6"))`)

		_, err := p.Plan(req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no matching commit")
		assert.Contains(t, err.Error(), "-t service/v0.11.6")
	})

	t.Run("pseudo-version does not suggest fetching a tag", func(t *testing.T) {
		req, p := dspRequest()
		// Same module, now pinned at a pseudo-version nothing resolves.
		for i := range req.BuildList {
			if req.BuildList[i].Path == "github.com/opentdf/platform/service" {
				req.BuildList[i].Version = "v0.11.7-0.20260101000000-badc0ffee000"
			}
		}

		_, err := p.Plan(req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pseudo-version")
		assert.NotContains(t, err.Error(), "-t ", "there is no tag to fetch")
	})
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

// Going full satisfies the root-widening member but does nothing for a later
// member whose replace leaves the repository entirely — that one still breaks
// the build. Returning as soon as the checkout went full dropped exactly those
// skips, so the rig was reported clean and failed at compile time instead.
func TestWidenSparseToRootStillReportsLaterEscapes(t *testing.T) {
	root, err := modfile.Parse("go.mod", []byte(`
module github.com/acme/repo/sub

go 1.23

replace github.com/acme/repo => ../
`), nil)
	require.NoError(t, err)

	escapes, err := modfile.Parse("go.mod", []byte(`
module github.com/acme/repo/other

go 1.23

replace github.com/acme/elsewhere => ../../elsewhere
`), nil)
	require.NoError(t, err)

	c := Checkout{Dir: "repo-x", Repo: "acme/repo", Sparse: []string{"other", "sub"}}
	members := []Member{
		// "sub" first, so the root widening happens before "other" is visited.
		{Path: "github.com/acme/repo/sub", Checkout: "repo-x", Subdir: "sub"},
		{Path: "github.com/acme/repo/other", Checkout: "repo-x", Subdir: "other"},
	}

	skips := WidenSparse(&c, members, map[string]*modfile.File{"sub": root, "other": escapes})

	assert.True(t, c.Full)
	assert.Empty(t, c.Sparse)
	require.Len(t, skips, 1)
	assert.Equal(t, SkipEscapedReplace, skips[0].Kind)
	assert.Equal(t, "github.com/acme/repo/other", skips[0].Path)
	assert.Contains(t, skips[0].Detail, "leaves acme/repo")
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
		assert.Equal(t, SkipEscapedReplace, s.Kind)
		assert.Contains(t, s.Detail, "leaves acme/repo")
	}
	assert.Equal(t, []string{"sub"}, c.Sparse, "the base set survives")
}

func TestWidenSparseSkipsFullCheckouts(t *testing.T) {
	c := Checkout{Dir: "repo-x", Full: true}
	assert.Nil(t, WidenSparse(&c, nil, nil))
	assert.True(t, c.Full)
	assert.Empty(t, c.Sparse)
}

// A full checkout has nothing to widen, but an escaped replace is unsatisfiable
// however much of the repo is present — and the primary's checkout is always
// full, so returning early hid those from the likeliest module to carry them.
func TestWidenSparseReportsEscapedReplacesOnFullCheckouts(t *testing.T) {
	f, err := modfile.Parse("go.mod", []byte(`
module github.com/acme/repo-x

go 1.24

require github.com/acme/other v1.0.0

replace github.com/acme/other => ../../other-repo
`), nil)
	require.NoError(t, err)

	c := Checkout{Dir: "repo-x", Repo: "acme/repo-x", Full: true}
	members := []Member{{Path: "github.com/acme/repo-x", Version: "v1.0.0", Checkout: "repo-x"}}

	skips := WidenSparse(&c, members, map[string]*modfile.File{"": f})

	require.Len(t, skips, 1)
	assert.Equal(t, SkipEscapedReplace, skips[0].Kind)
	assert.Equal(t, "github.com/acme/repo-x", skips[0].Path)
	assert.True(t, c.Full, "reporting must not narrow the checkout")
	assert.Empty(t, c.Sparse)
}

func TestNormaliseUse(t *testing.T) {
	assert.Equal(t, []string{"", "sdk"}, normaliseUse([]string{".", "./sdk"}))
	assert.Equal(t, []string{"", "a/b"}, normaliseUse([]string{"./a/b", ".", "a/b", "  "}))
	assert.Empty(t, normaliseUse(nil))
}

func mustCheckout(t *testing.T, m *Manifest, dir string) Checkout {
	t.Helper()
	c := m.CheckoutByDir(dir)
	require.NotNilf(t, c, "no checkout %q in %v", dir, m.Checkouts)
	return *c
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

func TestResolvePrimaryAgreesWithThePlanner(t *testing.T) {
	req, p := dspRequest()

	pc, err := p.ResolvePrimary(req.Name, req.Primary)
	require.NoError(t, err)

	// The whole two-phase scheme rests on this: `rig new` materialises the
	// primary before it has a build list, so the name it picks then must be the
	// name the planner would have picked afterwards. If these ever diverge the
	// manifest describes a directory nobody created.
	m, err := p.Plan(req)
	require.NoError(t, err)
	require.NotEmpty(t, m.Checkouts)
	planned := m.Checkouts[0]

	assert.Equal(t, "data-security-platform-v2.7.1", pc.Dir)
	assert.Equal(t, planned.Dir, pc.Dir)
	assert.Equal(t, planned.Workspace, pc.Workspace)
	assert.Equal(t, planned.Repo, pc.Repo)
	assert.Equal(t, planned.MainClone, pc.MainClone)
	assert.Equal(t, planned.Commit, pc.Commit)
	assert.Equal(t, planned.Tag, pc.Tag)

	// The primary is checked out in full: `go list` against the repo's own
	// go.work needs every module that go.work names.
	assert.True(t, pc.Full)
}

func TestPlanAdoptsThePreWarmedPrimary(t *testing.T) {
	req, p := dspRequest()
	pc, err := p.ResolvePrimary(req.Name, req.Primary)
	require.NoError(t, err)

	// Pretend the caller ended up somewhere else — a collision suffix, say.
	pc.Dir = "dsp-prewarmed"
	req.PrimaryCheckout = pc

	m, err := p.Plan(req)
	require.NoError(t, err)

	assert.Equal(t, "dsp-prewarmed", m.Checkouts[0].Dir)
	assert.True(t, m.Checkouts[0].Full, "the adopted full/sparse decision survives planning")

	// Members must point at the adopted directory, or go.work `use` paths name
	// a directory that does not exist.
	var seen int
	for _, mem := range m.Members {
		if mem.Checkout == "dsp-prewarmed" {
			seen++
		}
	}
	assert.Greater(t, seen, 0)
	assert.NoError(t, m.Validate())
}

func TestResolvePrimaryFailsLoudly(t *testing.T) {
	req, p := dspRequest()

	// A dependency we cannot reach is a skip; the primary is the rig.
	p.Locator = &fakeLocator{err: map[string]error{
		"virtru-corp/data-security-platform": errors.New("no such repository"),
	}}
	_, err := p.ResolvePrimary(req.Name, req.Primary)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such repository")

	// An unfetched tag: the revset matches nothing, and the failure has to carry
	// the fetch advice rather than just jj's own message.
	_, unfetched := dspRequest()
	unfetched.Resolver = &fakeResolver{}
	_, err = unfetched.ResolvePrimary(req.Name, req.Primary)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no matching commit")
	assert.Contains(t, err.Error(), "jj git fetch", "the error names the command that fixes it")

	// A resolver that returns an empty commit without erroring is not what jj
	// does, but the interface allows it and it must not read as success.
	_, empty := dspRequest()
	empty.Resolver = &fakeResolver{commits: map[string]string{
		`present(tags(exact:"v2.7.1"))`: "",
	}}
	_, err = empty.ResolvePrimary(req.Name, req.Primary)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jj git fetch")
}

func TestPlanSkipsUnpinnedModules(t *testing.T) {
	req, p := dspRequest()
	// What a go.work `use` sibling looks like in a build list: real module
	// path, no release behind it.
	req.BuildList = append(req.BuildList, gomod.Module{
		Path: "github.com/opentdf/platform/lib/scratch", Version: gomod.DevelVersion,
	})

	m, err := p.Plan(req)
	// A hard error here would be the old behaviour: the revset
	// tags(exact:"(devel)") resolves to nothing, and resolveAll treats that as
	// fatal. One unreleased sibling must not take the other nine checkouts
	// down with it.
	require.NoError(t, err)

	skip := findSkip(m.Skipped, "github.com/opentdf/platform/lib/scratch")
	require.NotNil(t, skip)
	assert.Equal(t, SkipUnpinned, skip.Kind)
	assert.Contains(t, skip.Detail, "names no release")

	for _, c := range m.Checkouts {
		assert.NotContains(t, c.Dir, "scratch")
	}
}

func TestPlanRejectsAnUnpinnedPrimary(t *testing.T) {
	req, p := dspRequest()
	req.Primary.Version = gomod.DevelVersion
	req.BuildList[0] = req.Primary

	_, err := p.Plan(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot check out the primary module")
	assert.Contains(t, err.Error(), string(SkipUnpinned))
}

func TestResolvePrimaryCommit(t *testing.T) {
	_, p := dspRequest()
	p.Resolver = &fakeResolver{commits: map[string]string{
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef": "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	}}

	c, err := p.ResolvePrimaryCommit("dsp-devel", "virtru-corp", "data-security-platform",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	require.NoError(t, err)

	assert.Equal(t, "virtru-corp/data-security-platform", c.Repo)
	assert.Equal(t, dspClone, c.MainClone)
	// The commit is used as the revset directly: there is no tag to go through.
	assert.Equal(t, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", c.Revset)
	assert.Empty(t, c.Tag, "an untagged commit must not be given a tag")
	assert.True(t, c.Full)
	// With no tag the directory falls back to the short commit.
	assert.Equal(t, "data-security-platform-deadbeef", c.Dir)
	assert.Equal(t, "rig-dsp-devel-deadbeef", c.Workspace)
}

func TestResolvePrimaryCommitReportsAnUnknownCommit(t *testing.T) {
	_, p := dspRequest()
	_, err := p.ResolvePrimaryCommit("dsp-devel", "virtru-corp", "data-security-platform", "cafebabe")
	require.Error(t, err)
	// Fetching tags cannot conjure a commit, so the hint must not suggest it.
	assert.Contains(t, err.Error(), "never seen")
	assert.NotContains(t, err.Error(), "-t ")
}

// TestPlanAdoptsAnUnpinnedPrimaryCheckout is the --from-binary shape: the
// artifact reports "(devel)", the caller pinned it to a vcs.revision commit and
// checked it out, and the plan has to be built around that.
func TestPlanAdoptsAnUnpinnedPrimaryCheckout(t *testing.T) {
	req, p := dspRequest()
	req.Primary.Version = gomod.DevelVersion
	req.BuildList[0] = req.Primary
	req.PrimaryCheckout = &Checkout{
		Dir:       "data-security-platform-deadbeef",
		Workspace: "rig-dsp-devel-deadbeef",
		Repo:      "virtru-corp/data-security-platform",
		MainClone: dspClone,
		Revset:    "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		Commit:    "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		Full:      true,
	}

	m, err := p.Plan(req)
	require.NoError(t, err)

	require.Len(t, m.Checkouts, 9)
	assert.Equal(t, *req.PrimaryCheckout, m.Checkouts[0], "the adopted checkout must survive verbatim")
	assert.Nil(t, findSkip(m.Skipped, req.Primary.Path), "the primary is already pinned; it must not be skipped")

	// The primary's members are still attached to it, including the two `use`
	// directories from the repo's own go.work.
	var use []string
	for _, mem := range m.Members {
		if mem.Checkout == req.PrimaryCheckout.Dir {
			use = append(use, mem.Subdir)
		}
	}
	assert.ElementsMatch(t, []string{"", "sdk"}, use)
}

// TestPlanDoesNotReResolveAnAdoptedPrimary guards the reason adoption exists:
// the version it would resolve through may not name anything.
func TestPlanDoesNotReResolveAnAdoptedPrimary(t *testing.T) {
	req, p := dspRequest()
	loc := p.Locator.(*fakeLocator)
	req.PrimaryCheckout = &Checkout{
		Dir: "dsp-adopted", Workspace: "rig-dsp-2-7-1-dsp00000",
		Repo: "virtru-corp/data-security-platform", MainClone: dspClone,
		Revset: "x", Commit: "dsp0000000000000000000000000000000000000", Full: true,
	}

	_, err := p.Plan(req)
	require.NoError(t, err)

	require.NotEmpty(t, loc.calls, "the dependencies still have to be located")
	for _, slug := range loc.calls {
		assert.NotEqual(t, "virtru-corp/data-security-platform", slug,
			"the primary's repo was already located by the caller")
	}
}

func findSkip(skips []Skip, path string) *Skip {
	for i := range skips {
		if skips[i].Path == path {
			return &skips[i]
		}
	}
	return nil
}
