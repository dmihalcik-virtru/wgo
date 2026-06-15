package jj

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Client is the interface every jj-aware caller in wgo depends on. It is
// intentionally jj-shaped; no concession is made to the prior git
// vocabulary. See the package-level docs in types.go for the data model.
type Client interface {
	// Discovery
	Root(path string) (string, error)
	IsRepo(path string) bool
	RemoteURLs(path string) (map[string]string, error)

	// Workspaces
	ListWorkspaces(repo string) ([]Workspace, error)
	WorkspaceAdd(repo, name, dest, revset string) error
	WorkspaceForget(repo, name string) error
	WorkspaceRoot(path string) (string, error)
	UpdateStale(workspacePath string) error

	// DAG / status
	Log(repo, revset string) ([]LogEntry, error)
	CurrentChange(workspacePath string) (Change, error)
	Resolve(repo, revset string) (string, error)
	Status(workspacePath string) (Status, error)
	IsClean(workspacePath string) (bool, []string, error)

	// Bookmarks
	BookmarkList(repo string, opts BookmarkListOpts) ([]Bookmark, error)
	BookmarkSet(repo, name, revset string, allowBackwards bool) error
	BookmarkCreate(repo, name, revset string) error
	BookmarkDelete(repo, name string) error

	// Mutations
	New(workspacePath, revset, msg string) error
	Describe(workspacePath, msg string) error
	EditChange(workspacePath, revset string) error

	// Git interop
	GitInit(path string, opts InitOpts) error
	GitClone(url, dest string) error
	GitFetch(repo, remote string, refs []string) error
	GitPush(repo string, opts PushOpts) (PushResult, error)
}

// CLIClient shells out to the system `jj` binary. The binary path defaults
// to "jj" (looked up via PATH); override Binary for tests that point at a
// pinned build.
type CLIClient struct {
	// Binary is the path or name of the jj executable. Defaults to "jj".
	Binary string
}

// NewCLI returns a CLIClient using "jj" from PATH.
func NewCLI() *CLIClient {
	return &CLIClient{Binary: "jj"}
}

// compile-time interface satisfaction check.
var _ Client = (*CLIClient)(nil)

// runIn executes jj inside dir and returns stdout. On non-zero exit the
// returned error wraps stderr verbatim plus the joined command.
func (c *CLIClient) runIn(dir string, args ...string) (string, error) {
	binary := c.Binary
	if binary == "" {
		binary = "jj"
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("jj %s: %s: %w",
			strings.Join(args, " "), strings.TrimSpace(stderr.String()), err)
	}
	return stdout.String(), nil
}

// runR runs `jj -R <repo> ...` from an arbitrary working directory, useful
// when the caller does not want jj to snapshot the cwd as a workspace.
func (c *CLIClient) runR(repo string, args ...string) (string, error) {
	full := append([]string{"-R", repo}, args...)
	return c.runIn("", full...)
}

// Root returns the workspace root for path (the directory containing .jj/).
func (c *CLIClient) Root(path string) (string, error) {
	out, err := c.runIn(path, "root")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// IsRepo reports whether path is inside a jj repo by checking for the .jj/
// directory anywhere on the way up.
func (c *CLIClient) IsRepo(path string) bool {
	cur, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for {
		info, err := os.Stat(filepath.Join(cur, ".jj"))
		if err == nil && info.IsDir() {
			return true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return false
		}
		cur = parent
	}
}

// RemoteURLs returns a map of remote name -> URL by parsing
// `jj git remote list`. Output format is "<name> <url>" per line.
func (c *CLIClient) RemoteURLs(path string) (map[string]string, error) {
	out, err := c.runIn(path, "git", "remote", "list")
	if err != nil {
		return nil, err
	}
	remotes := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		remotes[parts[0]] = parts[1]
	}
	return remotes, nil
}

// ListWorkspaces enumerates the workspaces attached to repo.
func (c *CLIClient) ListWorkspaces(repo string) ([]Workspace, error) {
	out, err := c.runR(repo, "workspace", "list", "-T", WorkspaceListTemplate)
	if err != nil {
		return nil, err
	}
	return ParseWorkspaces([]byte(out))
}

