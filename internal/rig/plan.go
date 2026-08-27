package rig

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	gh "github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/gomod"
	"golang.org/x/mod/modfile"
)

// RepoLocator finds the main clone backing a repository, cloning it if needed.
//
// Narrow on purpose: planning is the part of `wgo rig` worth testing exhaustively,
// and a two-method surface keeps it testable without a repository on disk.
type RepoLocator interface {
	Locate(owner, repo string) (mainClone string, err error)
}

// CommitFetcher is an optional RepoLocator extension for locators that can also
// bring the clone's branch tips up to date.
//
// Tags are enough for every pin derived from a version, so Locate fetches only
// those — it runs once per repository in a rig, and a full fetch on each would
// be paid by every dependency to serve the one case that needs it.
// ResolvePrimaryCommit is that case: a bare `vcs.revision` off an untagged CI
// build is reachable from a branch and from nothing else. A locator that cannot
// do this is still usable; resolution just reports the miss.
type CommitFetcher interface {
	FetchCommits(mainClone string) error
}

// RevResolver resolves a revset within a clone to a commit id. Implementations
// are expected to fetch missing tags and retry before giving up.
type RevResolver interface {
	Resolve(mainClone, revset string) (commit string, err error)
}

// Request is everything the planner needs to lay out a rig. It carries no
// clients: resolution goes through Locator and Resolver.
type Request struct {
	// Name is the rig's directory name under rig.dir.
	Name string
	// Source records where the pins came from, for `sync` and `show`.
	Source Source
	// OrgPrefixes limits which modules get a source checkout. The primary is
	// always checked out regardless.
	OrgPrefixes []string
	// Sparse requests partial checkouts. A repo-root module gains nothing from
	// sparse and is materialised in full either way.
	Sparse bool
	// Primary is the artifact's own module.
	Primary gomod.Module
	// PrimaryCheckout, when non-nil, is a checkout the caller has already
	// materialised for the primary. The planner adopts it verbatim instead of
	// deriving its own.
	//
	// `rig new --from` has to check the primary out before it can run `go list`
	// to get the build list, so by the time planning happens the directory name
	// and the full/sparse decision are already facts on disk. Re-deriving them
	// would risk a plan that describes a directory nobody created: the derived
	// name depends on the group's anchor module, and the group is empty but for
	// the primary until the build list exists.
	PrimaryCheckout *Checkout
	// PrimaryUse mirrors the primary repository's own go.work `use` list as
	// repo-relative directories. A repo that ships a go.work builds against a
	// different build list than its root module alone, so the rig has to
	// reproduce that list to match the artifact.
	PrimaryUse []string
	// BuildList is the artifact's resolved dependency set.
	BuildList []gomod.Module
	// GoVersion is the primary's own `go` directive, folded into the maximum
	// written to go.work.
	GoVersion string
	// Baseline is the third-party dependency set to compare against later.
	Baseline map[string]string
	// Created is an RFC3339 timestamp, supplied by the caller so planning stays
	// deterministic and testable.
	Created string
	// WgoVersion is recorded for provenance.
	WgoVersion string
}

// Planner turns a build list into a rig layout.
type Planner struct {
	Locator  RepoLocator
	Resolver RevResolver
}

// candidate is a module that survived filtering, with its resolved location.
type candidate struct {
	mod       gomod.Module
	origin    gomod.Origin
	mainClone string
	revset    string
	commit    string
	tag       string
}

