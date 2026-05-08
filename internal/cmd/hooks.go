package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/hooks"
)

var hooksCmd = &cobra.Command{
	Use:   "hooks",
	Short: "Manage passive git hooks",
	Long:  `Install, uninstall, or check status of global git hooks that automatically track your work.`,
}

var hooksInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install global git hooks",
	Long: `Set core.hooksPath to ~/.wgo/hooks/ and generate hook scripts that
call wgo to record branch checkouts, commits, merges, and rebases.

Existing hooks (both global and per-repo) are chained and will still fire.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		wgoDir := filepath.Join(home, ".wgo")

		mgr, err := hooks.NewManager(wgoDir)
		if err != nil {
			return err
		}

		if err := mgr.Install(); err != nil {
			return fmt.Errorf("install failed: %w", err)
		}

		fmt.Fprintln(os.Stderr, "Git hooks installed.")
		fmt.Fprintf(os.Stderr, "  hooks dir: %s/hooks/\n", wgoDir)
		fmt.Fprintln(os.Stderr, "  Chaining to any previous hooks.")
		fmt.Fprintln(os.Stderr, "  Run 'wgo hooks status' to verify.")
		return nil
	},
}

var hooksUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove global git hooks",
	Long:  `Restore the previous core.hooksPath setting and remove ~/.wgo/hooks/.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		wgoDir := filepath.Join(home, ".wgo")

		mgr, err := hooks.NewManager(wgoDir)
		if err != nil {
			return err
		}

		if err := mgr.Uninstall(); err != nil {
			return fmt.Errorf("uninstall failed: %w", err)
		}

		fmt.Fprintln(os.Stderr, "Git hooks uninstalled.")
		fmt.Fprintln(os.Stderr, "  Previous core.hooksPath restored.")
		return nil
	},
}

var hooksStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show hook installation status",
	RunE: func(cmd *cobra.Command, args []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		wgoDir := filepath.Join(home, ".wgo")

		mgr, err := hooks.NewManager(wgoDir)
		if err != nil {
			return err
		}

		status, err := mgr.Status()
		if err != nil {
			return err
		}

		if status.Installed {
			fmt.Println("Hooks: installed")
		} else {
			fmt.Println("Hooks: not installed")
		}

		fmt.Printf("  Directory: %s\n", status.HooksDir)

		if len(status.ActiveHooks) > 0 {
			fmt.Printf("  Active hooks: %d\n", len(status.ActiveHooks))
			for _, h := range status.ActiveHooks {
				fmt.Printf("    - %s\n", h)
			}
		} else {
			fmt.Println("  Active hooks: none")
		}

		if status.PreviousPath != "" {
			fmt.Printf("  Previous hooksPath: %s\n", status.PreviousPath)
		}

		_ = config.Init()
		if cfg := config.Get(); cfg != nil {
			fmt.Printf("  spec_required: %v\n", cfg.Hooks.SpecRequired)
		}

		return nil
	},
}

func init() {
	hooksCmd.AddCommand(hooksInstallCmd)
	hooksCmd.AddCommand(hooksUninstallCmd)
	hooksCmd.AddCommand(hooksStatusCmd)
	rootCmd.AddCommand(hooksCmd)
}
