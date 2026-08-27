package rig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/virtru/wgo/internal/jj"
	"golang.org/x/mod/modfile"
)

// Workspaces is the jj surface materialisation needs.
//
// Narrow for the same reason RepoLocator is: it keeps the create-and-roll-back
// logic — the part that can strand workspaces in a user's main clone — testable
// without a jj repository on disk.
type Workspaces interface {
	WorkspaceAdd(repo, dest string, opts jj.WorkspaceAddOpts) error
	WorkspaceForget(repo, name string) error
	SparseSet(workspacePath string, opts jj.SparseSetOpts) error
}

// GoWorkValidator formats and thereby parses a rendered go.work. Satisfied by
// a *gotool.Client already pointed at the rig's workspace file.
type GoWorkValidator interface {
	WorkEditFmt() error
}

// Materializer turns a planned Manifest into checkouts on disk.
//
// It is stateful and single-use: it remembers every workspace and directory it
// created so Rollback can undo exactly that and nothing else. `wgo rig new`
// needs this to span two phases — the primary's checkout has to exist before
// the build list can be computed, and a failure while computing the build list
// must still take the primary back down.
type Materializer struct {
	// JJ creates and forgets the workspaces.
	JJ Workspaces
	// Validate parses the rendered go.work. Optional: a nil validator skips
	// the check, which is what --dry-run and unit tests want.
	Validate GoWorkValidator
	// Logf reports progress. Everything it emits belongs on stderr, because
	// `wgo rig new` prints the rig path to stdout and callers do
	// `cd $(wgo rig new ...)`.
	Logf func(format string, args ...any)

	// done is what this Materializer created, in creation order.
	done []created
	// madeRoot records that the rig root did not exist before this run, so
	// Rollback knows whether removing it is ours to do.
	madeRoot bool
	// madeSrc is the same for src/, which is created inside a rig root that
	// may well have existed already — `rig new` into a directory the user
	// pre-made, say. Without it a rolled-back run leaves an empty src/ behind,
	// and the next `rig new` sees a non-empty directory and refuses.
	madeSrc bool
}

func (mz *Materializer) logf(format string, args ...any) {
	if mz.Logf != nil {
		mz.Logf(format, args...)
	}
}

// created records one workspace this run made, so rollback can undo exactly
// what it did and nothing else.
type created struct {
	mainClone string
	workspace string
	dir       string
}

