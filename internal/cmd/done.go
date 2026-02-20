package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

var doneNote string

// doneCmd represents the `wgo done` command.
var doneCmd = &cobra.Command{
	Use:   "done [pattern]",
	Short: "Mark a task as done",
	Long:  `Mark the first matching task as done and move it to today's daily log.`,
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return completeTask(joinArgs(args), doneNote, true)
	},
}

// cancelCmd represents the `wgo cancel` command.
var cancelCmd = &cobra.Command{
	Use:   "cancel [pattern]",
	Short: "Mark a task as cancelled",
	Long:  `Mark the first matching task as cancelled and move it to today's daily log.`,
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return completeTask(joinArgs(args), doneNote, false)
	},
}

func init() {
	rootCmd.AddCommand(doneCmd)
	rootCmd.AddCommand(cancelCmd)
	doneCmd.Flags().StringVar(&doneNote, "note", "", "Add a note to the log entry")
	cancelCmd.Flags().StringVar(&doneNote, "note", "", "Add a note to the log entry")
}

func completeTask(pattern, note string, done bool) error {
	s, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}

	if err := s.EnsureDir(); err != nil {
		return err
	}

	content, err := s.LoadPlan()
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	p, err := plan.Parse(content)
	if err != nil {
		return fmt.Errorf("failed to parse plan: %w", err)
	}

	task := p.RemoveTask(pattern)
	if task == nil {
		return fmt.Errorf("no task matching %q", pattern)
	}

	if err := s.SavePlan(p.Render()); err != nil {
		return fmt.Errorf("failed to save plan: %w", err)
	}

	// Load today's log and append entry
	today := time.Now().Format("2006-01-02")
	dl, err := s.LoadDailyLog(today)
	if err != nil {
		return fmt.Errorf("failed to load daily log: %w", err)
	}

	if done {
		dl.AddCompleted(task.Text, note)
		fmt.Fprintf(os.Stderr, "✓ Done: %s\n", task.Text)
	} else {
		dl.AddCancelled(task.Text, note)
		fmt.Fprintf(os.Stderr, "✗ Cancelled: %s\n", task.Text)
	}

	if err := s.SaveDailyLog(dl); err != nil {
		return fmt.Errorf("failed to save daily log: %w", err)
	}

	return nil
}
