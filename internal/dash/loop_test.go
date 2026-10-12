package dash

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoopTicksAtOnceRepeatsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var n atomic.Int32
	done := make(chan struct{})
	go func() {
		Loop(ctx, 10*time.Millisecond, func(context.Context) {
			if n.Add(1) == 3 {
				cancel()
			}
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Loop did not stop after cancel")
	}
	if got := n.Load(); got != 3 {
		t.Fatalf("ticks = %d, want 3", got)
	}
	// A cancelled context never ticks.
	Loop(ctx, time.Millisecond, func(context.Context) { t.Error("ticked after cancel") })
}
