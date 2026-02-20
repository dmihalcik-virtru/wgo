package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/bujo"
	"github.com/virtru/wgo/internal/store"
)

// logCmd represents the `wgo log` command.
var logCmd = &cobra.Command{
	Use:   "log [date]",
	Short: "View daily log",
	Long:  `View the daily log for today, yesterday, a relative offset (-3d), or a date (YYYY-MM-DD).`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dateArg := "today"
		if len(args) > 0 {
			dateArg = args[0]
		}
		return showLog(dateArg)
	},
}

func init() {
	rootCmd.AddCommand(logCmd)
}

func showLog(dateArg string) error {
	date, err := bujo.ParseDateArg(dateArg, time.Now())
	if err != nil {
		return fmt.Errorf("invalid date: %w", err)
	}

	s, err := store.New()
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}

	dl, err := s.LoadDailyLog(bujo.FormatDate(date))
	if err != nil {
		return fmt.Errorf("failed to load daily log: %w", err)
	}

	if len(dl.Completed) == 0 && len(dl.Cancelled) == 0 && len(dl.Events) == 0 {
		fmt.Printf("No entries for %s\n", bujo.FormatDate(date))
		return nil
	}

	fmt.Print(dl.Render())
	return nil
}
