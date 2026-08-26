package rig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/virtru/wgo/internal/gomod"
	"github.com/virtru/wgo/internal/jj"
)

// checkoutKey identifies a checkout by what it holds rather than by where it
// sits.
//
// Dir and Workspace are persisted and never recomputed, precisely because they
// carry a collision suffix that depends on what else existed when the rig was
// built — so a freshly resolved plan can legitimately name the same commit of
// the same repository a different directory. The repository and the commit are
// the pin, and Manifest.Validate already guarantees no two checkouts share one.
func checkoutKey(c Checkout) string { return c.Repo + "@" + c.Commit }

// Widening is a sparse set that has to grow.
//
// Set is applied verbatim, so it is the whole pattern list rather than the
// delta: `jj sparse set --clear --add …` replaces the set, and sending only the
// new directories would drop everything already materialised.
type Widening struct {
	// Dir is the checkout's directory name under src/.
	Dir string
	// Set is the pattern list to apply, ["."] for a checkout going full.
	Set []string
	// Added is what the widening newly materialises, for reporting.
	Added []string
}

// Diff is the work needed to bring a rig on disk in line with a manifest.
//
// Add and Restore are separated because they need different handling, not just
// different prose: a restored checkout's jj workspace may still be registered
// in the main clone even though its directory is gone, and `jj workspace add`
// fails on a duplicate name.
type Diff struct {
	// Add are checkouts the plan wants that the rig does not have.
	Add []Checkout
	// Restore are recorded checkouts whose directory has gone missing.
	Restore []Checkout
	// Remove are recorded checkouts the plan no longer wants.
	Remove []Checkout
	// Widen are checkouts whose sparse set has to grow.
	Widen []Widening
}

// Empty reports whether the rig on disk already matches the manifest.
func (d *Diff) Empty() bool {
	return d == nil || len(d.Add)+len(d.Restore)+len(d.Remove)+len(d.Widen) == 0
}

// Summary describes the diff in one line.
func (d *Diff) Summary() string {
	if d.Empty() {
		return "no changes"
	}
	var parts []string
	for _, p := range []struct {
		n     int
		label string
	}{
		{len(d.Add), "to add"},
		{len(d.Restore), "to restore"},
		{len(d.Widen), "to widen"},
		{len(d.Remove), "obsolete"},
	} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.label))
		}
	}
	return strings.Join(parts, ", ")
}

