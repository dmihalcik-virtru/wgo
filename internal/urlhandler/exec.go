package urlhandler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner runs a process from an explicit argv and returns its stdout.
// ExecRunner is the real one; tests inject a fake so they never run
// osascript, osacompile, plutil, codesign or lsregister.
type Runner interface {
	Output(ctx context.Context, argv []string) (string, error)
}

// runTimeout bounds one process. The confirmation dialog gives up on its
// own after dialogTimeout seconds, well inside this.
const runTimeout = 3 * time.Minute

// ExecRunner runs processes with os/exec: argv only, never a shell.
type ExecRunner struct{}

// Output implements Runner.
func (ExecRunner) Output(ctx context.Context, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		if msg != "" {
			return stdout.String(), fmt.Errorf("%s: %v: %s", argv[0], err, msg)
		}
		return stdout.String(), fmt.Errorf("%s: %v", argv[0], err)
	}
	return stdout.String(), nil
}
