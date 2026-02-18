package cmd

import "os"

// isTerminal reports whether stdout is connected to a terminal.
// When false, the caller is likely in a pipe and should emit machine-friendly output.
func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
