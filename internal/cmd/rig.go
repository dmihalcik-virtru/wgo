package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/gomod"
	"github.com/virtru/wgo/internal/gotool"
	"github.com/virtru/wgo/internal/interrupt"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/rig"
	"github.com/virtru/wgo/models"
	"golang.org/x/mod/modfile"
)

var (
	rigNewFrom       string
	rigNewFromBinary string
	rigNewModules    []string
	rigNewOrgs       []string
	rigNewFull       bool
	rigNewNoVerify   bool
	rigNewDryRun     bool

	rigLsFormat string
	rigShowEnv  bool
	rigRmForce  bool
)

var rigCmd = &cobra.Command{
	Use:   "rig",
	Short: "Pinned, disposable Go workspaces for debugging a shipped artifact",
	Long: `A rig is a directory of jj workspaces, each pinned to the commit a released
artifact shipped with, wired together by a generated go.work.

It exists to answer "what was actually running in production" — attach a
debugger to the exact code that shipped, across every repo it came from,
without disturbing any of your branch work.

Checkouts are sparse by default and carry no bookmark: they are pinned to
tags, so there is nothing to push from them. Land fixes from a normal
worktree instead.`,
}

var rigNewCmd = &cobra.Command{
	Use:   "new <name>",
	Short: "Create a rig from a released artifact",
	Long: `Resolves every in-org module a released artifact depends on to the commit it
shipped from, checks each one out, and generates a go.work that binds them
together.

Pins come from one of two sources:

  --from <owner/repo>@<ref>   the artifact's tagged source, whose dependency
                              set is read with ` + "`go list`" + `
  --from-binary <path>        the compiled artifact itself, whose dependency
                              set is read from its embedded build info

Prefer --from-binary when you have the shipped binary: build info records the
versions that were actually linked, so the rig reproduces them exactly rather
than re-deriving them and risking a different resolution.

The rig path is the only thing printed to stdout, so it composes with cd:
  cd $(wgo rig new dsp-2.7.1 --from virtru-corp/data-security-platform@v2.7.1)

Then source the generated env.sh — or eval "$(wgo rig show <name> --env)" —
before running any go command. GOWORK must point at the rig's go.work; the
checkouts ship their own, and Go's upward search finds those first.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRigNew(args[0])
	},
}

var rigLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List rigs",
	Long: `List every rig under rig.dir.

When stdout is not a terminal, prints one rig path per line by default, so
it composes with fzf and xargs. Use --format to override.`,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRigLs()
	},
}

var rigShowCmd = &cobra.Command{
	Use:   "show [name]",
	Short: "Show a rig's modules, pins and sparse sets",
	Long: `Show what a rig pins and where each module comes from.

With no name, shows the rig containing the current directory.

--env prints shell exports instead, for:
  eval "$(wgo rig show dsp-2.7.1 --env)"`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRigShow(args)
	},
}

var rigRmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Forget a rig's workspaces and remove its tree",
	Long: `Forget every jj workspace the rig registered in its main clones, then delete
the rig directory.

Deleting the directory by hand instead leaves those workspaces registered in
the main clones with nothing on disk naming them.

Refuses if any checkout has uncommitted changes, unless --force.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRigRm(args[0])
	},
}

func init() {
	rootCmd.AddCommand(rigCmd)
	rigCmd.AddCommand(rigNewCmd, rigLsCmd, rigShowCmd, rigRmCmd)

	// `new` takes a name that does not exist yet, so there is nothing to
	// complete; the rest take one that does.
	rigNewCmd.ValidArgsFunction = cobra.NoFileCompletions
	for _, c := range []*cobra.Command{rigShowCmd, rigRmCmd, rigVerifyCmd, rigSyncCmd, rigAddCmd} {
		c.ValidArgsFunction = rigNameCompletions
	}

	rigNewCmd.Flags().StringVar(&rigNewFrom, "from", "", "Resolve pins from a tagged repo: <owner/repo>@<ref>")
	rigNewCmd.Flags().StringVar(&rigNewFromBinary, "from-binary", "", "Resolve pins from a compiled binary's embedded build info")
	rigNewCmd.Flags().StringArrayVarP(&rigNewModules, "module", "m", nil, "Add a module explicitly: <path>@<version> (repeatable)")
	rigNewCmd.Flags().StringArrayVar(&rigNewOrgs, "org", nil, "Treat this module path prefix as in-org (repeatable, overrides config)")
	rigNewCmd.Flags().BoolVar(&rigNewFull, "full", false, "Use full checkouts instead of sparse ones")
	rigNewCmd.Flags().BoolVar(&rigNewNoVerify, "no-verify", false,
		"Skip the drift check that otherwise runs once the rig is built")
	rigNewCmd.Flags().BoolVar(&rigNewDryRun, "dry-run", false, "Print the plan and exit without leaving a rig behind")

	rigLsCmd.Flags().StringVar(&rigLsFormat, "format", "", "Output format: table, path, json (default: table when TTY, path when piped)")
	rigShowCmd.Flags().BoolVar(&rigShowEnv, "env", false, `Print shell exports for eval "$(wgo rig show <name> --env)"`)
	rigRmCmd.Flags().BoolVar(&rigRmForce, "force", false, "Remove even if a checkout has uncommitted changes")
}

