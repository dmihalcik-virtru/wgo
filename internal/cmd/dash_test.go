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

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/dash"
	"github.com/virtru/wgo/internal/launch"
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

func TestApplyDashConfigFlagsWin(t *testing.T) {
	oldDays, oldPort, oldFlags := dashDays, dashPort, dashFlags
	t.Cleanup(func() { dashDays, dashPort, dashFlags = oldDays, oldPort, oldFlags })
	fs := (&cobra.Command{}).Flags()
	fs.IntVar(&dashPort, "port", DefaultDashPort, "")
	fs.IntVar(&dashDays, "days", dash.DefaultDays, "")
	dashFlags = fs

	if err := applyDashConfig(config.DashConfig{Port: 9000, Days: 30}); err != nil {
		t.Fatal(err)
	}
	if dashPort != 9000 || dashDays != 30 {
		t.Fatalf("config not applied: port %d days %d", dashPort, dashDays)
	}
	if err := fs.Parse([]string{"--port", "9100", "--days", "7"}); err != nil {
		t.Fatal(err)
	}
	if err := applyDashConfig(config.DashConfig{Port: 9000, Days: 30}); err != nil {
		t.Fatal(err)
	}
	if dashPort != 9100 || dashDays != 7 {
		t.Fatalf("flags lost: port %d days %d", dashPort, dashDays)
	}
	fs = (&cobra.Command{}).Flags()
	fs.IntVar(&dashPort, "port", DefaultDashPort, "")
	fs.IntVar(&dashDays, "days", dash.DefaultDays, "")
	dashFlags = fs
	if err := applyDashConfig(config.DashConfig{Port: 70000}); err == nil || !strings.Contains(err.Error(), "[dash] port") {
		t.Fatalf("bad port: %v", err)
	}
	if err := applyDashConfig(config.DashConfig{Days: 999}); err == nil || !strings.Contains(err.Error(), "[dash] days") {
		t.Fatalf("bad days: %v", err)
	}
}

func TestDashInterval(t *testing.T) {
	for in, want := range map[int]time.Duration{0: dash.DefaultRefreshInterval, -3: dash.DefaultRefreshInterval, 1: 5 * time.Second, 60: time.Minute} {
		if got := dashInterval(config.DashConfig{RefreshSeconds: in}); got != want {
			t.Errorf("refresh_seconds %d: %v, want %v", in, got, want)
		}
	}
}

func TestDashLaunchConfigDegrades(t *testing.T) {
	var warn bytes.Buffer
	good := config.DashConfig{Terminal: "command", TerminalCommand: []string{"wezterm", "start", "--cwd", "{workspace}"}, Resume: "claude", Editor: "/usr/local/bin/zed"}
	if got := dashLaunchConfig(good, &warn); got.Terminal != "command" || got.Resume != "claude" || got.Editor != good.Editor || warn.Len() != 0 {
		t.Fatalf("good config: %+v %q", got, warn.String())
	}
	got := dashLaunchConfig(config.DashConfig{Terminal: "kitty", Resume: "bash -c evil", Editor: "zed"}, &warn)
	if got.Terminal != "" || got.TerminalCommand != nil || got.Resume != "" || got.Editor != "zed" {
		t.Fatalf("degraded = %+v", got)
	}
	for _, want := range []string{"ignoring [dash] resume", "ignoring [dash] terminal"} {
		if !strings.Contains(warn.String(), want) {
			t.Errorf("warnings lack %q: %s", want, warn.String())
		}
	}
	warn.Reset()
	got = dashLaunchConfig(config.DashConfig{Terminal: "ghostty", TerminalCommand: []string{"wezterm", "--cwd={workspace}"}, Resume: "codex"}, &warn)
	if got.Terminal != "" || got.TerminalCommand != nil || got.Resume != "codex" || !strings.Contains(warn.String(), "whole argument") {
		t.Fatalf("partial = %+v %q", got, warn.String())
	}
}

// TestServeDashActionsNeedPageToken checks the wired server refuses an
// action without the page token and launches nothing.
func TestServeDashActionsNeedPageToken(t *testing.T) {
	ln, err := listenDash(0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := &syncBuffer{}
	done := make(chan error, 1)
	fl := &recordingLauncher{}
	go func() {
		done <- serveDash(ctx, out, ln, dashServer{
			source:   nilSource{},
			refresh:  func(context.Context) error { return nil },
			interval: time.Hour,
			errOut:   io.Discard,
			actions:  &dash.ActionOptions{Resolver: noResolver{}, Launcher: fl},
		})
	}()
	base := "http://" + ln.Addr().String()
	var resp *http.Response
	for i := 0; i < 100; i++ {
		req, _ := http.NewRequest(http.MethodPost, base+dash.ActionPath, strings.NewReader(`{"kind":"terminal","workspace_id":"ws-0000000000000000"}`))
		req.Header.Set("Origin", base)
		req.Header.Set("Content-Type", "application/json")
		if resp, err = http.DefaultClient.Do(req); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || fl.n.Load() != 0 {
		t.Fatalf("tokenless action: %d, %d launches", resp.StatusCode, fl.n.Load())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "actions enabled for this page only") {
		t.Fatalf("banner: %q", out.String())
	}
}

type recordingLauncher struct{ n atomic.Int32 }

func (r *recordingLauncher) Launch(context.Context, launch.Action) launch.Result {
	r.n.Add(1)
	return launch.Result{}
}

type noResolver struct{}

func (noResolver) Resolve(string) (dash.Target, error) {
	return dash.Target{}, dash.ErrUnknownWorkspace
}
