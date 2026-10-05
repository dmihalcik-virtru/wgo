package review

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLiveJSLogic runs the live explorer's pure functions under node. It is
// skipped where node is not installed; nothing else depends on node.
func TestLiveJSLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the live.js logic tests")
	}
	live, err := filepath.Abs(filepath.Join("web", "live.js"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, filepath.Join("testdata", "live_logic.js"), live).CombinedOutput()
	if err != nil {
		t.Fatalf("node live_logic.js: %v\n%s", err, out)
	}
	if !strings.HasPrefix(string(out), "ok ") {
		t.Fatalf("unexpected output: %s", out)
	}
	t.Logf("live.js logic: %s", strings.TrimSpace(string(out)))
}