// rigNameCompletions completes the `[name]` argument of every rig subcommand
// that acts on an existing rig.
//
// Every failure returns no completions rather than an error: a completion
// function's output is spliced into the user's command line, so a config
// problem must degrade to "no suggestions", never to a diagnostic appearing
// mid-prompt. rig.Names is used rather than rig.List for the same reason — it
// parses nothing and warns about nothing.
func rigNameCompletions(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	_, rigDir, err := rigSetup()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names, err := rig.Names(rigDir)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// resolveRigRef identifies the rig a workspace belongs to, for `wgo .` and the
// statusline. Returns nil for ordinary branch work.
//
// Local-only and cheap enough for the statusline's hot path: a bounded walk up
// to rig.dir looking for a rig.toml, then one TOML read to name the pin. Every
// failure degrades to nil or to a ref without a pin — a prompt segment is not
// worth an error.
func resolveRigRef(wsRoot string) *models.RigRef {
	cfg := config.Get()
	if cfg == nil {
		// `wgo .` and the statusline both tolerate an uninitialised config;
		// without one there is no rig.dir, so there is no rig to be in.
		return nil
	}
	return rigRefIn(cfg.Rig.Dir, wsRoot)
}

// rigRefIn is resolveRigRef against an explicit rig.dir.
func rigRefIn(rigDir, wsRoot string) *models.RigRef {
	root := rigRootContaining(rigDir, wsRoot)
	if root == "" {
		return nil
	}
	ref := &models.RigRef{Name: filepath.Base(root), Path: root}
	m, err := rig.Load(root)
	if err != nil {
		return ref
	}
	ref.Name = m.Name
	if c := rigCheckoutFor(m, root, wsRoot); c != nil {
		ref.Pin = rigPin(*c)
	}
	return ref
}

// rigRefLabel renders a rig reference as "<name>@<pin>", or just the name when
// the pin is unknown — a manifest too broken to load still tells us which rig
// the user is standing in, which is the part that changes how to read the rest
// of the line.
func rigRefLabel(r models.RigRef) string {
	if r.Pin == "" {
		return r.Name
	}
	return r.Name + "@" + r.Pin
}

// rigCheckoutFor finds the manifest entry for the checkout containing dir.
//
// The checkout is the first path component under <rig>/src, so this works from
// a subdirectory of a checkout as well as from its root.
func rigCheckoutFor(m *rig.Manifest, rigRoot, dir string) *rig.Checkout {
	rel, err := filepath.Rel(filepath.Join(rigRoot, rig.SrcDir), dir)
	if err != nil {
		return nil
	}
	name, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	if name == "" || name == "." || name == ".." {
		// At the rig root, or above it: no one checkout to name.
		return nil
	}
	return m.CheckoutByDir(name)
}

// rigLogf writes progress to stderr.
//
// Everything a rig command says goes to stderr except the rig path itself, so
// `cd $(wgo rig new ...)` gets a path and not a progress log.
func rigLogf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// rigSetup resolves the config and rig directory shared by every subcommand.
func rigSetup() (*config.Config, string, error) {
	if err := config.Init(); err != nil {
		return nil, "", fmt.Errorf("config: %w", err)
	}
	cfg := config.Get()
	dir := strings.TrimSpace(cfg.Rig.Dir)
	if dir == "" {
		return nil, "", errors.New("rig.dir is not configured; set it in ~/.wgo/config.toml")
	}
	// config.Init has already expanded a leading ~.
	if !filepath.IsAbs(dir) {
		// The rig root becomes GOWORK, which Go rejects unless absolute.
		return nil, "", fmt.Errorf("rig.dir must be an absolute path, got %q", cfg.Rig.Dir)
	}
	return cfg, dir, nil
}

// cloneLocator adapts findOrCloneRepo to rig.RepoLocator, fetching tags once
// per repository.
//
// jj does not fetch tags by default, and every rig pin resolves through a tag
// revset. Without this the first `rig new` against a freshly cloned repo fails
// on every module at once.
type cloneLocator struct {
	jjc      jj.Client
	cfg      *config.Config
	fetched  map[string]bool
	fetchedC map[string]bool
}

// FetchCommits updates the clone's branch tips, once per repository.
//
// Locate fetches tags, which is all a version-derived pin ever needs. A commit
// named directly — `--from-binary` on a CI build, where go records
// `vcs.revision` and no version — is usually reachable only from a branch, so
// the tag-only fetch leaves it missing. rig.Planner calls this only on that
// path, and only after resolution has already failed once.
func (l *cloneLocator) FetchCommits(clone string) error {
	if l.fetchedC == nil {
		l.fetchedC = map[string]bool{}
	}
	if l.fetchedC[clone] {
		return nil
	}
	l.fetchedC[clone] = true
	rigLogf("fetching branches for %s...", clone)
	return l.jjc.GitFetch(clone, "origin", nil)
}

func (l *cloneLocator) Locate(owner, repo string) (string, error) {
	// NoInit: a rig needs history. `wgo to`'s empty-repo fallback would hand
	// back a commitless repo here, whose pins then fail to resolve as "no such
	// tag" — aborting the whole rig over what is really an unreachable
	// repository, which the planner knows how to skip.
	clone, err := findOrCloneRepoNoInit(l.jjc, l.cfg, owner, repo)
	if err != nil {
		return "", err
	}
	if l.fetched == nil {
		l.fetched = map[string]bool{}
	}
	if !l.fetched[clone] {
		l.fetched[clone] = true
		rigLogf("fetching tags for %s/%s...", owner, repo)
		if err := l.jjc.GitFetchTags(clone, "origin", nil); err != nil {
			// Best-effort: the tag may already be present from an earlier
			// fetch, and resolution reports a far more specific error than
			// this if it is not.
			rigLogf("warning: fetching tags for %s/%s failed (using cached state): %v", owner, repo, err)
		}
	}
	return clone, nil
}

func runRigNew(name string) (retErr error) {
	cfg, rigDir, err := rigSetup()
	if err != nil {
		return err
	}
	// Before touching the disk: the build list decides which checkouts exist,
	// and neither source can produce one without the toolchain — `go list` for
	// --from, `go version -m` for --from-binary.
	if !gotool.Available() {
		return errors.New("`go` is not on PATH; a rig's checkout set is read with the go toolchain, so it cannot be built without it")
	}
	if err := rig.ValidateName(name); err != nil {
		return err
	}
	if err := checkRigSourceFlags(); err != nil {
		return err
	}
	orgs := rigNewOrgs
	if len(orgs) == 0 {
		orgs = cfg.Rig.OrgPrefixes
	}
	if len(orgs) == 0 {
		return errors.New("no in-org module prefixes configured; " +
			"set rig.org_prefixes in ~/.wgo/config.toml or pass --org github.com/your-org.\n" +
			"Without one, every dependency is left to the module cache and the rig holds only the artifact itself")
	}
	extra, err := parseModulePins(rigNewModules)
	if err != nil {
		return err
	}

	rigRoot := filepath.Join(rigDir, name)
	if err := checkRigRootFree(rigRoot, name); err != nil {
		return err
	}

	// A rig build is the longest-running mutation wgo performs, and until the
	// manifest lands an interrupted one strands both a directory and a
	// workspace. Catch the signal so the rollback below actually runs.
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

	mz := &rig.Materializer{
		JJ: jjc,
		// Rollback outlives the cancellation that triggered it: forgetting a
		// workspace with the cancelled client would fail on every one and
		// leave exactly the mess rollback exists to clear.
		Cleanup: base.WithContext(context.WithoutCancel(ctx)),
		Logf:    rigLogf,
	}
	// From the first checkout until Materialize writes the manifest the rig is
	// on disk with nothing recording it, so nothing but this can clean it up.
	defer func() {
		if retErr != nil {
			mz.Rollback(rigRoot)
		}
	}()

	pins, err := resolveRigPins(planner, func(c *rig.Checkout) (string, error) {
		return mz.Checkout(ctx, rigRoot, c)
	}, name, rigSource{
		from:    rigNewFrom,
		binary:  rigNewFromBinary,
		modules: rigNewModules,
		orgs:    orgs,
	})
	if err != nil {
		return err
	}
	buildList := append(pins.buildList, extra...)

	// rig.sparse decides; --full is the override. Reading only the flag would
	// make a `sparse = false` config silently do nothing.
	sparse := cfg.Rig.Sparse && !rigNewFull

	m, err := planner.Plan(ctx, rig.Request{
		Name:            name,
		Source:          pins.source,
		OrgPrefixes:     orgs,
		Sparse:          sparse,
		Primary:         pins.primary,
		PrimaryUse:      pins.use,
		PrimaryCheckout: pins.checkout,
		BuildList:       buildList,
		GoVersion:       pins.primary.GoVersion,
		Baseline:        baselineOf(buildList),
		Created:         time.Now().UTC().Format(time.RFC3339),
		WgoVersion:      getVersionString(),
	})
	if err != nil {
		return err
	}

	if rigNewDryRun {
		printRigPlan(m, rigRoot)
		// The primary had to be checked out to get this far; a dry run that
		// left it behind would not be a dry run.
		mz.Rollback(rigRoot)
		return nil
	}

	gc := gotool.NewClient().In(rigRoot).WithWork(filepath.Join(rigRoot, rig.GoWorkName))
	mz.Validate = gc
	if err := mz.Materialize(ctx, m, rigRoot); err != nil {
		return err
	}

	reportSkips(m)
	rigLogf("rig %s: %d checkouts, %d modules", m.Name, len(m.LiveCheckouts()), len(m.Members))

	if cfg.Rig.VerifyOnNew && !rigNewNoVerify {
		verifyNewRig(gc, m, rigRoot, cfg.Rig.Freeze)
	}

	rigLogf("run `. %s` before any go command", filepath.Join(rigRoot, rig.EnvShName))
	fmt.Println(rigRoot)
	return nil
}

// verifyNewRig runs the drift check straight after creation, because the moment
// a rig is built is the moment its drift is cheapest to act on.
//
// Nothing here is fatal. The rig exists, its checkouts are pinned, and stdout
// has to stay a usable `cd $(wgo rig new ...)` — reporting drift by deleting
// the thing that would let you look at it would be absurd. Failures to measure
// are reported and dropped for the same reason.
func verifyNewRig(gc *gotool.Client, m *rig.Manifest, rigRoot string, freeze bool) {
	patterns := m.PackagePatterns()
	if len(patterns) == 0 {
		return
	}
	actual, err := gc.ListPackageModules(patterns...)
	if err != nil {
		rigLogf("could not check for drift: %v", err)
		rigLogf("check it later with: wgo rig verify %s", m.Name)
		return
	}
	rep := rig.Verify(m, actual)
	failing := rep.Failing()
	if len(failing) == 0 {
		rigLogf("no drift: all %d baseline modules resolve as the artifact shipped them", rep.Compared)
		return
	}

	rigLogf("%d module(s) do not match what %s shipped with:", len(failing), m.Primary)
	for _, d := range failing {
		rigLogf("  %s", d)
	}
	if !freeze {
		rigLogf("pin them back with: wgo rig verify %s --freeze", m.Name)
		return
	}

	res, freezeErr := rig.Freeze(gc, m, rep, os.DevNull)
	// Recorded before the error is reported: a freeze that fails partway has
	// already written replaces into go.work, and a manifest that does not name
	// them leaves nothing for `--unfreeze` to drop.
	if len(res.Froze) > 0 {
		if err := rig.Save(rigRoot, m); err != nil {
			rigLogf("froze %d module(s) but could not record it: %v", len(res.Froze), err)
			return
		}
		rigLogf("froze %d module(s) back to the baseline in %s", len(res.Froze), rig.GoWorkName)
	}
	if freezeErr != nil {
		rigLogf("could not freeze: %v", freezeErr)
		return
	}
	reportUnfrozen(res, m)
	if res.BuildErr != nil {
		// The pins are in force and the rig no longer compiles: a member wants
		// a version the artifact did not ship with. Say which way out exists
		// rather than leaving a green-looking rig that cannot build.
		rigLogf("but the rig no longer builds: %v", res.BuildErr)
		rigLogf("drop the pin that broke it with: wgo rig verify %s --unfreeze <module>", m.Name)
		return
	}
	if len(res.Froze) == 0 {
		return
	}
	// A replace only takes effect if nothing else in the graph overrides it, so
	// the freeze is not proof the drift is gone. Saying "froze 4 modules" and
	// stopping there would report a fix that may not have held.
	actual, err = gc.ListPackageModules(patterns...)
	if err != nil {
		rigLogf("could not confirm the freeze took: %v", err)
		return
	}
	if left := rig.Verify(m, actual).Failing(); len(left) > 0 {
		rigLogf("%d module(s) still do not match after the freeze:", len(left))
		for _, d := range left {
			rigLogf("  %s", d)
		}
	}
}

// checkRigRootFree rejects a rig root that is already occupied.
func checkRigRootFree(rigRoot, name string) error {
	entries, err := os.ReadDir(rigRoot)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("rig: checking %s: %w", rigRoot, err)
	case len(entries) == 0:
		// An empty directory the user made ahead of time is not in the way.
		return nil
	}
	if _, err := rig.Load(rigRoot); err == nil {
		// Deliberately an error rather than an implicit sync. `rig new` carries
		// flags — --full, --org, --module — that a sync of an existing rig does
		// not honour, so quietly running one instead would ignore half of what
		// was typed and report success.
		return fmt.Errorf("rig %q already exists at %s\n"+
			"bring it up to date with: wgo rig sync %s\n"+
			"or start over with:        wgo rig rm %s", name, rigRoot, name, name)
	}
	return fmt.Errorf("%s already exists and is not a rig; remove it or choose another name", rigRoot)
}