// Plan resolves every in-org pin and groups the results into checkouts.
//
// The returned Manifest is the plan: `--dry-run` prints it, and materialisation
// consumes it. Nothing here touches the filesystem beyond what Locator does.
func (p *Planner) Plan(req Request) (*Manifest, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("rig: plan requires a name")
	}
	if req.Primary.Path == "" {
		return nil, errors.New("rig: plan requires a primary module")
	}
	// With no prefixes the in-org filter admits nothing, so every dependency is
	// skipped and the rig is the primary alone. That is a legal manifest and a
	// useless rig, and it looks like success — so treat the empty filter as the
	// misconfiguration it almost always is rather than silently honouring it.
	if len(req.OrgPrefixes) == 0 {
		return nil, errors.New("rig: plan requires at least one org prefix; " +
			"set rig.org_prefixes in ~/.wgo/config.toml or pass --org")
	}

	m := &Manifest{
		Name:       req.Name,
		Created:    req.Created,
		WgoVersion: req.WgoVersion,
		Sparse:     req.Sparse,
		Source:     req.Source,
		Primary:    req.Primary.Path,
		PrimaryUse: normaliseUse(req.PrimaryUse),
		Baseline:   req.Baseline,
	}

	mods, skips := p.selectModules(req)
	m.Skipped = skips

	// A primary the caller already materialised is already located and pinned;
	// resolving it again would be wasted work at best and wrong at worst.
	adopted, mods, err := adoptPrimary(req, mods)
	if err != nil {
		return nil, err
	}

	cands, moreSkips, err := p.resolveAll(mods)
	if err != nil {
		return nil, err
	}
	if adopted != nil {
		cands = append(cands, *adopted)
	}
	m.Skipped = append(m.Skipped, moreSkips...)
	sortSkips(m.Skipped)

	// Every other skip degrades the rig; this one empties it of meaning. A rig
	// exists to put the artifact's own source under a debugger, so if the
	// primary got no checkout there is nothing to debug — and because skips are
	// warnings, the rig would otherwise be built, validated and reported as a
	// success with the one module that mattered quietly absent.
	if skip := skipFor(m.Skipped, req.Primary.Path); skip != nil {
		return nil, fmt.Errorf("rig: cannot check out the primary module %s: %s",
			req.Primary.Path, skip)
	}

	m.Checkouts, m.Members = groupCheckouts(cands, req)
	m.GoVersion = planGoVersion(req, m.Members)

	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// skipFor returns the skip recorded for a module path, or nil.
func skipFor(skips []Skip, modulePath string) *Skip {
	for i := range skips {
		if skips[i].Path == modulePath {
			return &skips[i]
		}
	}
	return nil
}

