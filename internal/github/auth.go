package github

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// tokenSource resolves a GitHub API token, caching the result in memory.
// Resolution order:
//  1. GITHUB_TOKEN environment variable
//  2. `gh auth token` (only shell-out to gh allowed in this package)
//
// The result is cached for the lifetime of the process. If both sources
// fail, every call returns the same error.
type tokenSource struct {
	mu       sync.Mutex
	resolved bool
	token    string
	err      error
}

// Token returns the resolved API token, fetching it on first call.
func (t *tokenSource) Token() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resolved {
		return t.token, t.err
	}
	t.token, t.err = resolveToken()
	t.resolved = true
	return t.token, t.err
}

// Reset clears the cached token so the next call re-resolves. Primarily for
// tests; callers should not need this.
func (t *tokenSource) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resolved = false
	t.token = ""
	t.err = nil
}

// SetToken installs a token directly, skipping resolution. Primarily for tests.
func (t *tokenSource) SetToken(token string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.token = token
	t.err = nil
	t.resolved = true
}

func resolveToken() (string, error) {
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		return tok, nil
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("no GitHub token: set GITHUB_TOKEN env var or run `gh auth login` (gh auth token failed: %w)", err)
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", fmt.Errorf("no GitHub token: `gh auth token` returned empty; run `gh auth login`")
	}
	return tok, nil
}

// ghAvailable reports whether the gh CLI is on PATH. Used for the legacy
// Available() method on Client; we want it to remain non-blocking and cheap.
func ghAvailable() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}