// parseRigFrom splits `--from <owner>/<repo>@<ref>`.
func parseRigFrom(from string) (owner, repo, ref string, err error) {
	from = strings.TrimSpace(from)
	if from == "" {
		return "", "", "", errors.New("a source is required: --from <owner/repo>@<ref>")
	}
	slug, ref, ok := strings.Cut(from, "@")
	if !ok || strings.TrimSpace(ref) == "" {
		return "", "", "", fmt.Errorf("--from %q: expected <owner/repo>@<ref>, e.g. --from virtru-corp/data-security-platform@v2.7.1", from)
	}
	owner, repo, ok = strings.Cut(slug, "/")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(repo) == "" {
		return "", "", "", fmt.Errorf("--from %q: expected <owner/repo>@<ref>, e.g. --from virtru-corp/data-security-platform@v2.7.1", from)
	}
	return owner, repo, ref, nil
}

// parseModulePins parses repeated `-m <path>@<version>` flags.
func parseModulePins(pins []string) ([]gomod.Module, error) {
	var out []gomod.Module
	for _, pin := range pins {
		path, version, ok := strings.Cut(strings.TrimSpace(pin), "@")
		if !ok || path == "" || version == "" {
			return nil, fmt.Errorf("-m %q: expected <module-path>@<version>, e.g. -m github.com/opentdf/platform/sdk@v0.10.1", pin)
		}
		out = append(out, gomod.Module{Path: path, Version: version})
	}
	return out, nil
}

