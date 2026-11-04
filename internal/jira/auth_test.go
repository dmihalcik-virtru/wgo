package jira

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// withFakeAcli puts an acli on PATH that runs script.
func withFakeAcli(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "acli"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// TestIsAuthError covers what acli 1.3 printed with an expired login, and
// a work item view, which does not say why it failed.
func TestIsAuthError(t *testing.T) {
	for msg, want := range map[string]bool{
		"✗ Error: unauthorized: use 'acli jira auth login' to authenticate":      true,
		"✗ Error: unauthorized: use 'acli [product] auth login' to authenticate": true,
		ErrNotLoggedIn.Error():                                 true,
		"✗ Error: failed to fetch work item details":           false,
		"✗ Error: dial tcp: lookup jira.example: no such host": false,
	} {
		if got := IsAuthError(msg); got != want {
			t.Errorf("IsAuthError(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestLoginStatus(t *testing.T) {
	withFakeAcli(t, `echo "✓ Authenticated"`)
	if err := LoginStatus(context.Background()); err != nil {
		t.Fatalf("logged in: %v", err)
	}

	withFakeAcli(t, `echo "✗ Error: unauthorized: use 'acli jira auth login' to authenticate"; exit 1`)
	if err := LoginStatus(context.Background()); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("logged out: %v, want ErrNotLoggedIn", err)
	}

	withFakeAcli(t, `echo "✗ Error: dial tcp: lookup jira.example: no such host" >&2; exit 1`)
	err := LoginStatus(context.Background())
	if err == nil || errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("acli could not tell: %v, want another error", err)
	}
	if want := "acli jira auth status: exit status 1: ✗ Error: dial tcp: lookup jira.example: no such host"; err.Error() != want {
		t.Fatalf("error %q, want %q", err, want)
	}
}
