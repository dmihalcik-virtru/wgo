package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/rig"
)

func TestRigVerifyFlagsAreRegistered(t *testing.T) {
	// The spec's documented surface; a flag silently missing turns a scripted
	// invocation into an "unknown flag" failure.
	for _, name := range []string{"all", "freeze", "unfreeze", "format", "write-back"} {
		assert.NotNil(t, rigVerifyCmd.Flags().Lookup(name), "wgo rig verify --%s", name)
	}
}

// withVerifyFlags restores the package-level flag vars, which cobra binds once
// per process and every test in this file would otherwise share.
func withVerifyFlags(t *testing.T, format string) {
	t.Helper()
	saved := rigVerifyFormat
	rigVerifyFormat = format
	t.Cleanup(func() { rigVerifyFormat = saved })
}

func TestPrintRigVerifyReportsACleanRig(t *testing.T) {
	withVerifyFlags(t, "text")
	rep := &rig.Report{Rig: "dsp-2.7.1", Compared: 42}

	out := captureStdout(t, func() {
		require.NoError(t, printRigVerify(rep, "/rigs/dsp-2.7.1"))
	})

	assert.Contains(t, out, "dsp-2.7.1")
	assert.Contains(t, out, "/rigs/dsp-2.7.1")
	// The count is the difference between "checked 42 modules, all matched" and
	// "checked nothing and said nothing was wrong".
	assert.Contains(t, out, "42")
	assert.Contains(t, out, "no drift")
}

func TestPrintRigVerifyTabulatesDrift(t *testing.T) {
	withVerifyFlags(t, "text")
	rep := &rig.Report{
		Rig:      "dsp-2.7.1",
		Compared: 40,
		Frozen:   []string{"github.com/spf13/cobra"},
		Drifts: []rig.Drift{
			{Path: "google.golang.org/grpc", Kind: rig.DriftUpgraded, Baseline: "v1.68.0", Actual: "v1.69.2"},
			{Path: "github.com/kr/pretty", Kind: rig.DriftAdded, Actual: "v0.3.1"},
		},
	}

	out := captureStdout(t, func() {
		require.NoError(t, printRigVerify(rep, "/rigs/dsp-2.7.1"))
	})

	assert.Contains(t, out, "google.golang.org/grpc")
	assert.Contains(t, out, "v1.68.0")
	assert.Contains(t, out, "v1.69.2")
	assert.Contains(t, out, "upgraded")
	assert.Contains(t, out, "github.com/spf13/cobra", "the frozen set explains why some modules are not drifting")
	assert.Contains(t, out, "1 of 40 compared modules moved")

	// Added modules are counted, not listed. A rig's go.work covers every
	// module of every checkout, so this set routinely runs to hundreds and
	// would bury the one line that is the answer.
	assert.NotContains(t, out, "github.com/kr/pretty")
	assert.Contains(t, out, "1 module(s) in the workspace were not in the artifact's build list")
	assert.Contains(t, out, "--format=json")
}

func TestPrintRigVerifyJSONIsMachineReadable(t *testing.T) {
	withVerifyFlags(t, "json")
	rep := &rig.Report{
		Rig:      "dsp-2.7.1",
		Compared: 40,
		Drifts:   []rig.Drift{{Path: "google.golang.org/grpc", Kind: rig.DriftUpgraded, Baseline: "v1.68.0", Actual: "v1.69.2"}},
	}

	out := captureStdout(t, func() {
		require.NoError(t, printRigVerify(rep, "/rigs/dsp-2.7.1"))
	})

	var got rig.Report
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "dsp-2.7.1", got.Rig)
	assert.Equal(t, 40, got.Compared)
	require.Len(t, got.Drifts, 1)
	assert.Equal(t, rig.DriftUpgraded, got.Drifts[0].Kind)
	// The advisory prose goes to stderr, so --format=json stays parseable when
	// stdout is piped.
	assert.False(t, strings.Contains(out, "MVS root"))
}

func TestCheckRigVerifyFlags(t *testing.T) {
	withVerifyFlags(t, "text")
	saveF, saveW := rigVerifyFreeze, rigVerifyWriteBack
	t.Cleanup(func() { rigVerifyFreeze, rigVerifyWriteBack = saveF, saveW })

	rigVerifyFreeze, rigVerifyWriteBack = false, false
	assert.NoError(t, checkRigVerifyFlags())

	// Opposites: one pins the workspace back to the baseline, the other
	// overwrites the baseline with the workspace. Running both would leave
	// neither meaning anything, and --write-back is not recoverable.
	rigVerifyFreeze, rigVerifyWriteBack = true, true
	err := checkRigVerifyFlags()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--freeze and --write-back")

	rigVerifyFreeze, rigVerifyWriteBack = false, false
	rigVerifyFormat = "yaml"
	err = checkRigVerifyFlags()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "yaml")
	assert.Contains(t, err.Error(), "text or json")
}