// WorkspaceAdd adds a workspace under dest. name is passed via --name when
// non-empty; otherwise jj defaults to the basename of dest. revset, when
// non-empty, becomes the new workspace's parent (-r).
func (c *CLIClient) WorkspaceAdd(repo, name, dest, revset string) error {
	args := []string{"workspace", "add"}
	if name != "" {
		args = append(args, "--name", name)
	}
	if revset != "" {
		args = append(args, "-r", revset)
	}
	args = append(args, dest)
	_, err := c.runR(repo, args...)
	return err
}

// WorkspaceForget stops tracking the named workspace's working-copy commit.
// The on-disk directory is not removed.
func (c *CLIClient) WorkspaceForget(repo, name string) error {
	_, err := c.runR(repo, "workspace", "forget", name)
	return err
}

// WorkspaceRoot returns the root directory of the workspace containing
// path. Equivalent to `jj workspace root` invoked from path.
func (c *CLIClient) WorkspaceRoot(path string) (string, error) {
	out, err := c.runIn(path, "workspace", "root")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// UpdateStale brings a stale workspace forward to the current operation
// log head (`jj workspace update-stale`).
func (c *CLIClient) UpdateStale(workspacePath string) error {
	_, err := c.runIn(workspacePath, "workspace", "update-stale")
	return err
}

// Log returns parsed LogEntry records for revset, ordered by jj's default
// traversal (children before parents). When revset is empty, jj's default
// revset (`revsets.log`) is used.
func (c *CLIClient) Log(repo, revset string) ([]LogEntry, error) {
	args := []string{"log", "--no-graph", "-T", LogEntryTemplate}
	if revset != "" {
		args = append(args, "-r", revset)
	}
	out, err := c.runR(repo, args...)
	if err != nil {
		return nil, err
	}
	return ParseLogEntries([]byte(out))
}

// CurrentChange returns the workspace's `@` change.
func (c *CLIClient) CurrentChange(workspacePath string) (Change, error) {
	out, err := c.runIn(workspacePath, "log", "--no-graph", "-r", "@", "-T", LogEntryTemplate)
	if err != nil {
		return Change{}, err
	}
	entries, err := ParseLogEntries([]byte(out))
	if err != nil {
		return Change{}, err
	}
	if len(entries) == 0 {
		return Change{}, fmt.Errorf("jj log @: no entries returned")
	}
	return entries[0], nil
}

// Resolve resolves revset to a single commit id. Errors if revset is empty
// or resolves to multiple commits.
func (c *CLIClient) Resolve(repo, revset string) (string, error) {
	if revset == "" {
		return "", fmt.Errorf("jj resolve: empty revset")
	}
	out, err := c.runR(repo, "log", "--no-graph", "-r", revset, "-T", `commit_id.short(40) ++ "\n"`, "-n", "1")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", fmt.Errorf("jj resolve %q: no matching commit", revset)
	}
	return id, nil
}

// Status returns a parsed snapshot of the workspace state. The current
// change is fetched in the same invocation chain so callers don't have to
// run two commands.
func (c *CLIClient) Status(workspacePath string) (Status, error) {
	out, err := c.runIn(workspacePath, "status")
	if err != nil {
		return Status{}, err
	}
	st := parseStatus(out)
	cur, err := c.CurrentChange(workspacePath)
	if err == nil {
		st.CurrentChange = cur
	}
	return st, nil
}

// IsClean reports whether the working copy matches its parent commit. When
// dirty, the second return is the list of porcelain-style entries so the
// caller can display them.
func (c *CLIClient) IsClean(workspacePath string) (bool, []string, error) {
	out, err := c.runIn(workspacePath, "status")
	if err != nil {
		return false, nil, err
	}
	st := parseStatus(out)
	if st.Clean {
		return true, nil, nil
	}
	var entries []string
	for _, f := range st.Added {
		entries = append(entries, "A "+f)
	}
	for _, f := range st.Modified {
		entries = append(entries, "M "+f)
	}
	for _, f := range st.Deleted {
		entries = append(entries, "D "+f)
	}
	return false, entries, nil
}

