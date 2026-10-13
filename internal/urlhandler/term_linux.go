package urlhandler

import (
	"os"

	"golang.org/x/sys/unix"
)

// IsTerminal reports whether f is an interactive terminal (see
// term_darwin.go for why a character device is not enough).
func IsTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}
