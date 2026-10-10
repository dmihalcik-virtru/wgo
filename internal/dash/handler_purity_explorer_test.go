package dash

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/discovery"
)

// TestExplorerRoutesArePure: /lookup, /api/review-links and /review/ routes
// never run discovery, spawn a process or touch the network. No t.Parallel:
// it swaps global state.
func TestExplorerRoutesArePure(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var discovered atomic.Int32
	c := NewCollector(Config{Discover: func() ([]discovery.DiscoveredRepo, error) {
		discovered.Add(1)
		t.Errorf("Discover called from the HTTP read path")
		return nil, nil
	}})
	ft := &failingTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = ft
	t.Cleanup(func() { http.DefaultTransport = old })

	runs := t.TempDir()
	writeReviewRun(t, runs, "2026-test", time.Now())
	d, err := Open(Options{Collector: c})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Publish(liveFixture(time.Now())); err != nil {
		t.Fatal(err)
	}
	cl := newReviewServer(t, d, scannedIndex(t, runs))
	for i := 0; i < 3; i++ {
		for _, p := range []string{"/lookup?entity=effort:x", "/lookup?entity=effort:gh-70-dash", "/api/review-links", "/review/", "/review/2026-test/", "/review/2026-test/graph.json"} {
			resp, _ := fetch(t, cl, p)
			if resp.StatusCode >= 500 {
				t.Fatalf("%s: %d", p, resp.StatusCode)
			}
		}
	}
	if n := discovered.Load(); n != 0 {
		t.Fatalf("discovery ran %d times", n)
	}
	if n := ft.calls.Load(); n != 0 {
		t.Fatalf("%d network calls from explorer routes", n)
	}
}