// selectModules applies the in-org filter and drops modules that need no
// checkout, returning the survivors sorted by path for determinism.
func (p *Planner) selectModules(req Request) ([]gomod.Module, []Skip) {
	var (
		out   []gomod.Module
		skips []Skip
		seen  = map[string]bool{}
	)

	consider := func(mod gomod.Module, isPrimary bool) {
		if mod.Path == "" || seen[mod.Path] {
			return
		}
		seen[mod.Path] = true

		// A module replaced with a directory is served from whichever checkout
		// holds that directory — most often the primary's own tree. Giving it a
		// second checkout would produce two copies of the same source and an
		// ambiguous go.work.
		//
		// The detail names the directive, not a checkout, because at this point
		// there are no checkouts to name: whether that directory actually lands
		// inside one is only decidable once the layout exists. WidenSparse makes
		// that call later and reports the misses as SkipEscapedReplace.
		if mod.Replace != nil && mod.Replace.Version == "" {
			skips = append(skips, Skip{
				Path: mod.Path, Version: mod.Version,
				Kind: SkipLocalReplace, Detail: "replaced by " + mod.Replace.Path,
			})
			return
		}
		// The primary is what we are reproducing; it is checked out whether or
		// not the org filter would have admitted it.
		if !isPrimary && !gomod.InOrg(mod.Path, req.OrgPrefixes) {
			skips = append(skips, Skip{
				Path: mod.Path, Version: mod.Version,
				Kind: SkipOutOfOrg, Detail: "left to the module cache",
			})
			return
		}
		// A "(devel)" version names no release, so no revset resolves it.
		// Filtered here rather than in resolveAll because that treats an
		// unresolvable revision as a hard error on the grounds that the repo is
		// in hand and the pin still is not in it — which points at a broken
		// module-to-repo mapping. This is not that: the mapping is fine and the
		// module simply was never released. It is a build-list fact, and the
		// build list is what this function filters.
		//
		// A primary the caller already checked out is exempt: it is pinned to a
		// commit already, which is exactly the case a binary built outside a
		// release produces — "(devel)" plus a `vcs.revision`. For any other
		// primary the skip is not the end of it, because Plan turns a skipped
		// primary into a hard error: a rig whose artifact has no checkout has
		// nothing to debug.
		preResolved := isPrimary && req.PrimaryCheckout != nil
		if !preResolved && !gomod.IsResolvableVersion(mod.Version) {
			skips = append(skips, Skip{
				Path: mod.Path, Version: mod.Version,
				Kind:   SkipUnpinned,
				Detail: unpinnedDetail(mod.Version),
			})
			return
		}
		out = append(out, mod)
	}

	consider(req.Primary, true)
	for _, mod := range req.BuildList {
		if mod.Main {
			continue // the primary, already considered
		}
		consider(mod, false)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, skips
}

// adoptPrimary turns a primary checkout the caller already materialised into a
// resolved candidate, and drops the primary from the list still to resolve.
//
// Returns a nil candidate when there is nothing to adopt, either because the
// caller supplied no checkout or because selectModules already dropped the
// primary. The second case is left to Plan's own skipped-primary check, which
// explains it far better than anything this function could say.
func adoptPrimary(req Request, mods []gomod.Module) (*candidate, []gomod.Module, error) {
	pc := req.PrimaryCheckout
	if pc == nil {
		return nil, mods, nil
	}
	rest := make([]gomod.Module, 0, len(mods))
	var primary *gomod.Module
	for i := range mods {
		if mods[i].Path == req.Primary.Path {
			primary = &mods[i]
			continue
		}
		rest = append(rest, mods[i])
	}
	if primary == nil {
		return nil, mods, nil
	}
	// The origin is still needed: it supplies the subdir every member of this
	// checkout is keyed by, and the slug the grouping key is built from.
	origin, err := gomod.ParseOrigin(req.Primary.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("rig: primary module %s: %w", req.Primary.Path, err)
	}
	return &candidate{
		mod: *primary, origin: origin, mainClone: pc.MainClone,
		revset: pc.Revset, commit: pc.Commit, tag: pc.Tag,
	}, rest, nil
}

// unpinnedDetail explains an unresolvable version in the terms of whatever
// produced it, since the fix differs.
func unpinnedDetail(version string) string {
	switch strings.TrimSpace(version) {
	case gomod.DevelVersion:
		return "built from a working copy or supplied by a go.work, so it names no release"
	case "":
		return "no version recorded"
	default:
		return fmt.Sprintf("version %q is not a release or pseudo-version", version)
	}
}

// resolveAll maps each module to a repository and commit.
//
// The two failures are treated differently on purpose. A repository we cannot
// *locate* yields a skip: aborting a nine-checkout rig because one module lives
// in a repo we cannot reach would be hostile, and the remaining checkouts are
// still worth having. A revision we cannot *resolve* inside a repository we can
// reach is a hard error, because the repo is in hand and the pin still does not
// exist in it — which usually means the module-to-repo mapping is wrong, and
// every other checkout derived from that mapping is suspect too.
//
// The caller is responsible for the one skip that must not stay a skip: an
// unreachable *primary* leaves the rig with no copy of the artifact it exists
// to debug. Plan checks for that after this returns.
func (p *Planner) resolveAll(mods []gomod.Module) ([]candidate, []Skip, error) {
	var (
		cands []candidate
		skips []Skip
		// One Locate call per repo, not per module: a monorepo contributes many
		// modules and cloning is expensive.
		clones = map[string]string{}
		failed = map[string]string{}
	)

	for _, mod := range mods {
		origin, err := gomod.ParseOrigin(mod.Path)
		if err != nil {
			skips = append(skips, Skip{
				Path: mod.Path, Version: mod.Version,
				Kind: SkipUnsupportedHost, Detail: err.Error(),
			})
			continue
		}

		slug := origin.Slug()
		if reason, bad := failed[slug]; bad {
			skips = append(skips, Skip{
				Path: mod.Path, Version: mod.Version,
				Kind: SkipUnreachable, Detail: reason,
			})
			continue
		}
		clone, ok := clones[slug]
		if !ok {
			clone, err = p.Locator.Locate(origin.Owner, origin.Repo)
			if err != nil {
				failed[slug] = err.Error()
				skips = append(skips, Skip{
					Path: mod.Path, Version: mod.Version,
					Kind: SkipUnreachable, Detail: err.Error(),
				})
				continue
			}
			clones[slug] = clone
		}

		revset := origin.Revset(mod.Version)
		commit, err := p.Resolver.Resolve(clone, revset)
		if err != nil || commit == "" {
			return nil, nil, resolveFailure(origin, mod.Path, mod.Version, clone, revset, err)
		}

		tag := ""
		if gomod.PseudoCommit(mod.Version) == "" {
			tag = origin.TagFor(mod.Version)
		}
		cands = append(cands, candidate{
			mod: mod, origin: origin, mainClone: clone,
			revset: revset, commit: commit, tag: tag,
		})
	}
	return cands, skips, nil
}

// ResolvePrimaryRepo describes the primary's checkout from a repository
// reference rather than a module path, which is what `--from owner/repo@ref`
// supplies.
//
// The module path is not knowable yet: it lives in the go.mod at that tag, and
// reading it means checking the repository out first. Owner, repo and tag are
// enough to do that, so the caller resolves the module path from the working
// copy this describes.
//
// ref is used verbatim as the tag. A repo-root module's tag is its version, so
// for the common case the two coincide.
func (p *Planner) ResolvePrimaryRepo(rigName, owner, repo, ref string) (*Checkout, error) {
	if owner == "" || repo == "" || ref == "" {
		return nil, errors.New("rig: resolving the primary requires owner, repo and a ref")
	}
	clone, err := p.Locator.Locate(owner, repo)
	if err != nil {
		return nil, fmt.Errorf("rig: locating %s/%s: %w", owner, repo, err)
	}
	revset := fmt.Sprintf(`present(tags(exact:%q))`, ref)
	commit, err := p.Resolver.Resolve(clone, revset)
	if err != nil || commit == "" {
		detail := "resolved to nothing"
		if err != nil {
			detail = err.Error()
		}
		return nil, fmt.Errorf(
			"rig: resolving %s in %s/%s (revset %s): %s\n"+
				"the tag may not have been fetched (jj does not fetch tags by default); try:\n"+
				"  jj git fetch -R %s --remote origin -t %s",
			ref, owner, repo, revset, detail, clone, ref)
	}
	c := &Checkout{
		Dir:       uniqueDir(nil, repo, ref, commit),
		Repo:      owner + "/" + repo,
		MainClone: clone,
		Revset:    revset,
		Commit:    commit,
		Tag:       ref,
		Full:      true,
	}
	c.Workspace = workspaceName(rigName, commit)
	return c, nil
}

// ResolvePrimaryCommit describes the primary's checkout from a bare commit id.
//
// This is what a binary built outside a release supplies: `go version -m`
// records its main module as "(devel)" and the commit separately, under the
// `vcs.revision` build setting. There is no tag to resolve through, so the
// commit is used as the revset directly and Tag is left empty — which is also
// what tells `rig show` to display a hash rather than invent a version.
func (p *Planner) ResolvePrimaryCommit(rigName, owner, repo, rev string) (*Checkout, error) {
	if owner == "" || repo == "" || rev == "" {
		return nil, errors.New("rig: resolving the primary requires owner, repo and a commit")
	}
	clone, err := p.Locator.Locate(owner, repo)
	if err != nil {
		return nil, fmt.Errorf("rig: locating %s/%s: %w", owner, repo, err)
	}
	commit, err := p.Resolver.Resolve(clone, rev)
	if err != nil || commit == "" {
		// Every other pin resolves through a tag, and the locator fetches tags
		// for exactly that reason — but `jj git fetch --tag glob:*` updates no
		// bookmarks, so a commit that is only reachable from a branch tip is
		// not in the clone even though the tags are current. That is the normal
		// case here: a binary built by CI off an untagged commit. Fetch the
		// branches once and retry before reporting a miss.
		if f, ok := p.Locator.(CommitFetcher); ok {
			if ferr := f.FetchCommits(clone); ferr == nil {
				commit, err = p.Resolver.Resolve(clone, rev)
			}
		}
	}
	if err != nil || commit == "" {
		detail := "resolved to nothing"
		if err != nil {
			detail = err.Error()
		}
		return nil, fmt.Errorf(
			"rig: resolving commit %s in %s/%s: %s\n"+
				"the binary may have been built from a commit this clone has never seen, "+
				"or from one that was never pushed; try:\n"+
				"  jj git fetch -R %s --remote origin",
			rev, owner, repo, detail, clone)
	}
	c := &Checkout{
		Dir:       uniqueDir(nil, repo, "", commit),
		Repo:      owner + "/" + repo,
		MainClone: clone,
		Revset:    rev,
		Commit:    commit,
		Full:      true,
	}
	c.Workspace = workspaceName(rigName, commit)
	return c, nil
}

// ResolvePrimary describes the checkout for the primary module alone, so
// `rig new` can materialise it before it has a build list to plan from.
//
// The result is meant to be handed straight back as Request.PrimaryCheckout;
// the planner then adopts it rather than deriving a second, possibly different,
// directory name.
//
// The checkout is always full. The build list is computed by running `go list`
// inside it against the repository's own go.work, and that go.work names
// sibling modules which `go list` fails on if they are missing — so there is
// nothing useful to narrow to at this point.
//
// Unlike a dependency, an unreachable or unresolvable primary is fatal: there
// is no rig to build without it.
func (p *Planner) ResolvePrimary(rigName string, primary gomod.Module) (*Checkout, error) {
	if primary.Path == "" {
		return nil, errors.New("rig: resolving the primary requires a module path")
	}
	origin, err := gomod.ParseOrigin(primary.Path)
	if err != nil {
		return nil, fmt.Errorf("rig: primary module %s: %w", primary.Path, err)
	}
	clone, err := p.Locator.Locate(origin.Owner, origin.Repo)
	if err != nil {
		return nil, fmt.Errorf("rig: locating %s for primary %s: %w", origin.Slug(), primary.Path, err)
	}
	revset := origin.Revset(primary.Version)
	commit, err := p.Resolver.Resolve(clone, revset)
	if err != nil || commit == "" {
		return nil, resolveFailure(origin, primary.Path, primary.Version, clone, revset, err)
	}

	tag := ""
	if gomod.PseudoCommit(primary.Version) == "" {
		tag = origin.TagFor(primary.Version)
	}
	c := &Checkout{
		Dir:       uniqueDir(nil, origin.Repo, tag, commit),
		Repo:      origin.Slug(),
		MainClone: clone,
		Revset:    revset,
		Commit:    commit,
		Tag:       tag,
		Full:      true,
	}
	c.Workspace = workspaceName(rigName, commit)
	return c, nil
}

// resolveHint suggests the command most likely to fix an empty resolution.
//
// The two cases need different advice. A tag pin resolves through
// `tags(exact:…)`, and the usual cause is simply that the tag was never
// fetched — jj does not fetch tags by default, so `-t` is the fix. A
// pseudo-version carries its own commit, and no amount of tag fetching
// conjures a commit that is not in the repo; suggesting `-t` there sends the
// user down a dead end when the real cause is a wrong repository.
// resolveFailure explains a pin that did not resolve, whether the resolver
// returned an error or an empty commit.
//
// The two used to be separate branches, and only one of them carried the hint —
// the one that never fires. A RevResolver reports a revset matching nothing as
// an error ("no matching commit"), so in production the empty-commit branch is
// reachable only from a test double, and the advice that makes the failure
// actionable never reached a user. The interface still permits an empty commit,
// so both are funnelled here rather than one being deleted.
func resolveFailure(origin gomod.Origin, modPath, version, clone, revset string, err error) error {
	detail := "resolved to nothing"
	if err != nil {
		detail = err.Error()
	}
	return fmt.Errorf("rig: resolving %s@%s in %s (revset %s): %s\n%s",
		modPath, version, origin.Slug(), revset, detail,
		resolveHint(origin, version, clone))
}

func resolveHint(origin gomod.Origin, version, clone string) string {
	if gomod.PseudoCommit(version) != "" {
		return fmt.Sprintf(
			"a pseudo-version names a commit directly, so this is not a missing tag;\n"+
				"check that %s is really published from %s, then:\n"+
				"  jj git fetch -R %s --remote origin",
			origin.Slug(), clone, clone)
	}
	return fmt.Sprintf(
		"the tag may not have been fetched (jj does not fetch tags by default); try:\n"+
			"  jj git fetch -R %s --remote origin -t %s",
		clone, origin.TagFor(version))
}

// groupCheckouts collapses candidates onto one checkout per (repo, commit).
//
// Modules released from the same commit of a monorepo — normal when one release
// tags a whole tree — would otherwise get byte-identical working copies.
func groupCheckouts(cands []candidate, req Request) ([]Checkout, []Member) {
	type group struct {
		anchor  candidate
		primary bool
		members []candidate
	}
	var (
		order  []*group
		groups = map[string]*group{}
	)
	for _, c := range cands {
		key := c.origin.Slug() + "@" + c.commit
		g, ok := groups[key]
		if !ok {
			g = &group{}
			groups[key] = g
			order = append(order, g)
		}
		g.members = append(g.members, c)
		if c.mod.Path == req.Primary.Path {
			g.primary = true
		}
	}
	for _, g := range order {
		g.anchor = anchorFor(g.members)
	}

	// Order for a reader, not for a machine: the artifact being reproduced
	// first, then each repo's checkouts in tag order. Sorting on the grouping
	// key would be just as deterministic but would interleave the repos by
	// commit hash, which tells nobody anything.
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if a.primary != b.primary {
			return a.primary
		}
		if a.anchor.origin.Slug() != b.anchor.origin.Slug() {
			return a.anchor.origin.Slug() < b.anchor.origin.Slug()
		}
		if a.anchor.tag != b.anchor.tag {
			return a.anchor.tag < b.anchor.tag
		}
		return a.anchor.commit < b.anchor.commit
	})

	var (
		checkouts []Checkout
		members   []Member
		usedDirs  = map[string]bool{}
	)
	for _, g := range order {
		anchor := g.anchor

		// An adopted primary checkout is already on disk; its directory name
		// and full/sparse decision are facts, not choices left to make.
		adopted := g.primary && req.PrimaryCheckout != nil

		var c Checkout
		if adopted {
			c = *req.PrimaryCheckout
		} else {
			c = Checkout{
				Dir:       uniqueDir(usedDirs, anchor.origin.Repo, anchor.tag, anchor.commit),
				Repo:      anchor.origin.Slug(),
				MainClone: anchor.mainClone,
				Revset:    anchor.revset,
				Commit:    anchor.commit,
				Tag:       anchor.tag,
			}
			c.Workspace = workspaceName(req.Name, c.Commit)
		}
		usedDirs[c.Dir] = true

		sparse := map[string]bool{}
		full := !req.Sparse
		for _, mem := range g.members {
			members = append(members, Member{
				Path:     mem.mod.Path,
				Version:  mem.mod.Version,
				Checkout: c.Dir,
				Subdir:   mem.origin.Subdir,
				Indirect: mem.mod.Indirect,
			})
			// A module at the repository root already needs the whole tree;
			// sparse would only add bookkeeping.
			if mem.origin.Subdir == "" {
				full = true
			} else {
				sparse[mem.origin.Subdir] = true
			}
		}
		switch {
		case adopted:
			// The caller already narrowed (or did not narrow) this working
			// copy. Recording a different decision here would put the manifest
			// out of step with the disk, and the sparse set is what `rig
			// verify` and the widening pass both read.
		case full:
			c.Full = true
		default:
			c.Sparse = sortedKeys(sparse)
		}
		checkouts = append(checkouts, c)
	}

	// The primary's extra `use` entries come from the repo's own go.work rather
	// than the build list, so they are not modules in their own right. Attach
	// them to the primary's checkout as members with no version of their own.
	members = append(members, primaryUseMembers(req, members)...)

	// Members follow their checkout's order so rig.toml reads top-to-bottom the
	// same way go.work does.
	pos := make(map[string]int, len(checkouts))
	for i, c := range checkouts {
		pos[c.Dir] = i
	}
	sort.Slice(members, func(i, j int) bool {
		if pos[members[i].Checkout] != pos[members[j].Checkout] {
			return pos[members[i].Checkout] < pos[members[j].Checkout]
		}
		return members[i].UseDir() < members[j].UseDir()
	})
	return checkouts, members
}