// Reconcile matches a recorded rig against a freshly resolved plan and returns
// the manifest to save plus the work needed to make disk match it.
//
// The plan decides *what* the rig holds — members, baseline, skips, the go
// directive. The recorded rig decides *where*: a checkout the plan and the
// manifest agree on keeps the directory and workspace name it already has, and
// the plan's members are remapped onto it. Renaming a directory that a user may
// have an editor, a debugger and a shell open in, over a suffix that shifted
// because some unrelated module joined the rig, would be gratuitous.
//
// onDisk reports whether a checkout directory exists; a nil func assumes they
// all do, which is what a unit test wants.
//
// Sparse sets only ever grow. Narrowing one deletes files from a working copy
// the user may have edited, and the cost of keeping a directory the rig no
// longer needs is disk rather than correctness.
func Reconcile(have, want *Manifest, onDisk func(dir string) bool) (*Manifest, *Diff, error) {
	if have == nil || want == nil {
		return nil, nil, errors.New("rig: reconcile needs both the recorded rig and a plan")
	}
	if onDisk == nil {
		onDisk = func(string) bool { return true }
	}

	existing := map[string]*Checkout{}
	for i := range have.Checkouts {
		existing[checkoutKey(have.Checkouts[i])] = &have.Checkouts[i]
	}

	merged := *want
	merged.Name = have.Name
	// The rig was created once. A sync re-resolves its pins; it does not make a
	// new rig, and overwriting this would lose the only record of when the
	// artifact was captured.
	merged.Created = have.Created
	// Freeze state is the user's, not the plan's. frozenReplaces already drops
	// any pin whose module has left the baseline, so a re-resolution that
	// changes the dependency set cannot leave a dangling replace behind.
	merged.Frozen = have.Frozen
	merged.Checkouts = nil
	merged.Members = nil
	// Copied because ApplyDiff appends the widening pass's skips to it, and
	// growing a slice the caller still holds a header for is a good way to have
	// two manifests disagree about what was skipped.
	merged.Skipped = append([]Skip(nil), want.Skipped...)

	diff := &Diff{}
	dirFor := make(map[string]string, len(want.Checkouts))
	kept := map[string]bool{}

	for _, w := range want.Checkouts {
		key := checkoutKey(w)
		c := w
		if old, ok := existing[key]; ok {
			kept[key] = true
			c.Dir, c.Workspace, c.MainClone = old.Dir, old.Workspace, old.MainClone
			c.Full, c.Sparse = unionSparse(*old, w)
			switch {
			case !onDisk(c.Dir):
				diff.Restore = append(diff.Restore, c)
			default:
				if wd, grew := widening(*old, c); grew {
					diff.Widen = append(diff.Widen, wd)
				}
			}
		} else {
			diff.Add = append(diff.Add, c)
		}
		dirFor[w.Dir] = c.Dir
		merged.Checkouts = append(merged.Checkouts, c)
	}

	for _, mem := range want.Members {
		mem.Checkout = dirFor[mem.Checkout]
		merged.Members = append(merged.Members, mem)
	}

	for _, old := range have.Checkouts {
		if !kept[checkoutKey(old)] {
			diff.Remove = append(diff.Remove, old)
		}
	}

	if err := merged.Validate(); err != nil {
		return nil, nil, err
	}
	return &merged, diff, nil
}

// unionSparse combines two views of one checkout's materialised set, never
// narrowing. Full wins over any pattern list: a checkout that is already whole
// cannot be made more partial without deleting files.
func unionSparse(old, want Checkout) (full bool, sparse []string) {
	if old.Full || want.Full {
		return true, nil
	}
	set := map[string]bool{}
	for _, d := range old.Sparse {
		set[d] = true
	}
	for _, d := range want.Sparse {
		set[d] = true
	}
	return false, sortedKeys(set)
}

// widening reports what has to be re-materialised for old to hold want's set.
func widening(old, want Checkout) (Widening, bool) {
	if old.Full {
		return Widening{}, false
	}
	have := map[string]bool{}
	for _, d := range old.Sparse {
		have[d] = true
	}
	if want.Full {
		// "." is jj's own spelling of a full working copy, and what
		// `jj sparse list` prints for one.
		return Widening{Dir: old.Dir, Set: []string{"."}, Added: []string{"."}}, true
	}
	var added []string
	for _, d := range want.Sparse {
		if !have[d] {
			added = append(added, d)
		}
	}
	if len(added) == 0 {
		return Widening{}, false
	}
	return Widening{Dir: old.Dir, Set: want.Sparse, Added: added}, true
}

