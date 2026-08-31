package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/gomod"
	"github.com/virtru/wgo/internal/gotool"
	"github.com/virtru/wgo/internal/interrupt"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/rig"
)

var (
	rigSyncPrune  bool
	rigSyncForce  bool
	rigSyncDryRun bool

	rigAddModules []string
	rigAddDryRun  bool
)

var rigSyncCmd = &cobra.Command{
	Use:   "sync [name]",
	Short: "Re-resolve a rig's source and bring its checkouts back in line",
	Long: `Resolve the rig's recorded source again, add or restore whatever
checkouts it now calls for, widen any sparse set that has grown, and rewrite
go.work.

A sync re-resolves the *same* source; it never bumps a pin. What it does catch
is a checkout someone deleted, a sparse set narrowed by hand, a go.work that
got clobbered, and a source whose own dependency set moved under a tag that was
re-pointed.

Nothing is deleted by default. A checkout the plan no longer wants is reported,
left on disk, and kept in the manifest as an obsolete entry — that entry is the
only record of the jj workspace still registered in its main clone, so dropping
it would strand the workspace. Pass --prune to remove them; one with
uncommitted changes is still kept unless you add --force.

When the source cannot be read back — a --from-binary rig whose artifact is
gone, or one assembled entirely by hand — sync falls back to reconciling the
rig against what its own manifest already records, which still restores missing
checkouts and rewrites the generated files.

With no name, syncs the rig containing the current directory.`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRigSync(args)
	},
}

var rigAddCmd = &cobra.Command{
	Use:   "add [name]",
	Short: "Add a module to a rig by hand",
	Long: `Check out one more module at a pinned version and add it to the rig's
go.work:

  wgo rig add dsp-2.7.1 -m github.com/opentdf/platform/lib/fixtures@v0.3.0

The pin is recorded on the rig's source, so a later ` + "`wgo rig sync`" + ` keeps it
rather than reporting its checkout as obsolete.

Only the named modules are resolved; the rest of the rig is left exactly where
it is. A module already in the rig at a different version is refused — a rig is
deliberately frozen, and moving a pin makes it a different rig.

Unlike ` + "`wgo rig new -m`" + `, an explicit pin here is not filtered by
rig.org_prefixes. Naming the module is the request, and dropping it silently
would report success having done nothing.

With no name, adds to the rig containing the current directory.`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRigAdd(args)
	},
}

func init() {
	rigCmd.AddCommand(rigSyncCmd, rigAddCmd)

	rigSyncCmd.Flags().BoolVar(&rigSyncPrune, "prune", false,
		"Also remove checkouts the re-resolved plan no longer wants")
	rigSyncCmd.Flags().BoolVar(&rigSyncForce, "force", false,
		"With --prune, remove an obsolete checkout even if it has uncommitted changes")
	rigSyncCmd.Flags().BoolVar(&rigSyncDryRun, "dry-run", false,
		"Print what would change and exit without touching anything")

	rigAddCmd.Flags().StringArrayVarP(&rigAddModules, "module", "m", nil,
		"Module to add: <path>@<version> (repeatable)")
	rigAddCmd.Flags().BoolVar(&rigAddDryRun, "dry-run", false,
		"Print what would change and exit without touching anything")
}

// errSourceGone marks a source that cannot be read back, which downgrades a
// sync to a reconciliation rather than failing it.
var errSourceGone = errors.New("rig: the source cannot be re-resolved")

