package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/gomod"
	"github.com/virtru/wgo/internal/gotool"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/rig"
	"golang.org/x/mod/modfile"
)

var (
	rigNewFrom    string
	rigNewModules []string
	rigNewOrgs    []string
	rigNewFull    bool
	rigNewDryRun  bool

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

	rigNewCmd.Flags().StringVar(&rigNewFrom, "from", "", "Resolve pins from a tagged repo: <owner/repo>@<ref>")
	rigNewCmd.Flags().StringArrayVarP(&rigNewModules, "module", "m", nil, "Add a module explicitly: <path>@<version> (repeatable)")
	rigNewCmd.Flags().StringArrayVar(&rigNewOrgs, "org", nil, "Treat this module path prefix as in-org (repeatable, overrides config)")
	rigNewCmd.Flags().BoolVar(&rigNewFull, "full", false, "Use full checkouts instead of sparse ones")
	rigNewCmd.Flags().BoolVar(&rigNewDryRun, "dry-run", false, "Print the plan and exit without leaving a rig behind")

	rigLsCmd.Flags().StringVar(&rigLsFormat, "format", "", "Output format: table, path, json (default: table when TTY, path when piped)")
	rigShowCmd.Flags().BoolVar(&rigShowEnv, "env", false, `Print shell exports for eval "$(wgo rig show <name> --env)"`)
	rigRmCmd.Flags().BoolVar(&rigRmForce, "force", false, "Remove even if a checkout has uncommitted changes")
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
	jjc     jj.Client
	cfg     *config.Config
	fetched map[string]bool
}

