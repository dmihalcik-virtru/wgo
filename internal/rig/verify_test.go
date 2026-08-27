package rig

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/gomod"
)

// verifyManifest is a rig with one member served from a checkout and a
// three-module baseline, which is enough shape to exercise every exemption.
func verifyManifest() *Manifest {
	return &Manifest{
		Name:    "dsp-2.7.1",
		Primary: "github.com/virtru-corp/data-security-platform",
		Members: []Member{
			{Path: "github.com/virtru-corp/data-security-platform", Version: "v2.7.1", Checkout: "dsp-v2.7.1"},
			{Path: "github.com/opentdf/platform/sdk", Version: "v0.11.6", Checkout: "platform-sdk-v0.11.6", Subdir: "sdk"},
		},
		Baseline: map[string]string{
			"google.golang.org/grpc": "v1.68.0",
			"github.com/spf13/cobra": "v1.8.1",
			"golang.org/x/sync":      "v0.9.0",
		},
	}
}

func mod(path, version string) gomod.Module {
	return gomod.Module{Path: path, Version: version}
}

func driftFor(t *testing.T, rep *Report, path string) Drift {
	t.Helper()
	for _, d := range rep.Drifts {
		if d.Path == path {
			return d
		}
	}
	t.Fatalf("no drift reported for %s; got %v", path, rep.Drifts)
	return Drift{}
}

func TestVerifyReportsNoDriftWhenEverythingMatches(t *testing.T) {
	m := verifyManifest()
	rep := Verify(m, []gomod.Module{
		mod("google.golang.org/grpc", "v1.68.0"),
		mod("github.com/spf13/cobra", "v1.8.1"),
		mod("golang.org/x/sync", "v0.9.0"),
	})

	assert.Empty(t, rep.Drifts)
	assert.False(t, rep.Failed())
	// Compared is what tells "everything matched" apart from "nothing was
	// checked", so it has to be the real count.
	assert.Equal(t, 3, rep.Compared)
	assert.Equal(t, "dsp-2.7.1", rep.Rig)
}

func TestVerifyClassifiesEachKindOfDifference(t *testing.T) {
	m := verifyManifest()
	rep := Verify(m, []gomod.Module{
		mod("google.golang.org/grpc", "v1.69.2"), // upgraded
		mod("github.com/spf13/cobra", "v1.8.0"),  // downgraded
		mod("github.com/kr/pretty", "v0.3.1"),    // added
		// golang.org/x/sync absent -> missing
	})

	assert.Equal(t, DriftUpgraded, driftFor(t, rep, "google.golang.org/grpc").Kind)
	assert.Equal(t, DriftDowngraded, driftFor(t, rep, "github.com/spf13/cobra").Kind)
	assert.Equal(t, DriftAdded, driftFor(t, rep, "github.com/kr/pretty").Kind)
	assert.Equal(t, DriftMissing, driftFor(t, rep, "golang.org/x/sync").Kind)

	// Only the two version moves are failures; added and missing are shape
	// changes in the graph, not different code than what shipped.
	assert.True(t, rep.Failed())
	assert.Len(t, rep.Failing(), 2)

	// Failing kinds sort first so the reason for the exit code reads at the top.
	require.Len(t, rep.Drifts, 4)
	assert.True(t, rep.Drifts[0].Kind.Fails())
	assert.True(t, rep.Drifts[1].Kind.Fails())

	up := driftFor(t, rep, "google.golang.org/grpc")
	assert.Equal(t, "v1.68.0", up.Baseline)
	assert.Equal(t, "v1.69.2", up.Actual)
}

