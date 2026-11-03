package dash

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// browserSuite is an addon's suite in testdata/live_browser.js (its header
// describes them): TestLivePageInBrowser serves a page with the handler
// options options returns and passes its URL as --<flag>=<URL>, which the
// suite the addon's block registered under that flag drives. Each addon's
// _test.go file appends its own to browserSuites in an init function.
type browserSuite struct {
	flag    string
	options func(t testing.TB) HandlerOptions
}

// browserSuites are the addons' browser suites, in the order they run.
var browserSuites []browserSuite

type noAck struct{}

func (noAck) Acknowledge(uint64) error { return nil }

// TestLivePageInBrowser drives the live page in headless Chrome
// (testdata/live_browser.js): the URL round-trip and history model, chart
// clicks as clauses, the query bar, an addon view and panel with their
// ctx (setState, getState, subscribe, destroy, a token-free boot), the
// analysis table (gh-101: keyboard column chooser, sort kept across a
// reload, kind: rows, the context toggle, row keys and hover), and Copy as
// Markdown under the page's CSP, with no console errors; then it times
// filter changes on a 2,000-node snapshot, and the table on the analysis
// fixture scaled to 2,000 nodes; then every addon's suite in browserSuites
// (gh-102's linked filters and gh-103's scatterplot and CI grid on the
// analysis fixture). It needs node and Chrome,
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
	// The page inlines the real addons, then the probe; AddonFS replaces the
	// embedded files, so it carries the real ones too.
	addons := fstest.MapFS{"web/live-probe.js": {Data: []byte(probeAddon)}}
	scripts, styles := liveAddonFiles(liveAddons)
	for _, name := range append(slices.Clone(scripts), styles...) {
		b, err := os.ReadFile(filepath.Join("..", "review", name))
		if err != nil {
			t.Fatal(err)
		}
		addons[name] = &fstest.MapFile{Data: b}
	}
	// Ack makes the page carry an action token, which addons must not see.
	srv := newServerOpts(t, HandlerOptions{Source: StaticSource{V: v}, Ack: noAck{}, Addons: append(scripts, "web/live-probe.js"),
		AddonStyles: styles, AddonFS: addons})
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
	// The analysis fixture scaled to 2,000 nodes, for the table's timing.
	scaled := scaleAnalysisFixture(t, loadAnalysisFixture(t), 2000)
	scaledRaw, err := json.Marshal(scaled)
	if err != nil {
		t.Fatal(err)
	}
	scaledView, err := newViewJSON(scaled, scaledRaw, computeDelta(nil, scaled), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	table := newServerOpts(t, HandlerOptions{Source: StaticSource{V: scaledView}})
	args := []string{filepath.Join("testdata", "live_browser.js"), chrome, srv.URL + "/", "--perf=" + perf.URL + "/", "--perf-table=" + table.URL + "/"}
	for _, s := range browserSuites {
		args = append(args, "--"+s.flag+"="+newServerOpts(t, s.options(t)).URL+"/")
	}
	if dir := os.Getenv("WGO_SHOTS"); dir != "" {
		args = append(args, "--shots="+dir)
	}
	out, err := exec.Command(node, args...).CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "ok ") {
		t.Fatalf("live_browser.js: %v\n%s", err, out)
	}
	t.Logf("browser: %s", strings.TrimSpace(string(out)))
}
