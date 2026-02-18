package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/store"
)

// trackCmd represents the `wgo track` command.
var trackCmd = &cobra.Command{
	Use:   "track [path]",
	Short: "Register a repository to track",
	Long:  `Register a git repository in the current or specified path to start tracking it.`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "."
		if len(args) > 0 {
			path = args[0]
		}
		return trackRepository(path)
	},
}

func init() {
	rootCmd.AddCommand(trackCmd)
}

func trackRepository(path string) error {
	// Resolve to absolute path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}

	// Create git client for this path
	gitClient := git.New(absPath)

	isRepo, err := gitClient.IsRepo(absPath)
	if err != nil || !isRepo {
		return fmt.Errorf("not a git repository")
	}

	repoName, err := gitClient.RepoName(absPath)
	if err != nil {
		return fmt.Errorf("failed to get repository name: %w", err)
	}

	remoteURL, err := gitClient.RemoteURL(absPath)
	if err != nil {
		remoteURL = ""
	}

	// Load and update state
	s, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}

	if err := s.EnsureDir(); err != nil {
		return err
	}

	state, err := s.LoadState()
	if err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	state.AddRepo(absPath, remoteURL)

	if err := s.SaveState(state); err != nil {
		return fmt.Errorf("failed to save state: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Tracked: %s (%s)\n", repoName, absPath)
	return nil
}
