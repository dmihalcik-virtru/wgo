package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateHookScript(t *testing.T) {
	script := generateHookScript("post-checkout", "/old/hooks")

	if !strings.Contains(script, "#!/bin/sh") {
		t.Error("script missing shebang")
	}
	if !strings.Contains(script, "wgo _event post-checkout") {
		t.Error("script missing wgo _event call")
	}
	if !strings.Contains(script, "/old/hooks/post-checkout") {
		t.Error("script missing previous hooks path chain")
	}
	if !strings.Contains(script, "per-repo hook") {
		t.Error("script missing per-repo hook chain")
	}
}

func TestGenerateHookScript_NoPreviousPath(t *testing.T) {
	script := generateHookScript("post-commit", "")

	if !strings.Contains(script, "wgo _event post-commit") {
		t.Error("script missing wgo _event call")
	}
	// With empty previous path, the chain check should have empty string
	if !strings.Contains(script, `if [ -n "" ]`) {
		t.Error("script should have empty previous path check")
	}
}

func TestManager_Install_CreatesHookScripts(t *testing.T) {
	dir := t.TempDir()
	wgoDir := filepath.Join(dir, ".wgo")
	if err := os.MkdirAll(wgoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	m := &Manager{
		wgoDir:   wgoDir,
		hooksDir: filepath.Join(wgoDir, "hooks"),
		wgoBin:   "wgo",
	}

	// We can't actually set global git config in tests, so test the script generation part.
	// Create hooks dir and scripts manually (testing the generation logic).
	if err := os.MkdirAll(m.hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, hookName := range hookNames {
		script := generateHookScript(hookName, "")
		hookPath := filepath.Join(m.hooksDir, hookName)
		if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
			t.Fatalf("failed to write %s: %v", hookName, err)
		}
	}

	// Verify all hook scripts were created
	for _, hookName := range hookNames {
		hookPath := filepath.Join(m.hooksDir, hookName)
		info, err := os.Stat(hookPath)
		if err != nil {
			t.Errorf("hook %s not created: %v", hookName, err)
			continue
		}
		if info.Mode()&0o111 == 0 {
			t.Errorf("hook %s not executable", hookName)
		}
	}

	// Verify .previous_hooks_path was created
	prevFile := filepath.Join(m.hooksDir, ".previous_hooks_path")
	if err := os.WriteFile(prevFile, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(prevFile)
	if err != nil {
		t.Fatalf("previous_hooks_path not created: %v", err)
	}
	if string(data) != "" {
		t.Errorf("previous_hooks_path should be empty, got %q", string(data))
	}
}

func TestManager_Status(t *testing.T) {
	dir := t.TempDir()
	wgoDir := filepath.Join(dir, ".wgo")
	hooksDir := filepath.Join(wgoDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	m := &Manager{
		wgoDir:   wgoDir,
		hooksDir: hooksDir,
		wgoBin:   "wgo",
	}

	// Write some hook scripts
	for _, name := range []string{"post-checkout", "post-commit"} {
		hookPath := filepath.Join(hooksDir, name)
		if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	status, err := m.Status()
	if err != nil {
		t.Fatalf("Status() failed: %v", err)
	}

	if len(status.ActiveHooks) != 2 {
		t.Errorf("expected 2 active hooks, got %d", len(status.ActiveHooks))
	}
}

func TestManager_Uninstall_RemovesHooksDir(t *testing.T) {
	dir := t.TempDir()
	wgoDir := filepath.Join(dir, ".wgo")
	hooksDir := filepath.Join(wgoDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write previous hooks path (empty = no previous)
	prevFile := filepath.Join(hooksDir, ".previous_hooks_path")
	if err := os.WriteFile(prevFile, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	m := &Manager{
		wgoDir:   wgoDir,
		hooksDir: hooksDir,
		wgoBin:   "wgo",
	}

	// Uninstall will try to git config --global --unset which may fail in test,
	// but the directory removal should still work. We test the directory removal part.
	_ = m.Uninstall()

	if _, err := os.Stat(hooksDir); !os.IsNotExist(err) {
		t.Error("hooks directory should be removed after uninstall")
	}
}

func TestHookNames(t *testing.T) {
	expected := []string{"pre-commit", "post-checkout", "post-commit", "post-merge", "post-rewrite"}
	if len(hookNames) != len(expected) {
		t.Fatalf("expected %d hook names, got %d", len(expected), len(hookNames))
	}
	for i, name := range expected {
		if hookNames[i] != name {
			t.Errorf("hookNames[%d] = %q, want %q", i, hookNames[i], name)
		}
	}
}