// checkRigSourceFlags requires exactly one pin source.
//
// Checked before anything is resolved. Both sources produce a build list, and
// silently preferring one would build a rig the user did not ask for out of
// pins they did not expect.
func checkRigSourceFlags() error {
	from := strings.TrimSpace(rigNewFrom) != ""
	binary := strings.TrimSpace(rigNewFromBinary) != ""
	switch {
	case from && binary:
		return errors.New("--from and --from-binary are alternatives; pass one.\n" +
			"--from-binary is the more faithful of the two: build info records what was linked, " +
			"rather than what re-resolving the source would produce today")
	case !from && !binary:
		return errors.New("a source is required:\n" +
			"  --from <owner/repo>@<ref>   resolve pins from the artifact's tagged source\n" +
			"  --from-binary <path>        resolve pins from the compiled artifact")
	}
	return nil
}

// rigPins is everything a pin source contributes to a plan: where the pins came
// from, the artifact's identity and dependency set, and a checkout of the
// artifact's own source that already exists on disk.
type rigPins struct {
	source    rig.Source
	checkout  *rig.Checkout
	primary   gomod.Module
	use       []string
	buildList []gomod.Module
}

// rigSource is the pin source a run was asked for.
//
// Lifted out of the command's flag variables because `wgo rig sync` re-resolves
// the source recorded in an existing rig.toml rather than one typed on the
// command line, and reading the flags directly would have made it re-resolve
// whatever the last `rig new` happened to leave in them.
type rigSource struct {
	// from is "<owner>/<repo>@<ref>".
	from string
	// binary is the path to a compiled artifact.
	binary string
	// modules are the explicit `-m` pins, recorded on the manifest's Source.
	modules []string
	// orgs is the in-org filter in force.
	orgs []string
}

// checkoutFunc materialises the primary's checkout and returns its path.
//
// `rig new` creates it. `rig sync` hands back the one the rig already has,
// which is what keeps a sync from rebuilding a working copy the user may be
// sitting in.
type checkoutFunc func(*rig.Checkout) (string, error)

// resolveRigPins materialises the primary checkout and reads the build list.
//
// Both sources need the primary on disk before they can finish: --from cannot
// run `go list` without it, and --from-binary reads the repository's own
// go.work `use` list from it. So both go through checkout, and for `rig new`
// everything it creates is on the Materializer's rollback list from that point
// on.
func resolveRigPins(planner *rig.Planner, checkout checkoutFunc, name string, src rigSource) (*rigPins, error) {
	if strings.TrimSpace(src.binary) != "" {
		return pinsFromBinary(planner, checkout, name, src)
	}
	return pinsFromRepo(planner, checkout, name, src)
}

// pinsFromRepo resolves pins by checking the artifact's source out at a tag and
// running `go list -deps` inside it.
//
// The build list comes from `go list` run with the repository's *own* go.work,
// not the rig's — the rig's does not exist yet, and the point is to reproduce
// the build list the artifact shipped with.
func pinsFromRepo(planner *rig.Planner, checkout checkoutFunc, name string, src rigSource) (*rigPins, error) {
	owner, repo, ref, err := parseRigFrom(src.from)
	if err != nil {
		return nil, err
	}
	pc, err := planner.ResolvePrimaryRepo(name, owner, repo, ref)
	if err != nil {
		return nil, err
	}
	dest, err := checkout(pc)
	if err != nil {
		return nil, err
	}
	// No subdir: `--from <owner>/<repo>@<ref>` names a repository, so the
	// artifact is its root module by construction.
	mod, use, err := readPrimaryModule(dest, "", "")
	if err != nil {
		return nil, err
	}
	primary := gomod.Module{Path: mod.Path, Version: ref, Main: true, GoVersion: mod.GoVersion}

	gc := gotool.NewClient().In(dest)
	work, err := gotool.FindWorkFile(dest, dest)
	if err != nil {
		return nil, err
	}
	if work != "" {
		gc = gc.WithWork(work)
	}
	buildList, err := gc.ListPackageModules("./...")
	if err != nil {
		return nil, fmt.Errorf("rig: listing dependencies of %s: %w", primary.Path, err)
	}

	return &rigPins{
		source: rig.Source{
			Kind:        "repo",
			Ref:         owner + "/" + repo + "@" + ref,
			Modules:     src.modules,
			OrgPrefixes: src.orgs,
		},
		checkout:  pc,
		primary:   primary,
		use:       use,
		buildList: buildList,
	}, nil
}

