package rig

import (
	"fmt"
	"sort"
	"strings"

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
	// BaselineFrom and ActualFrom are the module paths the versions belong to,
	// set only when a replace redirected the module somewhere other than Path.
	// A version is only meaningful alongside the module it versions: v0.10.2 of
	// a fork and v0.10.1 of the upstream are not orderable, and calling the
	// first an upgrade of the second is nonsense.
	BaselineFrom string `json:"baseline_from,omitempty"`
	ActualFrom   string `json:"actual_from,omitempty"`
}

// qualify renders a version alongside the module it came from, when that is not
// the module being reported on.
func qualify(from, version string) string {
	if from == "" {
		return version
	}
	return from + "@" + version
}

// Shipped and Resolved render the two sides of a drift, each qualified with the
// module its version belongs to when a replace redirected it elsewhere. A bare
// "v1.68.1 -> v1.68.0" reads as a downgrade of one module; naming the fork on
// one side is what makes it a substitution.
func (d Drift) Shipped() string  { return qualify(d.BaselineFrom, d.Baseline) }
func (d Drift) Resolved() string { return qualify(d.ActualFrom, d.Actual) }

// String renders a drift for a one-line report.
func (d Drift) String() string {
	switch d.Kind {
	case DriftAdded:
		return fmt.Sprintf("%s: added at %s", d.Path, d.Resolved())
	case DriftMissing:
		return fmt.Sprintf("%s: gone, was %s", d.Path, d.Shipped())
	default:
		return fmt.Sprintf("%s: %s -> %s (%s)", d.Path, d.Shipped(), d.Resolved(), d.Kind)
	}
}

// BaselineEntry encodes what a build list entry resolved to, for recording in a
// manifest's Baseline.
//
// Usually just the version. A module a replace redirected to a different module
// path — a fork — is recorded as "path@version", because the version alone
// would be attributed to the module that was replaced rather than the one that
// was built, and a later verify would compare two different modules' versions
// and call the difference an upgrade. Returns "" for anything unpinnable: a
// directory replace has no version, and "(devel)" names no release, so neither
// gives a later build anything to be compared against.
func BaselineEntry(mod gomod.Module) string {
	eff := mod.Effective()
	if !gomod.IsResolvableVersion(eff.Version) {
		return ""
	}
	if eff.Path != "" && eff.Path != mod.Path {
		return eff.Path + "@" + eff.Version
	}
	return eff.Version
}

