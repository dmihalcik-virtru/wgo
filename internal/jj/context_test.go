package jj_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/jjtest"
)

// A client with no context behaves exactly as before: every read-only caller
// in wgo relies on that, and cancellation must stay opt-in.
func TestWithoutContextIsUncancellable(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	if _, err := c.Root(repo); err != nil {
		t.Fatalf("Root on a context-free client: %v", err)
	}
}

func TestWithContextCancelsBeforeRunning(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.WithContext(ctx).Root(repo)
	if err == nil {
		t.Fatal("a cancelled context must fail the command")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled in the chain, got %v", err)
	}
}

// The raw failure of a signalled child is "signal: interrupt", which names an
// implementation detail rather than the cancellation the user caused.
func TestCancellationErrorNamesTheCancellationNotTheSignal(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.WithContext(ctx).Log(repo, "root()")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want a context.Canceled chain, got %q", err)
	}
}

// WithContext must not mutate the receiver: rollback holds an uncancellable
// clone of the same base client and has to survive the cancellation.
func TestWithContextDoesNotMutateTheReceiver(t *testing.T) {
	repo, base := jjtest.NewRepo(t)
	ctx, cancel := context.WithCancel(context.Background())

	cancelled := base.WithContext(ctx)
	survivor := base.WithContext(context.WithoutCancel(ctx))
	cancel()

	if _, err := cancelled.Root(repo); err == nil {
		t.Fatal("the cancelled clone should fail")
	}
	if _, err := survivor.Root(repo); err != nil {
		t.Fatalf("the WithoutCancel clone must still run: %v", err)
	}
	if _, err := base.Root(repo); err != nil {
		t.Fatalf("the base client must be untouched: %v", err)
	}
}

// exec.CommandContext kills with SIGKILL by default, which would deny jj the
// chance to release its locks. The client must interrupt instead.
func TestCancelSendsInterruptRatherThanKill(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-jj")
	// Reports which signal it received, then exits. If cmd.Cancel were left at
	// the default the process would die on SIGKILL and write nothing.
	//
	// sleep runs in the background with an explicit wait: a POSIX shell defers
	// traps until the current foreground command finishes, so a foreground
	// sleep would swallow the interrupt this test is trying to observe.
	// The sleep also gives up the inherited stdout/stderr: a background child
	// holding the write end of the command's pipe keeps Wait blocked until
	// WaitDelay expires, which would make this test measure the wrong thing.
	//
	// The ready file is the handshake. Cancelling on a timer instead would race
	// the shell to its `trap` line on a loaded machine, and a SIGINT that lands
	// before the trap is installed kills the shell outright — the very outcome
	// this test reports as a failure.
	ready := filepath.Join(dir, "ready")
	got := filepath.Join(dir, "got")
	body := "#!/bin/sh\n" +
		"trap 'echo INT >\"" + got + "\"; exit 0' INT\n" +
		"sleep 10 >/dev/null 2>&1 &\n" +
		": >\"" + ready + "\"\n" +
		"wait\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	c := (&jj.CLIClient{Binary: script})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.WithContext(ctx).Root(dir)
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the fake jj never reached its trap")
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not return")
	}

	if _, err := os.Stat(got); err != nil {
		t.Fatal("the child was killed outright; it must be interrupted so jj can unlock the repo")
	}
}
