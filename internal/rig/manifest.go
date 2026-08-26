package rig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ErrNoManifest is returned when a directory holds no rig.toml.
var ErrNoManifest = errors.New("rig: no rig.toml in directory")

// Source records where a rig's pins came from, so `wgo rig sync` can re-resolve
// the same input and `wgo rig show` can explain the rig without re-deriving it.
type Source struct {
	// Kind is "repo", "binary" or "manual".
	Kind string `toml:"kind"`
	// Ref is the "<owner>/<repo>@<version>" a repo-sourced rig was built from.
	Ref string `toml:"ref,omitempty"`
	// Binary is the absolute path a binary-sourced rig read build info from.
	// It is recorded for provenance; the file may well be gone by the time the
	// rig is next synced, which is why Baseline is stored rather than re-read.
	Binary string `toml:"binary,omitempty"`
	// Modules are the explicit `-m path@version` pins.
	Modules []string `toml:"modules,omitempty"`
	// OrgPrefixes is the in-org filter that was in force. Stored because the
	// config default can change underneath an existing rig, and a sync that
	// silently widened or narrowed the checkout set would be surprising.
	OrgPrefixes []string `toml:"org_prefixes,omitempty"`
}

// Checkout is one materialised jj workspace: a repository at one commit, with
// the sparse set needed by every module served from it.
//
// Several modules can share a Checkout. Modules released from one commit of a
// monorepo — common when a release tags a whole tree — would otherwise get
// byte-identical working copies.
type Checkout struct {
	// Dir is the checkout's directory name under <rig>/src. Persisted rather
	// than recomputed: it carries a collision suffix that depends on what else
	// existed when the rig was created.
	Dir string `toml:"dir"`
	// Workspace is the jj workspace name registered in the main clone.
	Workspace string `toml:"workspace"`
	// Repo is "owner/repo".
	Repo string `toml:"repo"`
	// MainClone is the absolute path of the clone whose .jj/repo backs this
	// workspace.
	MainClone string `toml:"main_clone"`
	// Revset is what the pin was resolved through: a tag revset or a
	// pseudo-version's commit.
	Revset string `toml:"revset"`
	// Commit is the resolved commit id. This is the pin: `wgo doctor` compares
	// the workspace's parent against it to detect a checkout that has drifted.
	Commit string `toml:"commit"`
	// Tag is the anchor tag the directory name was derived from, empty for a
	// pseudo-version pin.
	Tag string `toml:"tag,omitempty"`
	// Sparse is the set of repo-relative directories materialised, empty for a
	// full checkout.
	Sparse []string `toml:"sparse,omitempty"`
	// Full records that this checkout is deliberately not sparse, so `show` can
	// distinguish "full" from "sparse set not computed yet".
	Full bool `toml:"full,omitempty"`
}

// Member is one module the rig promotes into the go.work.
type Member struct {
	// Path is the Go module path.
	Path string `toml:"path"`
	// Version is the version the artifact pinned.
	Version string `toml:"version"`
	// Checkout is the Checkout.Dir serving this module.
	Checkout string `toml:"checkout"`
	// Subdir is the module's directory within the repository, "" at the root.
	Subdir string `toml:"subdir,omitempty"`
	// Indirect mirrors the build list: recorded so go.work can annotate it and
	// a reader can tell which members the primary imports directly.
	Indirect bool `toml:"indirect,omitempty"`
}

// UseDir is the go.work `use` path for the member, relative to the rig root.
func (m Member) UseDir() string {
	p := "./" + filepath.ToSlash(filepath.Join(SrcDir, m.Checkout, m.Subdir))
	return strings.TrimSuffix(p, "/")
}

// Skip records an in-org module that got no checkout, and why.
//
// Skips are warnings rather than failures: aborting a nine-checkout rig because
// one module lives in a repository we cannot reach would be hostile, and the
// rest of the rig is still useful for debugging.
type Skip struct {
	Path    string `toml:"path"`
	Version string `toml:"version"`
	Reason  string `toml:"reason"`
}

// Manifest is a rig's entire persistent state.
type Manifest struct {
	// Name is the rig's directory name under rig.dir.
	Name string `toml:"name"`
	// Created is an RFC3339 timestamp. A string rather than a time.Time so a
	// hand-edited manifest with a sloppy timestamp still loads.
	Created string `toml:"created"`
	// WgoVersion is the wgo build that wrote the manifest.
	WgoVersion string `toml:"wgo_version,omitempty"`
	// GoVersion is the `go` directive written into go.work.
	GoVersion string `toml:"go_version,omitempty"`
	// Sparse records whether the rig was created with sparse checkouts, so a
	// sync does not silently switch modes when the config default changes.
	Sparse bool `toml:"sparse"`

	Source Source `toml:"source"`

	// Primary is the module path of the artifact being reproduced.
	Primary string `toml:"primary"`
	// PrimaryUse mirrors the primary repo's own go.work `use` list, as
	// repo-relative directories. A repo that ships a go.work (as
	// data-security-platform does) builds against a different build list than
	// its root module alone, so the rig must reproduce that list to match.
	PrimaryUse []string `toml:"primary_use,omitempty"`

	Checkouts []Checkout `toml:"checkout"`
	Members   []Member   `toml:"member"`
	Skipped   []Skip     `toml:"skipped,omitempty"`

	// Baseline is the dependency set the artifact shipped with, keyed by module
	// path. Every `use` promotes a module to an MVS root, so third-party
	// versions in the rig can only move up; this is what `wgo rig verify`
	// compares against. Stored rather than recomputed because the source (a
	// binary, or a tag whose module cache entry may be evicted) need not still
	// be available.
	Baseline map[string]string `toml:"baseline,omitempty"`
	// Frozen are the modules pinned back down to their baseline via a go.work
	// replace.
	Frozen []string `toml:"frozen,omitempty"`
}