// ApplyDiff brings the rig under rigRoot in line with m.
//
// Creations come first and are recorded for rollback, so a failure anywhere
// after them takes the new checkouts back down and leaves the rig as it was.
// The manifest is written last, for the same reason Materialize writes it last:
// it is the record `wgo rig rm` reads to find the workspaces it must forget, so
// advertising a checkout before it exists strands it.
//
// Obsolete checkouts are removed only when prune is set, and only after the
// manifest that no longer names them has been saved. Removing a working copy is
// the one irreversible thing a sync can do, and a rig checkout is somewhere a
// user may have uncommitted debugging edits.
func (mz *Materializer) ApplyDiff(m *Manifest, d *Diff, rigRoot string, prune bool) (retErr error) {
	if !filepath.IsAbs(rigRoot) {
		return fmt.Errorf("rig: rig root must be absolute, got %q", rigRoot)
	}
	if err := m.Validate(); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			mz.Rollback(rigRoot)
		}
	}()

	if err := mz.ensureRoot(rigRoot); err != nil {
		return err
	}
	srcRoot := filepath.Join(rigRoot, SrcDir)

	for i := range d.Restore {
		c := &d.Restore[i]
		// A checkout this Materializer already made — `rig sync` re-materialises
		// the primary before it can re-read the build list — is on disk and on
		// the rollback list already.
		if mz.alreadyMade(c.Workspace) {
			continue
		}
		mz.logf("restoring %s @ %s (its checkout is missing)", c.Repo, pinLabel(*c))
		// The directory is gone but the workspace may still be registered in
		// the main clone, and `jj workspace add --name` fails on a duplicate.
		// A stale registration is the expected case here, so a failure to
		// forget is not worth stopping for — the add that follows reports it
		// far more precisely.
		if err := mz.JJ.WorkspaceForget(c.MainClone, c.Workspace); err != nil {
			mz.logf("note: %s was not registered in %s (%v)", c.Workspace, c.MainClone, err)
		}
		if err := mz.create(c, filepath.Join(srcRoot, c.Dir)); err != nil {
			return err
		}
	}
	for i := range d.Add {
		c := &d.Add[i]
		if mz.alreadyMade(c.Workspace) {
			continue
		}
		mz.logf("adding %s @ %s", c.Repo, pinLabel(*c))
		if err := mz.create(c, filepath.Join(srcRoot, c.Dir)); err != nil {
			return err
		}
	}
	for _, w := range d.Widen {
		mz.logf("widening %s to cover %s", w.Dir, strings.Join(w.Added, ", "))
		dest := filepath.Join(srcRoot, w.Dir)
		if err := mz.JJ.SparseSet(dest, jj.SparseSetOpts{Clear: true, Add: w.Set}); err != nil {
			return fmt.Errorf("rig: widening %s to %s: %w", w.Dir, strings.Join(w.Set, ", "), err)
		}
	}

	// Every checkout, not only the changed ones: a new member on an existing
	// checkout brings its own local replaces, and its go.mod has to be there.
	for i := range m.Checkouts {
		c := &m.Checkouts[i]
		dest := filepath.Join(srcRoot, c.Dir)
		if err := verifyMembers(m, c, dest); err != nil {
			return err
		}
		skips, err := mz.widen(m, c, dest)
		if err != nil {
			return err
		}
		m.Skipped = append(m.Skipped, skips...)
	}
	// Deduped, unlike Materialize's one-shot pass: a sync re-runs the widening
	// over checkouts whose skips the manifest already records, so without this
	// every sync would grow the skip list by the same escaped replaces again.
	m.Skipped = dedupeSkips(m.Skipped)

	if err := mz.writeGoWork(m, rigRoot); err != nil {
		return err
	}
	for _, f := range GeneratedFiles(m, rigRoot) {
		if err := writeFile(filepath.Join(rigRoot, f.Name), f.Content, f.Mode); err != nil {
			return err
		}
	}
	if err := Save(rigRoot, m); err != nil {
		return err
	}

	if prune {
		mz.prune(d.Remove, srcRoot)
	}
	return nil
}

// create registers one workspace and narrows it, recording it for rollback.
func (mz *Materializer) create(c *Checkout, dest string) error {
	if err := mz.addWorkspace(c, dest); err != nil {
		return err
	}
	mz.done = append(mz.done, created{mainClone: c.MainClone, workspace: c.Workspace, dir: dest})
	return mz.applySparse(c, dest)
}

