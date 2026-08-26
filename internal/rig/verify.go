package rig

import (
	"fmt"
	"sort"

	"github.com/virtru/wgo/internal/gomod"
)

// DriftKind classifies one difference between what the artifact shipped with
// and what the rig's workspace actually resolves to.
type DriftKind string

const (
	// DriftUpgraded is a third-party module the workspace resolved higher than
	// the artifact shipped with. This is the expected failure: every `use`
	// directive promotes a module to an MVS root, contributing its whole
	// requirement graph, so versions can move up without anything being edited.
	DriftUpgraded DriftKind = "upgraded"
	// DriftDowngraded is a module resolved lower than the baseline. MVS cannot
	// produce this on its own, so it means the baseline is stale, a freeze is
	// in force, or someone edited go.work.
	DriftDowngraded DriftKind = "downgraded"
	// DriftChanged is a version difference that cannot be ordered, because one
	// side is not a valid version. Reported rather than guessed at.
	DriftChanged DriftKind = "changed"
	// DriftAdded is a module in the workspace that the artifact did not have.
	DriftAdded DriftKind = "added"
	// DriftMissing is a baseline module the workspace no longer resolves at all.
	DriftMissing DriftKind = "missing"
)

// Fails reports whether a drift means the rig is not reproducing the artifact.
//
// Only version differences count. Added and missing modules are shape changes
// in the graph — a member's own dependencies moved — and they routinely happen
// in a rig that still compiles the shipped code against the shipped versions of
// everything it actually imports.
func (k DriftKind) Fails() bool {
	switch k {
	case DriftUpgraded, DriftDowngraded, DriftChanged:
		return true
	default:
		return false
	}
}

// Drift is one module whose resolved version does not match the baseline.
type Drift struct {
	Path     string    `json:"path"`
	Kind     DriftKind `json:"kind"`
	Baseline string    `json:"baseline,omitempty"`
	Actual   string    `json:"actual,omitempty"`
}

// String renders a drift for a one-line report.
func (d Drift) String() string {
	switch d.Kind {
	case DriftAdded:
		return fmt.Sprintf("%s: added at %s", d.Path, d.Actual)
	case DriftMissing:
		return fmt.Sprintf("%s: gone, was %s", d.Path, d.Baseline)
	default:
		return fmt.Sprintf("%s: %s -> %s (%s)", d.Path, d.Baseline, d.Actual, d.Kind)
	}
}

// Report is the outcome of comparing a rig's workspace against its baseline.
type Report struct {
	Rig string `json:"rig"`
	// Compared is how many baseline modules were actually checked, so a report
	// of "no drift" can be told apart from one where nothing was comparable.
	Compared int      `json:"compared"`
	Drifts   []Drift  `json:"drifts,omitempty"`
	Frozen   []string `json:"frozen,omitempty"`
}

// Failed reports whether any drift means the rig is not reproducing the
// artifact, which is what `wgo rig verify` exits 1 on.
func (r *Report) Failed() bool {
	for _, d := range r.Drifts {
		if d.Kind.Fails() {
			return true
		}
	}
	return false
}

// Failing returns just the drifts that make the report fail.
func (r *Report) Failing() []Drift {
	var out []Drift
	for _, d := range r.Drifts {
		if d.Kind.Fails() {
			out = append(out, d)
		}
	}
	return out
}

// Verify compares a resolved build list against the manifest's baseline.
//
// It is pure: the caller runs `go list -m` with the rig's GOWORK and hands the
// result in. That keeps the interesting half — the classification, and which
// modules are exempt from it — testable without a workspace on disk, and lets
// the caller choose between the package-contributing modules and the full
// module graph (`wgo rig verify --all`).
//
// Members are exempt. A module the rig checks out is a main module of the
// workspace: it has no version to compare, and its source is the whole point of
// the rig. Comparing it against the version the artifact shipped with would
// report drift on every single checkout.
func Verify(m *Manifest, actual []gomod.Module) *Report {
	served := make(map[string]bool, len(m.Members))
	for _, mem := range m.Members {
		served[mem.Path] = true
	}

	rep := &Report{Rig: m.Name, Frozen: append([]string(nil), m.Frozen...)}
	seen := map[string]bool{}

	for _, mod := range actual {
		if mod.Path == "" || mod.Main || served[mod.Path] {
			continue
		}
		// Effective, not the module as declared: a freeze is a go.work replace,
		// and the replacement's version is what gets built. Comparing the
		// declared version would report a frozen module as still drifting.
		// Marked seen before the comparability check: a module replaced by a
		// directory is present in the workspace, just not pinned. Skipping it
		// without recording it would send it round to the missing-baseline
		// sweep below and report it as gone.
		seen[mod.Path] = true
		eff := mod.Effective()
		if eff.Version == "" {
			// Replaced by a directory. There is no version to compare, and the
			// source is coming from disk rather than from a pin.
			continue
		}

		base, ok := m.Baseline[mod.Path]
		if !ok {
			rep.Drifts = append(rep.Drifts, Drift{
				Path: mod.Path, Kind: DriftAdded, Actual: eff.Version,
			})
			continue
		}
		rep.Compared++
		if base == eff.Version {
			continue
		}
		// Different strings can still be the same version: "v2.0.0" against
		// "v2.0.0+incompatible". classify says so with an empty kind.
		kind := classify(base, eff.Version)
		if kind == "" {
			continue
		}
		rep.Drifts = append(rep.Drifts, Drift{
			Path: mod.Path, Kind: kind, Baseline: base, Actual: eff.Version,
		})
	}

	for path, base := range m.Baseline {
		if seen[path] || served[path] {
			continue
		}
		rep.Drifts = append(rep.Drifts, Drift{
			Path: path, Kind: DriftMissing, Baseline: base,
		})
	}

	sort.Slice(rep.Drifts, func(i, j int) bool {
		if rep.Drifts[i].Kind != rep.Drifts[j].Kind {
			// Failing kinds sort first; they are what the exit code is about.
			fi, fj := rep.Drifts[i].Kind.Fails(), rep.Drifts[j].Kind.Fails()
			if fi != fj {
				return fi
			}
			return rep.Drifts[i].Kind < rep.Drifts[j].Kind
		}
		return rep.Drifts[i].Path < rep.Drifts[j].Path
	})
	return rep
}

