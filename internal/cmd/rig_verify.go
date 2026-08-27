package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/gomod"
	"github.com/virtru/wgo/internal/gotool"
	"github.com/virtru/wgo/internal/rig"
)

var (
	rigVerifyAll       bool
	rigVerifyFreeze    bool
	rigVerifyUnfreeze  string
	rigVerifyFormat    string
	rigVerifyWriteBack bool
)

var rigVerifyCmd = &cobra.Command{
	Use:   "verify [name]",
	Short: "Check that a rig still resolves the versions the artifact shipped with",
	Long: `Compare the versions the rig's workspace resolves against the baseline
recorded when it was created, and exit 1 if any of them moved.

This is not paranoia. Every module a rig checks out becomes a main module of
the workspace, which makes its whole requirement graph an MVS root — so a
third-party dependency can be resolved higher than the artifact shipped with
without anyone editing anything. A rig that silently builds against a
different gRPC than production is worse than no rig at all.

Added and missing modules are reported but do not fail: those are shape
changes in the graph, not a different version of code that shipped.

--freeze pins every moved module back to its baseline with a go.work replace
and then rebuilds, because forcing a version down can be exactly what breaks
the workspace:
  wgo rig verify dsp-2.7.1 --freeze

With no name, verifies the rig containing the current directory.`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRigVerify(args)
	},
}

func init() {
	rigCmd.AddCommand(rigVerifyCmd)

	rigVerifyCmd.Flags().BoolVar(&rigVerifyAll, "all", false,
		"Compare the whole module graph, not just the modules contributing packages")
	rigVerifyCmd.Flags().BoolVar(&rigVerifyFreeze, "freeze", false,
		"Pin every moved module back to its baseline in go.work, then rebuild")
	rigVerifyCmd.Flags().StringVar(&rigVerifyUnfreeze, "unfreeze", "",
		"Drop the baseline pin for one module path")
	rigVerifyCmd.Flags().StringVar(&rigVerifyFormat, "format", "text", "Output format: text or json")
	rigVerifyCmd.Flags().BoolVar(&rigVerifyWriteBack, "write-back", false,
		"Replace the baseline with what the workspace resolves now, accepting the drift")
}

// checkRigVerifyFlags rejects flag combinations before any work happens: a rig
// with a large module graph should not spend a `go list` on it only to be told
// the format was misspelled, and --write-back is not recoverable once it runs.
func checkRigVerifyFlags() error {
	if rigVerifyFormat != "text" && rigVerifyFormat != "json" {
		return fmt.Errorf("unknown --format %q: expected text or json", rigVerifyFormat)
	}
	if rigVerifyFreeze && rigVerifyWriteBack {
		// One pins the workspace back to the baseline, the other moves the
		// baseline up to the workspace. Doing both leaves neither meaning
		// anything.
		return errors.New("--freeze and --write-back are opposites: one pins the workspace back to the baseline, " +
			"the other accepts the workspace as the new baseline")
	}
	return nil
}

func runRigVerify(args []string) error {
	if err := checkRigVerifyFlags(); err != nil {
		return err
	}
	_, rigDir, err := rigSetup()
	if err != nil {
		return err
	}
	if !gotool.Available() {
		return errors.New("`go` is not on PATH; drift is measured with `go list`, so it cannot be checked without it")
	}
	rigRoot, err := resolveRigRoot(rigDir, args)
	if err != nil {
		return err
	}
	m, err := rig.Load(rigRoot)
	if err != nil {
		return err
	}
	if len(m.Baseline) == 0 {
		return fmt.Errorf("rig %s records no baseline, so there is nothing to verify against\n"+
			"it predates baseline recording, or was created with an empty build list; recreate it with: wgo rig new %s ...",
			m.Name, m.Name)
	}

	gc := gotool.NewClient().In(rigRoot).WithWork(filepath.Join(rigRoot, rig.GoWorkName))

	if rigVerifyUnfreeze != "" {
		dropped, err := rig.Unfreeze(gc, m, rigVerifyUnfreeze)
		if err != nil {
			return err
		}
		if !dropped {
			return fmt.Errorf("%s is not frozen in rig %s\nlist the frozen modules with: wgo rig show %s",
				rigVerifyUnfreeze, m.Name, m.Name)
		}
		if err := rig.Save(rigRoot, m); err != nil {
			return err
		}
		rigLogf("unfroze %s", rigVerifyUnfreeze)
	}

	actual, err := rigBuildList(gc, m)
	if err != nil {
		return err
	}

	if rigVerifyWriteBack {
		// rigVerifyAll: without it the measurement covers only the modules that
		// contribute packages, and a baseline replaced by that subset loses its
		// record of every other module.
		n := rig.Rebaseline(m, actual, rigVerifyAll)
		if err := rig.Save(rigRoot, m); err != nil {
			return err
		}
		// Loud, because the old baseline is what made the rig trustworthy and
		// it is not recoverable from the rig once overwritten.
		rigLogf("rebaselined %s to %d modules as currently resolved", m.Name, n)
		rigLogf("the record of what %s shipped with is gone; recreate the rig to get it back", m.Primary)
		return nil
	}

	rep := rig.Verify(m, actual)

	if rigVerifyFreeze && rep.Failed() {
		// os.DevNull as the output dir keeps the build check from littering the
		// rig with compiled binaries; cmd/go special-cases it and discards them.
		res, freezeErr := rig.Freeze(gc, m, rep, os.DevNull)
		if len(res.Froze) > 0 {
			// Saved before the error is returned: the replaces that were
			// written are in go.work either way, and a manifest that does not
			// record them leaves nothing for --unfreeze to find.
			if err := rig.Save(rigRoot, m); err != nil {
				return err
			}
			rigLogf("froze %d module(s) back to the baseline in %s:", len(res.Froze), rig.GoWorkName)
			for _, p := range res.Froze {
				rigLogf("  %s => %s", p, m.Baseline[p])
			}
		}
		if freezeErr != nil {
			return freezeErr
		}
		reportUnfrozen(res, m)
		if res.BuildErr != nil {
			return fmt.Errorf("the freeze was written to %s, but the workspace no longer builds:\n%w\n\n"+
				"a member module requires a version the artifact did not ship with, so the two cannot both hold.\n"+
				"drop the pin that broke it with: wgo rig verify %s --unfreeze <module>",
				rig.GoWorkName, res.BuildErr, m.Name)
		}
		// Re-measure rather than assuming: a replace only takes effect if
		// nothing else in the graph overrides it.
		actual, err = rigBuildList(gc, m)
		if err != nil {
			return err
		}
		rep = rig.Verify(m, actual)
	}

	if err := printRigVerify(rep, rigRoot); err != nil {
		return err
	}
	if rep.Failed() {
		os.Exit(1)
	}
	return nil
}

