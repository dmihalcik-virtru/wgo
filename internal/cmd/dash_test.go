package cmd

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/dash"
)

func TestDashRejectsBadFlags(t *testing.T) {
	oldJSON, oldDays, oldPort := dashJSON, dashDays, dashPort
	t.Cleanup(func() { dashJSON, dashDays, dashPort = oldJSON, oldDays, oldPort })
	dashJSON, dashDays, dashPort = false, dash.DefaultDays, 0
	if err := runDash(context.Background(), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--port") {
		t.Fatalf("want a --port error, got %v", err)
	}
	dashPort, dashDays = DefaultDashPort, 0
	if err := runDash(context.Background(), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--days") {
		t.Fatalf("want a --days error, got %v", err)
	}
}

func TestListenDashBindsLoopbackOnly(t *testing.T) {
	ln, err := listenDash(0)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	host, _, _ := net.SplitHostPort(ln.Addr().String())
	if host != "127.0.0.1" {
		t.Fatalf("bound %s, want 127.0.0.1 only", ln.Addr())
	}
}

func TestListenDashBusyPortSuggestsPort(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	ln, err := listenDash(port)
	if err == nil {
		ln.Close()
		t.Fatal("listenDash succeeded on a busy port")
	}
	msg := err.Error()
	if !strings.Contains(msg, "already in use") || !strings.Contains(msg, "wgo dash --port") || !strings.Contains(msg, strconv.Itoa(port)) {
		t.Fatalf("busy-port error should name the port and suggest --port, got %q", msg)
	}
}

type nilSource struct{}

func (nilSource) Current() *dash.View { return nil }

// syncBuffer is a goroutine-safe writer for the server's output.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestServeDashServesRefreshesOpensAndStops(t *testing.T) {
	var opened []string
	old := openInBrowser
	openInBrowser = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openInBrowser = old })

	ln, err := listenDash(0)
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + ln.Addr().String() + "/"
	var refreshes atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- serveDash(ctx, out, ln, dashServer{
			source:   nilSource{},
			refresh:  func(context.Context) error { refreshes.Add(1); return nil },
			interval: 10 * time.Millisecond,
			open:     true,
		})
	}()

	// The page answers before any snapshot exists.
	var resp *http.Response
	for i := 0; i < 100; i++ {
		if resp, err = http.Get(url); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "wgo dash") {
		t.Fatalf("GET / = %d", resp.StatusCode)
	}
	resp, err = http.Get(url + "api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"loading"`) {
		t.Fatalf("snapshot before any refresh should be loading, got %s", body)
	}
	// Read-only: no POST anywhere.
	resp, err = http.Post(url+"api/snapshot", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", resp.StatusCode)
	}

	deadline := time.Now().Add(5 * time.Second)
	for refreshes.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if refreshes.Load() < 2 {
		t.Fatalf("refresh loop ran %d times, want repeated refreshes", refreshes.Load())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveDash: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveDash did not stop after cancel")
	}
	if len(opened) != 1 || opened[0] != url {
		t.Fatalf("opened %v, want [%s]", opened, url)
	}
	if !strings.Contains(out.String(), url) || !strings.Contains(out.String(), "stopped") {
		t.Fatalf("output = %q", out.String())
	}
	if _, err := http.Get(url); err == nil {
		t.Fatal("server still answering after shutdown")
	}
}

func TestServeDashNoOpen(t *testing.T) {
	old := openInBrowser
	openInBrowser = func(string) error { t.Error("opened a browser with open=false"); return nil }
	t.Cleanup(func() { openInBrowser = old })
	ln, err := listenDash(0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := serveDash(ctx, io.Discard, ln, dashServer{
		source:   nilSource{},
		refresh:  func(context.Context) error { return nil },
		interval: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestServeDashReportsReviewScanErrors(t *testing.T) {
	runs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runs, "corrupt"), 0o755); err != nil { // a run with no ledger
		t.Fatal(err)
	}
	ix := dash.NewReviewIndex(runs)
	ln, err := listenDash(0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errOut := &syncBuffer{}
	var ticks atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- serveDash(ctx, io.Discard, ln, dashServer{
			source:   nilSource{},
			reviews:  ix,
			refresh:  func(context.Context) error { ticks.Add(1); return nil },
			interval: 5 * time.Millisecond,
			errOut:   errOut,
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for ticks.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := errOut.String()
	if strings.Count(got, "review run corrupt") != 1 {
		t.Fatalf("scan error should be printed once, got %q", got)
	}
	if errs := ix.Errors(); len(errs) != 1 {
		t.Fatalf("index errors = %v", errs)
	}
}

// TestDashFetchersAbsentIntegrations: with no GitHub credentials, no gh and no
// acli, --refresh has nothing to fetch with, so it skips those lookups instead
// of failing every run.
func TestDashFetchersAbsentIntegrations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	f := dashFetchers()
	if f.PR != nil || f.Issue != nil || f.Jira != nil {
		t.Fatalf("fetchers without any integration: %+v", f)
	}
	want := map[dash.JobKind]string{
		dash.JobPR:    "no GitHub token or gh",
		dash.JobIssue: "no GitHub token or gh",
		dash.JobJira:  "acli not on PATH",
	}
	for k, why := range want {
		if f.Missing[k] != why {
			t.Fatalf("Missing[%s] = %q, want %q", k, f.Missing[k], why)
		}
	}
}