func runRigSync(args []string) (retErr error) {
	cfg, rigDir, err := rigSetup()
	if err != nil {
		return err
	}
	rigRoot, err := resolveRigRoot(rigDir, args)
	if err != nil {
		return err
	}
	have, err := rig.Load(rigRoot)
	if err != nil {
		return err
	}

	g := interrupt.Listen()
	defer g.Stop()
	ctx := g.Context()
	defer func() { retErr = g.Wrap(retErr) }()

	base := jj.NewCLI()
	jjc := base.WithContext(ctx)
	mz := &rig.Materializer{
		JJ:      jjc,
		Cleanup: base.WithContext(context.WithoutCancel(ctx)),
		Logf:    rigLogf,
	}
	// Re-resolving may materialise a primary at a commit the rig does not have
	// yet. Until ApplyDiff writes the manifest, nothing else records it.
	defer func() {
		if retErr != nil {
			mz.Rollback(rigRoot)
		}
	}()

	want, err := resyncPlan(ctx, cfg, jjc, mz, have, rigRoot)
	switch {
	case errors.Is(err, errSourceGone):
		rigLogf("%v", err)
		rigLogf("reconciling %s against its own manifest instead; pins are unchanged", have.Name)
		want = have
	case err != nil:
		return err
	}

	merged, diff, err := rig.Reconcile(have, want, checkoutOnDisk(rigRoot))
	if err != nil {
		return err
	}

	if rigSyncDryRun {
		printRigDiff(merged, diff, rigRoot)
		mz.Rollback(rigRoot)
		return nil
	}
	if diff.Empty() {
		// Still rewritten: go.work and the helper files are generated from the
		// manifest, and repairing a clobbered one is half of what sync is for.
		rigLogf("rig %s: checkouts already match; regenerating %s", merged.Name, rig.GoWorkName)
	}

	gc := gotool.NewClient().In(rigRoot).WithWork(filepath.Join(rigRoot, rig.GoWorkName))
	mz.Validate = gc
	if err := mz.ApplyDiff(ctx, merged, diff, rigRoot, rig.PruneOpts{Prune: rigSyncPrune, Force: rigSyncForce}); err != nil {
		return err
	}

	reportSkips(merged)
	rigLogf("rig %s: %s (%d checkouts, %d modules)",
		merged.Name, diff.Summary(), len(merged.LiveCheckouts()), len(merged.Members))
	if len(diff.Remove) > 0 && !rigSyncPrune {
		reportObsolete(merged.Name, diff.Remove)
	}
	fmt.Println(rigRoot)
	return nil
}

// resyncPlan re-resolves the rig's recorded source into a fresh plan.
//
// The primary is not re-materialised when the rig already holds it: `go list`
// and the go.mod read both need it on disk, and it is on disk — in a directory
// the user may have an editor and a debugger attached to.
func resyncPlan(ctx context.Context, cfg *config.Config, jjc jj.Client, mz *rig.Materializer, have *rig.Manifest, rigRoot string) (*rig.Manifest, error) {
	src, err := recordedSource(have)
	if err != nil {
		return nil, err
	}
	if !gotool.Available() {
		return nil, fmt.Errorf("%w: `go` is not on PATH, and the build list is read with the go toolchain", errSourceGone)
	}

	planner := &rig.Planner{
		Locator:  &cloneLocator{jjc: jjc, cfg: cfg},
		Resolver: jjc,
	}
	extra, err := parseModulePins(have.Source.Modules)
	if err != nil {
		return nil, fmt.Errorf("rig: the recorded -m pins of %s: %w", have.Name, err)
	}

	pins, err := resolveRigPins(planner, reusePrimary(ctx, have, mz, rigRoot), have.Name, src)
	if err != nil {
		return nil, err
	}
	buildList := append(pins.buildList, extra...)

	return planner.Plan(ctx, rig.Request{
		Name:   have.Name,
		Source: pins.source,
		// The filter is re-applied, so the pins that were admitted past it have
		// to be named again or `wgo rig add`'s work is undone on the next sync.
		OrgPrefixes:     src.orgs,
		Unfiltered:      have.Source.Unfiltered,
		Sparse:          have.Sparse,
		Primary:         pins.primary,
		PrimaryUse:      pins.use,
		PrimaryCheckout: pins.checkout,
		BuildList:       buildList,
		GoVersion:       pins.primary.GoVersion,
		Baseline:        baselineOf(buildList),
		Created:         time.Now().UTC().Format(time.RFC3339),
		WgoVersion:      getVersionString(),
	})
}

