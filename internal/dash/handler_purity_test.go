package dash

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/virtru/wgo/internal/discovery"
)

type failingTransport struct{ calls atomic.Int32 }

func (f *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls.Add(1)
	return nil, errors.New("network access from a read path")
}

// TestHandlerReadPathIsPure: GET / and GET /api/snapshot serve the in-memory
// view and never run discovery, spawn a process or touch the network, both
// before and after a snapshot is published. No t.Parallel: it swaps global
// state.
func TestHandlerReadPathIsPure(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var discovered atomic.Int32
	c := NewCollector(Config{Discover: func() ([]discovery.DiscoveredRepo, error) {
		discovered.Add(1)
		t.Errorf("Discover called from the HTTP read path")
		return nil, nil
	}})
	d, err := Open(Options{Collector: c})
	if err != nil {
		t.Fatal(err)
	}
	ft := &failingTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = ft
	t.Cleanup(func() { http.DefaultTransport = old })

	srv := newServer(t, d)
	// The test client's own transport talks to the loopback server and is
	// not http.DefaultTransport, so ft only sees traffic the handler makes.
	hit := func(wantStatus string) {
		for i := 0; i < 5; i++ {
			for _, p := range []string{"/", "/api/snapshot"} {
				resp, err := srv.Client().Get(srv.URL + p)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Fatalf("%s: %d", p, resp.StatusCode)
				}
				if p == "/api/snapshot" && !strings.Contains(string(body), `"status":"`+wantStatus+`"`) {
					t.Fatalf("%s: %s", p, body[:min(len(body), 80)])
				}
			}
		}
	}
	hit("loading")
	published := largeDash(t).Current().Snapshot
	cp := *published
	if err := d.Publish(&cp); err != nil {
		t.Fatal(err)
	}
	hit("ready")
	if n := discovered.Load(); n != 0 {
		t.Fatalf("discovery ran %d times", n)
	}
	if n := ft.calls.Load(); n != 0 {
		t.Fatalf("%d network calls from the read path", n)
	}
}
