// Package gotool shells out to the `go` toolchain.
//
// It exists so the rest of wgo can ask the real toolchain questions — what is
// in this build list, what did this binary link against, does this workspace
// build — without every caller re-deriving the environment discipline these
// invocations need.
//
// # Why GOWORK is always explicit
//
// Go locates a workspace by walking up from the working directory until it
// finds a go.work. That is exactly the wrong behaviour here: a rig materialises
// checkouts of repositories that ship their own committed go.work, so running
// `go list` inside one would silently answer for *that repo's* workspace rather
// than the rig's. Worse, an inherited GOWORK from the user's shell would leak
// into every subprocess.
//
// So Client always sets GOWORK explicitly, and a zero-valued Client sets it to
// "off". Choosing a workspace is a deliberate act (Client.WithWork), never an
// accident of the working directory. Callers that genuinely want a repository's
// own workspace should locate it with FindWorkFile, which is bounded to the
// checkout, and pass the resulting absolute path.
//
// Parsing of the toolchain's output lives in internal/gomod, which is pure;
// this package only runs commands.
package gotool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/virtru/wgo/internal/gomod"
)

// WorkOff is the GOWORK value that disables workspace mode entirely.
const WorkOff = "off"

// Available reports whether a `go` binary is on PATH.
func Available() bool {
	_, err := exec.LookPath("go")
	return err == nil
}

// ExitError is returned when a `go` invocation fails. Stderr carries the
// toolchain's full diagnostics; Error() summarises them, so callers that want
// to show a complete build failure should errors.As for this type and print
// Stderr themselves.
type ExitError struct {
	Args   []string
	Dir    string
	Stderr string
	Err    error
}

// maxErrSummary caps how much stderr goes into Error(). Build failures can run
// to hundreds of lines, which is useful output but a terrible error string.
const maxErrSummary = 400

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if len(msg) > maxErrSummary {
		msg = msg[:maxErrSummary] + "… (truncated)"
	}
	if msg == "" {
		return fmt.Sprintf("go %s: %v", strings.Join(e.Args, " "), e.Err)
	}
	return fmt.Sprintf("go %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *ExitError) Unwrap() error { return e.Err }

// Client runs `go` commands with a fixed working directory, workspace and
// environment. It is immutable: the With* methods return copies, so a Client
// scoped to a rig can be cheaply re-pointed at individual checkouts.
type Client struct {
	// GoBinary is the path or name of the go executable. Defaults to "go".
	GoBinary string
	// Dir is the working directory for every invocation.
	Dir string
	// Work is the GOWORK value: an absolute path to a go.work file, or
	// WorkOff. Empty is treated as WorkOff — never as "let Go search".
	Work string
	// Env holds extra KEY=VALUE settings applied after the inherited
	// environment, and after GOWORK/GOFLAGS.
	Env []string
}

// NewClient returns a Client using "go" from PATH, with workspace mode off.
func NewClient() *Client { return &Client{GoBinary: "go", Work: WorkOff} }

// In returns a copy of c rooted at dir.
func (c *Client) In(dir string) *Client {
	out := *c
	out.Dir = resolve(dir)
	return &out
}

// WithWork returns a copy of c using goWork as its GOWORK. Pass WorkOff to
// disable workspace mode.
//
// A relative path is made absolute against c.Dir, falling back to the process
// working directory when c.Dir is unset — Go rejects a relative GOWORK outright
// ("invalid GOWORK: not an absolute path"), so a relative value would fail every
// subsequent call. run enforces the same rule for clients built by other means.
func (c *Client) WithWork(goWork string) *Client {
	out := *c
	if goWork == "" || goWork == WorkOff {
		out.Work = WorkOff
		return &out
	}
	if !filepath.IsAbs(goWork) {
		base := c.Dir
		if base == "" {
			base, _ = os.Getwd()
		}
		goWork = filepath.Join(base, goWork)
	}
	out.Work = resolve(goWork)
	return &out
}

// resolve returns path with symlinks expanded, falling back to expanding just
// the parent when path does not exist yet.
//
// This matters because `go work use` records members as paths relative to the
// go.work file, computed against the process's *resolved* working directory. On
// macOS a temp or checkout path under a symlinked root (/var -> /private/var)
// therefore yields a chain of "../.." escapes: functional, but unreadable and
// impossible to golden-test. Handing Go pre-resolved paths keeps the generated
// go.work clean. internal/lfs resolves for the same reason.
func resolve(path string) string {
	if path == "" {
		return path
	}
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	if r, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return filepath.Join(r, filepath.Base(path))
	}
	return path
}