func TestVerifyExemptsMembersAndTheMainModule(t *testing.T) {
	m := verifyManifest()
	m.Baseline["github.com/opentdf/platform/sdk"] = "v0.11.6"

	rep := Verify(m, []gomod.Module{
		// A member is a main module of the workspace: no version, source from
		// disk. Comparing it would report drift on every single checkout.
		{Path: "github.com/virtru-corp/data-security-platform", Main: true},
		{Path: "github.com/opentdf/platform/sdk", Version: gomod.DevelVersion},
		mod("google.golang.org/grpc", "v1.68.0"),
		mod("github.com/spf13/cobra", "v1.8.1"),
		mod("golang.org/x/sync", "v0.9.0"),
	})

	assert.Empty(t, rep.Drifts, "members must be exempt from drift, including as missing baseline entries")
	assert.Equal(t, 3, rep.Compared)
}

func TestVerifyIgnoresDirectoryReplacements(t *testing.T) {
	m := verifyManifest()
	rep := Verify(m, []gomod.Module{
		{
			Path:    "google.golang.org/grpc",
			Version: "v1.68.0",
			// Replaced by a directory: no version to compare, and the source is
			// coming from disk rather than from a pin.
			Replace: &gomod.Module{Path: "../grpc-fork", Dir: "/tmp/grpc-fork"},
		},
		mod("github.com/spf13/cobra", "v1.8.1"),
		mod("golang.org/x/sync", "v0.9.0"),
	})

	assert.Empty(t, rep.Drifts)
	// Not compared, and not reported missing either: it was seen, just not
	// comparable.
	assert.Equal(t, 2, rep.Compared)
}

func TestVerifyComparesTheReplacementVersionNotTheDeclaredOne(t *testing.T) {
	m := verifyManifest()
	// This is what a freeze looks like once it is in force: the module is still
	// required at v1.69.2, but go.work replaces it down to the baseline.
	rep := Verify(m, []gomod.Module{
		{
			Path:    "google.golang.org/grpc",
			Version: "v1.69.2",
			Replace: &gomod.Module{Path: "google.golang.org/grpc", Version: "v1.68.0"},
		},
		mod("github.com/spf13/cobra", "v1.8.1"),
		mod("golang.org/x/sync", "v0.9.0"),
	})

	assert.Empty(t, rep.Drifts, "a frozen module builds at the replacement version, so that is what must be compared")
}

func TestVerifyDoesNotReportIncompatibleAsDrift(t *testing.T) {
	m := &Manifest{Name: "r", Baseline: map[string]string{"github.com/old/thing": "v2.0.0"}}
	rep := Verify(m, []gomod.Module{mod("github.com/old/thing", "v2.0.0+incompatible")})

	// The strings differ but the versions do not; classify says so with an
	// empty kind, and an empty-kind drift would be nonsense to print.
	assert.Empty(t, rep.Drifts)
	assert.Equal(t, 1, rep.Compared)
}

func TestVerifyReportsAnUnorderableDifferenceAsChanged(t *testing.T) {
	m := &Manifest{Name: "r", Baseline: map[string]string{"github.com/x/y": "v1.0.0"}}
	rep := Verify(m, []gomod.Module{mod("github.com/x/y", "wat")})

	d := driftFor(t, rep, "github.com/x/y")
	assert.Equal(t, DriftChanged, d.Kind)
	assert.True(t, d.Kind.Fails(), "an unorderable difference is still a different version than what shipped")
}

func TestVerifyCarriesTheFrozenSet(t *testing.T) {
	m := verifyManifest()
	m.Frozen = []string{"google.golang.org/grpc"}
	rep := Verify(m, nil)

	assert.Equal(t, []string{"google.golang.org/grpc"}, rep.Frozen)
	// Copied, not aliased: the report is handed to a JSON encoder and to the
	// freeze path, which rewrites m.Frozen.
	m.Frozen[0] = "mutated"
	assert.Equal(t, []string{"google.golang.org/grpc"}, rep.Frozen)
}

