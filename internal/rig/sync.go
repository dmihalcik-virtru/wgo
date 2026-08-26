package rig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/virtru/wgo/internal/gomod"
	"github.com/virtru/wgo/internal/jj"
	"golang.org/x/mod/modfile"
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
//
// A checkout the plan no longer wants stays in the merged manifest, marked
// Obsolete. It is still on disk and its jj workspace is still registered in the
// main clone, and the manifest is the only thing that records the connection.
// ApplyDiff drops the entry once --prune has actually torn it down.
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
	// Every directory the rig will still hold afterwards, obsolete ones
	// included: they are still on disk, and a new checkout handed the same name
	// would be created straight on top of one.
	usedDirs := map[string]bool{}
	for _, old := range have.Checkouts {
		usedDirs[old.Dir] = true
	}

	// Two passes over want.Checkouts. The first claims the directory of every
	// checkout that already exists; only then can the second hand out names for
	// the new ones without stealing one a retained checkout is about to reclaim.
	fresh := make([]int, 0, len(want.Checkouts))
	merged.Checkouts = make([]Checkout, len(want.Checkouts))
	for i, w := range want.Checkouts {
		key := checkoutKey(w)
		old, ok := existing[key]
		if !ok {
			fresh = append(fresh, i)
			continue
		}
		kept[key] = true
		c := w
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
		dirFor[w.Dir] = c.Dir
		merged.Checkouts[i] = c
	}
	for _, i := range fresh {
		c := want.Checkouts[i]
		// A re-pointed tag is the case that needs this: v2.7.1 now names a
		// different commit, so this is a new checkout by key, but the planner
		// derived its directory from the same tag and produced the same name as
		// the checkout the old commit still occupies. Creating it there would
		// register a jj workspace on top of a populated directory.
		c.Dir = disambiguateDir(usedDirs, c.Dir, c.Commit)
		usedDirs[c.Dir] = true
		diff.Add = append(diff.Add, c)
		dirFor[want.Checkouts[i].Dir] = c.Dir
		merged.Checkouts[i] = c
	}

	for _, mem := range want.Members {
		mem.Checkout = dirFor[mem.Checkout]
		merged.Members = append(merged.Members, mem)
	}

	for _, old := range have.Checkouts {
		if kept[checkoutKey(old)] {
			continue
		}
		diff.Remove = append(diff.Remove, old)
		// Kept as a tombstone rather than dropped: the workspace is still
		// registered in old.MainClone, and this entry is the only thing that
		// says so. ApplyDiff removes it once --prune has torn it down.
		tomb := old
		tomb.Obsolete = true
		merged.Checkouts = append(merged.Checkouts, tomb)
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
// Obsolete checkouts are removed only when opts.Prune is set. Removing a
// working copy is the one irreversible thing a sync can do, and a rig checkout
// is somewhere a user may have uncommitted debugging edits — so an unpruned one
// stays on disk *and* in the manifest, marked Obsolete, and a dirty one is
// refused even under --prune.
//
// The manifest is saved twice when pruning: once with the tombstones, before
// anything is torn down, and again without whichever ones actually went. A
// crash between the two leaves tombstones for checkouts that are already gone,
// which the next --prune clears — the reverse order would leave live workspaces
// with nothing recording them.
func (mz *Materializer) ApplyDiff(ctx context.Context, m *Manifest, d *Diff, rigRoot string, opts PruneOpts) (retErr error) {
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
		if err := ctx.Err(); err != nil {
			return err
		}
		c := &d.Restore[i]
		// A checkout this Materializer already made — `rig sync` re-materialises
		// the primary before it can re-read the build list — is on disk and on
		// the rollback list already.
		if mz.alreadyMade(c.Workspace) || c.Obsolete {
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
		if err := ctx.Err(); err != nil {
			return err
		}
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
	// Obsolete ones are skipped — they serve no members, and --prune may be
	// about to delete the directory this would read.
	for i := range m.Checkouts {
		c := &m.Checkouts[i]
		if c.Obsolete {
			continue
		}
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
	m.GoVersion = foldMemberGoVersions(m, srcRoot)

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

	if opts.Prune {
		if gone := mz.prune(d.Remove, srcRoot, opts.Force); len(gone) > 0 {
			m.Checkouts = withoutCheckouts(m.Checkouts, gone)
			if err := Save(rigRoot, m); err != nil {
				return err
			}
		}
	}
	return nil
}

// PruneOpts controls what ApplyDiff does with the checkouts the plan dropped.
type PruneOpts struct {
	// Prune removes them instead of leaving tombstones.
	Prune bool
	// Force removes one even when its working copy has uncommitted changes.
	Force bool
}

// foldMemberGoVersions raises the workspace's `go` directive to cover every
// member module on disk.
//
// The planner derives it from the build list, which is the best it can do
// without checkouts, but it cannot cover a member the build list has no entry
// for: a `use` directory the primary does not depend on, or — the case this
// exists for — a module added by `wgo rig add`, whose `-m path@version` carries
// a path and a version and nothing else. A go.work older than a module it uses
// fails the build outright, and the failure names the toolchain rather than the
// module that needed it.
//
// Read tolerantly: an unreadable or unparsable go.mod contributes nothing.
// verifyMembers has already established the file exists, and refusing to write
// go.work over a directive this cannot parse would be a worse outcome than
// leaving the version where the planner put it.
func foldMemberGoVersions(m *Manifest, srcRoot string) string {
	versions := []string{m.GoVersion}
	seen := map[string]bool{}
	for _, mem := range m.Members {
		dir := filepath.Join(mem.Checkout, filepath.FromSlash(mem.Subdir))
		if seen[dir] {
			continue
		}
		seen[dir] = true
		versions = append(versions, goDirective(filepath.Join(srcRoot, dir, "go.mod")))
	}
	return gomod.MaxGoVersion(versions...)
}

// goDirective reads a go.mod's `go` line, or "" if it cannot.
func goDirective(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// Lax: this wants one directive out of a file that may use syntax this
	// modfile version does not know, and a strict parse would discard the whole
	// answer over a directive nothing here reads.
	f, err := modfile.ParseLax(path, data, nil)
	if err != nil || f.Go == nil {
		return ""
	}
	return f.Go.Version
}

// withoutCheckouts drops the named checkouts, matched by directory.
func withoutCheckouts(checkouts, drop []Checkout) []Checkout {
	gone := make(map[string]bool, len(drop))
	for _, c := range drop {
		gone[c.Dir] = true
	}
	out := make([]Checkout, 0, len(checkouts))
	for _, c := range checkouts {
		if !gone[c.Dir] {
			out = append(out, c)
		}
	}
	return out
}

// create registers one workspace and narrows it, recording it for rollback.
func (mz *Materializer) create(c *Checkout, dest string) error {
	if err := mz.addWorkspace(c, dest); err != nil {
		return err
	}
	mz.done = append(mz.done, created{mainClone: c.MainClone, workspace: c.Workspace, dir: dest})
	return mz.applySparse(c, dest)
}

// prune tears down checkouts the plan no longer wants and returns the ones it
// removed, so the caller can drop their tombstones from the manifest.
//
// Failures are reported rather than returned, and the checkout stays recorded.
// The sync itself succeeded — go.work no longer names any of these — and a
// workspace left registered in a main clone is invisible from the rig side once
// its directory is gone. The user needs the exact command, not an error.
//
// A checkout with uncommitted changes is kept unless force. It is a working
// copy: pinned and disposable by design, but nothing stops a user from editing
// one to test a theory, and `wgo rig sync --prune` is run to tidy up rather than
// to discard work. jj's auto-commit means those edits are in the operation log
// and `jj op restore` could get them back, but only for someone who knows to
// look, and only if they notice in time.
func (mz *Materializer) prune(remove []Checkout, srcRoot string, force bool) []Checkout {
	var gone []Checkout
	for _, c := range remove {
		dest := filepath.Join(srcRoot, c.Dir)
		if !force {
			if dirty, entries := mz.dirty(dest); dirty {
				mz.logf("keeping %s: it has uncommitted changes", c.Dir)
				for _, e := range entries {
					mz.logf("    %s", e)
				}
				mz.logf("  inspect them with: jj -R %s status\n"+
					"  remove it anyway with: wgo rig sync --prune --force", dest)
				continue
			}
		}
		mz.logf("removing %s @ %s (no longer in the plan)", c.Repo, pinLabel(c))
		if err := mz.JJ.WorkspaceForget(c.MainClone, c.Workspace); err != nil {
			mz.logf("warning: could not forget workspace %s in %s: %v\n"+
				"  run: jj -R %s workspace forget %s", c.Workspace, c.MainClone, err, c.MainClone, c.Workspace)
			continue
		}
		if err := os.RemoveAll(dest); err != nil {
			mz.logf("warning: could not remove %s: %v", dest, err)
			continue
		}
		gone = append(gone, c)
	}
	return gone
}

// dirty reports whether a checkout's working copy has changes against its pin.
//
// A directory that is already gone is not dirty — a user who deleted it by hand
// has said what they want, and refusing to finish the job would leave a
// tombstone nothing can clear. Neither is one whose status cannot be read: the
// checkout is being removed either way, and a workspace so broken that `jj
// status` fails in it is exactly what --prune is for.
func (mz *Materializer) dirty(dest string) (bool, []string) {
	ws, ok := mz.JJ.(WorkingCopy)
	if !ok {
		return false, nil
	}
	if _, err := os.Stat(dest); err != nil {
		return false, nil
	}
	clean, entries, err := ws.IsClean(dest)
	if err != nil {
		mz.logf("note: could not check %s for uncommitted changes: %v", dest, err)
		return false, nil
	}
	return !clean, entries
}

// WorkingCopy is an optional Workspaces extension for clients that can report
// uncommitted changes. Kept off Workspaces itself so the create-and-roll-back
// tests, which have no working copies at all, need not implement it.
type WorkingCopy interface {
	IsClean(workspacePath string) (clean bool, changed []string, err error)
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
// with a warning instead, and recorded in Source.Unfiltered so the next sync —
// which does re-apply the filter — keeps it.
//
// onDisk reports whether a checkout directory exists, as in Reconcile; a nil
// func assumes they all do. A module landing on a checkout whose directory has
// been deleted is otherwise neither added nor restored, and materialisation
// fails looking for a go.mod under a path that is not there.
func (p *Planner) AddModules(ctx context.Context, m *Manifest, mods []gomod.Module, onDisk func(dir string) bool) (*Manifest, *Diff, error) {
	if m == nil {
		return nil, nil, errors.New("rig: add needs a rig to add to")
	}
	if onDisk == nil {
		onDisk = func(string) bool { return true }
	}
	wanted, err := newPins(m, mods)
	if err != nil {
		return nil, nil, err
	}
	if len(wanted) == 0 {
		return m, &Diff{}, nil
	}

	cands, skips, err := p.resolveAll(ctx, wanted)
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
		// A tombstone the plan dropped and a hand-picked pin can name the same
		// commit — most often because the module was dropped from the build list
		// and the user wants it back. Serving a member revives the checkout;
		// leaving it marked would put it in go.work and in --prune's sights at
		// the same time.
		c.Obsolete = false
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
	// GoVersion is deliberately not touched here. A `-m path@version` pin says
	// nothing about the module's `go` directive — that lives in a go.mod which
	// does not exist on disk until ApplyDiff has created the checkout — so
	// folding it in at this point could only fold in the empty string.
	// foldMemberGoVersions reads it from the checkout instead.

	// Recorded on the source, not just materialised: a later `wgo rig sync`
	// re-resolves from Source and would otherwise plan a rig without them and
	// report the checkouts they brought as obsolete.
	updated.Source.Modules = append(append([]string(nil), m.Source.Modules...), pinStrings(wanted)...)
	// And the ones the filter would have dropped are recorded a second time, by
	// path: the sync re-applies the filter to the whole build list, so the entry
	// in Source.Modules alone would not survive it.
	for _, mod := range wanted {
		if !gomod.InOrg(mod.Path, updated.Source.OrgPrefixes) {
			updated.Source.Unfiltered = appendUnique(updated.Source.Unfiltered, mod.Path)
		}
	}

	// The diff is derived from the before/after checkout lists rather than
	// accumulated above, so there is one description of "what changed" and both
	// callers of ApplyDiff agree on its shape.
	old := map[string]Checkout{}
	for _, c := range before {
		old[c.Dir] = c
	}
	for _, c := range updated.Checkouts {
		prev, existed := old[c.Dir]
		switch {
		case c.Obsolete:
			// A tombstone serves nothing, so there is nothing to widen and
			// nothing that breaks if its directory is already gone. Recreating
			// one would be work in service of no `use` line.
		case !existed:
			diff.Add = append(diff.Add, c)
		case !onDisk(c.Dir):
			// Recorded but deleted. Without this the checkout is neither added
			// nor restored, and the first thing that notices is verifyMembers
			// failing on a missing go.mod — which blames the module-to-repo
			// mapping for a directory the user removed.
			diff.Restore = append(diff.Restore, c)
		default:
			if wd, grew := widening(prev, c); grew {
				diff.Widen = append(diff.Widen, wd)
			}
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
	seen := map[string]string{}
	for _, mod := range mods {
		switch prev, dup := seen[mod.Path]; {
		case strings.TrimSpace(mod.Path) == "":
			return nil, errors.New("rig: add needs a module path")
		case dup && prev == mod.Version:
			continue
		case dup:
			// Whichever won, the other was silently discarded — and the one that
			// lost is the version the user will look for in go.work. There is no
			// defensible pick between two pins of one module, and a rig holds
			// exactly one commit per module by construction.
			return nil, fmt.Errorf("rig: %s was named twice, at %s and at %s\n"+
				"a rig holds one commit per module; pick the version you want",
				mod.Path, prev, orDashVersion(mod.Version))
		case !gomod.IsResolvableVersion(mod.Version):
			return nil, fmt.Errorf("rig: %s@%s names no release, so there is no commit to pin a checkout to\n"+
				"pass a tagged version or a pseudo-version", mod.Path, orDashVersion(mod.Version))
		}
		seen[mod.Path] = mod.Version
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

func appendUnique(list []string, s string) []string {
	for _, have := range list {
		if have == s {
			return list
		}
	}
	return append(list, s)
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