// splitBaseline undoes BaselineEntry, returning the module path the version
// belongs to (empty when it is the key's own) and the version.
func splitBaseline(entry string) (from, version string) {
	// A version never contains "@", so the last one separates the two. Module
	// paths cannot contain "@" either, but splitting from the right costs
	// nothing and cannot be confused by one.
	if i := strings.LastIndex(entry, "@"); i >= 0 {
		return entry[:i], entry[i+1:]
	}
	return "", entry
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
		if eff.Version == "" || eff.Version == gomod.DevelVersion {
			// Replaced by a directory, or served from source by the workspace
			// and so recorded as "(devel)". There is no version to compare, and
			// the source is coming from disk rather than from a pin.
			//
			// Only these two: any other unrecognisable version is reported as
			// DriftChanged rather than passed over, since something that is not
			// a version at all is not something the artifact shipped with.
			continue
		}
		actualFrom := ""
		if eff.Path != "" && eff.Path != mod.Path {
			actualFrom = eff.Path
		}

		entry, ok := m.Baseline[mod.Path]
		if !ok {
			rep.Drifts = append(rep.Drifts, Drift{
				Path: mod.Path, Kind: DriftAdded, Actual: eff.Version, ActualFrom: actualFrom,
			})
			continue
		}
		baseFrom, base := splitBaseline(entry)
		rep.Compared++
		if baseFrom == actualFrom && base == eff.Version {
			continue
		}
		drift := Drift{
			Path: mod.Path, Baseline: base, Actual: eff.Version,
			BaselineFrom: baseFrom, ActualFrom: actualFrom,
		}
		if baseFrom != actualFrom {
			// The module is being built from somewhere else than it shipped
			// from — a fork replace was added, dropped, or re-pointed. The two
			// versions belong to different modules, so there is no ordering
			// between them to report.
			drift.Kind = DriftChanged
			rep.Drifts = append(rep.Drifts, drift)
			continue
		}
		// Different strings can still be the same version: "v2.0.0" against
		// "v2.0.0+incompatible". classify says so with an empty kind.
		kind := classify(base, eff.Version)
		if kind == "" {
			continue
		}
		drift.Kind = kind
		rep.Drifts = append(rep.Drifts, drift)
	}

	for path, entry := range m.Baseline {
		if seen[path] || served[path] {
			continue
		}
		from, base := splitBaseline(entry)
		rep.Drifts = append(rep.Drifts, Drift{
			Path: path, Kind: DriftMissing, Baseline: base, BaselineFrom: from,
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
	// Overridden are modules that were already pinned and are drifting anyway:
	// the replace is in go.work but something is overriding it. Without this a
	// second --freeze on the same rig would pin nothing, report nothing, and
	// still fail — indistinguishable from the freeze silently not working.
	Overridden []string
	// Unpinnable are drifting modules whose baseline names no version to pin
	// back to, so a freeze cannot address them.
	Unpinnable []string
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
// build can be reported before anything is persisted. A non-nil error still
// comes with a result: an edit that fails partway leaves the replaces written
// before it in go.work, and unless the caller records those the manifest no
// longer knows what is pinned and `--unfreeze` cannot find them.
func Freeze(f Freezer, m *Manifest, rep *Report, buildDir string) (*FreezeResult, error) {
	res := &FreezeResult{}
	frozen := map[string]bool{}
	for _, p := range m.Frozen {
		frozen[p] = true
	}
	// Applied before every return: the caller saves the manifest on the error
	// path too, so what is in go.work and what the manifest records stay the
	// same set.
	record := func() {
		if len(res.Froze) > 0 {
			m.Frozen = sortedKeys(frozen)
		}
	}

	for _, d := range rep.Failing() {
		if frozen[d.Path] {
			// Already pinned and still drifting: the replace is being overridden
			// by something else in go.work, and rewriting it changes nothing.
			res.Overridden = append(res.Overridden, d.Path)
			continue
		}
		if !gomod.IsResolvableVersion(d.Baseline) {
			// Nothing to pin to. A baseline recorded before this was filtered
			// out could still be "(devel)", and go.work rejects a replace whose
			// target has no version — the freeze would fail on that module and
			// abandon every module after it.
			res.Unpinnable = append(res.Unpinnable, d.Path)
			continue
		}
		// The replacement target is the module the artifact actually built, not
		// necessarily the one being replaced: a fork replace shipped
		// upstream => fork@v, and pinning upstream => upstream@v would override
		// the artifact's own replace and build code it never shipped.
		target := d.BaselineFrom
		if target == "" {
			target = d.Path
		}
		if err := f.WorkEditReplace(d.Path, "", target, d.Baseline); err != nil {
			record()
			return res, fmt.Errorf("rig: pinning %s back to %s: %w",
				d.Path, qualify(d.BaselineFrom, d.Baseline), err)
		}
		frozen[d.Path] = true
		res.Froze = append(res.Froze, d.Path)
	}
	if len(res.Froze) == 0 {
		return res, nil
	}

	record()
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
//
// whole says that actual is the entire module graph. It usually is not: verify
// measures the modules that contribute packages, which is a subset, and
// replacing the baseline with a subset silently deletes the record for every
// module outside it — including the ones a later `--all` verify would have
// checked. When actual is partial the recorded modules are updated and the rest
// left alone, so a write-back accepts the drift it was shown and nothing more.
func Rebaseline(m *Manifest, actual []gomod.Module, whole bool) int {
	served := make(map[string]bool, len(m.Members))
	for _, mem := range m.Members {
		served[mem.Path] = true
	}
	base := map[string]string{}
	if !whole {
		for path, entry := range m.Baseline {
			base[path] = entry
		}
	}
	for _, mod := range actual {
		if mod.Path == "" || mod.Main || served[mod.Path] {
			continue
		}
		if entry := BaselineEntry(mod); entry != "" {
			base[mod.Path] = entry
		}
	}
	m.Baseline = base
	return len(base)
}
