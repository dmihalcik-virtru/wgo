package review

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLiveJSLogic runs the live explorer's pure functions under node. It is
// skipped where node is not installed, except under CI, where a missing node
// would silently drop the only check of the explorer's linked selection.
func TestLiveJSLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node not on PATH under CI; the live.js logic tests need it")
		}
		t.Skip("node not on PATH; skipping the live.js logic tests")
	}
	live, err := filepath.Abs(filepath.Join("web", "live.js"))
	if err != nil {
		t.Fatal(err)
	}
	// The query vectors and the field registry are internal/dash's; the JS
	// evaluator must agree with the Go one on every case.
	dashData := filepath.Join("..", "dash", "testdata")
	out, err := exec.Command(node, filepath.Join("testdata", "live_logic.js"), live,
		filepath.Join(dashData, "fields.json"), filepath.Join(dashData, "query_vectors.json")).CombinedOutput()
	if err != nil {
		t.Fatalf("node live_logic.js: %v\n%s", err, out)
	}
	if !strings.HasPrefix(string(out), "ok ") {
		t.Fatalf("unexpected output: %s", out)
	}
	t.Logf("live.js logic: %s", strings.TrimSpace(string(out)))
}