// parseStatus translates `jj status` plain-text output into a Status.
// jj status surfaces a single block of "Working copy changes:" lines of
// the form "<flag> <path>" where flag is one of A/M/D. A "The working copy
// has no changes." string indicates a clean copy.
func parseStatus(out string) Status {
	st := Status{Clean: true}
	inChanges := false
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case strings.HasPrefix(line, "Working copy changes:"):
			inChanges = true
			st.Clean = false
			continue
		case strings.HasPrefix(line, "Working copy "), strings.HasPrefix(line, "Parent commit "):
			inChanges = false
			continue
		case strings.HasPrefix(line, "The working copy has no changes."):
			st.Clean = true
			inChanges = false
			continue
		}
		if !inChanges {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if len(trimmed) < 2 || trimmed[1] != ' ' {
			continue
		}
		flag := trimmed[0]
		path := strings.TrimSpace(trimmed[2:])
		switch flag {
		case 'A':
			st.Added = append(st.Added, path)
		case 'M':
			st.Modified = append(st.Modified, path)
		case 'D':
			st.Deleted = append(st.Deleted, path)
		}
	}
	return st
}

// BookmarkList lists bookmarks matching the filters in opts.
func (c *CLIClient) BookmarkList(repo string, opts BookmarkListOpts) ([]Bookmark, error) {
	args := []string{"bookmark", "list", "-T", BookmarkListTemplate}
	if opts.AllRemotes {
		args = append(args, "--all-remotes")
	}
	if opts.Tracked {
		args = append(args, "--tracked")
	}
	if opts.Conflicted {
		args = append(args, "--conflicted")
	}
	if opts.Remote != "" {
		args = append(args, "--remote", opts.Remote)
	}
	for _, n := range opts.Names {
		args = append(args, n)
	}
	out, err := c.runR(repo, args...)
	if err != nil {
		return nil, err
	}
	all, err := ParseBookmarks([]byte(out))
	if err != nil {
		return nil, err
	}
	if !opts.Local {
		return all, nil
	}
	filtered := all[:0]
	for _, b := range all {
		if b.Remote == "" {
			filtered = append(filtered, b)
		}
	}
	return filtered, nil
}

// BookmarkSet creates or moves a bookmark to revset. allowBackwards
// permits moving the bookmark to an ancestor or sibling of its current
// target.
func (c *CLIClient) BookmarkSet(repo, name, revset string, allowBackwards bool) error {
	args := []string{"bookmark", "set"}
	if allowBackwards {
		args = append(args, "--allow-backwards")
	}
	if revset != "" {
		args = append(args, "-r", revset)
	}
	args = append(args, name)
	_, err := c.runR(repo, args...)
	return err
}

// BookmarkCreate creates a bookmark at revset. Errors if it already exists
// (use BookmarkSet to move an existing bookmark).
func (c *CLIClient) BookmarkCreate(repo, name, revset string) error {
	args := []string{"bookmark", "create"}
	if revset != "" {
		args = append(args, "-r", revset)
	}
	args = append(args, name)
	_, err := c.runR(repo, args...)
	return err
}

// BookmarkDelete deletes a bookmark and queues the deletion to be pushed
// to any tracked remote on the next `jj git push`.
func (c *CLIClient) BookmarkDelete(repo, name string) error {
	_, err := c.runR(repo, "bookmark", "delete", name)
	return err
}

// New creates a new change. revset, when non-empty, becomes the new
// change's parent (default @). msg, when non-empty, populates the
// description without invoking an editor.
func (c *CLIClient) New(workspacePath, revset, msg string) error {
	args := []string{"new"}
	if msg != "" {
		args = append(args, "-m", msg)
	}
	if revset != "" {
		args = append(args, revset)
	}
	_, err := c.runIn(workspacePath, args...)
	return err
}

// Describe sets the description on the workspace's current change without
// invoking an editor.
func (c *CLIClient) Describe(workspacePath, msg string) error {
	_, err := c.runIn(workspacePath, "describe", "-m", msg)
	return err
}

// EditChange moves the workspace's @ to revset (`jj edit`). Use sparingly;
// jj's docs steer callers towards `jj new`.
func (c *CLIClient) EditChange(workspacePath, revset string) error {
	if revset == "" {
		return fmt.Errorf("jj edit: empty revset")
	}
	_, err := c.runIn(workspacePath, "edit", revset)
	return err
}

