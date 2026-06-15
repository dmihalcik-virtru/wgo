// Package jjtest provides helpers for tests that drive a real `jj` binary.
// Helpers create repositories under t.TempDir(), configure a deterministic
// author identity, and clean up automatically via testing.T cleanup hooks.
package jjtest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/virtru/wgo/internal/jj"
)

// RequireJJ skips the test if `jj` is not on PATH. The CI image is expected
// to install jj; locally, contributors can `brew install jj` (or equivalent).
func RequireJJ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj binary not found on PATH; skipping integration test")
	}
}

// NewRepo creates a fresh non-colocated jj repo inside t.TempDir(), seeds an
// initial described commit, and returns the repo root plus a CLIClient
// pointing at the system `jj` binary.
func NewRepo(t *testing.T) (string, *jj.CLIClient) {
	t.Helper()
	RequireJJ(t)
	repo := t.TempDir()
	runJJ(t, repo, "git", "init", "--no-colocate")
	// jj reads user identity from XDG config / env; pin both so tests are
	// deterministic without touching the contributor's global jj config.
	t.Setenv("JJ_USER", "wgo-test")
	t.Setenv("JJ_EMAIL", "wgo-test@example.com")
	runJJ(t, repo, "config", "set", "--repo", "user.name", "wgo-test")
	runJJ(t, repo, "config", "set", "--repo", "user.email", "wgo-test@example.com")
	runJJ(t, repo, "describe", "-m", "initial")
	return repo, jj.NewCLI()
}

// NewWorkspace creates an additional workspace at <repo>/../<name>-ws and
// returns its absolute path.
func NewWorkspace(t *testing.T, repo, name string) string {
	t.Helper()
	RequireJJ(t)
	parent := filepath.Dir(repo)
	dest := filepath.Join(parent, name+"-ws")
	runJJ(t, repo, "workspace", "add", "--name", name, dest)
	return dest
}

// Commit writes the given files into the workspace at workspacePath, sets
// the current change's description to msg, and starts a fresh empty change
// on top. After Commit returns, @- is the just-described change.
func Commit(t *testing.T, workspacePath, msg string, files map[string]string) {
	t.Helper()
	RequireJJ(t)
	for name, body := range files {
		full := filepath.Join(workspacePath, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	runJJ(t, workspacePath, "describe", "-m", msg)
	runJJ(t, workspacePath, "new")
}

// Bookmark creates a bookmark at the given revset. Errors if the bookmark
// already exists.
func Bookmark(t *testing.T, repo, name, revset string) {
	t.Helper()
	RequireJJ(t)
	runJJ(t, repo, "bookmark", "create", name, "-r", revset)
}

// runJJ executes jj inside dir, failing the test on non-zero exit. Stderr
// is included verbatim in the failure message.
func runJJ(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("jj", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("jj %v (in %s): %v\nstderr: %s", args, dir, err, stderr.String())
	}
}
