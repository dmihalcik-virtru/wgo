package dash

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// probeAddon is an addon as a later spec ships one: a view and a panel that
// register through window.WGOLive before the page starts, draw from ctx,
// keep their own URL key with ctx.setState, and record what the page tells
// them in window.__probe for live_browser.js to check.
const probeAddon = `(function () {
  var P = window.__probe = { renders: [], notes: 0, destroyed: 0, panelDestroyed: 0, boot: "" };
  var subscribed = false;
  window.WGOLive.registerView({ id: "probe", label: "Probe", question: "Do addon views appear?",
    render: function (ctx) {
      P.renders.push(ctx.reason); P.boot = Object.keys(ctx.boot).sort().join(","); P.ctx = ctx;
      if (!subscribed) { subscribed = true; ctx.subscribe(function () { P.notes++; }); }
      ctx.container.textContent = "probe view: " + (ctx.query ? ctx.query.rows.length : 0) + " rows, x=" + ctx.getState("x");
    },
    destroy: function () { P.destroyed++; subscribed = false; } });
  window.WGOLive.registerPanel({ id: "probe-panel", label: "Probe panel",
    render: function (ctx) { ctx.container.textContent = "probe panel"; },
    destroy: function () { P.panelDestroyed++; } });
})();`

type noAck struct{}

func (noAck) Acknowledge(uint64) error { return nil }

// TestLivePageInBrowser drives the live page in headless Chrome
// (testdata/live_browser.js): the URL round-trip and history model, chart
// clicks as clauses, the query bar, an addon view and panel with their
// ctx (setState, getState, subscribe, destroy, a token-free boot), and
// Copy as Markdown under the page's CSP, with no console errors; then it
// times filter changes on a 2,000-node snapshot. It needs node and Chrome,
// so it runs only when WGO_BROWSER_TESTS is set; WGO_CHROME names the
// browser and WGO_SHOTS a directory for light and dark screenshots.
func TestLivePageInBrowser(t *testing.T) {
	if os.Getenv("WGO_BROWSER_TESTS") == "" {
		t.Skip("set WGO_BROWSER_TESTS=1 (with node and Chrome installed) to drive the page in a browser")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node not on PATH")
	}
	chrome := os.Getenv("WGO_CHROME")
	if chrome == "" {
		chrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	}
	if _, err := os.Stat(chrome); err != nil {
		t.Fatalf("no Chrome at %s; set WGO_CHROME", chrome)
	}
	v, _ := vectorFixture(t)
	// Ack makes the page carry an action token, which addons must not see.
	srv := newServerOpts(t, HandlerOptions{Source: StaticSource{V: v}, Ack: noAck{}, Addons: []string{"web/live-probe.js"},
		AddonFS: fstest.MapFS{"web/live-probe.js": {Data: []byte(probeAddon)}}})
	// A 2,000-node snapshot for the re-render timing.
	var big Snapshot
	raw := genSnapshot(2000)
	if err := json.Unmarshal(raw, &big); err != nil {
		t.Fatal(err)
	}
	bigView, err := newViewJSON(&big, raw, computeDelta(nil, &big), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	perf := newServerOpts(t, HandlerOptions{Source: StaticSource{V: bigView}})
	args := []string{filepath.Join("testdata", "live_browser.js"), chrome, srv.URL + "/", "--perf=" + perf.URL + "/"}
	if dir := os.Getenv("WGO_SHOTS"); dir != "" {
		args = append(args, "--shots="+dir)
	}
	out, err := exec.Command(node, args...).CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "ok ") {
		t.Fatalf("live_browser.js: %v\n%s", err, out)
	}
	t.Logf("browser: %s", strings.TrimSpace(string(out)))
}
