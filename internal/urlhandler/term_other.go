//go:build !darwin && !linux

package urlhandler

import "os"

// IsTerminal reports false where wgo cannot tell: wgo open then never asks
// y/N, and refuses instead.
func IsTerminal(*os.File) bool { return false }
