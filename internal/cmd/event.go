package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/hooks"
	"github.com/virtru/wgo/internal/store"
)

var (
	eventBranch  string
	eventStaged  string
	eventMsgFile string
)

var eventCmd = &cobra.Command{
	Use:    "_event <hook-name> [args...]",
	Short:  "Process a git hook event (internal)",
	Hidden: true,
	Args:   cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		hookName := args[0]
		hookArgs := args[1:]

		// Determine repo path from current directory
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}

		// Initialize config (best-effort, use defaults if config missing)
		_ = config.Init()
		cfg := config.Get()

		var eventCfg *hooks.EventConfig
		if cfg != nil {
			eventCfg = &hooks.EventConfig{
				AutoPlan:             cfg.Hooks.AutoPlan,
				ExcludeBranches:      cfg.Hooks.ExcludeBranches,
				SpecRequired:         cfg.Hooks.SpecRequired,
				SpecRequiredMinLines: cfg.Hooks.SpecRequiredMinLines,
			}
		} else {
			eventCfg = &hooks.EventConfig{
				AutoPlan:             true,
				ExcludeBranches:      []string{"main", "master", "develop", "release/*"},
				SpecRequired:         false,
				SpecRequiredMinLines: 5,
			}
		}

		// Check if hooks are enabled
		if cfg != nil && !cfg.Hooks.Enabled {
			return nil
		}

		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		wgoDir := filepath.Join(home, ".wgo")

		s := store.NewWithDir(wgoDir)
		if err := s.EnsureDir(); err != nil {
			return err
		}

		gitClient := git.New(cwd)
		processor := hooks.NewEventProcessor(s, gitClient, eventCfg)

		switch hookName {
		case "pre-commit":
			branch := eventBranch
			if branch == "" && len(hookArgs) > 0 {
				branch = hookArgs[0]
			}
			var stagedFiles []string
			if eventStaged != "" {
				for _, f := range strings.Split(eventStaged, ",") {
					if f = strings.TrimSpace(f); f != "" {
						stagedFiles = append(stagedFiles, f)
					}
				}
			}
			ctx := hooks.PreCommitContext{
				RepoRoot:    cwd,
				Branch:      branch,
				StagedFiles: stagedFiles,
				MsgFile:     eventMsgFile,
			}
			decision, err := processor.HandlePreCommit(ctx)
			if err != nil {
				return err
			}
			if !decision.Allow {
				fmt.Fprint(os.Stderr, decision.Reason)
				os.Exit(1)
			}
			return nil
		case "post-checkout":
			if len(hookArgs) < 3 {
				return fmt.Errorf("post-checkout requires 3 args: prev-ref new-ref branch-flag")
			}
			return processor.HandlePostCheckout(cwd, hookArgs[0], hookArgs[1], hookArgs[2])
		case "post-commit":
			return processor.HandlePostCommit(cwd)
		case "post-merge":
			squash := "0"
			if len(hookArgs) > 0 {
				squash = hookArgs[0]
			}
			return processor.HandlePostMerge(cwd, squash)
		case "post-rewrite":
			command := ""
			if len(hookArgs) > 0 {
				command = hookArgs[0]
			}
			return processor.HandlePostRewrite(cwd, command)
		default:
			return fmt.Errorf("unknown hook: %s", hookName)
		}
	},
}

func init() {
	eventCmd.Flags().StringVar(&eventBranch, "branch", "", "current branch name")
	eventCmd.Flags().StringVar(&eventStaged, "staged", "", "comma-separated list of staged files")
	eventCmd.Flags().StringVar(&eventMsgFile, "msg-file", "", "path to .git/COMMIT_EDITMSG")
	rootCmd.AddCommand(eventCmd)
}
