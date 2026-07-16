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
commands fetch the real objects into the git-lfs cache and symlink tracked
paths to them, which is safer than a raw "git lfs checkout": jj has no
staging area, so hydrating a pointer to its full content in place would get
snapshotted straight into your current change as ordinary (potentially
huge) file content.

Hydrated paths still show up as modified in "jj diff"/"jj status" (a small
pointer-to-symlink-target diff, not the full blob). Run "jj restore <path>"
to revert a path back to its pointer before committing or pushing it.`,
}

var lfsSyncCmd = &cobra.Command{
	Use:   "sync [path]",
	Short: "Hydrate LFS pointer files in a workspace via symlinks into the main checkout's object cache",
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

func init() {
	rootCmd.AddCommand(lfsCmd)
	lfsCmd.AddCommand(lfsSyncCmd)
	lfsCmd.AddCommand(lfsStatusCmd)
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

	lc := lfs.NewClient()
	result, hydrateErr := lc.HydrateWorkspace(wsRoot, mainRoot, "origin", cur.CommitID)
	if hydrateErr != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", hydrateErr)
	}

	if len(result.Hydrated) == 0 && len(result.Missing) == 0 {
		fmt.Println("no LFS pointer files found")
		return nil
	}
	for _, p := range result.Hydrated {
		fmt.Printf("hydrated %s\n", p)
	}
	for _, p := range result.Missing {
		fmt.Printf("missing  %s (object not in cache; check the remote and try again)\n", p)
	}
	if len(result.Hydrated) > 0 {
		fmt.Fprintln(os.Stderr, "\nhydrated paths will show as modified in `jj diff`/`jj status`; run `jj restore <path>` to revert before committing or pushing them.")
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

	scan, err := lfs.Scan(wsRoot)
	if err != nil {
		return err
	}
	fmt.Println(wsRoot)
	fmt.Printf("  %d hydrated (symlinked)\n", len(scan.Hydrated))
	fmt.Printf("  %d not hydrated (pointer)\n", len(scan.Pointers))
	for _, p := range scan.Pointers {
		fmt.Printf("    pointer  %s\n", p)
	}
	return nil
}