// Checkout materialises a single checkout ahead of the rest of the rig and
// returns its absolute path.
//
// `wgo rig new --from` needs this: the build list comes from `go list -deps`
// run inside the primary's own checkout, so that checkout must exist before
// there is a plan to materialise. The result is recorded for rollback and
// Materialize will not create it a second time.
func (mz *Materializer) Checkout(rigRoot string, c *Checkout) (string, error) {
	if err := mz.ensureRoot(rigRoot); err != nil {
		return "", err
	}
	dest := filepath.Join(rigRoot, SrcDir, c.Dir)
	mz.logf("checkout: %s @ %s", c.Repo, pinLabel(*c))
	if err := mz.addWorkspace(c, dest); err != nil {
		return "", err
	}
	mz.done = append(mz.done, created{mainClone: c.MainClone, workspace: c.Workspace, dir: dest})
	if err := mz.applySparse(c, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// ensureRoot creates the rig root and its src/ directory, remembering whether
// the root was ours to make.
func (mz *Materializer) ensureRoot(rigRoot string) error {
	if !filepath.IsAbs(rigRoot) {
		// env.sh exports this as GOWORK, and Go rejects a relative GOWORK. A
		// relative root would produce a rig whose every `go` command fails.
		return fmt.Errorf("rig: rig root must be absolute, got %q", rigRoot)
	}
	switch _, err := os.Stat(rigRoot); {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(rigRoot, 0o755); err != nil {
			return fmt.Errorf("rig: creating %s: %w", rigRoot, err)
		}
		mz.madeRoot = true
	default:
		return fmt.Errorf("rig: checking %s: %w", rigRoot, err)
	}
	src := filepath.Join(rigRoot, SrcDir)
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		mz.madeSrc = true
	}
	if err := os.MkdirAll(src, 0o755); err != nil {
		return fmt.Errorf("rig: creating %s: %w", src, err)
	}
	return nil
}

// alreadyMade reports whether this Materializer created the given workspace,
// which is how Materialize recognises a checkout pre-warmed by Checkout.
func (mz *Materializer) alreadyMade(workspace string) bool {
	for _, d := range mz.done {
		if d.workspace == workspace {
			return true
		}
	}
	return false
}

// Materialize creates every checkout in m under rigRoot, widens sparse sets to
// cover local replaces, and writes go.work, rig.toml and the helper files.
//
// On any failure it rolls back in reverse order — forgetting each workspace it
// registered and removing each directory it created — so a half-built rig never
// survives. It removes rigRoot itself only when this run created it, since the
// user may have made the directory ahead of time.
//
// m is mutated: WidenSparse extends the sparse sets and any escaped replaces
// are appended to m.Skipped before the manifest is written.
func (mz *Materializer) Materialize(m *Manifest, rigRoot string) (retErr error) {
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

	for i := range m.Checkouts {
		c := &m.Checkouts[i]
		dest := filepath.Join(srcRoot, c.Dir)

		// A checkout pre-warmed by Checkout is already on disk and already
		// recorded for rollback; adding it again would fail on the workspace
		// name, and skipping the record would leak it.
		if !mz.alreadyMade(c.Workspace) {
			mz.logf("checkout %d/%d: %s @ %s", i+1, len(m.Checkouts), c.Repo, pinLabel(*c))
			if err := mz.addWorkspace(c, dest); err != nil {
				return err
			}
			mz.done = append(mz.done, created{mainClone: c.MainClone, workspace: c.Workspace, dir: dest})
			if err := mz.applySparse(c, dest); err != nil {
				return err
			}
		}

		if err := verifyMembers(m, c, dest); err != nil {
			return err
		}
		skips, err := mz.widen(m, c, dest)
		if err != nil {
			return err
		}
		m.Skipped = append(m.Skipped, skips...)
	}
	sortSkips(m.Skipped)

	if err := mz.writeGoWork(m, rigRoot); err != nil {
		return err
	}
	for _, f := range GeneratedFiles(m, rigRoot) {
		if err := writeFile(filepath.Join(rigRoot, f.Name), f.Content, f.Mode); err != nil {
			return err
		}
	}
	// The manifest goes last. It is the record `wgo rig rm` reads to find the
	// workspaces it must forget, so writing it before the rig is otherwise
	// complete would advertise a rig that does not exist yet.
	if err := Save(rigRoot, m); err != nil {
		return err
	}
	return nil
}

// addWorkspace registers one jj workspace, pinned to the checkout's commit.
//
// Sparse mode starts empty for a partial checkout so no time is spent
// materialising a 261M tree that applySparse immediately narrows.
func (mz *Materializer) addWorkspace(c *Checkout, dest string) error {
	sparse := jj.SparseFull
	if !c.Full {
		sparse = jj.SparseEmpty
	}
	opts := jj.WorkspaceAddOpts{
		Name:   c.Workspace,
		Revset: c.Commit,
		// A rig workspace carries no bookmark, so without a description it is
		// an anonymous empty change in `jj log` and `jj workspace list` — in a
		// main clone that may hold seven of them.
		Message:        workspaceMessage(c),
		SparsePatterns: sparse,
	}
	if err := mz.JJ.WorkspaceAdd(c.MainClone, dest, opts); err != nil {
		return fmt.Errorf("rig: creating workspace %s for %s@%s: %w",
			c.Workspace, c.Repo, pinLabel(*c), err)
	}
	return nil
}

// workspaceMessage describes a pinned checkout for `jj log`.
func workspaceMessage(c *Checkout) string {
	return fmt.Sprintf("rig checkout: %s @ %s", c.Repo, pinLabel(*c))
}

// applySparse narrows a partial checkout to exactly its recorded sparse set.
//
// dest, not the main clone: `jj sparse` acts on the working copy it finds from
// the current directory, and jj.SparseSet rejects a non-absolute path for that
// reason.
func (mz *Materializer) applySparse(c *Checkout, dest string) error {
	if c.Full || len(c.Sparse) == 0 {
		return nil
	}
	err := mz.JJ.SparseSet(dest, jj.SparseSetOpts{Clear: true, Add: c.Sparse})
	if err != nil {
		return fmt.Errorf("rig: narrowing %s to %s: %w", c.Dir, strings.Join(c.Sparse, ", "), err)
	}
	return nil
}

// verifyMembers checks that every module served from this checkout actually has
// a go.mod at the resolved commit.
//
// This is a hard error rather than a skip. A missing go.mod means the
// module-to-repo mapping put us in the wrong repository or the wrong
// subdirectory, and every other checkout derived from that mapping is suspect.
// Continuing would produce a go.work whose `use` points at a directory Go
// cannot load, which fails later with a message that names neither the module
// nor the pin.
func verifyMembers(m *Manifest, c *Checkout, dest string) error {
	for _, mem := range m.Members {
		if mem.Checkout != c.Dir {
			continue
		}
		modPath := filepath.Join(dest, filepath.FromSlash(mem.Subdir), "go.mod")
		if _, err := os.Stat(modPath); err != nil {
			return fmt.Errorf(
				"rig: %s has no go.mod at %s in %s@%s\n"+
					"the module path most likely does not map to this repository or subdirectory",
				mem.Path, orRoot(mem.Subdir), c.Repo, pinLabel(*c))
		}
	}
	return nil
}

func orRoot(subdir string) string {
	if subdir == "" {
		return "the repository root"
	}
	return subdir
}

// widen extends the sparse set to cover the directories local replace
// directives point at, then re-narrows the working copy to match.
//
// Reading the go.mod files requires them to be materialised, which is why this
// runs after applySparse rather than at planning time: the planner has no
// checkout to read from.
func (mz *Materializer) widen(m *Manifest, c *Checkout, dest string) ([]Skip, error) {
	if c.Full {
		return nil, nil
	}
	modFiles := map[string]*modfile.File{}
	for _, mem := range m.Members {
		if mem.Checkout != c.Dir {
			continue
		}
		f, err := readModFile(filepath.Join(dest, filepath.FromSlash(mem.Subdir), "go.mod"))
		if err != nil {
			return nil, err
		}
		modFiles[mem.Subdir] = f
	}

	before := append([]string(nil), c.Sparse...)
	skips := WidenSparse(c, m.Members, modFiles)
	for _, s := range skips {
		mz.logf("warning: %s: %s", s.Path, s.String())
	}

	switch {
	case c.Full:
		// A replace resolving to the repository root needs the whole tree.
		mz.logf("  %s: widened to a full checkout", c.Dir)
		if err := mz.JJ.SparseSet(dest, jj.SparseSetOpts{Clear: true, Add: []string{"."}}); err != nil {
			return nil, fmt.Errorf("rig: widening %s to full: %w", c.Dir, err)
		}
	case !equalStrings(before, c.Sparse):
		mz.logf("  %s: widened to %s", c.Dir, strings.Join(c.Sparse, ", "))
		if err := mz.applySparse(c, dest); err != nil {
			return nil, err
		}
	}
	return skips, nil
}

func readModFile(path string) (*modfile.File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("rig: reading %s: %w", path, err)
	}
	// Strict parsing, deliberately: modfile.ParseLax discards replace
	// directives, and replace is the only thing this function is read for.
	f, err := modfile.Parse(path, data, nil)
	if err != nil {
		return nil, fmt.Errorf("rig: parsing %s: %w", path, err)
	}
	return f, nil
}

