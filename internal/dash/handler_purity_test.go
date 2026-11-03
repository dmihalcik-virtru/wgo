package dash

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/inventory"
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

// TestHandlerReadPathIsPureWithInventory: with a warm PR inventory and a
// cached identity, collecting reads only caches, and GET / and GET
// /api/snapshot serve the inventory PRs without collecting, reading the
// identity or touching the network. No t.Parallel: it swaps global state.
func TestHandlerReadPathIsPureWithInventory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	seedInventory(t, inventory.ClosedSince(time.Now(), DefaultDays), map[string][]github.PRFacts{
		"alice/open-authored":    {openPR(1, "alice", "feat-a")},
		"alice/review-requested": {openPR(2, "carol", "feat-b")},
	})
	ft := &failingTransport{}
	old := http.DefaultTransport
	http.DefaultTransport = ft
	t.Cleanup(func() { http.DefaultTransport = old })

	var identity, discovered atomic.Int32
	c := NewCollector(Config{
		People: []Person{{Login: "alice"}},
		Identity: func() (string, string) {
			identity.Add(1)
			return "alice", ""
		},
		Discover: func() ([]discovery.DiscoveredRepo, error) {
			discovered.Add(1)
			return nil, nil
		},
	})
	d, err := Open(Options{Collector: c})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Refresh(context.Background(), RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := ft.calls.Load(); n != 0 {
		t.Fatalf("%d network calls while collecting from the inventory cache", n)
	}
	collected, ids := discovered.Load(), identity.Load()

	srv := newServer(t, d)
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
			if p == "/api/snapshot" && !strings.Contains(string(body), `"id":"pr:acme/widgets#2"`) {
				t.Fatalf("%s does not serve the inventory PR", p)
			}
		}
	}
	if n := ft.calls.Load(); n != 0 {
		t.Fatalf("%d network calls from the read path", n)
	}
	if discovered.Load() != collected || identity.Load() != ids {
		t.Fatalf("GET collected again (%d discoveries, %d identity reads; want %d and %d)", discovered.Load(), identity.Load(), collected, ids)
	}
}