// pinsFromBinary resolves pins from a compiled artifact's embedded build info.
//
// This is the higher-fidelity source: `go version -m` reports the versions that
// were actually linked, so the rig reproduces the artifact by construction
// rather than by re-deriving a build list that MVS may resolve differently
// today. It is also the only source that works when the artifact was built from
// a commit that was never tagged.
func pinsFromBinary(planner *rig.Planner, checkout checkoutFunc, name string, src rigSource) (*rigPins, error) {
	binary := src.binary
	abs, err := filepath.Abs(strings.TrimSpace(binary))
	if err != nil {
		return nil, fmt.Errorf("rig: resolving %s: %w", binary, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("rig: reading %s: %w", abs, err)
	}
	// `go version -m` walks a directory and reports every binary under it, and
	// BuildInfo parses that output as though it described one. A whole ./dist/
	// would silently merge several artifacts' dependency sets into a single
	// build list — a rig that reproduces nothing that was ever shipped. Stat
	// follows symlinks, so a link to a binary is still a regular file here.
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("rig: %s is not a file; --from-binary takes one compiled artifact", abs)
	}
	bi, err := gotool.NewClient().BuildInfo(abs)
	if err != nil {
		return nil, fmt.Errorf("rig: reading build info from %s: %w", abs, err)
	}
	if bi.Main.Path == "" {
		return nil, fmt.Errorf("rig: %s records no main module\n"+
			"it may have been built with -trimpath alone against GOFLAGS=-mod=vendor, or linked from a non-module build", abs)
	}

	pc, err := resolveBinaryPrimary(planner, name, abs, bi)
	if err != nil {
		return nil, err
	}
	dest, err := checkout(pc)
	if err != nil {
		return nil, err
	}
	// Read for the `go` directive and the repo's own go.work `use` list, both
	// of which build info does not record. The module path is already known, so
	// it is passed in as a cross-check on the module-to-repo mapping.
	//
	// The subdir matters here in a way it does not for --from: a binary's main
	// module can be any module of a monorepo, not just its root.
	origin, err := gomod.ParseOrigin(bi.Main.Path)
	if err != nil {
		return nil, fmt.Errorf("rig: main module %s of %s: %w", bi.Main.Path, abs, err)
	}
	mod, use, err := readPrimaryModule(dest, origin.Subdir, bi.Main.Path)
	if err != nil {
		return nil, err
	}

	return &rigPins{
		source: rig.Source{
			Kind:        "binary",
			Binary:      abs,
			Modules:     src.modules,
			OrgPrefixes: src.orgs,
		},
		checkout: pc,
		primary: gomod.Module{
			Path: bi.Main.Path, Version: bi.Main.Version, Main: true,
			// Build info records no per-dependency `go` directives, unlike
			// `go list`, so the usual "highest across the members" calculation
			// has only the primary to go on and can land below what a member
			// requires — which surfaces as `go build` refusing the workspace.
			// The toolchain that linked the artifact is recorded, though, and
			// it is by construction at least as high as every member needs.
			GoVersion: gomod.MaxGoVersion(mod.GoVersion, gomod.ToolchainVersion(bi.GoVersion)),
		},
		use:       use,
		buildList: buildInfoModules(bi),
	}, nil
}

// resolveBinaryPrimary pins the artifact's own source.
//
// A binary built from a released tag carries that version and resolves like any
// other module. One built by CI from an untagged commit carries "(devel)"
// instead, with the commit recorded separately as the `vcs.revision` build
// setting — which is exactly the case a rig is most wanted for, so it is
// supported rather than rejected.
func resolveBinaryPrimary(planner *rig.Planner, name, binary string, bi *gomod.BuildInfo) (*rig.Checkout, error) {
	if gomod.IsResolvableVersion(bi.Main.Version) {
		return planner.ResolvePrimary(name, gomod.Module{Path: bi.Main.Path, Version: bi.Main.Version})
	}

	rev := strings.TrimSpace(bi.Settings["vcs.revision"])
	if rev == "" {
		return nil, fmt.Errorf(
			"rig: %s reports main module %s at %q and records no vcs.revision, so there is no commit to pin its source to\n"+
				"binaries built with -buildvcs=false lose this; rebuild with build info, or use --from <owner/repo>@<ref>",
			binary, bi.Main.Path, orDash(bi.Main.Version))
	}
	origin, err := gomod.ParseOrigin(bi.Main.Path)
	if err != nil {
		return nil, fmt.Errorf("rig: main module %s of %s: %w", bi.Main.Path, binary, err)
	}
	if bi.Settings["vcs.modified"] == "true" {
		// The checkout will hold the commit, not the uncommitted edits that
		// were compiled into the binary. Everything downstream — the baseline,
		// `rig verify` — is still sound; only the primary's source may differ.
		rigLogf("warning: %s was built from a modified working copy (vcs.modified=true)", binary)
		rigLogf("         the primary checkout will hold commit %s, not the edits that were compiled", shortCommitID(rev))
	}
	rigLogf("%s has no released version; pinning its source to vcs.revision %s", filepath.Base(binary), shortCommitID(rev))
	return planner.ResolvePrimaryCommit(name, origin.Owner, origin.Repo, rev)
}

// buildInfoModules turns `go version -m` dep records into build-list entries.
//
// The one transformation is on directory replacements. Build info spells those
// as a "=>" record with version "(devel)", whereas `go list` — which the --from
// path uses — spells them as a Replace with no version at all. The planner
// recognises the latter and skips the module as served from another checkout,
// so the two sources are normalised onto that shape here rather than having the
// planner learn a second spelling of the same fact.
//
// Versioned replacements are passed through exactly as `go list` reports them,
// which is to say the planner pins the original module path rather than the
// fork. That is a pre-existing limitation shared by both sources, not something
// this path introduces.
func buildInfoModules(bi *gomod.BuildInfo) []gomod.Module {
	out := make([]gomod.Module, 0, len(bi.Deps))
	for _, dep := range bi.Deps {
		if dep.Replace != nil && !gomod.IsResolvableVersion(dep.Replace.Version) {
			replaced := *dep.Replace
			replaced.Version = ""
			dep.Replace = &replaced
		}
		out = append(out, dep)
	}
	return out
}

