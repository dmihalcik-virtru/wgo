package urlhandler

import (
	"os"

	"golang.org/x/sys/unix"
)

// IsTerminal reports whether f is an interactive terminal. A character
// device is not enough: the applet's do shell script gives wgo /dev/null
// as stdin, which is one too.
func IsTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TIOCGETA)
	return err == nil
}