// SrcDir is the subdirectory of a rig holding the checkouts.
const SrcDir = "src"

// GoWorkName is the generated workspace file.
const GoWorkName = "go.work"

// CheckoutByDir returns the checkout with the given directory name.
func (m *Manifest) CheckoutByDir(dir string) (Checkout, bool) {
	for _, c := range m.Checkouts {
		if c.Dir == dir {
			return c, true
		}
	}
	return Checkout{}, false
}

// Root returns the rig's directory given the configured rig.dir.
func (m *Manifest) Root(rigDir string) string { return filepath.Join(rigDir, m.Name) }

// Validate reports structural problems that would make the manifest unusable.
// A rig.toml is meant to be readable and hand-editable, so this favours a
// specific complaint over a parse error.
func (m *Manifest) Validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("rig: manifest has no name")
	}
	dirs := map[string]bool{}
	for i, c := range m.Checkouts {
		switch {
		case c.Dir == "":
			return fmt.Errorf("rig: checkout %d has no dir", i)
		case c.Workspace == "":
			return fmt.Errorf("rig: checkout %q has no workspace name", c.Dir)
		case c.MainClone == "":
			return fmt.Errorf("rig: checkout %q has no main clone", c.Dir)
		case dirs[c.Dir]:
			return fmt.Errorf("rig: duplicate checkout dir %q", c.Dir)
		}
		dirs[c.Dir] = true
	}
	for _, mem := range m.Members {
		if mem.Path == "" {
			return errors.New("rig: member has no module path")
		}
		if !dirs[mem.Checkout] {
			return fmt.Errorf("rig: member %q references unknown checkout %q", mem.Path, mem.Checkout)
		}
	}
	return nil
}

// ManifestPath returns the manifest's location within a rig root.
func ManifestPath(rigRoot string) string { return filepath.Join(rigRoot, ManifestName) }

// Load reads the manifest from a rig root.
func Load(rigRoot string) (*Manifest, error) {
	data, err := os.ReadFile(ManifestPath(rigRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNoManifest, rigRoot)
		}
		return nil, fmt.Errorf("rig: reading manifest: %w", err)
	}
	var m Manifest
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("rig: parsing %s: %w", ManifestPath(rigRoot), err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Save writes the manifest to a rig root, creating the directory if needed.
//
// The write is atomic: a partially written rig.toml would leave `wgo rig rm`
// unable to find the workspaces it needs to forget, stranding them in the main
// clone with no record of what created them.
func Save(rigRoot string, m *Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(rigRoot, 0o755); err != nil {
		return fmt.Errorf("rig: creating %s: %w", rigRoot, err)
	}
	data, err := toml.Marshal(m)
	if err != nil {
		return fmt.Errorf("rig: encoding manifest: %w", err)
	}
	header := "# Generated by `wgo rig` — records what this rig pins and which jj\n" +
		"# workspaces back it. `wgo rig rm` needs it to forget those workspaces.\n\n"

	tmp, err := os.CreateTemp(rigRoot, ".rig.toml.*")
	if err != nil {
		return fmt.Errorf("rig: creating temp manifest: %w", err)
	}
	// Removing the temp file is best-effort: on the success path the rename has
	// already consumed it, and on the failure path the write error is the one
	// worth reporting.
	defer func() { _ = os.Remove(tmp.Name()) }()

	if err := writeAll(tmp, header, string(data)); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("rig: writing manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("rig: closing manifest: %w", err)
	}
	if err := os.Rename(tmp.Name(), ManifestPath(rigRoot)); err != nil {
		return fmt.Errorf("rig: installing manifest: %w", err)
	}
	return nil
}

func writeAll(f *os.File, parts ...string) error {
	for _, p := range parts {
		if _, err := f.WriteString(p); err != nil {
			return err
		}
	}
	return nil
}

// List returns the manifests of every rig under rigDir, sorted by name.
//
// Directories without a readable rig.toml are skipped rather than reported:
// rig.dir is a directory the user owns and may hold anything.
func List(rigDir string) ([]*Manifest, error) {
	entries, err := os.ReadDir(rigDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("rig: reading %s: %w", rigDir, err)
	}
	var out []*Manifest
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := Load(filepath.Join(rigDir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