// readPrimaryModule reads the artifact's module path, `go` directive and
// workspace `use` list out of its freshly created checkout.
//
// subdir is the primary module's directory within the repository, empty for a
// root module. A monorepo publishes each of its modules separately, so the
// artifact's own go.mod is not necessarily at the checkout root:
// github.com/opentdf/platform/otdfctl lives at otdfctl/go.mod, and reading the
// root would report the repo as holding no module at all.
//
// wantPath, when non-empty, is the module path the caller already believes the
// checkout holds; a mismatch means the module-to-repo mapping put the primary
// in the wrong repository, and every checkout derived from that mapping is
// suspect. It is a warning rather than an error because a repo that moved its
// module path between the pinned commit and today produces exactly this, and
// the checkout is still the right source.
func readPrimaryModule(dest, subdir, wantPath string) (gomod.Module, []string, error) {
	var none gomod.Module

	modDir := filepath.Join(dest, filepath.FromSlash(subdir))
	modPath := filepath.Join(modDir, "go.mod")
	data, err := os.ReadFile(modPath)
	if err != nil {
		return none, nil, fmt.Errorf("rig: reading %s: %w\nthe ref may not point at a Go module's root", modPath, err)
	}
	f, err := modfile.Parse(modPath, data, nil)
	if err != nil {
		return none, nil, fmt.Errorf("rig: parsing %s: %w", modPath, err)
	}
	if f.Module == nil || f.Module.Mod.Path == "" {
		return none, nil, fmt.Errorf("rig: %s declares no module path", modPath)
	}
	mod := gomod.Module{Path: f.Module.Mod.Path, Main: true}
	if f.Go != nil {
		mod.GoVersion = f.Go.Version
	}
	if wantPath != "" && wantPath != mod.Path {
		rigLogf("warning: %s declares module %s, but the artifact reports %s", modPath, mod.Path, wantPath)
		rigLogf("         the module path may have moved, or the primary may be checked out of the wrong repository")
	}

	use, err := readWorkUse(dest, modDir)
	if err != nil {
		return none, nil, err
	}
	return mod, use, nil
}

// readWorkUse returns the `use` directives of the repository's own go.work as
// checkout-relative directories, or nil when it ships none.
//
// A repo with a go.work builds against a different set of modules than its root
// module alone, so a rig that ignored it would not reproduce the artifact.
//
// The search starts at the primary module and walks up to the checkout root,
// which is Go's own rule — a subdirectory module can be covered by a go.work at
// the repo root, as otdfctl is by platform's. Wherever it is found, the `use`
// paths are rebased onto the checkout root so they line up with Member.Subdir.
func readWorkUse(dest, modDir string) ([]string, error) {
	work, err := gotool.FindWorkFile(modDir, dest)
	if err != nil || work == "" {
		return nil, err
	}
	data, err := os.ReadFile(work)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rig: reading %s: %w", work, err)
	}
	f, err := modfile.ParseWork(work, data, nil)
	if err != nil {
		return nil, fmt.Errorf("rig: parsing %s: %w", work, err)
	}
	base, err := filepath.Rel(dest, filepath.Dir(work))
	if err != nil {
		return nil, fmt.Errorf("rig: locating %s within %s: %w", work, dest, err)
	}

	var use []string
	for _, u := range f.Use {
		rel := path.Join(filepath.ToSlash(base), u.Path)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			// A use directive pointing outside the checkout names source no
			// single repository holds; the rig cannot supply it, and inventing
			// a member for it would produce a go.work that does not resolve.
			rigLogf("warning: %s uses %s, which is outside the checkout — ignoring it", work, u.Path)
			continue
		}
		use = append(use, rel)
	}
	return use, nil
}

// baselineOf records the version of every module in the build list, so a later
// `wgo rig verify` can tell what the artifact shipped with.
//
// The recorded version is the *effective* one — what a `replace` redirected the
// module to, not what its own go.mod asked for. Those differ often enough to
// matter: a `replace ... => ../sibling` leaves the declared version as a stale
// pseudo-version nothing was ever built from, and a fork replace pins a version
// from a different module path entirely. Recording the declared version would
// make every replaced module read as drift on the first `rig verify`, since
// verification measures what the build actually resolves to.
func baselineOf(buildList []gomod.Module) map[string]string {
	out := map[string]string{}
	for _, mod := range buildList {
		if mod.Path == "" {
			continue
		}
		// BaselineEntry drops what cannot be compared later — a directory
		// replace has no version, "(devel)" names no release — and qualifies a
		// fork's version with the module path it belongs to.
		if entry := rig.BaselineEntry(mod); entry != "" {
			out[mod.Path] = entry
		}
	}
	return out
}

// reportSkips tells the user which modules got no checkout.
//
// Skips are the difference between "the rig is complete" and "the rig looks
// complete but three modules are silently coming from the module cache", so
// the ones that indicate something wrong are always shown.
func reportSkips(m *rig.Manifest) {
	var notable []rig.Skip
	counts := map[rig.SkipKind]int{}
	for _, s := range m.Skipped {
		counts[s.Kind]++
		switch s.Kind {
		case rig.SkipUnreachable, rig.SkipEscapedReplace, rig.SkipUnpinned:
			notable = append(notable, s)
		}
	}
	for _, s := range notable {
		rigLogf("warning: %s@%s got no checkout — %s", s.Path, s.Version, s.String())
	}
	if n := counts[rig.SkipOutOfOrg] + counts[rig.SkipUnsupportedHost] + counts[rig.SkipLocalReplace]; n > 0 {
		rigLogf("%d dependencies left to the module cache (out of org, unsupported host, or locally replaced)", n)
	}
}

// printRigPlan renders a --dry-run plan.
func printRigPlan(m *rig.Manifest, rigRoot string) {
	fmt.Printf("rig %s (%s)\n\n", m.Name, rigRoot)
	fmt.Printf("%-40s %-28s %s\n", "CHECKOUT", "PIN", "CONTENTS")
	fmt.Println(strings.Repeat("-", 100))
	for _, c := range m.LiveCheckouts() {
		fmt.Printf("%-40s %-28s %s\n", c.Dir, rigPin(c), rigContents(c))
	}
	fmt.Printf("\n%d checkouts, %d modules", len(m.LiveCheckouts()), len(m.Members))
	if len(m.Skipped) > 0 {
		fmt.Printf(", %d skipped", len(m.Skipped))
	}
	fmt.Printf("\n\n%s:\n\n%s", rig.GoWorkName, rig.RenderGoWork(m))
}

type rigRow struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Source    string `json:"source,omitempty"`
	Checkouts int    `json:"checkouts"`
	Modules   int    `json:"modules"`
	Created   string `json:"created,omitempty"`
	Frozen    bool   `json:"frozen,omitempty"`
}

