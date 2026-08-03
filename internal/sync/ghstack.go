package sync

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/virtru/wgo/internal/jj"
)

// minGHStackVersion is the minimum gh CLI version that ships the native Stack
// object and the `gh stack link` subcommand.
var minGHStackVersion = [2]int{2, 90}

// Linker publishes an ordered stack of branches to GitHub's native Stack. The
// production implementation shells out to `gh stack link`, which creates or
// updates a stack on GitHub without any local tracking (no .git/gh-stack) and
// without running git rebases — so it never fights jj's auto-restacking.
type Linker interface {
	// Available reports whether native linking can run for repoPath: gh on
	// PATH, the github/gh-stack extension installed, gh >= 2.90, and repoPath a
	// colocated (git-backed) jj repo so gh can read its refs.
	Available(repoPath string) bool
	// Link creates or updates the native Stack from orderedBranches (bottom→top).
	Link(repoPath string, orderedBranches []string) error
}

// cliLinker is the production Linker. The exec seams are injectable for tests.
type cliLinker struct {
	lookPath    func(string) (string, error)
	run         func(dir, name string, args ...string) (string, error)
	isColocated func(repoPath string) bool

	toolOnce  sync.Once
	toolAvail bool // gh present + gh-stack installed + version >= min (process-global)
}

// NewCLILinker returns a Linker backed by the real `gh` CLI and jj client.
func NewCLILinker() Linker {
	jjc := jj.NewCLI()
	return &cliLinker{
		lookPath: exec.LookPath,
		run: func(dir, name string, args ...string) (string, error) {
			cmd := exec.Command(name, args...)
			cmd.Dir = dir
			out, err := cmd.Output()
			return string(out), err
		},
		isColocated: jjc.IsColocated,
	}
}

func (l *cliLinker) Available(repoPath string) bool {
	if !l.toolAvailable() {
		return false
	}
	return l.isColocated(repoPath)
}

// toolAvailable memoizes the process-global gh checks (presence, extension,
// version); these don't vary per repo.
func (l *cliLinker) toolAvailable() bool {
	l.toolOnce.Do(func() {
		if _, err := l.lookPath("gh"); err != nil {
			return
		}
		if !l.ghStackExtensionInstalled() {
			return
		}
		if !l.ghVersionAtLeast(minGHStackVersion) {
			return
		}
		l.toolAvail = true
	})
	return l.toolAvail
}

func (l *cliLinker) ghStackExtensionInstalled() bool {
	out, err := l.run("", "gh", "extension", "list")
	if err != nil {
		return false
	}
	return strings.Contains(out, "github/gh-stack")
}

func (l *cliLinker) ghVersionAtLeast(min [2]int) bool {
	out, err := l.run("", "gh", "--version")
	if err != nil {
		return false
	}
	maj, minr, ok := parseGHVersion(out)
	if !ok {
		return false
	}
	if maj != min[0] {
		return maj > min[0]
	}
	return minr >= min[1]
}

// parseGHVersion extracts the major and minor version from `gh --version`
// output, whose first line looks like "gh version 2.90.1 (2024-...)".
func parseGHVersion(out string) (major, minor int, ok bool) {
	fields := strings.Fields(out)
	for i, f := range fields {
		if f == "version" && i+1 < len(fields) {
			parts := strings.SplitN(fields[i+1], ".", 3)
			if len(parts) < 2 {
				return 0, 0, false
			}
			maj, err1 := strconv.Atoi(parts[0])
			minr, err2 := strconv.Atoi(parts[1])
			if err1 != nil || err2 != nil {
				return 0, 0, false
			}
			return maj, minr, true
		}
	}
	return 0, 0, false
}

func (l *cliLinker) Link(repoPath string, orderedBranches []string) error {
	if len(orderedBranches) < 2 {
		return fmt.Errorf("gh stack link needs at least 2 branches, got %d", len(orderedBranches))
	}
	args := append([]string{"stack", "link"}, orderedBranches...)
	if _, err := l.run(repoPath, "gh", args...); err != nil {
		return fmt.Errorf("gh stack link: %w", err)
	}
	return nil
}
