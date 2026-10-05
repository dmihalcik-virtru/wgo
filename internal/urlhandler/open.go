package urlhandler

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode"

	"github.com/virtru/wgo/internal/dash"
	"github.com/virtru/wgo/internal/launch"
)

// ErrDeclined is returned when the user declines the confirmation.
var ErrDeclined = errors.New("not opened: the link was declined")

// ErrBusy is returned when another wgo open holds the lock; the link is
// dropped rather than queued, so a page cannot stack up dialogs.
var ErrBusy = errors.New("another wgo:// link is already being handled; this one was dropped")

// OpenOptions are what Open needs. Every field but Now and Warnf is
// required.
type OpenOptions struct {
	// Resolver resolves workspace IDs through current discovery.
	Resolver dash.Resolver
	// Roots are the configured discovery roots for the containment check.
	Roots []string
	// Launcher opens the terminal tab.
	Launcher launch.Launcher
	// Confirmer asks before the first open of a workspace at a path.
	Confirmer Confirmer
	// Approvals remembers confirmed workspaces.
	Approvals *Approvals
	// Lock takes the single-instance lock without waiting, returning
	// ErrBusy when another wgo open holds it; nil means no lock.
	Lock func() (release func(), err error)
	// Now stamps approvals; nil means time.Now.
	Now func() time.Time
	// Warnf reports problems that do not stop the open; nil drops them.
	Warnf func(format string, args ...any)
}

// Open handles one wgo:// URL: it accepts only the canonical
// wgo://open?ws=<id>, resolves the ID through current discovery and the
// same containment check the dashboard uses, confirms the first open of
// that workspace at that path, and opens a terminal tab there. It never
// resumes an agent and never runs anything taken from the URL.
//
// Error messages never quote the URL, not even the validated ID: the
// applet shows them in a notification credited to wgo.
//
// A launch that fell back to a copyable cd command returns the result and
// an error carrying the command.
func Open(ctx context.Context, raw string, o OpenOptions) (launch.Result, error) {
	if o.Resolver == nil || o.Launcher == nil || o.Confirmer == nil || o.Approvals == nil {
		return launch.Result{}, errors.New("wgo open is not fully configured")
	}
	warnf := o.Warnf
	if warnf == nil {
		warnf = func(string, ...any) {}
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	id, err := ParseOpenURL(raw)
	if err != nil {
		return launch.Result{}, fmt.Errorf("rejected wgo:// link: %w. Only %s is accepted; nothing was opened", err, AcceptedForm)
	}
	if o.Lock != nil {
		release, err := o.Lock()
		if err != nil {
			return launch.Result{}, err
		}
		defer release()
	}
	_, dir, err := dash.ResolveWorkspace(o.Resolver, id, o.Roots)
	var nc *dash.NotContainedError
	switch {
	case errors.Is(err, dash.ErrUnknownWorkspace), errors.Is(err, dash.ErrStaleWorkspace):
		return launch.Result{}, errors.New("unknown workspace: wgo does not discover it now (deleted, moved, or outside [discovery] base_dirs); nothing was opened. List current IDs with: wgo dash --json")
	case errors.As(err, &nc):
		return launch.Result{}, fmt.Errorf("the workspace cannot be opened: %v; nothing was opened", err)
	case err != nil:
		return launch.Result{}, fmt.Errorf("could not run discovery: %w; nothing was opened", err)
	}
	if !displaySafe(dir) {
		return launch.Result{}, errors.New("the workspace cannot be opened: its path contains control or invisible formatting characters that could disguise it in the confirmation; nothing was opened")
	}
	ok, err := o.Approvals.Approved(id, dir)
	if err != nil {
		warnf("could not read %s, so confirming again: %v", o.Approvals.Path(), err)
	}
	if !ok {
		yes, err := o.Confirmer.Confirm(ctx, Prompt{WorkspaceID: id, Path: dir})
		if err != nil {
			return launch.Result{}, err
		}
		if !yes {
			return launch.Result{}, ErrDeclined
		}
		if err := o.Approvals.Approve(id, dir, now(), warnf); err != nil {
			warnf("could not remember the approval in %s, so the next link will ask again: %v", o.Approvals.Path(), err)
		}
	}
	res := o.Launcher.Launch(ctx, launch.Action{Kind: launch.KindTerminal, Dir: dir})
	if !res.Launched {
		return res, fmt.Errorf("%s Run: %s", res.Message, res.Copy)
	}
	return res, nil
}

// displaySafe reports whether path can be shown in the confirmation as it
// is: no control characters and no Unicode format characters (category Cf,
// which includes the bidi overrides and isolates U+202A–U+202E and
// U+2066–U+2069, the marks U+200E, U+200F and U+061C, and zero-width
// characters), any of which could make one path look like another.
func displaySafe(path string) bool {
	for _, r := range path {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}