func runRigLs() error {
	_, rigDir, err := rigSetup()
	if err != nil {
		return err
	}
	manifests, err := rig.List(rigDir)
	if err != nil {
		return err
	}

	format := rigLsFormat
	if format == "" {
		format = "path"
		if isTerminal() {
			format = "table"
		}
	}

	rows := make([]rigRow, 0, len(manifests))
	for _, m := range manifests {
		rows = append(rows, rigRow{
			Name: m.Name,
			// m.Root, not rigDir/m.Name: `--format=path` is meant to be piped
			// into cd or xargs, and a renamed rig directory would send it
			// somewhere that does not exist.
			Path:      m.Root,
			Source:    rigSourceLabel(m.Source),
			Checkouts: len(m.LiveCheckouts()),
			Modules:   len(m.Members),
			Created:   m.Created,
			Frozen:    len(m.Frozen) > 0,
		})
	}

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	case "path":
		for _, r := range rows {
			fmt.Println(r.Path)
		}
		return nil
	case "table":
		if len(rows) == 0 {
			// stderr: an empty list piped into xargs should stay empty.
			fmt.Fprintf(os.Stderr, "no rigs in %s\ncreate one with: wgo rig new <name> --from <owner/repo>@<ref>\n", rigDir)
			return nil
		}
		fmt.Printf("%-24s %-44s %-10s %-8s %s\n", "NAME", "SOURCE", "CHECKOUTS", "MODULES", "CREATED")
		fmt.Println(strings.Repeat("-", 100))
		for _, r := range rows {
			fmt.Printf("%-24s %-44s %-10d %-8d %s\n", r.Name, orDash(r.Source), r.Checkouts, r.Modules, orDash(shortDate(r.Created)))
		}
		return nil
	default:
		return fmt.Errorf("unknown --format %q: expected table, path or json", format)
	}
}

func runRigShow(args []string) error {
	_, rigDir, err := rigSetup()
	if err != nil {
		return err
	}
	rigRoot, err := resolveRigRoot(rigDir, args)
	if err != nil {
		return err
	}
	m, err := rig.Load(rigRoot)
	if err != nil {
		return err
	}

	if rigShowEnv {
		// Printed rather than sourced from env.sh on disk so the exports stay
		// correct even if the rig has been moved.
		for _, f := range rig.GeneratedFiles(m, rigRoot) {
			if f.Name == rig.EnvShName {
				fmt.Print(f.Content)
				return nil
			}
		}
		return fmt.Errorf("rig: no %s could be rendered for %s", rig.EnvShName, m.Name)
	}

	fmt.Printf("rig %s\n", m.Name)
	fmt.Printf("  path      %s\n", rigRoot)
	fmt.Printf("  GOWORK    %s\n", filepath.Join(rigRoot, rig.GoWorkName))
	if m.Source.Ref != "" {
		fmt.Printf("  source    %s\n", m.Source.Ref)
	}
	if m.Source.Binary != "" {
		fmt.Printf("  binary    %s\n", m.Source.Binary)
	}
	if m.Created != "" {
		fmt.Printf("  created   %s\n", m.Created)
	}
	if len(m.Frozen) > 0 {
		fmt.Printf("  frozen    %d module(s) pinned back to the baseline in go.work:\n", len(m.Frozen))
		for _, p := range m.Frozen {
			fmt.Printf("              %s\n", p)
		}
	}

	byDir := map[string][]rig.Member{}
	for _, mem := range m.Members {
		byDir[mem.Checkout] = append(byDir[mem.Checkout], mem)
	}
	fmt.Printf("\n%-52s %-28s %s\n", "MODULE", "PIN", "PATH")
	fmt.Println(strings.Repeat("-", 120))
	for _, c := range m.Checkouts {
		mems := byDir[c.Dir]
		sort.Slice(mems, func(i, j int) bool { return mems[i].Path < mems[j].Path })
		for _, mem := range mems {
			fmt.Printf("%-52s %-28s %s\n", mem.Path, rigPin(c), mem.UseDir())
		}
	}

	fmt.Printf("\n%-40s %-28s %s\n", "CHECKOUT", "COMMIT", "CONTENTS")
	fmt.Println(strings.Repeat("-", 120))
	for _, c := range m.Checkouts {
		fmt.Printf("%-40s %-28s %s\n", c.Dir, shortCommitID(c.Commit), rigContents(c))
	}
	// Listed with the rest rather than hidden: the directory is still on disk
	// and the workspace is still registered, so a reader looking for either
	// needs to find it here.
	if obsolete := len(m.Checkouts) - len(m.LiveCheckouts()); obsolete > 0 {
		fmt.Printf("\n%d checkout(s) are obsolete: nothing in %s uses them, but they are still on disk.\n"+
			"remove them with: wgo rig sync %s --prune\n", obsolete, rig.GoWorkName, m.Name)
	}

	if len(m.Skipped) > 0 {
		fmt.Printf("\nSkipped (served from the module cache):\n")
		for _, s := range m.Skipped {
			fmt.Printf("  %-52s %s\n", s.Path, s.String())
		}
	}
	return nil
}

func runRigRm(name string) error {
	_, rigDir, err := rigSetup()
	if err != nil {
		return err
	}
	// A rig name is a directory name, never a path. Without this the join below
	// happily resolves "../notes" to a sibling of rig.dir, and this function
	// ends in a workspace-forgetting RemoveAll.
	if err := rig.ValidateName(name); err != nil {
		return err
	}
	rigRoot := filepath.Join(rigDir, name)
	m, err := rig.Load(rigRoot)
	if errors.Is(err, rig.ErrNoManifest) {
		return removeOrphanRig(jj.NewCLI(), rigDir, rigRoot, name)
	}
	if err != nil {
		return err
	}

	jjc := jj.NewCLI()
	if !rigRmForce {
		if err := checkRigCheckoutsClean(jjc, m, rigRoot); err != nil {
			return err
		}
	}
	if err := rig.Remove(jjc, m, rigRoot, rigLogf); err != nil {
		return err
	}
	rigLogf("removed rig %s (%d workspaces forgotten)", name, len(m.Checkouts))
	return nil
}

