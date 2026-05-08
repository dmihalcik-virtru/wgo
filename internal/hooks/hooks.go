package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var hookNames = []string{"post-checkout", "post-commit", "post-merge", "post-rewrite"}

// HookStatus describes the current hook installation state.
type HookStatus struct {
	Installed    bool
	HooksDir     string
	ActiveHooks  []string
	PreviousPath string
}

// Manager handles installation and removal of wgo git hooks.
type Manager struct {
	wgoDir   string
	hooksDir string
	wgoBin   string
}

// NewManager creates a new hook Manager.
// wgoDir is the ~/.wgo directory path.
func NewManager(wgoDir string) (*Manager, error) {
	wgoBin, err := exec.LookPath("wgo")
	if err != nil {
		// Fall back to "wgo" and let the hook script check at runtime
		wgoBin = "wgo"
	}
	return &Manager{
		wgoDir:   wgoDir,
		hooksDir: filepath.Join(wgoDir, "hooks"),
		wgoBin:   wgoBin,
	}, nil
}

// Install sets up global git hooks that call back into wgo.
func (m *Manager) Install() error {
	// Read current core.hooksPath
	previousPath := getGlobalHooksPath()

	// Create hooks directory
	if err := os.MkdirAll(m.hooksDir, 0o755); err != nil {
		return fmt.Errorf("failed to create hooks directory: %w", err)
	}

	// Save previous hooks path for uninstall
	prevFile := filepath.Join(m.hooksDir, ".previous_hooks_path")
	if err := os.WriteFile(prevFile, []byte(previousPath), 0o644); err != nil {
		return fmt.Errorf("failed to save previous hooks path: %w", err)
	}

	// Generate hook scripts
	for _, hookName := range hookNames {
		script := generateHookScript(hookName, previousPath)
		hookPath := filepath.Join(m.hooksDir, hookName)
		if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
			return fmt.Errorf("failed to write %s hook: %w", hookName, err)
		}
	}

	// Set global core.hooksPath
	cmd := exec.Command("git", "config", "--global", "core.hooksPath", m.hooksDir)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to set core.hooksPath: %w", err)
	}

	return nil
}

// Uninstall removes wgo git hooks and restores previous configuration.
func (m *Manager) Uninstall() error {
	// Read saved previous path
	prevFile := filepath.Join(m.hooksDir, ".previous_hooks_path")
	prevData, err := os.ReadFile(prevFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read previous hooks path: %w", err)
	}
	previousPath := strings.TrimSpace(string(prevData))

	// Restore or unset core.hooksPath
	var configErr error
	if previousPath != "" {
		cmd := exec.Command("git", "config", "--global", "core.hooksPath", previousPath)
		if err := cmd.Run(); err != nil {
			configErr = fmt.Errorf("failed to restore core.hooksPath: %w", err)
		}
	} else {
		cmd := exec.Command("git", "config", "--global", "--unset", "core.hooksPath")
		// --unset returns exit code 5 if key doesn't exist; ignore that
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 5 {
				// key didn't exist, that's fine
			} else {
				configErr = fmt.Errorf("failed to unset core.hooksPath: %w", err)
			}
		}
	}

	// Remove hooks directory
	if err := os.RemoveAll(m.hooksDir); err != nil {
		return fmt.Errorf("failed to remove hooks directory: %w", err)
	}

	return configErr
}

// Status returns the current hook installation state.
func (m *Manager) Status() (*HookStatus, error) {
	status := &HookStatus{
		HooksDir: m.hooksDir,
	}

	// Check if core.hooksPath points to our directory
	currentPath := getGlobalHooksPath()
	status.Installed = currentPath == m.hooksDir

	// Check which hook scripts exist
	for _, hookName := range hookNames {
		hookPath := filepath.Join(m.hooksDir, hookName)
		if info, err := os.Stat(hookPath); err == nil && info.Mode()&0o111 != 0 {
			status.ActiveHooks = append(status.ActiveHooks, hookName)
		}
	}

	// Read previous path
	prevFile := filepath.Join(m.hooksDir, ".previous_hooks_path")
	if data, err := os.ReadFile(prevFile); err == nil {
		status.PreviousPath = strings.TrimSpace(string(data))
	}

	return status, nil
}

// getGlobalHooksPath reads the current git config --global core.hooksPath.
func getGlobalHooksPath() string {
	cmd := exec.Command("git", "config", "--global", "core.hooksPath")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// generateHookScript creates the shell script content for a hook.
func generateHookScript(hookName, previousPath string) string {
	return fmt.Sprintf(`#!/bin/sh
# wgo hook: %s
# Managed by wgo - do not edit manually

# Record event (fail silently, never block git)
if command -v wgo >/dev/null 2>&1; then
    wgo _event %s "$@" 2>/dev/null || true
fi

# Chain to previous core.hooksPath hook
_prev="%s/%s"
if [ -n "%s" ] && [ -x "$_prev" ]; then
    "$_prev" "$@"
fi

# Chain to per-repo hook
_repo="$(git rev-parse --git-dir 2>/dev/null)/hooks/%s"
if [ -x "$_repo" ]; then
    "$_repo" "$@"
fi
`, hookName, hookName, previousPath, hookName, previousPath, hookName)
}
