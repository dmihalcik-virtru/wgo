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
	// Unfiltered are the module paths checked out despite falling outside
	// OrgPrefixes. `wgo rig add` deliberately ignores the filter — naming the
	// module is the request — so without a record of that decision the next
	// `wgo rig sync` would re-apply the filter, plan the module away, and report
	// the checkout the user just asked for as obsolete.
	//
	// A subset of Modules, by path. `wgo rig new -m` does not write it: there the
	// extras join a build list that is filtered wholesale, and the filter is the
	// point.
	Unfiltered []string `toml:"unfiltered,omitempty"`
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
	// Obsolete marks a checkout the current plan no longer wants but which is
	// still on disk, because `wgo rig sync` deletes nothing without --prune.
	//
	// It stays in the manifest precisely because it is still real. The manifest
	// is the only record of which jj workspaces belong to this rig — `wgo rig rm`
	// reads it to forget them — so dropping the entry while leaving the workspace
	// registered in the main clone would strand it permanently: invisible from
	// the rig, and unattributable from the repo. It contributes no `use` line, so
	// nothing builds against it; it is a tombstone that --prune can act on.
	Obsolete bool `toml:"obsolete,omitempty"`
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

// SkipKind classifies why a module got no checkout. Only SkipUnreachable and
// SkipEscapedReplace represent something going wrong; the rest are expected
// outcomes worth reporting in `wgo rig show`.
type SkipKind string

const (
	// SkipOutOfOrg is a module left to the module cache by the in-org filter.
	SkipOutOfOrg SkipKind = "out-of-org"
	// SkipUnsupportedHost is a module whose path does not map to a repository
	// we know how to check out (vanity imports, gopkg.in, golang.org/x).
	SkipUnsupportedHost SkipKind = "unsupported-host"
	// SkipLocalReplace is a module already served from another checkout by a
	// local replace directive, so it needs no checkout of its own.
	SkipLocalReplace SkipKind = "local-replace"
	// SkipUnreachable is an in-org module whose repository could not be located
	// or cloned — private, deleted, or moved.
	SkipUnreachable SkipKind = "unreachable"
	// SkipEscapedReplace is a local replace pointing outside its repository,
	// which no single checkout can satisfy.
	SkipEscapedReplace SkipKind = "escaped-replace"
	// SkipUnpinned is a module whose recorded version names no release —
	// "(devel)" for one supplied by a go.work or built from a working tree — so
	// there is no commit to pin a checkout to.
	SkipUnpinned SkipKind = "unpinned"
)

// Skip records an in-org module that got no checkout, and why.
//
// Skips are warnings rather than failures: aborting a nine-checkout rig because
// one module lives in a repository we cannot reach would be hostile, and the
// rest of the rig is still useful for debugging.
//
// Kind and Detail are separate fields rather than one prose reason because
// callers switch on the kind. Packing both into a string meant re-splitting it
// on ":" to recover the kind, which any detail containing a colon — a URL, a
// wrapped error — would have silently truncated.
type Skip struct {
	Path    string   `toml:"path"`
	Version string   `toml:"version"`
	Kind    SkipKind `toml:"kind"`
	Detail  string   `toml:"detail,omitempty"`
}

// String renders a skip for display.
func (s Skip) String() string {
	if s.Detail == "" {
		return string(s.Kind)
	}
	return string(s.Kind) + ": " + s.Detail
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
	//
	// A value is normally a version. Where a replace redirected the module to a
	// different module path — a fork — it is "path@version" instead, naming the
	// module the version belongs to: comparing a fork's version against the
	// upstream's would order two unrelated release series, and a freeze has to
	// restore the fork rather than pin the upstream to the fork's version. See
	// BaselineEntry.
	Baseline map[string]string `toml:"baseline,omitempty"`
	// Frozen are the modules pinned back down to their baseline via a go.work
	// replace.
	Frozen []string `toml:"frozen,omitempty"`
}

// SrcDir is the subdirectory of a rig holding the checkouts.
const SrcDir = "src"

// GoWorkName is the generated workspace file.
const GoWorkName = "go.work"

// CheckoutByDir returns the checkout with the given directory name, or nil.
//
// The pointer aliases the manifest's own slice element so callers that widen a
// sparse set (see WidenSparse) mutate the manifest rather than a copy that is
// then dropped on the floor.
func (m *Manifest) CheckoutByDir(dir string) *Checkout {
	for i := range m.Checkouts {
		if m.Checkouts[i].Dir == dir {
			return &m.Checkouts[i]
		}
	}
	return nil
}