// reportUnfrozen explains the drifts a freeze could not address.
//
// A freeze that pins nothing is otherwise silent, so a second `--freeze` on a
// rig that is still failing looks identical to one that worked: no output, exit
// 1. Both causes here are actionable, and neither is guessable from the drift
// report alone.
func reportUnfrozen(res *rig.FreezeResult, m *rig.Manifest) {
	for _, p := range res.Overridden {
		rigLogf("%s is already pinned to %s but still resolves higher: something in %s overrides the pin",
			p, m.Baseline[p], rig.GoWorkName)
	}
	for _, p := range res.Unpinnable {
		rigLogf("%s cannot be pinned: the baseline records no version for it", p)
	}
}

// rigBuildList reads the versions the workspace currently resolves.
//
// The default is the modules that actually contribute packages: a module can
// sit in the build list with none of its code imported, and a version move
// there cannot change what the binary does. --all widens to the whole graph
// for the cases where that matters anyway, such as a tool dependency.
func rigBuildList(gc *gotool.Client, m *rig.Manifest) ([]gomod.Module, error) {
	if rigVerifyAll {
		mods, err := gc.ListModules()
		if err != nil {
			return nil, fmt.Errorf("rig: listing the module graph: %w", err)
		}
		return mods, nil
	}
	patterns := m.PackagePatterns()
	if len(patterns) == 0 {
		// No members means no packages of our own to trace imports from, and
		// `go list -deps` with no pattern would silently ask about the rig root.
		return nil, nil
	}
	mods, err := gc.ListPackageModules(patterns...)
	if err != nil {
		return nil, fmt.Errorf("rig: listing the modules that contribute packages: %w", err)
	}
	return mods, nil
}

func printRigVerify(rep *rig.Report, rigRoot string) error {
	if rigVerifyFormat == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}

	fmt.Printf("rig %s (%s)\n", rep.Rig, rigRoot)
	if len(rep.Frozen) > 0 {
		fmt.Printf("%d module(s) frozen to the baseline: %s\n", len(rep.Frozen), strings.Join(rep.Frozen, " "))
	}
	if len(rep.Drifts) == 0 {
		fmt.Printf("\nno drift: all %d baseline modules resolve to the version the artifact shipped with\n", rep.Compared)
		return nil
	}

	failing := rep.Failing()
	if len(failing) > 0 {
		fmt.Printf("\n%-52s %-14s %-24s %s\n", "MODULE", "STATUS", "SHIPPED", "RESOLVED")
		fmt.Println(strings.Repeat("-", 120))
		for _, d := range failing {
			fmt.Printf("%-52s %-14s %-24s %s\n", d.Path, d.Kind, orDash(d.Shipped()), orDash(d.Resolved()))
		}
	}
	fmt.Printf("\n%d of %d compared modules moved\n", len(failing), rep.Compared)

	// Added and missing are listed only in JSON. A rig's go.work covers every
	// module of every checkout, not just the ones the artifact linked, so the
	// added set routinely runs to hundreds of entries — printing them buries
	// the two lines that are actually the answer.
	var added, missing int
	for _, d := range rep.Drifts {
		switch d.Kind {
		case rig.DriftAdded:
			added++
		case rig.DriftMissing:
			missing++
		}
	}
	if added > 0 {
		fmt.Printf("%d module(s) in the workspace were not in the artifact's build list "+
			"(the rig builds more than the artifact linked)\n", added)
	}
	if missing > 0 {
		fmt.Printf("%d baseline module(s) are no longer reached from the rig's packages\n", missing)
	}
	if added+missing > 0 {
		fmt.Println("neither is a failure; see them with --format=json")
	}

	if len(failing) > 0 {
		fmt.Fprintf(os.Stderr, "\nevery `use` promotes a module to an MVS root, so versions can only move up on their own.\n"+
			"pin them back with: wgo rig verify %s --freeze\n", rep.Rig)
	}
	return nil
}