func TestDriftStringNamesWhatHappened(t *testing.T) {
	assert.Equal(t, "github.com/x/y: v1.0.0 -> v1.1.0 (upgraded)",
		Drift{Path: "github.com/x/y", Kind: DriftUpgraded, Baseline: "v1.0.0", Actual: "v1.1.0"}.String())
	assert.Equal(t, "github.com/x/y: added at v1.1.0",
		Drift{Path: "github.com/x/y", Kind: DriftAdded, Actual: "v1.1.0"}.String())
	assert.Equal(t, "github.com/x/y: gone, was v1.0.0",
		Drift{Path: "github.com/x/y", Kind: DriftMissing, Baseline: "v1.0.0"}.String())
}

// fakeFreezer records the go.work edits a freeze asks for, and can be told the
// rebuild fails — which is the case the freeze path exists to report.
type fakeFreezer struct {
	replaced [][4]string
	dropped  [][2]string
	builds   []string
	patterns []string
	buildErr error
	editErr  error
	// failAfter, when positive, lets that many replaces through and fails the
	// next — a freeze interrupted partway.
	failAfter int
}

func (f *fakeFreezer) WorkEditReplace(oldPath, oldVersion, newPath, newVersion string) error {
	if f.editErr != nil {
		return f.editErr
	}
	if f.failAfter > 0 && len(f.replaced) >= f.failAfter {
		return errors.New("go.work became unwritable")
	}
	f.replaced = append(f.replaced, [4]string{oldPath, oldVersion, newPath, newVersion})
	return nil
}

func (f *fakeFreezer) WorkEditDropReplace(oldPath, oldVersion string) error {
	if f.editErr != nil {
		return f.editErr
	}
	f.dropped = append(f.dropped, [2]string{oldPath, oldVersion})
	return nil
}

func (f *fakeFreezer) Build(outputDir string, patterns ...string) error {
	f.builds = append(f.builds, outputDir)
	f.patterns = append(f.patterns, patterns...)
	return f.buildErr
}

func TestFreezePinsFailingDriftsBackToTheBaseline(t *testing.T) {
	m := verifyManifest()
	rep := Verify(m, []gomod.Module{
		mod("google.golang.org/grpc", "v1.69.2"), // upgraded
		mod("github.com/spf13/cobra", "v1.8.1"),
		mod("github.com/kr/pretty", "v0.3.1"), // added, must not be frozen
		mod("golang.org/x/sync", "v0.9.0"),
	})
	f := &fakeFreezer{}

	res, err := Freeze(f, m, rep, "/dev/null")
	require.NoError(t, err)

	assert.Equal(t, []string{"google.golang.org/grpc"}, res.Froze)
	assert.Equal(t, [][4]string{
		{"google.golang.org/grpc", "", "google.golang.org/grpc", "v1.68.0"},
	}, f.replaced, "an added module has no baseline to pin back to")
	assert.Equal(t, []string{"/dev/null"}, f.builds)
	// Per-member patterns, not "./...": in workspace mode a directory prefix
	// has to name a directory inside a listed module, and the rig root is not
	// one — a build check that cannot run is not a build check.
	assert.Equal(t, []string{
		"./src/dsp-v2.7.1/...",
		"./src/platform-sdk-v0.11.6/sdk/...",
	}, f.patterns)
	assert.NoError(t, res.BuildErr)
	assert.Equal(t, []string{"google.golang.org/grpc"}, m.Frozen)
}

// forked is grpc served from a fork, the way `go list -m` reports it.
func forked(path, version, forkPath, forkVersion string) gomod.Module {
	return gomod.Module{
		Path: path, Version: version,
		Replace: &gomod.Module{Path: forkPath, Version: forkVersion},
	}
}

