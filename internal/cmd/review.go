package cmd

import "github.com/spf13/cobra"

// reviewCmd groups the tools that work on year-in-review run directories.
var reviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Explore year-in-review results",
	Long: `Tools for the data behind a /year-in-review report.

The report is written by the year-in-review skill; these commands read the run
directory it leaves under ~/.wgo/cache/review/runs/ and never modify it.`,
}

func init() {
	rootCmd.AddCommand(reviewCmd)
}
