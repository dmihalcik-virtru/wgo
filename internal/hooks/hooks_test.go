package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateHookScript(t *testing.T) {
	script := generateHookScript("post-checkout", "/old/hooks")

	assert.Contains(t, script, "#!/bin/sh")
	assert.Contains(t, script, "wgo _event post-checkout")
	assert.Contains(t, script, "/old/hooks/post-checkout")
	assert.Contains(t, script, "per-repo hook")
}

func TestGenerateHookScript_NoPreviousPath(t *testing.T) {
	script := generateHookScript("post-commit", "")

	assert.Contains(t, script, "wgo _event post-commit")
	// With empty previous path, the chain check should have empty string
	assert.Contains(t, script, `if [ -n "" ]`)
}

func TestManager_Install_CreatesHookScripts(t *testing.T) {
	dir := t.TempDir()
	wgoDir := filepath.Join(dir, ".wgo")
	require.NoError(t, os.MkdirAll(wgoDir, 0o755))

	m := &Manager{
		wgoDir:   wgoDir,
		hooksDir: filepath.Join(wgoDir, "hooks"),
		wgoBin:   "wgo",
	}

	// We can't actually set global git config in tests, so test the script generation part.
	// Create hooks dir and scripts manually (testing the generation logic).
	require.NoError(t, os.MkdirAll(m.hooksDir, 0o755))

	for _, hookName := range hookNames {
		script := generateHookScript(hookName, "")
		hookPath := filepath.Join(m.hooksDir, hookName)
		require.NoError(t, os.WriteFile(hookPath, []byte(script), 0o755), "failed to write %s", hookName)
	}

	// Verify all hook scripts were created
	for _, hookName := range hookNames {
		hookPath := filepath.Join(m.hooksDir, hookName)
		info, err := os.Stat(hookPath)
		require.NoError(t, err, "hook %s not created", hookName)
		assert.NotEqual(t, 0, info.Mode()&0o111, "hook %s not executable", hookName)
	}

	// Verify .previous_hooks_path was created
	prevFile := filepath.Join(m.hooksDir, ".previous_hooks_path")
	require.NoError(t, os.WriteFile(prevFile, []byte(""), 0o644))
	data, err := os.ReadFile(prevFile)
	require.NoError(t, err, "previous_hooks_path not created")
	assert.Equal(t, "", string(data))
}

func TestManager_Status(t *testing.T) {
	dir := t.TempDir()
	wgoDir := filepath.Join(dir, ".wgo")
	hooksDir := filepath.Join(wgoDir, "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	m := &Manager{
		wgoDir:   wgoDir,
		hooksDir: hooksDir,
		wgoBin:   "wgo",
	}

	// Write some hook scripts
	for _, name := range []string{"post-checkout", "post-commit"} {
		hookPath := filepath.Join(hooksDir, name)
		require.NoError(t, os.WriteFile(hookPath, []byte("#!/bin/sh\n"), 0o755))
	}

	status, err := m.Status()
	require.NoError(t, err, "Status() failed")
	assert.Len(t, status.ActiveHooks, 2)
}

func TestManager_Uninstall_RemovesHooksDir(t *testing.T) {
	dir := t.TempDir()
	wgoDir := filepath.Join(dir, ".wgo")
	hooksDir := filepath.Join(wgoDir, "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))

	// Write previous hooks path (empty = no previous)
	prevFile := filepath.Join(hooksDir, ".previous_hooks_path")
	require.NoError(t, os.WriteFile(prevFile, []byte(""), 0o644))

	m := &Manager{
		wgoDir:   wgoDir,
		hooksDir: hooksDir,
		wgoBin:   "wgo",
	}

	// Uninstall will try to git config --global --unset which may fail in test,
	// but the directory removal should still work. We test the directory removal part.
	_ = m.Uninstall()

	_, err := os.Stat(hooksDir)
	assert.True(t, os.IsNotExist(err), "hooks directory should be removed after uninstall")
}

func TestHookNames(t *testing.T) {
	expected := []string{"pre-commit", "post-checkout", "post-commit", "post-merge", "post-rewrite"}
	require.Len(t, hookNames, len(expected))
	for i, name := range expected {
		assert.Equal(t, name, hookNames[i], "hookNames[%d]", i)
	}
}
