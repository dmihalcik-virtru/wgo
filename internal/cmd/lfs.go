package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/lfs"
)

// lfsCmd is the parent for git-lfs interop helpers.
var lfsCmd = &cobra.Command{
	Use:   "lfs",
	Short: "Git LFS helpers for jj workspaces",
	Long: `jj never invokes git's clean/smudge filters, so files tracked by
git-lfs stay as raw pointer text even in a colocated checkout. These
commands fetch the real objects into the git-lfs cache and materialize
tracked paths from it.

By default "wgo lfs sync" writes the real object content (via a copy-on-write
reflink where the filesystem supports it, otherwise a plain copy) so tools
like "docker build" can read the files directly. Use --symlink to instead
link into the object cache, which keeps "jj diff" tiny but can't be followed
by a plain "docker build" (the target lives outside the build context).

Either way, hydrated paths show up as modified in "jj diff"/"jj status", and
jj will snapshot the content. Run "jj restore <path>" to revert a path back
to its pointer before committing or pushing it — otherwise a push exports the
raw blob to the LFS path, bypassing git-lfs on the remote.`,
}

var lfsSyncSymlink bool

var lfsSyncCmd = &cobra.Command{
	Use:   "sync [path]",
	Short: "Hydrate LFS pointer files in a workspace from the main checkout's object cache",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runLFSSync(lfsArg(args))
	},
}

var lfsStatusCmd = &cobra.Command{
	Use:   "status [path]",
	Short: "Show LFS pointer / hydrated file counts in a workspace",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runLFSStatus(lfsArg(args))
	},
}

