package urlhandler

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Prompt is what the first-use confirmation shows.
type Prompt struct {
	WorkspaceID string
	// Path is the full canonical workspace path that would be opened.
	Path string
}

// Confirmer asks the user whether a wgo:// link may open a workspace. It
// returns false, nil when the user declines, and an error when it could
// not ask at all; both mean nothing is opened.
type Confirmer interface {
	Confirm(ctx context.Context, p Prompt) (bool, error)
}

// dialogTimeout is how many seconds the dialog waits before giving up,
// which counts as Cancel.
const dialogTimeout = 120

// dialogScript is the fixed AppleScript for the macOS confirmation. The
// path arrives as argument 1 and is never part of the script source.
// Cancel is the default button, Return and Escape both decline, and the
// dialog gives up (declines) after dialogTimeout seconds.
var dialogScript = `on run argv
	set wsPath to item 1 of argv
	activate
	set r to display dialog "A wgo:// link wants to open a terminal tab in this workspace:" & return & return & wsPath & return & return & "Only open it if you just clicked a wgo link. wgo remembers your answer for this workspace at this path." with title "wgo URL handler" buttons {"Cancel", "Open"} default button "Cancel" cancel button "Cancel" with icon caution giving up after ` + strconv.Itoa(dialogTimeout) + `
	if gave up of r then return "Cancel"
	return button returned of r
end run`

// DialogArgv is the osascript argv that shows the confirmation for path.
func DialogArgv(path string) []string {
	return []string{"osascript", "-e", dialogScript, path}
}

// SystemConfirmer asks with a macOS dialog, or, when that is unavailable
// (another OS, no GUI session) and stdin is a terminal, with a y/N prompt.
// It never accepts on its own.
type SystemConfirmer struct {
	GOOS   string
	Run    Runner
	Stdin  io.Reader
	TTY    bool      // Stdin is an interactive terminal
	Prompt io.Writer // where the y/N question is written
}

// Confirm implements Confirmer.
func (c SystemConfirmer) Confirm(ctx context.Context, p Prompt) (bool, error) {
	var why error
	if c.GOOS == "darwin" && c.Run != nil {
		out, err := c.Run.Output(ctx, DialogArgv(p.Path))
		if err == nil {
			return strings.TrimSpace(out) == "Open", nil
		}
		// osascript reports Cancel (and Escape) as error -128.
		if strings.Contains(err.Error(), "-128") {
			return false, nil
		}
		why = fmt.Errorf("the confirmation dialog failed: %w", err)
	} else {
		why = fmt.Errorf("there is no confirmation dialog on %s", c.GOOS)
	}
	if !c.TTY || c.Stdin == nil || c.Prompt == nil {
		return false, fmt.Errorf("%v, and stdin is not a terminal to ask on; nothing was opened. Run the same wgo open command in a terminal to confirm", why)
	}
	fmt.Fprintf(c.Prompt, "A wgo:// link wants to open a terminal tab in this workspace:\n\n  %s\n\nOnly open it if you just clicked a wgo link. Open it? [y/N] ", p.Path)
	line, err := bufio.NewReader(c.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}