// writeGoWork renders the workspace file and hands it to the toolchain, which
// parses it and so doubles as validation.
func (mz *Materializer) writeGoWork(m *Manifest, rigRoot string) error {
	if err := writeFile(goWorkPath(rigRoot), RenderGoWork(m), 0o644); err != nil {
		return err
	}
	if mz.Validate == nil {
		return nil
	}
	if err := mz.Validate.WorkEditFmt(); err != nil {
		return fmt.Errorf("rig: generated go.work does not parse: %w", err)
	}
	return nil
}

// Rollback undoes what this Materializer created, in reverse order.
//
// Materialize calls it on failure. A caller doing the two-phase `rig new` dance
// must call it itself if it fails between Checkout and Materialize — that is
// the window where the primary is on disk but no manifest records it.
//
// Every failure is reported and none aborts the loop: a workspace left
// registered in a main clone is invisible from the rig side once the directory
// is gone, so the user needs to be told which `jj workspace forget` to run by
// hand. Only workspaces this Materializer created are touched — never one
// merely present in a manifest, and never the rig root unless we made it.
func (mz *Materializer) Rollback(rigRoot string) {
	done, madeRoot, madeSrc := mz.done, mz.madeRoot, mz.madeSrc
	// Cleared first so a second call — Materialize's defer after the caller
	// already gave up, say — cannot forget the same workspaces twice.
	mz.done, mz.madeRoot, mz.madeSrc = nil, false, false

	if len(done) > 0 {
		mz.logf("rolling back %d workspace(s)", len(done))
	}
	for i := len(done) - 1; i >= 0; i-- {
		d := done[i]
		if err := mz.JJ.WorkspaceForget(d.mainClone, d.workspace); err != nil {
			mz.logf("warning: could not forget workspace %s in %s: %v\n"+
				"  run: jj -R %s workspace forget %s", d.workspace, d.mainClone, err, d.mainClone, d.workspace)
		}
		if err := os.RemoveAll(d.dir); err != nil {
			mz.logf("warning: could not remove %s: %v", d.dir, err)
		}
	}
	if !madeRoot {
		// The root was the user's; leave it, but do not leave our src/ inside
		// it. os.Remove, not RemoveAll: it succeeds only if src/ is empty,
		// which it is once the checkouts above are gone — and if something
		// unexpected is still in there, refusing to delete it is the right
		// answer.
		if madeSrc {
			if err := os.Remove(filepath.Join(rigRoot, SrcDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
				mz.logf("warning: could not remove %s: %v", filepath.Join(rigRoot, SrcDir), err)
			}
		}
		return
	}
	if err := os.RemoveAll(rigRoot); err != nil {
		mz.logf("warning: could not remove %s: %v", rigRoot, err)
	}
}

// writeFile writes content atomically, so an interrupted run cannot leave a
// truncated go.work or env.sh that looks complete.
func writeFile(path, content string, mode uint32) error {
	if mode == 0 {
		mode = 0o644
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("rig: creating temp file in %s: %w", dir, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("rig: writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("rig: closing %s: %w", path, err)
	}
	if err := os.Chmod(tmp.Name(), os.FileMode(mode)); err != nil {
		return fmt.Errorf("rig: setting mode on %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rig: installing %s: %w", path, err)
	}
	return nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Remove tears down a materialised rig: every workspace it registered is
// forgotten, then the tree is deleted.
//
// Forgetting comes first because the manifest is the only record of which
// workspaces belong to this rig. Deleting the tree first and failing partway
// would leave them registered in their main clones with nothing left on disk
// naming them.
func Remove(ws Workspaces, m *Manifest, rigRoot string, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var failed []string
	for _, c := range m.Checkouts {
		if err := ws.WorkspaceForget(c.MainClone, c.Workspace); err != nil {
			// A workspace the user already forgot by hand is not an error worth
			// blocking removal over, but every failure is reported so a genuine
			// one is not silently absorbed by the tree deletion that follows.
			logf("warning: could not forget workspace %s in %s: %v", c.Workspace, c.MainClone, err)
			failed = append(failed, fmt.Sprintf("jj -R %s workspace forget %s", c.MainClone, c.Workspace))
		}
	}
	if err := os.RemoveAll(rigRoot); err != nil {
		return fmt.Errorf("rig: removing %s: %w", rigRoot, err)
	}
	if len(failed) > 0 {
		return fmt.Errorf("rig: removed %s, but %d workspace(s) could not be forgotten; run:\n  %s",
			rigRoot, len(failed), strings.Join(failed, "\n  "))
	}
	return nil
}
