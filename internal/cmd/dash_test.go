package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/dash"
)

func TestDashRejectsBadFlags(t *testing.T) {
	oldJSON, oldDays, oldPort, oldRefresh := dashJSON, dashDays, dashPort, dashRefresh
	t.Cleanup(func() { dashJSON, dashDays, dashPort, dashRefresh = oldJSON, oldDays, oldPort, oldRefresh })
	dashJSON, dashDays, dashPort = false, dash.DefaultDays, 0
	if err := runDash(context.Background(), &bytes.Buffer{}, noDashFlags); err == nil || !strings.Contains(err.Error(), "--port") {
		t.Fatalf("want a --port error, got %v", err)
	}
	dashPort, dashDays = DefaultDashPort, 0
	if err := runDash(context.Background(), &bytes.Buffer{}, noDashFlags); err == nil || !strings.Contains(err.Error(), "--days") {
		t.Fatalf("want a --days error, got %v", err)
	}
	// --refresh only means something for the one-shot --json snapshot.
	dashDays, dashRefresh = dash.DefaultDays, true
	if err := runDash(context.Background(), &bytes.Buffer{}, noDashFlags); err == nil || !strings.Contains(err.Error(), "wgo dash --json --refresh") {
		t.Fatalf("want a --refresh error suggesting --json, got %v", err)
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

// A server on the wildcard address also answers on loopback, so it makes the
// port busy even though 127.0.0.1:P itself would bind on macOS.
func TestListenDashBusyWildcardPort(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "[::]:0"} {
		busy, err := net.Listen("tcp", addr)
		if err != nil {
			t.Logf("%s: %v (skipped)", addr, err)
			continue
		}
		port := busy.Addr().(*net.TCPAddr).Port
		ln, err := listenDash(port)
		busy.Close()
		if err == nil {
			ln.Close()
			t.Errorf("listenDash(%d) succeeded while %s held the port", port, busy.Addr())
			continue
		}
		if !strings.Contains(err.Error(), "already in use") {
			t.Errorf("%s: want a busy-port error, got %q", addr, err)
		}
	}
}

func TestBusyPortErrorStaysInRange(t *testing.T) {
	if msg := busyPortError("127.0.0.1:65535", 65535).Error(); !strings.Contains(msg, "--port 65534") {
		t.Fatalf("busy 65535 should suggest 65534, got %q", msg)
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

// A refresh that panics must not take the server down, and a failure that
// repeats every tick is printed once, then once more when it clears.
func TestServeDashSurvivesPanicsAndPrintsFailuresOnce(t *testing.T) {
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
			source: nilSource{},
			refresh: func(context.Context) error {
				switch n := ticks.Add(1); {
				case n == 1:
					panic("boom")
				case n < 5:
					return errors.New("jj unavailable")
				default:
					return nil
				}
			},
			interval: 5 * time.Millisecond,
			errOut:   errOut,
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for ticks.Load() < 6 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := errOut.String()
	if ticks.Load() < 6 || !strings.Contains(got, "refresh panicked: boom") {
		t.Fatalf("ticks %d; the loop should outlive a panic: %q", ticks.Load(), got)
	}
	if strings.Count(got, "refresh failed: jj unavailable") != 1 || strings.Count(got, "refresh is working again") != 1 {
		t.Fatalf("a repeated failure should be printed once, and its recovery once: %q", got)
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

// noDashFlags is a flagChanged for runDash with no flags passed.
func noDashFlags(string) bool { return false }

// saveDashFlags restores the dash flag variables when the test ends.
func saveDashFlags(t *testing.T) {
	t.Helper()
	oj, od, op, or := dashJSON, dashDays, dashPort, dashRefresh
	t.Cleanup(func() { dashJSON, dashDays, dashPort, dashRefresh = oj, od, op, or })
}

func TestApplyDashConfig(t *testing.T) {
	passed := func(names ...string) func(string) bool {
		return func(name string) bool { return slices.Contains(names, name) }
	}
	for _, tc := range []struct {
		name     string
		dc       config.DashConfig
		json     bool
		flags    func(string) bool
		wantPort int
		wantDays int
		wantErr  string
	}{
		{name: "unset", flags: noDashFlags, wantPort: 1111, wantDays: 2},
		{name: "config applies", dc: config.DashConfig{Port: 9000, Days: 30}, flags: noDashFlags, wantPort: 9000, wantDays: 30},
		{name: "flags win", dc: config.DashConfig{Port: 9000, Days: 30}, flags: passed("port", "days"), wantPort: 1111, wantDays: 2},
		{name: "flag hides bad config", dc: config.DashConfig{Port: 70000, Days: 999}, flags: passed("port", "days"), wantPort: 1111, wantDays: 2},
		{name: "boundaries", dc: config.DashConfig{Port: 65535, Days: 366, RefreshSeconds: 3600}, flags: noDashFlags, wantPort: 65535, wantDays: 366},
		{name: "low boundaries", dc: config.DashConfig{Port: 1, Days: 1, RefreshSeconds: 5}, flags: noDashFlags, wantPort: 1, wantDays: 1},
		{name: "port too big", dc: config.DashConfig{Port: 70000}, flags: noDashFlags, wantErr: "[dash] port"},
		{name: "port negative", dc: config.DashConfig{Port: -1}, flags: noDashFlags, wantErr: "[dash] port"},
		{name: "days too big", dc: config.DashConfig{Days: 999}, flags: noDashFlags, wantErr: "[dash] days"},
		{name: "days negative", dc: config.DashConfig{Days: -5}, flags: noDashFlags, wantErr: "[dash] days"},
		{name: "refresh too small", dc: config.DashConfig{RefreshSeconds: 4}, flags: noDashFlags, wantErr: "[dash] refresh_seconds"},
		{name: "refresh negative", dc: config.DashConfig{RefreshSeconds: -3}, flags: noDashFlags, wantErr: "[dash] refresh_seconds"},
		{name: "refresh too big", dc: config.DashConfig{RefreshSeconds: 3601}, flags: noDashFlags, wantErr: "[dash] refresh_seconds"},
		// --json never serves, so it ignores port and refresh_seconds.
		{name: "json ignores port and refresh", dc: config.DashConfig{Port: 70000, RefreshSeconds: 1, Days: 30}, json: true, flags: noDashFlags, wantPort: 1111, wantDays: 30},
		{name: "json still checks days", dc: config.DashConfig{Days: 999}, json: true, flags: noDashFlags, wantErr: "[dash] days"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saveDashFlags(t)
			dashJSON, dashPort, dashDays = tc.json, 1111, 2
			err := applyDashConfig(tc.dc, tc.flags)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "; fix it") {
					t.Fatalf("want a %s error with a fix, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if dashPort != tc.wantPort || dashDays != tc.wantDays {
				t.Fatalf("port %d days %d, want %d and %d", dashPort, dashDays, tc.wantPort, tc.wantDays)
			}
		})
	}
}

func TestDashInterval(t *testing.T) {
	for in, want := range map[int]time.Duration{0: dash.DefaultRefreshInterval, 5: 5 * time.Second, 60: time.Minute} {
		if got := dashInterval(config.DashConfig{RefreshSeconds: in}); got != want {
			t.Errorf("refresh_seconds %d: %v, want %v", in, got, want)
		}
	}
}

// TestRunDashChecksConfig verifies runDash applies [dash] config: a bad
// value stops it before anything is collected or served.
func TestRunDashChecksConfig(t *testing.T) {
	saveDashFlags(t)
	goldenHome(t, "[dash]\nport = 70000\n")
	dashJSON, dashDays, dashPort, dashRefresh = false, dash.DefaultDays, DefaultDashPort, false
	if err := runDash(context.Background(), &bytes.Buffer{}, noDashFlags); err == nil || !strings.Contains(err.Error(), "[dash] port") {
		t.Fatalf("want a [dash] port error, got %v", err)
	}
}

// TestDashCobraWiring runs wgo dash --json through cobra, so the real
// flag set decides which flags were passed: --days beats [dash] days,
// while [dash] days applies when --days is absent.
func TestDashCobraWiring(t *testing.T) {
	saveDashFlags(t)
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"flag wins", []string{"dash", "--json", "--days", "7"}, 7},
		{"config applies", []string{"dash", "--json"}, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			goldenHome(t, "[discovery]\nbase_dirs = [\""+t.TempDir()+"\"]\n[dash]\ndays = 30\nport = 70000\n")
			t.Cleanup(func() {
				rootCmd.SetArgs(nil)
				rootCmd.SetOut(nil)
				for _, name := range []string{"json", "days"} {
					f := dashCmd.Flags().Lookup(name)
					_ = f.Value.Set(f.DefValue)
					f.Changed = false
				}
			})
			var buf bytes.Buffer
			rootCmd.SetOut(&buf)
			rootCmd.SetArgs(tc.args)
			if err := rootCmd.Execute(); err != nil {
				t.Fatalf("execute: %v\n%s", err, buf.String())
			}
			var out dashJSONOut
			if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
				t.Fatalf("decode: %v\n%s", err, buf.String())
			}
			if out.Snapshot.Days != tc.want {
				t.Fatalf("days %d, want %d", out.Snapshot.Days, tc.want)
			}
		})
	}
}