// reusePrimary returns the primary's existing checkout when the rig already
// pins that commit, and materialises a new one otherwise.
//
// It rewrites the planner's freshly derived checkout to carry the recorded
// directory and workspace names before returning. Those are persisted for a
// reason — they hold a collision suffix that depends on what else existed when
// the rig was built — and leaving the derived ones in place would make Reconcile
// see the rig's own primary as a stranger and plan to check it out again.
func reusePrimary(ctx context.Context, have *rig.Manifest, mz *rig.Materializer, rigRoot string) checkoutFunc {
	return func(c *rig.Checkout) (string, error) {
		for _, existing := range have.Checkouts {
			if existing.Repo != c.Repo || existing.Commit != c.Commit {
				continue
			}
			dest := filepath.Join(rigRoot, rig.SrcDir, existing.Dir)
			if _, err := os.Stat(dest); err != nil {
				// Recorded but gone, so it has to be re-created — under the
				// recorded names, since Reconcile keys on them and a second
				// directory for the primary is the last thing a sync should
				// make. The workspace is almost certainly still registered in the
				// main clone (a deleted directory does not unregister it) and
				// `jj workspace add --name` fails on a duplicate, so forget it
				// first. A forget that fails is not worth stopping for: the add
				// that follows reports the collision far more precisely.
				c.Dir, c.Workspace = existing.Dir, existing.Workspace
				if err := mz.JJ.WorkspaceForget(existing.MainClone, existing.Workspace); err != nil {
					rigLogf("note: %s was not registered in %s (%v)", existing.Workspace, existing.MainClone, err)
				}
				break
			}
			c.Dir, c.Workspace = existing.Dir, existing.Workspace
			c.Full, c.Sparse = existing.Full, existing.Sparse
			return dest, nil
		}
		rigLogf("the primary is pinned to %s, which %s does not hold on disk", rigPin(*c), have.Name)
		return mz.Checkout(ctx, rigRoot, c)
	}
}

// recordedSource turns a manifest's Source back into something resolvable.
func recordedSource(m *rig.Manifest) (rigSource, error) {
	src := rigSource{modules: m.Source.Modules, orgs: m.Source.OrgPrefixes}
	if len(src.orgs) == 0 {
		// The filter is recorded rather than re-read from config precisely so a
		// sync cannot silently widen or narrow the checkout set. An empty one
		// admits nothing, which would plan every dependency away.
		return src, fmt.Errorf("%w: %s records no org prefixes, so a re-resolved plan would hold the artifact alone",
			errSourceGone, m.Name)
	}
	switch m.Source.Kind {
	case "repo":
		if strings.TrimSpace(m.Source.Ref) == "" {
			return src, fmt.Errorf("%w: %s records source kind %q but no ref", errSourceGone, m.Name, m.Source.Kind)
		}
		src.from = m.Source.Ref
	case "binary":
		if strings.TrimSpace(m.Source.Binary) == "" {
			return src, fmt.Errorf("%w: %s records source kind %q but no path", errSourceGone, m.Name, m.Source.Kind)
		}
		if _, err := os.Stat(m.Source.Binary); err != nil {
			// Expected, not exceptional: a rig outlives the build directory it
			// was made from. The baseline is stored for exactly this reason.
			return src, fmt.Errorf("%w: %s is gone, so its build info cannot be read again",
				errSourceGone, m.Source.Binary)
		}
		src.binary = m.Source.Binary
	default:
		return src, fmt.Errorf("%w: %s was assembled by hand (source kind %q)",
			errSourceGone, m.Name, orDash(m.Source.Kind))
	}
	return src, nil
}

func runRigAdd(args []string) (retErr error) {
	cfg, rigDir, err := rigSetup()
	if err != nil {
		return err
	}
	if len(rigAddModules) == 0 {
		return errors.New("nothing to add; name at least one module:\n" +
			"  wgo rig add <name> -m github.com/your-org/repo/module@v1.2.3")
	}
	mods, err := parseModulePins(rigAddModules)
	if err != nil {
		return err
	}
	rigRoot, err := resolveRigRoot(rigDir, args)
	if err != nil {
		return err
	}
	have, err := rig.Load(rigRoot)
	if err != nil {
		return err
	}
	warnOutOfOrg(have, mods)

	g := interrupt.Listen()
	defer g.Stop()
	ctx := g.Context()
	defer func() { retErr = g.Wrap(retErr) }()

	base := jj.NewCLI()
	jjc := base.WithContext(ctx)
	planner := &rig.Planner{
		Locator:  &cloneLocator{jjc: jjc, cfg: cfg},
		Resolver: jjc,
	}
	updated, diff, err := planner.AddModules(ctx, have, mods, checkoutOnDisk(rigRoot))
	if err != nil {
		return err
	}
	if diff.Empty() {
		rigLogf("rig %s already holds every module named; nothing to do", have.Name)
		fmt.Println(rigRoot)
		return nil
	}

	if rigAddDryRun {
		printRigDiff(updated, diff, rigRoot)
		return nil
	}

	mz := &rig.Materializer{
		JJ:      jjc,
		Cleanup: base.WithContext(context.WithoutCancel(ctx)),
		Logf:    rigLogf,
	}
	defer func() {
		if retErr != nil {
			mz.Rollback(rigRoot)
		}
	}()
	mz.Validate = gotool.NewClient().In(rigRoot).WithWork(filepath.Join(rigRoot, rig.GoWorkName))
	// Never prune from add: the diff it produces only ever grows the rig, so
	// there is nothing to remove and no way to ask for it.
	if err := mz.ApplyDiff(ctx, updated, diff, rigRoot, rig.PruneOpts{}); err != nil {
		return err
	}

	rigLogf("rig %s: %s (%d checkouts, %d modules)",
		updated.Name, diff.Summary(), len(updated.LiveCheckouts()), len(updated.Members))
	fmt.Println(rigRoot)
	return nil
}

