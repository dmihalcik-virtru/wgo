package jira

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/config"
)

// withFakeTwg places a v2-speaking twg on PATH for subprocess tests.
func withFakeTwg(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "twg"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
}

func TestIsAuthError(t *testing.T) {
	for msg, want := range map[string]bool{
		`AUTH_REQUIRED: login required`:                                        true,
		`{"ok":false,"error":{"code":"AUTH_EXPIRED","kind":"authentication"}}`: true,
		ErrNotLoggedIn.Error():                                                 true,
		`ISSUE_NOT_FOUND: no work item`:                                        false,
		`dial tcp: no such host`:                                               false,
	} {
		if got := IsAuthError(msg); got != want {
			t.Errorf("IsAuthError(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestLoginStatus(t *testing.T) {
	withFakeTwg(t, `test "$TWG_AGENT_DEFAULTS" = 1 || exit 2
echo '{"apiVersion":"v2","data":{"accountId":"acct","email":"user@example.com"}}'`)
	if err := LoginStatus(context.Background()); err != nil {
		t.Fatalf("logged in: %v", err)
	}

	withFakeTwg(t, `echo '{"apiVersion":"v2","ok":false,"error":{"code":"AUTH_REQUIRED","kind":"authentication","message":"login required"}}'; exit 1`)
	if err := LoginStatus(context.Background()); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("logged out: %v, want ErrNotLoggedIn", err)
	}

	withFakeTwg(t, `echo '{"apiVersion":"v2","ok":false,"error":{"code":"NETWORK_ERROR","kind":"network","message":"no such host"}}'; exit 1`)
	err := LoginStatus(context.Background())
	if err == nil || errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("network failure: %v, want another error", err)
	}
}

func TestBinaryPathFallbackAndMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	if _, err := BinaryPath(); !errors.Is(err, ErrMissingTWG) {
		t.Fatalf("missing binary: %v", err)
	}
	path := filepath.Join(home, ".local", "bin", "twg")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho '{\"apiVersion\":\"v2\",\"data\":{\"accountId\":\"a\"}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := BinaryPath(); err != nil || got != path {
		t.Fatalf("fallback = %q, %v", got, err)
	}
	if err := LoginStatus(context.Background()); err != nil {
		t.Fatalf("fallback login: %v", err)
	}
}

func TestCancelledTwgCallStops(t *testing.T) {
	withFakeTwg(t, `exec sleep 30`)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := GetIssueJSONContext(ctx, "TEAM-1", "summary")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("cancelled call took %v: %v", time.Since(start), err)
	}
}

func TestSiteHostUsesDefaultProfile(t *testing.T) {
	withFakeTwg(t, `echo '{"apiVersion":"v2","data":{"activeEnvironment":"prod","defaults":{"prod":"p2"},"profiles":[{"id":"p1","site":"wrong.example"},{"id":"p2","site":"right.example"}]}}'`)
	got, err := SiteHost()
	if err != nil || got != "right.example" {
		t.Fatalf("SiteHost = %q, %v", got, err)
	}
}

func TestTwgCommandFlags(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	withFakeTwg(t, `printf '%s\n' "$@" > '`+argsFile+`'
echo '{"apiVersion":"v2","data":{"accountId":"acct"}}'`)
	if err := LoginStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "-o\njson\n--output-summary\nnone\n") {
		t.Fatalf("missing JSON flags: %s", args)
	}
}

func TestTwgUsesConfiguredSite(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	withFakeTwg(t, `printf '%s\n' "$@" > '`+argsFile+`'
echo '{"apiVersion":"v2","data":{"accountId":"acct"}}'`)
	configDir := filepath.Join(os.Getenv("HOME"), ".wgo")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configFile, []byte("[jira]\nsite = \"jira.example\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(configFile, nil, 0o644)
		_ = config.Init()
	})
	if err := LoginStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(args), "--site\njira.example\nwhoami\n") {
		t.Fatalf("site flag missing: %s", args)
	}
}