var lfsRestoreCmd = &cobra.Command{
	Use:   "restore [path]",
	Short: "Revert hydrated LFS files back to their pointers (undo `wgo lfs sync`)",
	Long: `Revert every hydrated LFS path in a workspace (real content or symlink)
back to its pointer, via "jj restore" from the parent change. This undoes a
"wgo lfs sync" so those paths no longer show as modified in "jj diff"/"jj
status". Files that already contain pointer text are left untouched.

The real object stays in the git-lfs cache, so a later "wgo lfs sync"
re-hydrates instantly.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runLFSRestore(lfsArg(args))
	},
}

func init() {
	rootCmd.AddCommand(lfsCmd)
	lfsCmd.AddCommand(lfsSyncCmd)
	lfsCmd.AddCommand(lfsStatusCmd)
	lfsCmd.AddCommand(lfsRestoreCmd)
	lfsSyncCmd.Flags().BoolVar(&lfsSyncSymlink, "symlink", false,
		"symlink into the object cache instead of writing real content (keeps `jj diff` tiny, but not readable by `docker build`)")
}

func lfsArg(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return ""
}

// lfsResolvePath resolves the -C/--repo flag / cwd default when path is
// empty, otherwise returns path as an absolute path.
func lfsResolvePath(path string) (string, error) {
	if path == "" {
		return resolveCwd()
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	return abs, nil
}

func runLFSSync(path string) error {
	if !lfs.Available() {
		fmt.Fprintln(os.Stderr, "git-lfs not found on PATH; skipping (install git and git-lfs to use `wgo lfs sync`)")
		return nil
	}

	target, err := lfsResolvePath(path)
	if err != nil {
		return err
	}

	jjc := jj.NewCLI()
	if !jjc.IsRepo(target) {
		return fmt.Errorf("not a jj repository: %s", target)
	}
	wsRoot, err := jjc.Root(target)
	if err != nil {
		return fmt.Errorf("failed to get workspace root: %w", err)
	}
	mainRoot, err := jjc.MainWorkspaceRoot(target)
	if err != nil {
		return fmt.Errorf("resolve main checkout: %w", err)
	}

	if enabled, err := jjc.EnsureColocated(mainRoot); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not enable colocation for %s: %v\n", mainRoot, err)
	} else if enabled {
		fmt.Fprintf(os.Stderr, "enabling colocation for %s...\n", mainRoot)
	}

	cur, err := jjc.CurrentChange(wsRoot)
	if err != nil {
		return fmt.Errorf("resolve current change: %w", err)
	}

	mode := lfs.ModeCopy
	if lfsSyncSymlink {
		mode = lfs.ModeSymlink
	}

	lc := lfs.NewClient()
	result, hydrateErr := lc.HydrateWorkspace(wsRoot, mainRoot, "origin", cur.CommitID, mode)
	if hydrateErr != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", hydrateErr)
	}

	if len(result.Hydrated) == 0 && len(result.Missing) == 0 {
		if scan, scanErr := lfs.Scan(wsRoot, resolveMediaDir(jjc, target)); scanErr == nil && len(scan.Hydrated) > 0 {
			fmt.Printf("no LFS pointer files to hydrate (%d already synced)\n", len(scan.Hydrated))
		} else {
			fmt.Println("no LFS pointer files found")
		}
		return nil
	}
	for _, p := range result.Hydrated {
		fmt.Printf("hydrated %s\n", p)
	}
	for _, p := range result.Missing {
		fmt.Printf("missing  %s (object not in cache; check the remote and try again)\n", p)
	}
	if len(result.Hydrated) > 0 {
		if mode == lfs.ModeSymlink {
			fmt.Fprintln(os.Stderr, "\nhydrated paths are symlinks into the LFS cache and show as modified in `jj diff`/`jj status`; run `jj restore <path>` to revert before committing or pushing them.")
		} else {
			fmt.Fprintln(os.Stderr, "\nhydrated paths now hold real file content (readable by `docker build`) and show as modified in `jj diff`/`jj status`.\njj will snapshot this content, so run `jj restore <path>` before committing or pushing to avoid exporting raw blobs to the LFS path.")
		}
	}
	return nil
}

func runLFSStatus(path string) error {
	target, err := lfsResolvePath(path)
	if err != nil {
		return err
	}
	jjc := jj.NewCLI()
	if !jjc.IsRepo(target) {
		return fmt.Errorf("not a jj repository: %s", target)
	}
	wsRoot, err := jjc.Root(target)
	if err != nil {
		return fmt.Errorf("failed to get workspace root: %w", err)
	}

	scan, err := lfs.Scan(wsRoot, resolveMediaDir(jjc, target))
	if err != nil {
		return err
	}
	fmt.Println(wsRoot)
	fmt.Printf("  %d hydrated (real content or symlink)\n", len(scan.Hydrated))
	fmt.Printf("  %d not hydrated (pointer)\n", len(scan.Pointers))
	for _, p := range scan.Pointers {
		fmt.Printf("    pointer  %s\n", p)
	}
	return nil
}

// resolveMediaDir best-effort resolves the LFS object cache for target so Scan
// can recognize ModeCopy-hydrated real content, not just symlinks. Returns ""
// (symlink-only detection) when git-lfs is absent or the main checkout / cache
// can't be resolved.
func resolveMediaDir(jjc *jj.CLIClient, target string) string {
	if !lfs.Available() {
		return ""
	}
	mainRoot, err := jjc.MainWorkspaceRoot(target)
	if err != nil {
		return ""
	}
	md, err := lfs.NewClient().MediaDir(mainRoot)
	if err != nil {
		return ""
	}
	return md
}

func runLFSRestore(path string) error {
	target, err := lfsResolvePath(path)
	if err != nil {
		return err
	}
	jjc := jj.NewCLI()
	if !jjc.IsRepo(target) {
		return fmt.Errorf("not a jj repository: %s", target)
	}
	wsRoot, err := jjc.Root(target)
	if err != nil {
		return fmt.Errorf("failed to get workspace root: %w", err)
	}

	scan, err := lfs.Scan(wsRoot, resolveMediaDir(jjc, target))
	if err != nil {
		return err
	}
	if len(scan.Hydrated) == 0 {
		fmt.Println("no hydrated LFS files to restore")
		return nil
	}
	if err := jjc.Restore(wsRoot, scan.Hydrated); err != nil {
		return fmt.Errorf("restore hydrated paths: %w", err)
	}
	for _, p := range scan.Hydrated {
		fmt.Printf("restored %s\n", p)
	}
	return nil
}