// WithEnv returns a copy of c with additional KEY=VALUE settings.
func (c *Client) WithEnv(kv ...string) *Client {
	out := *c
	out.Env = append(append([]string(nil), c.Env...), kv...)
	return &out
}

func (c *Client) binary() string {
	if c.GoBinary == "" {
		return "go"
	}
	return c.GoBinary
}

func (c *Client) work() string {
	if c.Work == "" {
		return WorkOff
	}
	return c.Work
}

// workFile returns the go.work path for commands that take it as an argument
// rather than reading it from the environment.
//
// WorkOff is a valid GOWORK value but not a filename: passing it through would
// run `go work edit -fmt off` and report a missing file the caller never named.
func (c *Client) workFile() (string, error) {
	w := c.work()
	if w == WorkOff {
		return "", errors.New("gotool: this command needs a go.work; call Client.WithWork first")
	}
	return w, nil
}

// environ builds the child environment: the parent's, minus the variables this
// package owns, plus our own settings. readOnly pins GOFLAGS to -mod=readonly,
// which every command here sets except WorkSync — it stops a query, and the
// non-destructive `go work edit` forms, from rewriting a pinned checkout's
// go.mod as a side effect. GOFLAGS is stripped from the parent environment
// either way, so a user's setting cannot reintroduce -mod=mod.
func (c *Client) environ(readOnly bool) []string {
	env := stripEnv(os.Environ(), "GOWORK", "GOFLAGS")
	env = append(env, "GOWORK="+c.work())
	if readOnly {
		env = append(env, "GOFLAGS=-mod=readonly")
	}
	return append(env, c.Env...)
}

// stripEnv removes the named variables from a KEY=VALUE environment slice.
func stripEnv(env []string, names ...string) []string {
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	out := env[:0:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			out = append(out, kv)
		}
	}
	return out
}

func (c *Client) run(readOnly bool, args ...string) (string, error) {
	// Enforced here rather than in WithWork because Work is an exported field:
	// this is the one point every invocation crosses. Go rejects a relative
	// GOWORK outright, so catching it here names the offending value instead of
	// letting the toolchain report it without context.
	if w := c.work(); w != WorkOff && !filepath.IsAbs(w) {
		return "", fmt.Errorf("gotool: GOWORK must be an absolute path, got %q", w)
	}
	cmd := exec.Command(c.binary(), args...)
	cmd.Dir = c.Dir
	cmd.Env = c.environ(readOnly)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), &ExitError{
			Args: args, Dir: c.Dir, Stderr: stderr.String(), Err: err,
		}
	}
	return stdout.String(), nil
}

// Version returns the `go version` line, e.g. "go version go1.24.5 darwin/arm64".
func (c *Client) Version() (string, error) {
	out, err := c.run(true, "version")
	return strings.TrimSpace(out), err
}

// EnvVar returns the value of a single `go env` variable. Useful for asserting
// that GOWORK really is what we intended.
func (c *Client) EnvVar(name string) (string, error) {
	out, err := c.run(true, "env", name)
	return strings.TrimSpace(out), err
}

// ListModules returns the full module build list (`go list -m -json all`).
//
// In workspace mode this is the MVS result across every `use`d module, which is
// exactly the set a rig needs to compare against a shipped artifact.
func (c *Client) ListModules() ([]gomod.Module, error) {
	out, err := c.run(true, "list", "-m", "-json", "all")
	if err != nil {
		return nil, err
	}
	return gomod.ParseModuleList(strings.NewReader(out))
}

// ListPackageModules returns the modules that actually contribute packages to
// the transitive imports of patterns, deduplicated and excluding the standard
// library.
//
// This is a strictly smaller set than ListModules: a module can sit in the
// build list without any of its packages being imported, and a version bump
// there cannot change behaviour. Drift detection defaults to this set so it
// reports differences that can actually affect the binary.
func (c *Client) ListPackageModules(patterns ...string) ([]gomod.Module, error) {
	args := append([]string{"list", "-deps", "-json"}, patterns...)
	out, err := c.run(true, args...)
	if err != nil {
		return nil, err
	}
	return ParsePackageModules(strings.NewReader(out))
}

// pkg is the slice of `go list -json` output this package consumes.
type pkg struct {
	ImportPath string        `json:"ImportPath"`
	Standard   bool          `json:"Standard"`
	Module     *gomod.Module `json:"Module"`
}

// ParsePackageModules extracts the distinct modules from a `go list -deps
// -json` stream, preserving first-seen order and skipping standard-library and
// module-less packages.
func ParsePackageModules(r io.Reader) ([]gomod.Module, error) {
	dec := json.NewDecoder(r)
	var out []gomod.Module
	seen := map[string]bool{}
	for {
		var p pkg
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("gotool: decoding package list: %w", err)
		}
		if p.Standard || p.Module == nil {
			continue
		}
		key := p.Module.Path + "@" + p.Module.Version
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, *p.Module)
	}
}