// primaryUseMembers promotes the directories the primary repo's own go.work
// uses, minus any already present as build-list members.
func primaryUseMembers(req Request, members []Member) []Member {
	use := normaliseUse(req.PrimaryUse)
	if len(use) == 0 {
		return nil
	}
	primaryCheckout := ""
	for _, mem := range members {
		if mem.Path == req.Primary.Path {
			primaryCheckout = mem.Checkout
			break
		}
	}
	if primaryCheckout == "" {
		return nil
	}
	haveDir := map[string]bool{}
	havePath := map[string]bool{}
	for _, mem := range members {
		if mem.Checkout == primaryCheckout {
			haveDir[mem.Subdir] = true
		}
		// Across every checkout, not just the primary's. A monorepo module the
		// primary's go.work uses from its own tree can also be in the build
		// list under a released version, which gets a checkout of its own at a
		// different commit — two directories, one module path. go.work rejects
		// that outright ("module ... appears multiple times in workspace"), so
		// the whole rig fails to build over a duplicate neither entry knows
		// about. The build-list entry wins: it carries a version, so it is a
		// real pin that `rig verify` can check, whereas a use directory is
		// versionless by construction.
		havePath[mem.Path] = true
	}
	byDir := localReplacePaths(req.BuildList)
	origin, err := gomod.ParseOrigin(req.Primary.Path)
	if err != nil {
		origin = gomod.Origin{}
	}

	var out []Member
	for _, dir := range use {
		if haveDir[dir] {
			continue
		}
		haveDir[dir] = true
		modPath, ok := byDir[dir]
		if !ok {
			modPath = useDirModulePath(origin, req.Primary.Path, dir)
		}
		if havePath[modPath] {
			continue
		}
		havePath[modPath] = true
		out = append(out, Member{
			Path: modPath,
			// No version: these directories come from the primary repo's
			// go.work, not from the build list, so nothing pinned them. Copying
			// the primary's version would assert a pin that does not exist and
			// would put a bogus entry in front of `wgo rig verify`.
			Checkout: primaryCheckout,
			Subdir:   dir,
		})
	}
	return out
}

