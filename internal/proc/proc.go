// Package proc reads just enough about running processes to tell whether a
// recorded agent process is still the same live process: its PID, parent PID,
// name and an opaque start-time token. The start token guards against PID
// reuse: a PID only counts as live when the process exists AND its start token
// matches the one recorded with it.
//
// Lookups use sysctl on macOS and /proc on Linux, never a subprocess, so they
// are cheap enough to run on the statusline hot path.
package proc

import (
	"errors"
	"path/filepath"
	"strings"
)

// ErrNotFound reports that no live process has the requested PID.
var ErrNotFound = errors.New("process not found")

// Info describes one running process.
type Info struct {
	PID  int
	PPID int
	// Start is an opaque, platform-specific start-time token (microseconds
	// since the epoch on macOS, clock ticks since boot on Linux). It is only
	// meaningful when compared with another token from the same machine.
	Start int64
	// Name is the short process name (the kernel's comm), which may be
	// truncated (15 characters on Linux, 16 on macOS).
	Name string
}

// Inspector looks up processes. Tests substitute a fake.
type Inspector interface {
	// Lookup returns the process with the given PID, or ErrNotFound.
	Lookup(pid int) (Info, error)
	// Args returns the process's argument vector, best-effort. It is more
	// expensive than Lookup and is only used off the hot path.
	Args(pid int) ([]string, error)
}

// System returns the Inspector for the running operating system. On platforms
// without support every lookup reports ErrNotFound, so callers record no
// process identity and fall back to time-based liveness.
func System() Inspector { return systemInspector{} }

// Alive reports whether pid is running and still has the recorded start token.
// A zero pid or start token is never alive: without both there is no identity
// to verify.
func Alive(in Inspector, pid int, start int64) bool {
	if in == nil || pid <= 0 || start == 0 {
		return false
	}
	info, err := in.Lookup(pid)
	if err != nil {
		return false
	}
	return info.Start == start
}

// FindAncestor walks up the parent chain starting at pid (inclusive) and
// returns the first process that match accepts, visiting at most maxDepth
// processes. It stops at PID 1 (launchd/init), which is never an agent.
func FindAncestor(in Inspector, pid int, maxDepth int, match func(Info) bool) (Info, bool) {
	if in == nil {
		return Info{}, false
	}
	for depth := 0; depth < maxDepth && pid > 1; depth++ {
		info, err := in.Lookup(pid)
		if err != nil {
			return Info{}, false
		}
		if match(info) {
			return info, true
		}
		if info.PPID == pid {
			return Info{}, false
		}
		pid = info.PPID
	}
	return Info{}, false
}

// MatchesTool reports whether a process looks like the named agent tool. It
// matches the process name first ("claude", "codex", "Cursor Helper"), then
// the basename of argv[0], since a native Claude Code install runs an
// executable named after its version (comm "2.1.285", argv[0] "claude").
// For script runtimes such as node or bun it also checks the next arguments,
// which is how an npm-installed CLI shows up.
func MatchesTool(in Inspector, info Info, tool string) bool {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if tool == "" {
		return false
	}
	if nameMatches(info.Name, tool) {
		return true
	}
	args, err := in.Args(info.PID)
	if err != nil || len(args) == 0 {
		return false
	}
	if nameMatches(filepath.Base(args[0]), tool) {
		return true
	}
	switch strings.ToLower(info.Name) {
	case "node", "bun", "deno":
	default:
		return false
	}
	for _, a := range args[1:min(len(args), 3)] {
		if nameMatches(filepath.Base(a), tool) {
			return true
		}
		// npm installs a shim whose target lives under the package name, e.g.
		// .../@anthropic-ai/claude-code/cli.js.
		if strings.Contains(strings.ToLower(a), "/"+tool+"-code/") {
			return true
		}
	}
	return false
}

func nameMatches(name, tool string) bool {
	n := strings.ToLower(name)
	n = strings.TrimSuffix(n, ".exe")
	return n == tool || strings.HasPrefix(n, tool+" ") || strings.HasPrefix(n, tool+"-")
}