// Root returns the rig's directory given the configured rig.dir.
func (m *Manifest) Root(rigDir string) string { return filepath.Join(rigDir, m.Name) }

// LiveCheckouts are the checkouts the rig actually builds against.
//
// Anything counting or listing "the rig's checkouts" for a human wants this.
// An obsolete entry is a tombstone for a workspace still registered in a main
// clone; it is in the manifest so `wgo rig rm` can forget it, not because the
// rig has grown. Teardown — Remove, prune — wants the full slice instead, since
// a tombstone is exactly what it is there to clean up.
func (m *Manifest) LiveCheckouts() []Checkout {
	out := make([]Checkout, 0, len(m.Checkouts))
	for _, c := range m.Checkouts {
		if !c.Obsolete {
			out = append(out, c)
		}
	}
	return out
}

// PackagePatterns returns the `go` command patterns covering every package the
// rig's own modules contribute, for use from the rig root.
//
// Not "./...". In workspace mode a directory-prefix pattern has to name a
// directory inside a module the go.work lists, and the rig root is not one —
// the checkouts live under src/ — so `go list ./...` there fails with
// "directory prefix . does not contain modules listed in go.work". "all" does
// work, but it means the entire module graph and pulls its whole transitive
// closure over the network to answer a question about the rig's own code.
func (m *Manifest) PackagePatterns() []string {
	seen := map[string]bool{}
	var out []string
	for _, mem := range m.Members {
		p := mem.UseDir() + "/..."
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// MaxNameLen bounds a rig's name so that workspaceName's commit discriminator
// survives sanitisation.
//
// gh.SanitizeBranch truncates at 60 characters. WorkspacePrefix ("rig-") plus a
// separator plus an 8-character short commit costs 13, so a name longer than
// this could push the discriminator past the cut and make two checkouts of the
// same rig collide on one workspace name.
const MaxNameLen = 60 - len(WorkspacePrefix) - 1 - 8

// ValidateName rejects a rig name that cannot be used as a directory or would
// break the workspace naming scheme.
//
// Checked before anything is created rather than only at Save time: by then the
// jj workspaces already exist, under names derived from the name being
// rejected.
//
// The name is used verbatim as a directory under rig.dir, so a separator or a
// relative-path element would silently put the rig somewhere else.
func ValidateName(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return errors.New("rig: name is required")
	case len(name) > MaxNameLen:
		return fmt.Errorf("rig: name %q is %d characters, limit is %d\n"+
			"the limit exists so the commit discriminator in workspace names survives truncation",
			name, len(name), MaxNameLen)
	case name != filepath.Base(name), name == "." || name == "..":
		return fmt.Errorf("rig: name %q must be a single directory name, not a path", name)
	case strings.HasPrefix(name, "."):
		return fmt.Errorf("rig: name %q must not start with a dot", name)
	}
	return nil
}

// Validate reports structural problems that would make the manifest unusable.
// A rig.toml is meant to be readable and hand-editable, so this favours a
// specific complaint over a parse error.
//
// This is the one chokepoint every manifest crosses — Plan, Load and Save all
// call it — so the uniqueness invariants live here rather than being re-checked
// at each construction site.
func (m *Manifest) Validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("rig: manifest has no name")
	}
	if len(m.Name) > MaxNameLen {
		return fmt.Errorf("rig: name %q is %d characters, limit is %d",
			m.Name, len(m.Name), MaxNameLen)
	}
	var (
		dirs       = map[string]bool{}
		workspaces = map[string]bool{}
		pins       = map[string]bool{}
	)
	for i, c := range m.Checkouts {
		pin := c.Repo + "@" + c.Commit
		switch {
		case c.Dir == "":
			return fmt.Errorf("rig: checkout %d has no dir", i)
		case c.Workspace == "":
			return fmt.Errorf("rig: checkout %q has no workspace name", c.Dir)
		case c.MainClone == "":
			return fmt.Errorf("rig: checkout %q has no main clone", c.Dir)
		case c.Commit == "":
			// The commit is the pin. Without it `wgo doctor` cannot tell a
			// drifted checkout from an intact one, and there is nothing to
			// create the workspace at.
			return fmt.Errorf("rig: checkout %q has no commit", c.Dir)
		case dirs[c.Dir]:
			return fmt.Errorf("rig: duplicate checkout dir %q", c.Dir)
		case workspaces[c.Workspace]:
			// `jj workspace add --name` fails on a duplicate, which would abort
			// materialisation partway through and strand the workspaces already
			// created — `wgo rig rm` reads them from a manifest that was never
			// written.
			return fmt.Errorf("rig: duplicate workspace name %q", c.Workspace)
		case pins[pin]:
			// Two checkouts of one commit are two identical working copies, and
			// two go.work entries for the same source. They should have been
			// grouped.
			return fmt.Errorf("rig: duplicate checkout of %s", pin)
		case c.Full && len(c.Sparse) > 0:
			return fmt.Errorf("rig: checkout %q is both full and sparse", c.Dir)
		}
		dirs[c.Dir] = true
		workspaces[c.Workspace] = true
		pins[pin] = true
	}
	served := map[string]bool{}
	for _, mem := range m.Members {
		if mem.Path == "" {
			return errors.New("rig: member has no module path")
		}
		if !dirs[mem.Checkout] {
			return fmt.Errorf("rig: member %q references unknown checkout %q", mem.Path, mem.Checkout)
		}
		served[mem.Checkout] = true
	}
	for _, c := range m.Checkouts {
		switch {
		case served[c.Dir] && c.Obsolete:
			// The whole point of the tombstone is that nothing builds against it.
			// A member would put it back in go.work while --prune still stood
			// ready to delete the directory underneath.
			return fmt.Errorf("rig: checkout %q is marked obsolete but still serves members", c.Dir)
		case !served[c.Dir] && !c.Obsolete:
			// A checkout nothing is served from is a workspace that gets created,
			// occupies disk, and never appears in go.work.
			return fmt.Errorf("rig: checkout %q has no members", c.Dir)
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

// pathExists reports whether path is present, treating any stat error other
// than "not there" as absent: a name completion is not the place to surface a
// permissions problem.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Listing is a manifest together with the directory it was loaded from.
//
// The two can disagree. Nothing stops a rig directory from being renamed, and
// the manifest's Name is what the generated go.work, env.sh and every jj
// workspace already call it — rewriting it to match the directory would strand
// those. So a listing carries both: Name to identify the rig, Root to reach it
// on disk. Reconstructing Root from Name is the bug this type exists to make
// unrepresentable.
type Listing struct {
	*Manifest

	// Root is the directory the manifest was read from.
	Root string
}

// List returns every rig under rigDir, sorted by manifest name.
//
// A directory with no rig.toml is silently not a rig: rig.dir is a directory
// the user owns and may hold anything. A rig.toml that exists but does not load
// is different — it is a broken rig, and swallowing it would make `wgo rig ls`
// report the rig as gone while its jj workspaces stay registered in the main
// clone. Those are warned about and skipped, so one corrupt rig does not hide
// the healthy ones.
func List(rigDir string) ([]Listing, error) {
	entries, err := os.ReadDir(rigDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("rig: reading %s: %w", rigDir, err)
	}
	var out []Listing
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		root := filepath.Join(rigDir, e.Name())
		m, err := Load(root)
		switch {
		case errors.Is(err, ErrNoManifest):
			continue
		case err != nil:
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
			continue
		}
		out = append(out, Listing{Manifest: m, Root: root})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Names returns the name of every rig under rigDir, sorted.
//
// Deliberately not List: shell completion runs on every keystroke, so it must
// not pay for a TOML parse per rig, and it must not emit the warnings List
// prints for a broken manifest — stderr during completion lands in the middle
// of the user's prompt. A rig too broken to load still has a name, and offering
// it is right: `wgo rig rm` is how you get rid of it.
//
// A src/ directory counts as well as a rig.toml, because the wreckage of a
// `rig new` that died before the manifest write is exactly what `wgo rig rm
// --force` exists to clear — and requiring the manifest would leave the one
// name the user needs to type as the one name completion will not offer.
// Directories with neither are somebody else's: rig.dir is a directory the
// user owns and may keep anything in.
func Names(rigDir string) ([]string, error) {
	entries, err := os.ReadDir(rigDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("rig: reading %s: %w", rigDir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		root := filepath.Join(rigDir, e.Name())
		if !pathExists(ManifestPath(root)) && !pathExists(filepath.Join(root, SrcDir)) {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}