// localReplacePaths maps a directory in the primary's repository to the module
// path the build list records for it.
//
// A `use` directory the primary depends on is already in the build list, served
// by a replace pointing at that very directory, and that entry carries the one
// thing nothing else knows: the module's real path, major-version suffix and
// all. `replace …/dsp/sdk/v2 => ./sdk` says the source under "sdk" is module
// "…/dsp/sdk/v2" — a fact not derivable from the primary's own path, since the
// two modules version independently.
func localReplacePaths(buildList []gomod.Module) map[string]string {
	out := map[string]string{}
	for _, mod := range buildList {
		// Version != "" is a replace onto another *module*, which says nothing
		// about any directory in this repository.
		if mod.Path == "" || mod.Replace == nil || mod.Replace.Version != "" {
			continue
		}
		dir := repoRelativeReplace(mod.Replace.Path)
		if dir == "" {
			continue
		}
		out[dir] = mod.Path
	}
	return out
}

// repoRelativeReplace reduces a local replace target to a directory within the
// repository, or "" when it is not one. The repository root is "" as well: that
// is the primary itself, which is already a member.
func repoRelativeReplace(target string) string {
	if !strings.HasPrefix(target, "./") && !strings.HasPrefix(target, "../") {
		return "" // a module path, or an absolute path we cannot place
	}
	dir := path.Clean(target)
	if dir == "." || strings.HasPrefix(dir, "..") {
		return ""
	}
	return dir
}

