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
			"set rig.org_prefixes in ~/.wgo/config.toml or pass --org-prefix")
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

	cands, moreSkips, err := p.resolveAll(mods)
	if err != nil {
		return nil, err
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
		if err != nil {
			return nil, nil, fmt.Errorf("rig: resolving %s@%s in %s: %w", mod.Path, mod.Version, slug, err)
		}
		if commit == "" {
			return nil, nil, fmt.Errorf(
				"rig: %s@%s resolves to nothing in %s (revset %s)\n%s",
				mod.Path, mod.Version, slug, revset, resolveHint(origin, mod.Version, clone))
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

// resolveHint suggests the command most likely to fix an empty resolution.
//
// The two cases need different advice. A tag pin resolves through
// `tags(exact:…)`, and the usual cause is simply that the tag was never
// fetched — jj does not fetch tags by default, so `-t` is the fix. A
// pseudo-version carries its own commit, and no amount of tag fetching
// conjures a commit that is not in the repo; suggesting `-t` there sends the
// user down a dead end when the real cause is a wrong repository.
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

		c := Checkout{
			Dir:       uniqueDir(usedDirs, anchor.origin.Repo, anchor.tag, anchor.commit),
			Repo:      anchor.origin.Slug(),
			MainClone: anchor.mainClone,
			Revset:    anchor.revset,
			Commit:    anchor.commit,
			Tag:       anchor.tag,
		}
		c.Workspace = workspaceName(req.Name, c.Commit)
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
		if full {
			c.Full = true
		} else {
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
	have := map[string]bool{}
	for _, mem := range members {
		if mem.Checkout == primaryCheckout {
			have[mem.Subdir] = true
		}
	}
	// The primary's path may carry a major-version suffix ("…/dsp/v2"), which is
	// part of the *root* module's path and not a directory on disk. Joining a
	// use dir onto it invents "…/dsp/v2/sdk" for source that actually lives at
	// "…/dsp/sdk/v2". Rebuild the path from the origin instead.
	origin, err := gomod.ParseOrigin(req.Primary.Path)
	if err != nil {
		origin = gomod.Origin{}
	}

	var out []Member
	for _, dir := range use {
		if have[dir] {
			continue
		}
		have[dir] = true
		out = append(out, Member{
			Path: useDirModulePath(origin, req.Primary.Path, dir),
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

// useDirModulePath is the module path of a directory in the primary's repo.
func useDirModulePath(origin gomod.Origin, primaryPath, dir string) string {
	if origin.Host == "" {
		return path.Join(primaryPath, dir) // unmappable host; best effort
	}
	repoRoot := path.Join(origin.Host, origin.Owner, origin.Repo)
	return path.Join(repoRoot, origin.Subdir, dir)
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
func WidenSparse(c *Checkout, members []Member, modFiles map[string]*modfile.File) []Skip {
	if c.Full {
		return nil
	}
	widened := map[string]bool{}
	for _, dir := range c.Sparse {
		widened[dir] = true
	}

	var (
		skips []Skip
		full  bool
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
