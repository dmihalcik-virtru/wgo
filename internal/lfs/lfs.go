// Package lfs provides optional git-lfs interop for jj workspaces. jj never
// invokes git's clean/smudge filters, so files tracked by git-lfs stay as
// raw pointer text even in a colocated checkout (see internal/jj's
// EnsureColocated). This package fetches the real objects into the
// git-lfs cache and symlinks tracked paths to them, which is safer than a
// raw `git lfs checkout`: jj has no staging area, so hydrating a pointer to
// its full (potentially huge) content in place would get snapshotted
// straight into the current change as ordinary file content. A symlink
// swap still shows up as a change in `jj diff`, but only a tiny
// pointer-to-target diff rather than the full blob.
package lfs

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// MaxPointerSize is the git-lfs spec's hard cap on pointer file size.
const MaxPointerSize = 1024

// pointerHeader is the required first line of every git-lfs pointer file.
const pointerHeader = "version https://git-lfs.github.com/spec/v1"

var (
	oidLinePattern  = regexp.MustCompile(`^oid sha256:([0-9a-f]{64})$`)
	sizeLinePattern = regexp.MustCompile(`^size ([0-9]+)$`)
)

// Pointer is a parsed git-lfs pointer file.
type Pointer struct {
	// OID is "sha256:<64-hex>", exactly as it appears in the pointer.
	OID string
	// Size is the real object's size in bytes.
	Size int64
}

// ParsePointer parses data as a git-lfs pointer file. ok is false if data
// isn't a validly-formed pointer (e.g. it's real hydrated content, or a
// symlink target read as bytes).
func ParsePointer(data []byte) (p Pointer, ok bool) {
	if len(data) == 0 || len(data) > MaxPointerSize {
		return Pointer{}, false
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != pointerHeader {
		return Pointer{}, false
	}

	var oid string
	var size int64
	var sawOID, sawSize bool
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "":
			continue
		case oidLinePattern.MatchString(line):
			oid = "sha256:" + oidLinePattern.FindStringSubmatch(line)[1]
			sawOID = true
		case sizeLinePattern.MatchString(line):
			n, err := strconv.ParseInt(sizeLinePattern.FindStringSubmatch(line)[1], 10, 64)
			if err != nil {
				return Pointer{}, false
			}
			size = n
			sawSize = true
		default:
			// Unknown extension line (e.g. a custom "x-*" key) — ignore for
			// forward compatibility with the pointer spec.
		}
	}
	if scanner.Err() != nil || !sawOID || !sawSize {
		return Pointer{}, false
	}
	return Pointer{OID: oid, Size: size}, true
}

// Available reports whether both `git` and `git-lfs` are on PATH.
func Available() bool {
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	_, err := exec.LookPath("git-lfs")
	return err == nil
}

// Client shells out to git/git-lfs, scoped to one colocated main checkout.
type Client struct {
	// GitBinary is the path or name of the git executable. Defaults to "git".
	GitBinary string
}

// NewClient returns a Client using "git" from PATH.
func NewClient() *Client {
	return &Client{GitBinary: "git"}
}

func (c *Client) binary() string {
	if c.GitBinary == "" {
		return "git"
	}
	return c.GitBinary
}

func (c *Client) run(dir string, args ...string) (string, error) {
	cmd := exec.Command(c.binary(), args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %s: %w",
			strings.Join(args, " "), strings.TrimSpace(stderr.String()), err)
	}
	return stdout.String(), nil
}

// MediaDir returns repo's LFS object cache root by parsing `git lfs env`'s
// LocalMediaDir line (respects custom lfs.storage config rather than
// hardcoding ".git/lfs/objects").
func (c *Client) MediaDir(repo string) (string, error) {
	out, err := c.run(repo, "lfs", "env")
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "LocalMediaDir="); ok {
			if dir := strings.TrimSpace(rest); dir != "" {
				return dir, nil
			}
		}
	}
	return "", fmt.Errorf("git lfs env: LocalMediaDir not found in output")
}

// FetchObjects downloads the LFS objects needed for ref (a git commit SHA
// or branch name) from remote into repo's object cache. Does not touch
// repo's own working tree (`git -C repo lfs fetch <remote> <ref>`).
func (c *Client) FetchObjects(repo, remote, ref string) error {
	_, err := c.run(repo, "lfs", "fetch", remote, ref)
	return err
}