// useDirModulePath guesses the module path of a directory in the primary's
// repo, for a `use` directory the build list has no entry for.
//
// dir is relative to the checkout root, not to the primary module, because a
// go.work covering a subdirectory module lives above it: platform's names
// ./otdfctl and ./sdk as siblings, so joining them onto the otdfctl primary
// would invent github.com/opentdf/platform/otdfctl/sdk.
//
// Even so it is only a guess. The repository root is not a module path — a
// submodule carries its own major-version suffix, independent of the root
// module's, and nothing here can recover it: "sdk" under opentdf/platform may
// be ".../platform/sdk" or ".../platform/sdk/v2". See localReplacePaths for the
// case where the build list knows the answer.
func useDirModulePath(origin gomod.Origin, primaryPath, dir string) string {
	if origin.Host == "" {
		return path.Join(primaryPath, dir) // unmappable host; best effort
	}
	repoRoot := path.Join(origin.Host, origin.Owner, origin.Repo)
	return path.Join(repoRoot, dir)
}

// anchorFor picks the candidate a shared checkout is named after: the module
// closest to the repository root, tie-broken by path so the choice is stable.
func anchorFor(members []candidate) candidate {
	best := members[0]
	for _, c := range members[1:] {
		bd := strings.Count(best.origin.Subdir, "/")
		cd := strings.Count(c.origin.Subdir, "/")
		switch {
		case best.origin.Subdir == "":
			continue
		case c.origin.Subdir == "":
			best = c
		case cd < bd, cd == bd && c.mod.Path < best.mod.Path:
			best = c
		}
	}
	return best
}

