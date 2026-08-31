// Package interrupt turns Ctrl-C into a cancelled context instead of an
// immediate death, so a command that is halfway through mutating a repository
// can roll back before it exits.
//
// Without this, a `wgo rig new` interrupted between its first checkout and the
// manifest write leaves a rig directory that `rig new` refuses to overwrite
// and a jj workspace registered in the user's main clone. The deferred
// rollback that would have cleaned both up never runs, because the default
// disposition for SIGINT terminates the process before the deferred call.
package interrupt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// ErrInterrupted is what a cancelled operation reports. Commands wrap their
// own error in it so Execute can exit 130 rather than printing a diagnostic
// for something the user asked for.
var ErrInterrupted = errors.New("interrupted")

// exitInterrupted is the conventional shell exit code for SIGINT (128+2).
const exitInterrupted = 130

// Guard converts the first termination signal into context cancellation and
// the second into an immediate exit.
//
// The second signal matters more than it looks. signal.NotifyContext alone
// stays registered after the first signal and drops every one after it, so a
// rollback that hangs — a jj waiting on a lock another process holds — becomes
// unkillable by Ctrl-C. Restoring the ability to give up is the reason this
// type exists instead of a bare NotifyContext call.
type Guard struct {
	ctx    context.Context
	cancel context.CancelFunc
	stop   func()
	sig    chan os.Signal
	done   chan struct{}

	// out receives the two notices. Stderr in production: the rig commands
	// print their result path to stdout and callers do `cd $(wgo rig new)`.
	out io.Writer
	// exit ends the process on the second signal. A field so tests can
	// observe the call instead of dying.
	exit func(int)
}

// Listen installs a handler for SIGINT, SIGTERM and SIGHUP and returns a Guard
// whose context is cancelled when one arrives.
//
// SIGHUP is included because a rig build outlives the patience that closes a
// terminal, and a hangup strands exactly as much as a Ctrl-C does.
//
// The caller must call Stop, which unregisters the handler and restores the
// default disposition.
func Listen() *Guard {
	return listen(os.Stderr, os.Exit)
}

func listen(out io.Writer, exit func(int)) *Guard {
	ctx, cancel := context.WithCancel(context.Background())
	g := &Guard{
		ctx:    ctx,
		cancel: cancel,
		sig:    make(chan os.Signal, 2),
		done:   make(chan struct{}),
		out:    out,
		exit:   exit,
	}
	signal.Notify(g.sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	g.stop = func() { signal.Stop(g.sig) }
	go g.watch()
	return g
}

// watch cancels on the first signal and exits on the second.
func (g *Guard) watch() {
	for {
		select {
		case <-g.done:
			return
		case <-g.sig:
			if g.ctx.Err() == nil {
				fmt.Fprintln(g.out, "\ninterrupted — rolling back (press Ctrl-C again to abort and leave it in place)")
				g.cancel()
				continue
			}
			fmt.Fprintln(g.out, "\naborting: cleanup was interrupted and may be incomplete")
			g.exit(exitInterrupted)
			return
		}
	}
}

// Stop unregisters the handler. Safe to call more than once.
func (g *Guard) Stop() {
	select {
	case <-g.done:
		return
	default:
	}
	close(g.done)
	g.stop()
	g.cancel()
}

// Context is cancelled when a signal arrives. Pass it to
// jj.CLIClient.WithContext and to anything that loops.
func (g *Guard) Context() context.Context { return g.ctx }

// Interrupted reports whether a signal has arrived.
func (g *Guard) Interrupted() bool { return g.ctx.Err() != nil }

// Wrap replaces err with ErrInterrupted when a signal is what caused it.
//
// Cancellation surfaces from whatever call happened to be in flight, so the
// raw error names an implementation detail the user did not ask about — "jj
// workspace add: context canceled" for a Ctrl-C. Both the original error and
// ErrInterrupted stay in the chain so callers can test for either.
func (g *Guard) Wrap(err error) error {
	if err == nil || !g.Interrupted() {
		return err
	}
	return fmt.Errorf("%w: %w", ErrInterrupted, err)
}

// IsInterrupted reports whether err came from a signal.
func IsInterrupted(err error) bool {
	return errors.Is(err, ErrInterrupted) || errors.Is(err, context.Canceled)
}

// ExitCode is the process exit status for err.
func ExitCode(err error) int {
	if IsInterrupted(err) {
		return exitInterrupted
	}
	return 1
}