// ObjectPath returns the cache path for oid (the raw hex digest, without a
// "sha256:" prefix) under mediaDir: <mediaDir>/<oid[:2]>/<oid[2:4]>/<oid>.
func ObjectPath(mediaDir, oid string) string {
	return filepath.Join(mediaDir, oid[:2], oid[2:4], oid)
}

// HydrateResult summarizes one HydrateWorkspace run. Paths are relative to
// the workspace root that was scanned.
type HydrateResult struct {
	// Hydrated lists paths swapped from pointer to symlink.
	Hydrated []string
	// Missing lists pointer paths whose object wasn't found in the cache
	// even after fetching.
	Missing []string
}

// HydrateWorkspace walks workspacePath for regular files <= MaxPointerSize
// that parse as LFS pointers, fetches their objects into mainCheckout's
// object cache (best-effort: a fetch failure doesn't abort hydration —
// whatever is already cached still gets symlinked), and replaces each
// resolvable pointer with a relative symlink into the cache. Re-running is
// a no-op for paths already hydrated: a symlink is never treated as a
// pointer candidate.
func (c *Client) HydrateWorkspace(workspacePath, mainCheckout, remote, ref string) (HydrateResult, error) {
	var result HydrateResult

	mediaDir, err := c.MediaDir(mainCheckout)
	if err != nil {
		return result, fmt.Errorf("resolve LFS media dir: %w", err)
	}

	// Best-effort: hydration still proceeds against whatever is already
	// cached even if the fetch itself fails (offline, bad ref, no remote).
	fetchErr := c.FetchObjects(mainCheckout, remote, ref)

	// mediaDir comes back from `git lfs env` fully symlink-resolved (e.g.
	// /private/tmp/... on macOS, where /tmp is itself a symlink). Walk the
	// same resolved form of workspacePath so filepath.Rel computes a
	// symlink target that actually resolves, instead of a path relative to
	// two different spellings of the same directory.
	walkRoot := workspacePath
	if resolved, err := filepath.EvalSymlinks(workspacePath); err == nil {
		walkRoot = resolved
	}

	walkErr := filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".jj" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			// Already a symlink (hydrated) or other non-regular entry.
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > MaxPointerSize {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		ptr, ok := ParsePointer(data)
		if !ok {
			return nil
		}

		rel, relErr := filepath.Rel(walkRoot, path)
		if relErr != nil {
			rel = path
		}

		hex := strings.TrimPrefix(ptr.OID, "sha256:")
		objPath := ObjectPath(mediaDir, hex)
		if _, statErr := os.Stat(objPath); statErr != nil {
			result.Missing = append(result.Missing, rel)
			return nil
		}

		target, relErr := filepath.Rel(filepath.Dir(path), objPath)
		if relErr != nil {
			target = objPath
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		if err := os.Symlink(target, path); err != nil {
			return fmt.Errorf("symlink %s: %w", path, err)
		}
		result.Hydrated = append(result.Hydrated, rel)
		return nil
	})
	if walkErr != nil {
		return result, walkErr
	}
	if fetchErr != nil && len(result.Missing) > 0 {
		return result, fmt.Errorf("fetch objects: %w", fetchErr)
	}
	return result, nil
}

// ScanResult summarizes the LFS-relevant state of a workspace without
// changing anything.
type ScanResult struct {
	// Pointers lists paths still containing raw pointer text.
	Pointers []string
	// Hydrated lists paths that are symlinks into an LFS object cache.
	Hydrated []string
}

// Scan walks workspacePath and classifies every candidate path as a raw
// pointer or an already-hydrated symlink, without modifying anything.
func Scan(workspacePath string) (ScanResult, error) {
	var result ScanResult
	err := filepath.WalkDir(workspacePath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".jj" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(workspacePath, path)
		if relErr != nil {
			rel = path
		}
		if d.Type()&fs.ModeSymlink != 0 {
			lfsObjects := filepath.Join("lfs", "objects")
			if target, err := os.Readlink(path); err == nil && strings.Contains(target, lfsObjects) {
				result.Hydrated = append(result.Hydrated, rel)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > MaxPointerSize {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if _, ok := ParsePointer(data); ok {
			result.Pointers = append(result.Pointers, rel)
		}
		return nil
	})
	return result, err
}
