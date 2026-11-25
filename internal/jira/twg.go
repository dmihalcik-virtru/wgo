package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/config"
)

// ErrMissingTWG means the optional Jira integration is unavailable.
var ErrMissingTWG = errors.New("twg not found on PATH or at ~/.local/bin/twg")

// BinaryPath finds twg without requiring ~/.local/bin on PATH.
func BinaryPath() (string, error) {
	if path, err := exec.LookPath("twg"); err == nil {
		return path, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		path := filepath.Join(home, ".local", "bin", "twg")
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return path, nil
		}
	}
	return "", ErrMissingTWG
}

type envelope struct {
	APIVersion string          `json:"apiVersion"`
	OK         *bool           `json:"ok"`
	Data       json.RawMessage `json:"data"`
	Error      struct {
		Code    string `json:"code"`
		Kind    string `json:"kind"`
		Message string `json:"message"`
	} `json:"error"`
}

// runContext is the single subprocess boundary for Jira operations. Every
// call uses agent defaults and asks for an unabridged v2 JSON envelope.
func runContext(ctx context.Context, args ...string) (json.RawMessage, error) {
	path, err := BinaryPath()
	if err != nil {
		return nil, err
	}
	fullArgs := make([]string, 0, len(args)+7)
	if cfg := config.Get(); cfg != nil && cfg.Jira.Site != "" {
		fullArgs = append(fullArgs, "--site", cfg.Jira.Site)
	}
	fullArgs = append(fullArgs, args...)
	fullArgs = append(fullArgs, "-o", "json", "--output-summary", "none")
	cmd := exec.CommandContext(ctx, path, fullArgs...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = append(os.Environ(), "TWG_AGENT_DEFAULTS=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var env envelope
	parseErr := json.Unmarshal(stdout.Bytes(), &env)
	if parseErr == nil && (env.APIVersion != "" || env.OK != nil || len(env.Data) > 0 || env.Error.Message != "") {
		if (env.OK != nil && !*env.OK) || env.Error.Code != "" || env.Error.Message != "" {
			msg := strings.TrimSpace(env.Error.Message)
			if msg == "" {
				msg = "request failed"
			}
			if env.Error.Code != "" {
				msg = env.Error.Code + ": " + msg
			}
			if strings.EqualFold(env.Error.Kind, "authentication") || IsAuthError(msg+" "+env.Error.Kind) {
				return nil, fmt.Errorf("%w: %s", ErrNotLoggedIn, msg)
			}
			return nil, fmt.Errorf("twg %s: %s", strings.Join(args, " "), msg)
		}
		if runErr == nil {
			if env.APIVersion != "v2" {
				return nil, fmt.Errorf("twg %s: unsupported API version %q", strings.Join(args, " "), env.APIVersion)
			}
			if len(env.Data) == 0 {
				return nil, fmt.Errorf("twg %s: response has no data", strings.Join(args, " "))
			}
			return env.Data, nil
		}
	}
	message := strings.TrimSpace(stderr.String())
	if message == "" {
		message = strings.TrimSpace(stdout.String())
	}
	if IsAuthError(message) {
		return nil, fmt.Errorf("%w: %s", ErrNotLoggedIn, message)
	}
	if runErr != nil {
		return nil, fmt.Errorf("twg %s: %w: %s", strings.Join(args, " "), runErr, message)
	}
	return nil, fmt.Errorf("twg %s: invalid JSON envelope: %w", strings.Join(args, " "), parseErr)
}