// GitInit runs `jj git init --no-colocate` at path. When opts.GitRepo is
// set, --git-repo is forwarded (which itself disables colocation). The
// resulting repo is strictly pure jj.
func (c *CLIClient) GitInit(path string, opts InitOpts) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("jj git init: mkdir %s: %w", path, err)
	}
	args := []string{"git", "init"}
	if opts.GitRepo != "" {
		args = append(args, "--git-repo", opts.GitRepo)
	} else {
		args = append(args, "--no-colocate")
	}
	_, err := c.runIn(path, args...)
	if err != nil {
		return err
	}
	if opts.RemoteName != "" && opts.RemoteName != "origin" && opts.GitRepo != "" {
		// jj does not expose a remote rename during init; if a custom
		// remote name was requested, rename after the fact.
		_, err = c.runIn(path, "git", "remote", "rename", "origin", opts.RemoteName)
		if err != nil {
			return err
		}
	}
	return nil
}

// GitClone clones url into dest using `jj git clone --no-colocate`. The
// destination directory is created if missing.
func (c *CLIClient) GitClone(url, dest string) error {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("jj git clone: mkdir parent %s: %w", parent, err)
	}
	_, err := c.runIn(parent, "git", "clone", "--no-colocate", url, dest)
	return err
}

// GitFetch runs `jj git fetch [--remote R] [--branch X...]`.
func (c *CLIClient) GitFetch(repo, remote string, refs []string) error {
	args := []string{"git", "fetch"}
	if remote != "" {
		args = append(args, "--remote", remote)
	}
	for _, r := range refs {
		args = append(args, "--branch", r)
	}
	_, err := c.runR(repo, args...)
	return err
}

// GitPush pushes per opts. Recognised stderr patterns are translated into
// the package-level typed errors ErrLeaseFailed and ErrNothingToPush so
// callers can distinguish lease conflicts from no-op pushes without
// string-matching themselves.
//
// jj 0.42 implicitly accepts new bookmarks when they are named via
// --bookmark; opts.AllowNew is therefore retained on the struct (for API
// stability across jj versions) but not translated to a CLI flag here.
func (c *CLIClient) GitPush(repo string, opts PushOpts) (PushResult, error) {
	args := []string{"git", "push"}
	if opts.Remote != "" {
		args = append(args, "--remote", opts.Remote)
	}
	for _, b := range opts.Bookmarks {
		args = append(args, "--bookmark", b)
	}
	if len(opts.Bookmarks) == 0 {
		args = append(args, "--tracked")
	}
	if opts.AllowEmptyDescription {
		args = append(args, "--allow-empty-description")
	}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}

	binary := c.Binary
	if binary == "" {
		binary = "jj"
	}
	full := append([]string{"-R", repo}, args...)
	cmd := exec.Command(binary, full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	combined := stderr.String() + stdout.String()
	if err != nil {
		if isLeaseFailure(combined) {
			return PushResult{}, fmt.Errorf("%w: %s", ErrLeaseFailed, strings.TrimSpace(combined))
		}
		if isNothingToPush(combined) {
			return PushResult{}, ErrNothingToPush
		}
		return PushResult{}, fmt.Errorf("jj %s: %s: %w",
			strings.Join(full, " "), strings.TrimSpace(combined), err)
	}
	// jj 0.42 exits 0 even when there is nothing to push. Treat the
	// "Nothing changed." / "No bookmarks to push." messages as
	// ErrNothingToPush so callers don't have to special-case a successful
	// no-op.
	if isNothingToPush(combined) {
		return PushResult{}, ErrNothingToPush
	}
	return PushResult{Pushed: append([]string(nil), opts.Bookmarks...)}, nil
}

// isLeaseFailure matches jj's safety-check refusal text. jj 0.42 prints
// "Refusing to push a bookmark that unexpectedly moved on the remote." or
// the older "remote bookmark moved unexpectedly" depending on version.
func isLeaseFailure(s string) bool {
	low := strings.ToLower(s)
	return strings.Contains(low, "remote bookmark moved unexpectedly") ||
		strings.Contains(low, "unexpectedly moved on the remote") ||
		strings.Contains(low, "refusing to push")
}

// isNothingToPush matches jj's no-op-push messages.
func isNothingToPush(s string) bool {
	low := strings.ToLower(s)
	return strings.Contains(low, "no bookmarks to push") ||
		strings.Contains(low, "nothing changed")
}
