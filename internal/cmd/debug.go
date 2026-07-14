package cmd

import (
	"fmt"
	"os"
)

// debugf writes a diagnostic line to stderr when WGO_DEBUG is set, and does
// nothing otherwise. Hot-path helpers (the agent heartbeat, agent resolution)
// must not print on the normal path — that would pollute "wgo ." and statusline
// output — but they also must not swallow errors invisibly. Routing their
// swallowed errors through debugf turns a silent failure into a diagnosable one.
func debugf(format string, args ...any) {
	if os.Getenv("WGO_DEBUG") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "wgo: "+format+"\n", args...)
}
