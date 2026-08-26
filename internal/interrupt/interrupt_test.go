package interrupt

import (
	"bytes"
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitFor polls until cond holds, so the tests do not race the watch goroutine.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(msg)
}

func TestFirstSignalCancelsWithoutExiting(t *testing.T) {
	var out bytes.Buffer
	exited := make(chan int, 1)
	g := listen(&out, func(code int) { exited <- code })
	defer g.Stop()

	require.NoError(t, g.Context().Err(), "context is live before any signal")
	require.False(t, g.Interrupted())

	g.sig <- syscall.SIGINT

	waitFor(t, g.Interrupted, "first signal should cancel the context")
	assert.ErrorIs(t, g.Context().Err(), context.Canceled)
	assert.Contains(t, out.String(), "rolling back")
	assert.Empty(t, exited, "the first signal must not exit; rollback still has to run")
}

// The second Ctrl-C is the reason this package exists rather than a bare
// signal.NotifyContext, which swallows every signal after the first and leaves
// a hung rollback unkillable.
func TestSecondSignalExits(t *testing.T) {
	var out bytes.Buffer
	exited := make(chan int, 1)
	g := listen(&out, func(code int) { exited <- code })
	defer g.Stop()

	g.sig <- syscall.SIGINT
	waitFor(t, g.Interrupted, "first signal should cancel")

	g.sig <- syscall.SIGINT

	select {
	case code := <-exited:
		assert.Equal(t, 130, code, "SIGINT exits 128+2")
	case <-time.After(2 * time.Second):
		t.Fatal("second signal should force an exit")
	}
	assert.Contains(t, out.String(), "cleanup was interrupted")
}

func TestSIGTERMAndSIGHUPAlsoCancel(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			g := listen(&bytes.Buffer{}, func(int) {})
			defer g.Stop()
			g.sig <- sig
			waitFor(t, g.Interrupted, "should cancel on "+sig.String())
		})
	}
}

func TestStopIsIdempotentAndReleasesTheContext(t *testing.T) {
	g := listen(&bytes.Buffer{}, func(int) {})
	g.Stop()
	assert.NotPanics(t, g.Stop, "Stop runs from a defer and may also be called explicitly")
	assert.ErrorIs(t, g.Context().Err(), context.Canceled, "Stop releases the context")
}

func TestWrapOnlyMarksErrorsCausedByASignal(t *testing.T) {
	g := listen(&bytes.Buffer{}, func(int) {})
	defer g.Stop()

	boom := errors.New("disk full")
	assert.Equal(t, boom, g.Wrap(boom), "an ordinary failure is passed through untouched")
	assert.NoError(t, g.Wrap(nil))

	g.sig <- syscall.SIGINT
	waitFor(t, g.Interrupted, "should cancel")

	wrapped := g.Wrap(boom)
	assert.ErrorIs(t, wrapped, ErrInterrupted, "callers can test for the interrupt")
	assert.ErrorIs(t, wrapped, boom, "and the original cause survives in the chain")
}

func TestExitCode(t *testing.T) {
	assert.Equal(t, 1, ExitCode(errors.New("boom")))
	assert.Equal(t, 130, ExitCode(ErrInterrupted))
	// Cancellation surfaces raw from jj when the signal lands mid-subprocess,
	// before any Wrap has had a chance to label it.
	assert.Equal(t, 130, ExitCode(context.Canceled))
}