// warnOutOfOrg flags a pin the rig's own filter would not have admitted.
//
// Not an error: `wgo rig add` exists to put source under a debugger, and a
// third-party dependency is a perfectly good thing to want to step through.
// Worth saying, because it will show up in `wgo rig verify` as an added module
// forever after.
func warnOutOfOrg(m *rig.Manifest, mods []gomod.Module) {
	for _, mod := range mods {
		if !gomod.InOrg(mod.Path, m.Source.OrgPrefixes) {
			rigLogf("note: %s is outside %s's org prefixes; adding it anyway because you named it",
				mod.Path, m.Name)
		}
	}
}

// checkoutOnDisk reports whether a checkout directory is still there, which is
// what separates "this checkout needs restoring" from "this checkout is fine".
func checkoutOnDisk(rigRoot string) func(string) bool {
	return func(dir string) bool {
		info, err := os.Stat(filepath.Join(rigRoot, rig.SrcDir, dir))
		return err == nil && info.IsDir()
	}
}

// reportObsolete names the checkouts a sync left alone, and how to remove them.
func reportObsolete(name string, remove []rig.Checkout) {
	rigLogf("%d checkout(s) are no longer in the plan and were left in place:", len(remove))
	for _, c := range remove {
		rigLogf("  %s (%s @ %s)", c.Dir, c.Repo, rigPin(c))
	}
	rigLogf("they are not in %s any more, so nothing builds against them, but they stay in %s\n"+
		"as obsolete entries so their jj workspaces can still be found; remove them with: wgo rig sync %s --prune",
		rig.GoWorkName, rig.ManifestName, name)
}

func printRigDiff(m *rig.Manifest, d *rig.Diff, rigRoot string) {
	fmt.Printf("rig %s (%s)\n", m.Name, rigRoot)
	if d.Empty() {
		fmt.Printf("\nno changes: %d checkouts, %d modules\n", len(m.LiveCheckouts()), len(m.Members))
		return
	}
	fmt.Println()
	for _, c := range d.Add {
		fmt.Printf("  + %-40s %s @ %s\n", c.Dir, c.Repo, rigPin(c))
	}
	for _, c := range d.Restore {
		fmt.Printf("  ~ %-40s %s @ %s (missing, would be restored)\n", c.Dir, c.Repo, rigPin(c))
	}
	for _, w := range d.Widen {
		fmt.Printf("  > %-40s widen to cover %s\n", w.Dir, strings.Join(w.Added, ", "))
	}
	for _, c := range d.Remove {
		fmt.Printf("  - %-40s %s @ %s (obsolete)\n", c.Dir, c.Repo, rigPin(c))
	}
	fmt.Printf("\n%s\n", d.Summary())
	switch {
	case len(d.Remove) == 0:
	case !rigSyncPrune:
		fmt.Printf("obsolete checkouts stay on disk, and in %s so their workspaces stay findable, "+
			"unless you pass --prune\n", rig.ManifestName)
	case !rigSyncForce:
		fmt.Printf("one with uncommitted changes would still be kept; --force removes it anyway\n")
	}
}
