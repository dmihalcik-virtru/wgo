package dash

import (
	"context"
	"time"
)

// DefaultRefreshInterval is how often the served dashboard re-collects
// local state and refreshes remote caches.
const DefaultRefreshInterval = 30 * time.Second

// Loop calls tick at once and then every interval until ctx is done. Ticks
// never overlap: one that overruns the interval delays the next instead of
// queueing more. It returns when ctx is done.
func Loop(ctx context.Context, interval time.Duration, tick func(context.Context)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