// prune tears down checkouts the plan no longer wants.
//
// Failures are reported rather than returned: the manifest that no longer names
// these checkouts is already saved, so the sync itself succeeded, and a
// workspace left registered in a main clone is invisible from the rig side once
// its directory is gone. The user needs the exact command, not an error.
func (mz *Materializer) prune(remove []Checkout, srcRoot string) {
	for _, c := range remove {
		mz.logf("removing %s @ %s (no longer in the plan)", c.Repo, pinLabel(c))
		if err := mz.JJ.WorkspaceForget(c.MainClone, c.Workspace); err != nil {
			mz.logf("warning: could not forget workspace %s in %s: %v\n"+
				"  run: jj -R %s workspace forget %s", c.Workspace, c.MainClone, err, c.MainClone, c.Workspace)
		}
		if err := os.RemoveAll(filepath.Join(srcRoot, c.Dir)); err != nil {
			mz.logf("warning: could not remove %s: %v", filepath.Join(srcRoot, c.Dir), err)
		}
	}
}

// dedupeSkips sorts skips and drops exact repeats.
func dedupeSkips(skips []Skip) []Skip {
	sortSkips(skips)
	out := skips[:0]
	var prev Skip
	for i, s := range skips {
		if i > 0 && s == prev {
			continue
		}
		prev = s
		out = append(out, s)
	}
	return out
}

// AddModules folds hand-picked pins into an existing rig and returns the
// manifest to save plus the work needed to materialise them.
//
// Only the named modules are resolved. Re-planning the whole rig would mean
// re-deriving the artifact's build list, which the manifest does not record —
// a locally replaced module is stored as a skip long after the replace
// directive that caused it is out of view — and would risk renaming checkouts
// over a pin the user asked for on the side.
//
// The in-org filter is deliberately not applied. For `rig new -m` the extras
// join a build list that is filtered wholesale, but here the module *is* the
// request, and silently dropping it because it falls outside rig.org_prefixes
// would report success having done nothing. An out-of-org pin is checked out
// with a warning instead.
func (p *Planner) AddModules(m *Manifest, mods []gomod.Module) (*Manifest, *Diff, error) {
	if m == nil {
		return nil, nil, errors.New("rig: add needs a rig to add to")
	}
	wanted, err := newPins(m, mods)
	if err != nil {
		return nil, nil, err
	}
	if len(wanted) == 0 {
		return m, &Diff{}, nil
	}

	cands, skips, err := p.resolveAll(wanted)
	if err != nil {
		return nil, nil, err
	}
	// A skip is the right answer for a module that merely turned up in a build
	// list. It is the wrong answer for one the user named: `wgo rig add` would
	// print a warning, exit 0, and leave the rig exactly as it was.
	if len(skips) > 0 {
		return nil, nil, fmt.Errorf("rig: cannot check out %s@%s: %s",
			skips[0].Path, skips[0].Version, skips[0].String())
	}

	updated := *m
	updated.Checkouts = append([]Checkout(nil), m.Checkouts...)
	updated.Members = append([]Member(nil), m.Members...)

	byKey := map[string]int{}
	usedDirs := map[string]bool{}
	for i, c := range updated.Checkouts {
		byKey[checkoutKey(c)] = i
		usedDirs[c.Dir] = true
	}
	before := make([]Checkout, len(updated.Checkouts))
	copy(before, updated.Checkouts)

	diff := &Diff{}
	for _, cand := range cands {
		key := cand.origin.Slug() + "@" + cand.commit
		i, ok := byKey[key]
		if !ok {
			c := Checkout{
				Dir:       uniqueDir(usedDirs, cand.origin.Repo, cand.tag, cand.commit),
				Repo:      cand.origin.Slug(),
				MainClone: cand.mainClone,
				Revset:    cand.revset,
				Commit:    cand.commit,
				Tag:       cand.tag,
			}
			c.Workspace = workspaceName(updated.Name, c.Commit)
			usedDirs[c.Dir] = true
			updated.Checkouts = append(updated.Checkouts, c)
			i = len(updated.Checkouts) - 1
			byKey[key] = i
		}
		c := &updated.Checkouts[i]
		// A module at the repository root needs the whole tree, so a sparse
		// checkout that acquires one has to go full.
		if !m.Sparse || cand.origin.Subdir == "" {
			c.Full, c.Sparse = true, nil
		} else if !c.Full {
			c.Sparse = sortedKeys(withDir(c.Sparse, cand.origin.Subdir))
		}
		updated.Members = append(updated.Members, Member{
			Path:     cand.mod.Path,
			Version:  cand.mod.Version,
			Checkout: c.Dir,
			Subdir:   cand.origin.Subdir,
			Indirect: cand.mod.Indirect,
		})
	}

	sortMembers(updated.Checkouts, updated.Members)
	updated.GoVersion = gomod.MaxGoVersion(append(goVersionsOf(mods), m.GoVersion)...)

	// Recorded on the source, not just materialised: a later `wgo rig sync`
	// re-resolves from Source and would otherwise plan a rig without them and
	// report the checkouts they brought as obsolete.
	updated.Source.Modules = append(append([]string(nil), m.Source.Modules...), pinStrings(wanted)...)

	// The diff is derived from the before/after checkout lists rather than
	// accumulated above, so there is one description of "what changed" and both
	// callers of ApplyDiff agree on its shape.
	old := map[string]Checkout{}
	for _, c := range before {
		old[c.Dir] = c
	}
	for _, c := range updated.Checkouts {
		prev, existed := old[c.Dir]
		if !existed {
			diff.Add = append(diff.Add, c)
			continue
		}
		if wd, grew := widening(prev, c); grew {
			diff.Widen = append(diff.Widen, wd)
		}
	}

	if err := updated.Validate(); err != nil {
		return nil, nil, err
	}
	return &updated, diff, nil
}

