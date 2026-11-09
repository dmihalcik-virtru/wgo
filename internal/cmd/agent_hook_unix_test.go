//go:build unix

package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/store"
)

// realLookupWorkspace is lookupWorkspace before any test stubs it.
var realLookupWorkspace = lookupWorkspace

// TestLookupWorkspaceTimesOut: a jj that never answers (blocked on a lock
// another jj holds, say) costs lookupWorkspace workspaceLookupTimeout, not
// the hook's whole budget, and jj is run without snapshotting.
func TestLookupWorkspaceTimesOut(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	require.NoError(t, err)
	bin, ws := t.TempDir(), t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\necho \"$@\" >> " + argsFile + "\nexec " + sleep + " 30\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "jj"), []byte(script), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(ws, ".jj"), 0o755))
	t.Setenv("PATH", bin)
	old := workspaceLookupTimeout
	workspaceLookupTimeout = time.Second
	t.Cleanup(func() { workspaceLookupTimeout = old })

	start := time.Now()
	_, err = realLookupWorkspace(ws)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second, "jj is interrupted, not waited for")

	args, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(args), "--ignore-working-copy "), "jj ran as %q", args)
}

// TestAgentHookReportsJJTimeout: a new session whose workspace jj could not
// resolve in time is not recorded, and the warning says jj was slow rather
// than blaming Claude Code's PATH.
func TestAgentHookReportsJJTimeout(t *testing.T) {
	e := newAgentEnv(t)
	lookupWorkspace = func(string) (workspaceInfo, error) {
		return workspaceInfo{}, context.DeadlineExceeded
	}
	runHookFixture(t, "post_tool_use.json", "PostToolUse")
	assert.Nil(t, loadState(t).GetAgentSession(hookSessionID))
	msg := hookWarning(t, e.warnings.String())
	assert.Contains(t, msg, "did not resolve the workspace at /ws/sub/dir")
	assert.NotContains(t, msg, "PATH")
}

// TestAgentHookGivesUpOnHeldStateLock: while another wgo holds state.json's
// lock, a hook gives up after hookLockTimeout instead of holding up the
// prompt until Claude Code kills it, and records the session on a later
// event once the lock is free.
func TestAgentHookGivesUpOnHeldStateLock(t *testing.T) {
	e := newAgentEnv(t)
	old := hookLockTimeout
	hookLockTimeout = 50 * time.Millisecond
	t.Cleanup(func() { hookLockTimeout = old })

	s, err := store.New()
	require.NoError(t, err)
	require.NoError(t, s.EnsureDir())
	f, err := os.OpenFile(filepath.Join(s.BaseDir(), "state.json.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX))

	start := time.Now()
	runHookFixture(t, "post_tool_use.json", "UserPromptSubmit")
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.Nil(t, loadState(t).GetAgentSession(hookSessionID))
	assert.Contains(t, hookWarning(t, e.warnings.String()), "still held after")

	require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_UN))
	e.advance(time.Second)
	runHookFixture(t, "post_tool_use.json", "PostToolUse")
	assert.NotNil(t, loadState(t).GetAgentSession(hookSessionID))
}