// A version only means something alongside the module it versions. A fork's
// v1.68.1 and the upstream's v1.68.0 are versions of different modules, so
// neither ordering nor equality between them says anything — and the baseline
// has to record which module the artifact actually built.
func TestVerifyDoesNotCompareAcrossModulePaths(t *testing.T) {
	const (
		grpc = "google.golang.org/grpc"
		fork = "github.com/virtru-corp/grpc-go"
	)
	m := verifyManifest()
	m.Baseline[grpc] = BaselineEntry(forked(grpc, "v1.68.0", fork, "v1.68.1"))
	require.Equal(t, fork+"@v1.68.1", m.Baseline[grpc], "the baseline names the module it versions")

	// The fork is still in force: no drift, even though the versions on the two
	// sides of the replace differ.
	rep := Verify(m, []gomod.Module{forked(grpc, "v1.68.0", fork, "v1.68.1")})
	assert.Empty(t, rep.Failing())
	assert.Equal(t, 1, rep.Compared)

	// The replace was dropped, so the build is now getting upstream code. That
	// is drift, and it is not an ordering: reporting "v1.68.1 -> v1.68.0
	// (downgraded)" would invite freezing the fork's version onto upstream.
	rep = Verify(m, []gomod.Module{mod(grpc, "v1.68.0")})
	d := driftFor(t, rep, grpc)
	assert.Equal(t, DriftChanged, d.Kind)
	assert.Equal(t, fork, d.BaselineFrom)
	assert.Empty(t, d.ActualFrom)
	assert.Contains(t, d.String(), fork+"@v1.68.1")
}

// Freezing a fork-served module back to its baseline means restoring the fork,
// not pinning the upstream path to the fork's version — that second replace
// would override the artifact's own and build code it never shipped.
func TestFreezeRestoresTheModuleTheArtifactActuallyBuilt(t *testing.T) {
	const (
		grpc = "google.golang.org/grpc"
		fork = "github.com/virtru-corp/grpc-go"
	)
	m := verifyManifest()
	m.Baseline[grpc] = BaselineEntry(forked(grpc, "v1.68.0", fork, "v1.68.1"))
	rep := Verify(m, []gomod.Module{mod(grpc, "v1.69.2")})
	f := &fakeFreezer{}

	res, err := Freeze(f, m, rep, "/dev/null")
	require.NoError(t, err)
	assert.Equal(t, []string{grpc}, res.Froze)
	assert.Equal(t, [][4]string{{grpc, "", fork, "v1.68.1"}}, f.replaced)
}

func TestBaselineEntry(t *testing.T) {
	tests := []struct {
		name string
		mod  gomod.Module
		want string
	}{
		{"plain", mod("google.golang.org/grpc", "v1.68.0"), "v1.68.0"},
		{
			"fork replace names the module the version belongs to",
			forked("google.golang.org/grpc", "v1.68.0", "github.com/virtru-corp/grpc-go", "v1.68.1"),
			"github.com/virtru-corp/grpc-go@v1.68.1",
		},
		{
			"a version replace of the same module is just a version",
			forked("google.golang.org/grpc", "v1.68.0", "google.golang.org/grpc", "v1.68.2"),
			"v1.68.2",
		},
		{
			"a directory replace has nothing to compare against",
			gomod.Module{Path: "golang.org/x/sync", Version: "v0.9.0",
				Replace: &gomod.Module{Path: "../sync", Dir: "/tmp/sync"}},
			"",
		},
		{"(devel) names no release", mod("golang.org/x/sync", gomod.DevelVersion), ""},
		{"a main module has no version", gomod.Module{Path: "example.com/app", Main: true}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, BaselineEntry(tt.mod))
		})
	}
}

func TestPackagePatternsCoverEveryMemberOnce(t *testing.T) {
	m := verifyManifest()
	// Two modules can share one checkout; a duplicate pattern would make `go
	// list` do the same work twice.
	m.Members = append(m.Members,
		Member{Path: "github.com/opentdf/platform/service", Checkout: "platform-sdk-v0.11.6", Subdir: "sdk"},
		Member{Path: "github.com/opentdf/platform/lib/ocrypto", Checkout: "platform-sdk-v0.11.6", Subdir: "lib/ocrypto"})

	assert.Equal(t, []string{
		"./src/dsp-v2.7.1/...",
		"./src/platform-sdk-v0.11.6/sdk/...",
		"./src/platform-sdk-v0.11.6/lib/ocrypto/...",
	}, m.PackagePatterns())

	assert.Empty(t, (&Manifest{Name: "empty"}).PackagePatterns())
}