// newPins rejects what cannot be added and drops what is already there.
func newPins(m *Manifest, mods []gomod.Module) ([]gomod.Module, error) {
	member := map[string]string{}
	for _, mem := range m.Members {
		member[mem.Path] = mem.Version
	}
	var out []gomod.Module
	seen := map[string]bool{}
	for _, mod := range mods {
		switch {
		case strings.TrimSpace(mod.Path) == "":
			return nil, errors.New("rig: add needs a module path")
		case seen[mod.Path]:
			continue
		case !gomod.IsResolvableVersion(mod.Version):
			return nil, fmt.Errorf("rig: %s@%s names no release, so there is no commit to pin a checkout to\n"+
				"pass a tagged version or a pseudo-version", mod.Path, orDashVersion(mod.Version))
		}
		seen[mod.Path] = true
		if have, ok := member[mod.Path]; ok {
			if have != mod.Version {
				// Moving a pin means a different commit, a different checkout,
				// and a build list that no longer matches the artifact. A rig is
				// deliberately frozen; that is a different rig.
				return nil, fmt.Errorf("rig: %s is already in %s at %s\n"+
					"`wgo rig add` does not move a pin; create a rig for the version you want",
					mod.Path, m.Name, have)
			}
			continue
		}
		out = append(out, mod)
	}
	return out, nil
}

func orDashVersion(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(no version)"
	}
	return v
}

func withDir(dirs []string, add string) map[string]bool {
	set := map[string]bool{add: true}
	for _, d := range dirs {
		set[d] = true
	}
	return set
}

// pinStrings renders modules in the "<path>@<version>" spelling Source.Modules
// uses, which is also what `-m` accepts, so the recorded source stays something
// a user can copy back onto a command line.
func pinStrings(mods []gomod.Module) []string {
	out := make([]string, 0, len(mods))
	for _, mod := range mods {
		out = append(out, mod.Path+"@"+mod.Version)
	}
	return out
}

func goVersionsOf(mods []gomod.Module) []string {
	out := make([]string, 0, len(mods))
	for _, mod := range mods {
		out = append(out, mod.GoVersion)
	}
	return out
}

// sortMembers orders members by their checkout's position and then by use path,
// so rig.toml reads top-to-bottom the same way the generated go.work does.
func sortMembers(checkouts []Checkout, members []Member) {
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
}
