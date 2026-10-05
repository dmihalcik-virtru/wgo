package launch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RunTimeout bounds a launcher that should return promptly. osascript can
// wait on the macOS Automation consent prompt, so it gets this long.
const RunTimeout = 2 * time.Minute

// startGrace is how long Start waits for a detached process to fail fast
// (a bad flag, a missing display) before reporting it as running.
const startGrace = 1500 * time.Millisecond

// ExecRunner runs processes with os/exec: argv only, never a shell.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, dir string, argv []string) error {
	if len(argv) == 0 {
		return errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, RunTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return describe(err, stderr.String())
	}
	return nil
}

// Start implements Runner. The process gets its own process group, so
// stopping wgo dash does not close the terminal it opened.
func (ExecRunner) Start(dir string, argv []string) error {
	if len(argv) == 0 {
		return errors.New("empty command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return describe(err, "")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return describe(err, stderr.String())
		}
		return nil
	case <-time.After(startGrace):
		return nil // still running: it opened; Wait reaps it later
	}
}

// LookPath implements Runner.
func (ExecRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

// AppInstalled implements Runner: it looks for name.app in the standard
// application folders.
func (ExecRunner) AppInstalled(name string) bool {
	dirs := []string{"/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Applications"))
	}
	for _, d := range dirs {
		if fi, err := os.Stat(filepath.Join(d, name+".app")); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// describe turns an exec failure into a short message with the first line
// of stderr.
func describe(err error, stderr string) error {
	msg := strings.TrimSpace(stderr)
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	if msg != "" {
		return fmt.Errorf("%v: %s", err, msg)
	}
	return err
}