// uniqueDir builds the checkout's directory name, disambiguating with the
// commit when two repositories would otherwise collide on one tag name.
func uniqueDir(used map[string]bool, repo, tag, commit string) string {
	anchor := tag
	if anchor == "" {
		anchor = shortCommit(commit)
	}
	dir := gh.SanitizeBranch(repo + "-" + anchor)
	if dir == "" {
		dir = shortCommit(commit)
	}
	if !used[dir] {
		return dir
	}
	return dir + "-" + shortCommit(commit)
}

// workspaceName is the jj workspace registered in the main clone. The rig
// prefix is what lets `wgo clean` and `wgo doctor` recognise it from the repo
// side, where the rig directory is not in view.
//
// The commit, not the checkout dir, is the discriminator, and it is appended
// *after* sanitising: gh.SanitizeBranch truncates at 60 characters, so building
// the whole name and sanitising it as one string let the truncation eat the
// part that made it unique. Two checkouts of one rig would then be handed the
// same workspace name, and `jj workspace add --name` fails on the second —
// partway through materialisation, with the manifest not yet written and the
// earlier workspaces stranded. Manifest.Validate caps Name so the suffix always
// fits, and rejects a duplicate outright as a backstop.
func workspaceName(rigName, commit string) string {
	return gh.SanitizeBranch(WorkspacePrefix+rigName) + "-" + shortCommit(commit)
}