func TestFreezeReportsThatThePinBrokeTheBuild(t *testing.T) {
	m := verifyManifest()
	rep := Verify(m, []gomod.Module{mod("google.golang.org/grpc", "v1.69.2")})
	boom := errors.New("sdk requires grpc v1.69.0")
	f := &fakeFreezer{buildErr: boom}

	res, err := Freeze(f, m, rep, "/dev/null")
	// The pins were applied; the workspace no longer compiles. That is a
	// result to report, not an error in freezing.
	require.NoError(t, err)
	assert.ErrorIs(t, res.BuildErr, boom)
	assert.Equal(t, []string{"google.golang.org/grpc"}, res.Froze)
}

func TestFreezeDoesNothingWhenThereIsNothingToPin(t *testing.T) {
	m := verifyManifest()
	rep := Verify(m, []gomod.Module{
		mod("google.golang.org/grpc", "v1.68.0"),
		mod("github.com/spf13/cobra", "v1.8.1"),
		mod("golang.org/x/sync", "v0.9.0"),
	})
	f := &fakeFreezer{}

	res, err := Freeze(f, m, rep, "/dev/null")
	require.NoError(t, err)
	assert.Empty(t, res.Froze)
	// No pointless rebuild, and no manifest churn to commit.
	assert.Empty(t, f.builds)
	assert.Empty(t, m.Frozen)
}

func TestFreezeSkipsAModuleThatIsAlreadyFrozen(t *testing.T) {
	m := verifyManifest()
	m.Frozen = []string{"google.golang.org/grpc"}
	// Still drifting despite the pin: something else in go.work is overriding
	// it, and rewriting the same replace would change nothing.
	rep := Verify(m, []gomod.Module{mod("google.golang.org/grpc", "v1.69.2")})
	f := &fakeFreezer{}

	res, err := Freeze(f, m, rep, "/dev/null")
	require.NoError(t, err)
	assert.Empty(t, res.Froze)
	assert.Empty(t, f.replaced)
}

func TestFreezeSurfacesAFailedWorkEdit(t *testing.T) {
	m := verifyManifest()
	rep := Verify(m, []gomod.Module{mod("google.golang.org/grpc", "v1.69.2")})
	f := &fakeFreezer{editErr: errors.New("go.work is read-only")}

	_, err := Freeze(f, m, rep, "/dev/null")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "google.golang.org/grpc")
	assert.Contains(t, err.Error(), "v1.68.0")
	assert.Empty(t, m.Frozen, "a failed edit must not leave the manifest claiming the pin is in force")
}

// An edit that fails partway has already written the replaces before it. The
// manifest is the only record of which modules are pinned — `--unfreeze` reads
// it — so those have to be in it even though the freeze as a whole failed.
func TestFreezeRecordsThePinsItManagedBeforeFailing(t *testing.T) {
	m := verifyManifest()
	rep := Verify(m, []gomod.Module{
		mod("google.golang.org/grpc", "v1.69.2"),
		mod("github.com/spf13/cobra", "v1.9.0"),
		mod("golang.org/x/sync", "v0.9.0"),
	})
	require.Len(t, rep.Failing(), 2)
	f := &fakeFreezer{failAfter: 1}

	res, err := Freeze(f, m, rep, "/dev/null")
	require.Error(t, err)
	require.Len(t, res.Froze, 1)
	assert.Equal(t, res.Froze, m.Frozen,
		"go.work holds the pin either way; the manifest has to know which")
	// No rebuild: the freeze is incomplete, so what it would prove is not the
	// question the user asked.
	assert.Empty(t, f.builds)
}

