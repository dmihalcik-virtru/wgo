package urlhandler

import (
	"os"
	"testing"
)

// TestIsTerminalDevNull: /dev/null is a character device but not a
// terminal, and it is what the applet gives wgo open as stdin.
func TestIsTerminalDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skip(err)
	}
	defer f.Close()
	if IsTerminal(f) {
		t.Fatalf("IsTerminal(%s) = true", os.DevNull)
	}
}
