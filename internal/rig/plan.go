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

// Skip kinds. Only SkipUnreachable and SkipEscapedReplace represent something
// going wrong; the rest are expected outcomes worth reporting in `wgo rig show`.
const (
	// SkipOutOfOrg is a module left to the module cache by the in-org filter.
	SkipOutOfOrg = "out-of-org"
	// SkipUnsupportedHost is a module whose path does not map to a repository
	// we know how to check out (vanity imports, gopkg.in, golang.org/x).
	SkipUnsupportedHost = "unsupported-host"
	// SkipLocalReplace is a module already served from another checkout by a
	// local replace directive, so it needs no checkout of its own.
	SkipLocalReplace = "local-replace"
	// SkipUnreachable is an in-org module whose repository could not be located
	// or cloned — private, deleted, or moved.
	SkipUnreachable = "unreachable"
	// SkipEscapedReplace is a local replace pointing outside its repository,
	// which no single checkout can satisfy.
	SkipEscapedReplace = "escaped-replace"
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

	m.Checkouts, m.Members = groupCheckouts(cands, req)
	m.GoVersion = planGoVersion(req)

	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
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
		if mod.Replace != nil && mod.Replace.Version == "" {
			skips = append(skips, Skip{
				Path: mod.Path, Version: mod.Version,
				Reason: skipReason(SkipLocalReplace, "served from "+mod.Replace.Path),
			})
			return
		}
		// The primary is what we are reproducing; it is checked out whether or
		// not the org filter would have admitted it.
		if !isPrimary && !gomod.InOrg(mod.Path, req.OrgPrefixes) {
			skips = append(skips, Skip{
				Path: mod.Path, Version: mod.Version,
				Reason: skipReason(SkipOutOfOrg, "left to the module cache"),
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
// A repository we cannot locate yields a skip: aborting a nine-checkout rig
// because one module lives in a repo we cannot reach would be hostile, and the
// remaining checkouts are still worth having. A tag we cannot resolve inside a
// repository we *can* reach is a hard error, because it means the pin is wrong
// rather than merely unavailable.
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
				Reason: skipReason(SkipUnsupportedHost, err.Error()),
			})
			continue
		}

		slug := origin.Slug()
		if reason, bad := failed[slug]; bad {
			skips = append(skips, Skip{
				Path: mod.Path, Version: mod.Version,
				Reason: skipReason(SkipUnreachable, reason),
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
					Reason: skipReason(SkipUnreachable, err.Error()),
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
				"rig: %s@%s resolves to nothing in %s (revset %s)\n"+
					"the tag may not have been fetched; try:\n  jj git fetch -R %s --remote origin",
				mod.Path, mod.Version, slug, revset, clone)
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
		c.Workspace = workspaceName(req.Name, c.Dir)
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
	var out []Member
	for _, dir := range use {
		if have[dir] {
			continue
		}
		have[dir] = true
		out = append(out, Member{
			Path:     path.Join(req.Primary.Path, dir),
			Version:  req.Primary.Version,
			Checkout: primaryCheckout,
			Subdir:   dir,
		})
	}
	return out
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
func workspaceName(rigName, dir string) string {
	return gh.SanitizeBranch(WorkspacePrefix + rigName + "-" + dir)
}

func shortCommit(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}

// planGoVersion is the `go` directive for the generated go.work: the highest
// across every member, since a workspace cannot be older than what it contains.
func planGoVersion(req Request) string {
	versions := []string{req.GoVersion, req.Primary.GoVersion}
	for _, mod := range req.BuildList {
		versions = append(versions, mod.GoVersion)
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

	var skips []Skip
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
			// needed; sparse cannot express less than that.
			if target == "" {
				c.Full = true
				c.Sparse = nil
				return skips
			}
			widened[target] = true
		}
		for _, target := range escaped {
			skips = append(skips, Skip{
				Path: mem.Path, Version: mem.Version,
				Reason: skipReason(SkipEscapedReplace,
					fmt.Sprintf("replace target %q leaves %s", target, c.Repo)),
			})
		}
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

func skipReason(kind, detail string) string {
	if detail == "" {
		return kind
	}
	return kind + ": " + detail
}

// SkipKind returns the leading kind of a Skip's reason.
func (s Skip) SkipKind() string {
	kind, _, _ := strings.Cut(s.Reason, ":")
	return kind
}

func sortSkips(skips []Skip) {
	sort.Slice(skips, func(i, j int) bool {
		if skips[i].Path != skips[j].Path {
			return skips[i].Path < skips[j].Path
		}
		return skips[i].Reason < skips[j].Reason
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