func (l *cloneLocator) Locate(owner, repo string) (string, error) {
	clone, err := findOrCloneRepo(l.jjc, l.cfg, owner, repo)
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
	// and there is no way to compute it without the toolchain.
	if !gotool.Available() {
		return errors.New("`go` is not on PATH; a rig's checkout set comes from `go list`, so it cannot be built without it")
	}
	if err := rig.ValidateName(name); err != nil {
		return err
	}
	owner, repo, ref, err := parseRigFrom(rigNewFrom)
	if err != nil {
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

	jjc := jj.NewCLI()
	planner := &rig.Planner{
		Locator:  &cloneLocator{jjc: jjc, cfg: cfg},
		Resolver: jjc,
	}

	primaryCheckout, err := planner.ResolvePrimaryRepo(name, owner, repo, ref)
	if err != nil {
		return err
	}

	mz := &rig.Materializer{JJ: jjc, Logf: rigLogf}
	// Between here and Materialize the primary is on disk with no manifest
	// recording it, so nothing but this can clean it up.
	defer func() {
		if retErr != nil {
			mz.Rollback(rigRoot)
		}
	}()

	dest, err := mz.Checkout(rigRoot, primaryCheckout)
	if err != nil {
		return err
	}

	primary, primaryUse, buildList, err := inspectPrimary(dest, ref)
	if err != nil {
		return err
	}
	buildList = append(buildList, extra...)

	m, err := planner.Plan(rig.Request{
		Name: name,
		Source: rig.Source{
			Kind:        "repo",
			Ref:         owner + "/" + repo + "@" + ref,
			Modules:     rigNewModules,
			OrgPrefixes: orgs,
		},
		OrgPrefixes:     orgs,
		Sparse:          !rigNewFull,
		Primary:         primary,
		PrimaryUse:      primaryUse,
		PrimaryCheckout: primaryCheckout,
		BuildList:       buildList,
		GoVersion:       primary.GoVersion,
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
	if err := mz.Materialize(m, rigRoot); err != nil {
		return err
	}

	reportSkips(m)
	rigLogf("rig %s: %d checkouts, %d modules", m.Name, len(m.Checkouts), len(m.Members))
	rigLogf("run `. %s` before any go command", filepath.Join(rigRoot, rig.EnvShName))
	fmt.Println(rigRoot)
	return nil
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
		return fmt.Errorf("rig %q already exists at %s\nremove it with: wgo rig rm %s", name, rigRoot, name)
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

// inspectPrimary reads the artifact's identity and dependency set out of its
// freshly created checkout.
//
// The build list comes from `go list -deps` run with the repository's *own*
// go.work, not the rig's — the rig's does not exist yet, and the point is to
// reproduce the build list the artifact shipped with.
func inspectPrimary(dest, ref string) (gomod.Module, []string, []gomod.Module, error) {
	var none gomod.Module

	modPath := filepath.Join(dest, "go.mod")
	data, err := os.ReadFile(modPath)
	if err != nil {
		return none, nil, nil, fmt.Errorf("rig: reading %s: %w\nthe ref may not point at a Go module's root", modPath, err)
	}
	f, err := modfile.Parse(modPath, data, nil)
	if err != nil {
		return none, nil, nil, fmt.Errorf("rig: parsing %s: %w", modPath, err)
	}
	if f.Module == nil || f.Module.Mod.Path == "" {
		return none, nil, nil, fmt.Errorf("rig: %s declares no module path", modPath)
	}
	primary := gomod.Module{Path: f.Module.Mod.Path, Version: ref, Main: true}
	if f.Go != nil {
		primary.GoVersion = f.Go.Version
	}

	primaryUse, err := readWorkUse(dest)
	if err != nil {
		return none, nil, nil, err
	}

	gc := gotool.NewClient().In(dest)
	work, err := gotool.FindWorkFile(dest, dest)
	if err != nil {
		return none, nil, nil, err
	}
	if work != "" {
		gc = gc.WithWork(work)
	}
	buildList, err := gc.ListPackageModules("./...")
	if err != nil {
		return none, nil, nil, fmt.Errorf("rig: listing dependencies of %s: %w", primary.Path, err)
	}
	return primary, primaryUse, buildList, nil
}

// readWorkUse returns the `use` directives of the repository's own go.work as
// repo-relative directories, or nil when it ships none.
//
// A repo with a go.work builds against a different set of modules than its root
// module alone, so a rig that ignored it would not reproduce the artifact.
func readWorkUse(dest string) ([]string, error) {
	path := filepath.Join(dest, rig.GoWorkName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rig: reading %s: %w", path, err)
	}
	f, err := modfile.ParseWork(path, data, nil)
	if err != nil {
		return nil, fmt.Errorf("rig: parsing %s: %w", path, err)
	}
	var use []string
	for _, u := range f.Use {
		use = append(use, u.Path)
	}
	return use, nil
}

// baselineOf records the version of every module in the build list, so a later
// `wgo rig verify` can tell what the artifact shipped with.
func baselineOf(buildList []gomod.Module) map[string]string {
	out := map[string]string{}
	for _, mod := range buildList {
		if mod.Path == "" || mod.Version == "" {
			continue
		}
		out[mod.Path] = mod.Version
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
		if s.Kind == rig.SkipUnreachable || s.Kind == rig.SkipEscapedReplace {
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
	for _, c := range m.Checkouts {
		fmt.Printf("%-40s %-28s %s\n", c.Dir, rigPin(c), rigContents(c))
	}
	fmt.Printf("\n%d checkouts, %d modules", len(m.Checkouts), len(m.Members))
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
			Name:      m.Name,
			Path:      filepath.Join(rigDir, m.Name),
			Source:    m.Source.Ref,
			Checkouts: len(m.Checkouts),
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
	rigRoot := filepath.Join(rigDir, name)
	m, err := rig.Load(rigRoot)
	if errors.Is(err, rig.ErrNoManifest) {
		return fmt.Errorf("no rig named %q in %s", name, rigDir)
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

// rigPin describes what a checkout is pinned to, preferring the tag.
func rigPin(c rig.Checkout) string {
	if c.Tag != "" {
		return c.Tag
	}
	return shortCommitID(c.Commit)
}

// rigContents summarises how much of a repository a checkout materialises.
func rigContents(c rig.Checkout) string {
	if c.Full || len(c.Sparse) == 0 {
		return "full"
	}
	return "sparse: " + strings.Join(c.Sparse, " ")
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