// A freeze that pins nothing is otherwise silent, and the second `--freeze` on
// a rig whose pin is being overridden looks exactly like one that worked.
func TestFreezeReportsWhatItCouldNotPin(t *testing.T) {
	m := verifyManifest()
	m.Frozen = []string{"google.golang.org/grpc"}
	m.Baseline["github.com/kr/pretty"] = gomod.DevelVersion
	rep := Verify(m, []gomod.Module{
		mod("google.golang.org/grpc", "v1.69.2"),
		mod("github.com/kr/pretty", "v0.3.1"),
	})
	f := &fakeFreezer{}

	res, err := Freeze(f, m, rep, "/dev/null")
	require.NoError(t, err)
	assert.Empty(t, res.Froze)
	assert.Equal(t, []string{"google.golang.org/grpc"}, res.Overridden)
	assert.Equal(t, []string{"github.com/kr/pretty"}, res.Unpinnable,
		"a baseline that names no version cannot become a go.work replace")
	assert.Empty(t, f.replaced)
}

func TestUnfreezeDropsThePin(t *testing.T) {
	m := verifyManifest()
	m.Frozen = []string{"github.com/spf13/cobra", "google.golang.org/grpc"}
	f := &fakeFreezer{}

	dropped, err := Unfreeze(f, m, "google.golang.org/grpc")
	require.NoError(t, err)
	assert.True(t, dropped)
	assert.Equal(t, [][2]string{{"google.golang.org/grpc", ""}}, f.dropped)
	assert.Equal(t, []string{"github.com/spf13/cobra"}, m.Frozen)
}

func TestUnfreezeReportsAModuleThatWasNotFrozen(t *testing.T) {
	m := verifyManifest()
	m.Frozen = []string{"github.com/spf13/cobra"}
	f := &fakeFreezer{}

	dropped, err := Unfreeze(f, m, "google.golang.org/grpc")
	require.NoError(t, err)
	assert.False(t, dropped)
	// No edit attempted: dropping a replace that was never written would be a
	// confusing way to report a typo in the module path.
	assert.Empty(t, f.dropped)
	assert.Equal(t, []string{"github.com/spf13/cobra"}, m.Frozen)
}

func TestRebaselineAcceptsTheWorkspaceAsTheNewBaseline(t *testing.T) {
	m := verifyManifest()
	n := Rebaseline(m, []gomod.Module{
		{Path: "github.com/virtru-corp/data-security-platform", Main: true},
		{Path: "github.com/opentdf/platform/sdk", Version: gomod.DevelVersion},
		mod("google.golang.org/grpc", "v1.69.2"),
		mod("github.com/kr/pretty", "v0.3.1"),
		{Path: "golang.org/x/sync", Version: "v0.9.0",
			Replace: &gomod.Module{Path: "../sync", Dir: "/tmp/sync"}},
	}, true)

	assert.Equal(t, 2, n)
	assert.Equal(t, map[string]string{
		"google.golang.org/grpc": "v1.69.2",
		"github.com/kr/pretty":   "v0.3.1",
	}, m.Baseline)

	// The whole point: what used to drift now does not.
	rep := Verify(m, []gomod.Module{
		mod("google.golang.org/grpc", "v1.69.2"),
		mod("github.com/kr/pretty", "v0.3.1"),
	})
	assert.False(t, rep.Failed())
}

// `wgo rig verify --write-back` without --all measures only the modules that
// contribute packages. Replacing the baseline with that subset would delete the
// record for every module outside it — accepting drift the user was never shown
// and could not have seen without --all.
func TestRebaselineOnAPartialMeasurementKeepsWhatItDidNotSee(t *testing.T) {
	m := verifyManifest()
	n := Rebaseline(m, []gomod.Module{mod("google.golang.org/grpc", "v1.69.2")}, false)

	assert.Equal(t, 3, n)
	assert.Equal(t, map[string]string{
		"google.golang.org/grpc": "v1.69.2", // measured, so updated
		"github.com/spf13/cobra": "v1.8.1",  // not measured, so left alone
		"golang.org/x/sync":      "v0.9.0",
	}, m.Baseline)
}