func shortCommit(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}

// planGoVersion is the `go` directive for the generated go.work: the highest
// across the modules the workspace actually contains, since a workspace cannot
// be older than its members.
//
// Members, not the whole build list. A `use` directive is what forces the
// workspace's `go` version up; a dependency left to the module cache is
// compiled under its own go.mod and constrains nothing. Folding the build list
// in let one out-of-org dependency with a newer `go` directive raise the rig's
// go.work above what any checked-out module needs — which then demands a newer
// toolchain than the artifact was ever built with.
//
// The lookup is by module path, so it depends on members carrying the path the
// build list uses: a synthesised one never matches and silently contributes
// nothing, which is what localReplacePaths exists to prevent. A `use` directory
// the primary does not depend on has no build-list entry at all and so cannot
// be accounted for here; its go.mod is only readable once the checkout exists.
func planGoVersion(req Request, members []Member) string {
	byPath := make(map[string]string, len(req.BuildList))
	for _, mod := range req.BuildList {
		byPath[mod.Path] = mod.GoVersion
	}
	versions := []string{req.GoVersion, req.Primary.GoVersion}
	for _, mem := range members {
		versions = append(versions, byPath[mem.Path])
	}
	return gomod.MaxGoVersion(versions...)
}

// WidenSparse extends a checkout's sparse set to cover the directories its
// modules' local replace directives point at.
//
// Without this the replaced module's source is simply absent from the working
// copy and the build fails with a confusing "directory not found". Targets that
// leave the repository cannot be satisfied by any single checkout and come back
// as skips.
//
// modFiles is keyed by the module's subdir within the repository, matching
// Member.Subdir, and may omit modules whose go.mod could not be read.
//
// A checkout that is already full has nothing to widen, but it is still
// scanned: a replace target outside the repository is unsatisfiable no matter
// how much of the repository is present, and the primary's checkout is always
// full, so skipping the scan hid those targets from the one module most likely
// to carry them.
func WidenSparse(c *Checkout, members []Member, modFiles map[string]*modfile.File) []Skip {
	widened := map[string]bool{}
	for _, dir := range c.Sparse {
		widened[dir] = true
	}

	var (
		skips []Skip
		full  = c.Full
	)
	for _, mem := range members {
		if mem.Checkout != c.Dir {
			continue
		}
		f, ok := modFiles[mem.Subdir]
		if !ok {
			continue
		}
		inRepo, escaped := gomod.LocalReplaceTargets(f, mem.Subdir)
		for _, target := range inRepo {
			// A replace resolving to the repository root means the whole tree is
			// needed; sparse cannot express less than that. Note it and keep
			// scanning rather than returning: the escaped replaces of the
			// members not yet visited are the ones that will break the build,
			// and going full does not fix a single one of them.
			if target == "" {
				full = true
				continue
			}
			widened[target] = true
		}
		for _, target := range escaped {
			skips = append(skips, Skip{
				Path: mem.Path, Version: mem.Version,
				Kind:   SkipEscapedReplace,
				Detail: fmt.Sprintf("replace target %q leaves %s", target, c.Repo),
			})
		}
	}
	if full {
		c.Full = true
		c.Sparse = nil
		return skips
	}
	c.Sparse = sortedKeys(widened)
	return skips
}

// normaliseUse cleans go.work `use` paths into plain repo-relative directories,
// with the repository root as "".
func normaliseUse(dirs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		d = path.Clean(d)
		if d == "." {
			d = ""
		}
		d = strings.TrimPrefix(d, "./")
		if seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func sortSkips(skips []Skip) {
	sort.Slice(skips, func(i, j int) bool {
		if skips[i].Path != skips[j].Path {
			return skips[i].Path < skips[j].Path
		}
		if skips[i].Kind != skips[j].Kind {
			return skips[i].Kind < skips[j].Kind
		}
		return skips[i].Detail < skips[j].Detail
	})
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