// classify orders a baseline against an observed version.
func classify(baseline, actual string) DriftKind {
	cmp, ok := gomod.CompareVersions(actual, baseline)
	switch {
	case !ok:
		return DriftChanged
	case cmp > 0:
		return DriftUpgraded
	case cmp < 0:
		return DriftDowngraded
	default:
		// Different strings that compare equal: "v1.2.3" against
		// "v1.2.3+incompatible", say. Not drift.
		return ""
	}
}

// Freezer applies and removes the go.work replaces that pin a module back to
// its baseline, and reports whether the result still builds.
//
// Narrow on purpose, so the freeze bookkeeping can be tested without a
// toolchain: internal/gotool.Client satisfies it.
type Freezer interface {
	WorkEditReplace(oldPath, oldVersion, newPath, newVersion string) error
	WorkEditDropReplace(oldPath, oldVersion string) error
	Build(outputDir string, patterns ...string) error
}

// FreezeResult records what a freeze did and whether the rig still compiles.
type FreezeResult struct {
	// Froze are the modules newly pinned back to their baseline.
	Froze []string
	// BuildErr is the result of building after the freeze. Non-nil means the
	// pins were applied but the workspace no longer compiles.
	BuildErr error
}

// Freeze pins every failing drift back to the version the artifact shipped with
// by writing a go.work replace, then rebuilds.
//
// The rebuild is not optional. Forcing a version down is exactly the kind of
// change MVS exists to prevent: a member module may require the higher version,
// and the freeze that made `rig verify` pass can be the same edit that stops
// the rig from compiling. Reporting the pins as applied without saying whether
// they still build would hand back a rig that is green and useless.
//
// The manifest is updated in memory only; the caller saves it, so a failed
// build can be reported before anything is persisted.
func Freeze(f Freezer, m *Manifest, rep *Report, buildDir string) (*FreezeResult, error) {
	res := &FreezeResult{}
	frozen := map[string]bool{}
	for _, p := range m.Frozen {
		frozen[p] = true
	}

	for _, d := range rep.Failing() {
		if frozen[d.Path] {
			// Already pinned and still drifting: the replace is being overridden
			// by something else in go.work, and rewriting it changes nothing.
			continue
		}
		if d.Baseline == "" {
			continue
		}
		if err := f.WorkEditReplace(d.Path, "", d.Path, d.Baseline); err != nil {
			return nil, fmt.Errorf("rig: pinning %s back to %s: %w", d.Path, d.Baseline, err)
		}
		frozen[d.Path] = true
		res.Froze = append(res.Froze, d.Path)
	}
	if len(res.Froze) == 0 {
		return res, nil
	}

	m.Frozen = sortedKeys(frozen)
	if patterns := m.PackagePatterns(); len(patterns) > 0 {
		res.BuildErr = f.Build(buildDir, patterns...)
	}
	return res, nil
}

// Unfreeze drops the baseline pin for a module, returning false if it was not
// frozen in the first place.
func Unfreeze(f Freezer, m *Manifest, modulePath string) (bool, error) {
	kept := make([]string, 0, len(m.Frozen))
	found := false
	for _, p := range m.Frozen {
		if p == modulePath {
			found = true
			continue
		}
		kept = append(kept, p)
	}
	if !found {
		return false, nil
	}
	if err := f.WorkEditDropReplace(modulePath, ""); err != nil {
		return false, fmt.Errorf("rig: dropping the baseline pin for %s: %w", modulePath, err)
	}
	m.Frozen = kept
	return true, nil
}

// Rebaseline replaces the manifest's baseline with what the workspace currently
// resolves, so the rig stops reporting drift it has been decided to accept.
//
// This is `wgo rig verify --write-back`, and it is destructive in a quiet way:
// the record of what the artifact actually shipped with is what makes a rig
// trustworthy, and once overwritten it cannot be recovered from the rig. The
// caller is expected to say so before calling.
func Rebaseline(m *Manifest, actual []gomod.Module) int {
	served := make(map[string]bool, len(m.Members))
	for _, mem := range m.Members {
		served[mem.Path] = true
	}
	base := map[string]string{}
	for _, mod := range actual {
		if mod.Path == "" || mod.Main || served[mod.Path] {
			continue
		}
		eff := mod.Effective()
		if eff.Version == "" {
			continue
		}
		base[mod.Path] = eff.Version
	}
	m.Baseline = base
	return len(base)
}