// removeOrphanRig cleans up a rig directory that has no manifest.
//
// This is the wreckage of a `rig new` that died between its first checkout and
// the manifest write — a kill, a crash, a full disk. The directory blocks
// `rig new` and, without this, `rig rm` declined it too on the grounds that
// Load found no rig there, which left the workspaces registered in the user's
// main clones and no supported way to clear them.
//
// An empty directory is removed without ceremony. Anything else is reported
// before it is touched: this path has no manifest to tell clean from dirty, so
// the user is shown what was found and asked for --force rather than having it
// deleted on their behalf.
func removeOrphanRig(js rig.Orphans, rigDir, rigRoot, name string) error {
	// Defence in depth behind runRigRm's ValidateName. Everything below ends in
	// os.RemoveAll, and this is the one rm path that does not first read a
	// manifest confirming the directory is a rig — so the containment check is
	// the only thing standing between a bad name and a tree the user cares
	// about. Cheap enough to repeat; too expensive to omit.
	if !rig.UnderDir(rigDir, rigRoot) {
		return fmt.Errorf("refusing to remove %s: it is not inside the rig directory %s", rigRoot, rigDir)
	}
	if _, err := os.Stat(rigRoot); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no rig named %q in %s", name, rigDir)
	} else if err != nil {
		return fmt.Errorf("rig: checking %s: %w", rigRoot, err)
	}

	orphans, err := rig.FindOrphans(js, rigRoot)
	if err != nil {
		return err
	}

	registered := make([]rig.Orphan, 0, len(orphans))
	for _, o := range orphans {
		if o.Workspace != "" {
			registered = append(registered, o)
		}
	}

	if len(orphans) == 0 {
		// Nothing under src/: an empty root, or a directory that only ever got
		// as far as being created. Removing it is what unblocks `rig new`.
		if err := os.RemoveAll(rigRoot); err != nil {
			return fmt.Errorf("rig: removing %s: %w", rigRoot, err)
		}
		rigLogf("removed %s (no manifest, nothing checked out)", rigRoot)
		return nil
	}

	if !rigRmForce {
		var b strings.Builder
		fmt.Fprintf(&b, "%s has no manifest — it is the remains of an interrupted or failed `wgo rig new`.\n", rigRoot)
		fmt.Fprintf(&b, "found %d checkout(s), %d still registered as jj workspaces:\n", len(orphans), len(registered))
		for _, o := range orphans {
			switch {
			case o.Workspace != "":
				fmt.Fprintf(&b, "  %s → workspace %s in %s\n", filepath.Base(o.Dir), o.Workspace, o.MainClone)
			case o.Err != nil:
				fmt.Fprintf(&b, "  %s → not a readable jj workspace (%v)\n", filepath.Base(o.Dir), o.Err)
			default:
				fmt.Fprintf(&b, "  %s → no workspace registered\n", filepath.Base(o.Dir))
			}
		}
		b.WriteString("these carry no bookmark, so anything edited in them is lost with the directory.\n")
		fmt.Fprintf(&b, "forget the workspaces and delete the tree with: wgo rig rm %s --force", name)
		return errors.New(b.String())
	}

	if err := rig.RemoveOrphans(js, orphans, rigRoot, rigLogf); err != nil {
		return err
	}
	rigLogf("removed %s (%d workspace(s) forgotten)", rigRoot, len(registered))
	return nil
}

// checkRigCheckoutsClean refuses removal while a checkout holds edits.
//
// Rig checkouts carry no bookmark, so an edit here exists only in this working
// copy: forgetting the workspace and deleting the tree is the one way to lose
// it permanently. Every checkout is inspected before anything is reported, so
// the user sees the whole list rather than fixing them one at a time.
func checkRigCheckoutsClean(jjc jj.Client, m *rig.Manifest, rigRoot string) error {
	var dirty []string
	for _, c := range m.Checkouts {
		dest := filepath.Join(rigRoot, rig.SrcDir, c.Dir)
		if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
			continue
		}
		clean, changed, err := jjc.IsClean(dest)
		if err != nil {
			// Unreadable is not the same as clean, and guessing wrong here
			// discards work.
			return fmt.Errorf("rig: checking %s for changes: %w\nuse --force to remove anyway", c.Dir, err)
		}
		if !clean {
			dirty = append(dirty, fmt.Sprintf("  %s (%d file(s) changed)", c.Dir, len(changed)))
		}
	}
	if len(dirty) == 0 {
		return nil
	}
	return fmt.Errorf("rig %s has uncommitted changes in %d checkout(s):\n%s\n"+
		"these carry no bookmark, so removing the rig discards them.\n"+
		"reproduce the change in a normal worktree, or re-run with --force",
		m.Name, len(dirty), strings.Join(dirty, "\n"))
}

// resolveRigRoot picks the rig to act on: the named one, or the one containing
// the current directory.
func resolveRigRoot(rigDir string, args []string) (string, error) {
	if len(args) == 1 {
		if err := rig.ValidateName(args[0]); err != nil {
			return "", err
		}
		root := filepath.Join(rigDir, args[0])
		if _, err := os.Stat(rig.ManifestPath(root)); err != nil {
			return "", fmt.Errorf("no rig named %q in %s\nlist them with: wgo rig ls", args[0], rigDir)
		}
		return root, nil
	}
	cwd, err := resolveCwd()
	if err != nil {
		return "", err
	}
	if root := rigRootContaining(rigDir, cwd); root != "" {
		return root, nil
	}
	return "", fmt.Errorf("not inside a rig, and no name given\nusage: wgo rig show <name>   (list them with: wgo rig ls)")
}

// rigRootContaining walks up from dir looking for a rig root, bounded by
// rigDir so the search cannot wander into unrelated parents.
func rigRootContaining(rigDir, dir string) string {
	if !rig.UnderDir(rigDir, dir) {
		return ""
	}
	for {
		if _, err := os.Stat(rig.ManifestPath(dir)); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir || !rig.UnderDir(rigDir, parent) {
			return ""
		}
		dir = parent
	}
}

// rigSourceLabel names where a rig's pins came from, in one column's worth.
//
// A binary-sourced rig has no Ref, and showing an empty source next to a rig
// that plainly came from somewhere reads as missing data. The base name is
// enough to recognise the artifact; `wgo rig show` prints the full path.
func rigSourceLabel(s rig.Source) string {
	if s.Ref != "" {
		return s.Ref
	}
	if s.Binary != "" {
		return filepath.Base(s.Binary)
	}
	return ""
}

// rigPin describes what a checkout is pinned to, preferring the tag.
func rigPin(c rig.Checkout) string {
	if c.Tag != "" {
		return c.Tag
	}
	return shortCommitID(c.Commit)
}

// rigContents summarises how much of a repository a checkout materialises.
func rigContents(c rig.Checkout) string {
	label := "full"
	if !c.Full && len(c.Sparse) > 0 {
		label = "sparse: " + strings.Join(c.Sparse, " ")
	}
	if c.Obsolete {
		return "obsolete, " + label
	}
	return label
}

func shortCommitID(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// shortDate trims an RFC3339 timestamp to its date.
func shortDate(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