// BuildInfo reads the module metadata embedded in a compiled binary
// (`go version -m`). This is the highest-fidelity pin source available: it
// records the versions actually linked into a shipped artifact.
func (c *Client) BuildInfo(binary string) (*gomod.BuildInfo, error) {
	out, err := c.run(true, "version", "-m", binary)
	if err != nil {
		return nil, err
	}
	bi, err := gomod.ParseBuildInfo([]byte(out))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", binary, err)
	}
	return bi, nil
}

// Build compiles patterns. When outputDir is non-empty the resulting binaries
// are written there rather than into the working directory, which keeps a
// build-check from littering a checkout.
func (c *Client) Build(outputDir string, patterns ...string) error {
	args := []string{"build"}
	if outputDir != "" {
		args = append(args, "-o", outputDir)
	}
	args = append(args, patterns...)
	_, err := c.run(true, args...)
	return err
}

// WorkEditFmt reformats c.Work in place. This is a pure formatting pass: it
// reads and rewrites only the go.work file, never the member modules.
//
// Requires WithWork.
func (c *Client) WorkEditFmt() error {
	work, err := c.workFile()
	if err != nil {
		return err
	}
	_, err = c.run(true, "work", "edit", "-fmt", work)
	return err
}

// WorkEditReplace adds or updates a replace directive in c.Work. oldVersion may
// be empty to replace every version of the module.
//
// This is how drift is frozen: pinning a promoted module's dependency back to
// the version the shipped artifact used.
//
// Requires WithWork.
func (c *Client) WorkEditReplace(oldPath, oldVersion, newPath, newVersion string) error {
	work, err := c.workFile()
	if err != nil {
		return err
	}
	old := oldPath
	if oldVersion != "" {
		old += "@" + oldVersion
	}
	replacement := newPath
	if newVersion != "" {
		replacement += "@" + newVersion
	}
	_, err = c.run(true, "work", "edit", "-replace", old+"="+replacement, work)
	return err
}

// WorkEditDropReplace removes a replace directive from c.Work.
// Requires WithWork.
func (c *Client) WorkEditDropReplace(oldPath, oldVersion string) error {
	work, err := c.workFile()
	if err != nil {
		return err
	}
	old := oldPath
	if oldVersion != "" {
		old += "@" + oldVersion
	}
	_, err = c.run(true, "work", "edit", "-dropreplace", old, work)
	return err
}

// WorkUse adds directories to c.Work's use list. Requires WithWork: it takes
// the target from GOWORK rather than an argument, so without one it would edit
// whichever go.work Go finds by searching upward.
func (c *Client) WorkUse(dirs ...string) error {
	if len(dirs) == 0 {
		return nil
	}
	if _, err := c.workFile(); err != nil {
		return err
	}
	args := append([]string{"work", "use"}, dirs...)
	_, err := c.run(true, args...)
	return err
}

// WorkSync writes the workspace's resolved build list back into every member
// module's go.mod.
//
// DESTRUCTIVE for a rig: every checkout is pinned to a released tag, and this
// dirties all of them at once, replacing the versions the artifact shipped with
// the workspace's MVS result — destroying the very thing the rig reproduces.
// Never call it as part of a normal flow; it must stay behind an explicit
// opt-in flag. Requires WithWork.
func (c *Client) WorkSync() error {
	if _, err := c.workFile(); err != nil {
		return err
	}
	_, err := c.run(false, "work", "sync")
	return err
}

// FindWorkFile returns the nearest go.work at or above startDir, stopping after
// stopDir, or "" if there is none.
//
// The bounded walk is the point: Go's own search continues past the repository
// root and out into the user's home directory, which is how a checkout ends up
// accidentally joined to an unrelated workspace. Both arguments should be
// absolute and stopDir should be an ancestor of startDir; if it is not, the
// walk terminates at the filesystem root.
//
// Only a genuine absence continues the walk. A stat that fails for any other
// reason — a directory the user cannot traverse, most likely — is reported
// rather than read as "this repo ships no go.work", which would silently
// compute the baseline build list against the wrong workspace.
func FindWorkFile(startDir, stopDir string) (string, error) {
	dir := filepath.Clean(startDir)
	stop := filepath.Clean(stopDir)
	for {
		candidate := filepath.Join(dir, "go.work")
		switch st, err := os.Stat(candidate); {
		case err == nil && !st.IsDir():
			return candidate, nil
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return "", fmt.Errorf("gotool: looking for go.work: %w", err)
		}
		if dir == stop {
			return "", nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}
