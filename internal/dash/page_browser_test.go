package dash

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// probeAddon is an addon view as a later spec ships one: it registers
// through window.WGOLive before the page starts and draws from ctx.
const probeAddon = `(function () {
  window.WGOLive.registerView({ id: "probe", label: "Probe", question: "Do addon views appear?",
    render: function (ctx) { ctx.container.textContent = "probe view: " + (ctx.query ? ctx.query.rows.length : 0) + " rows"; } });
})();`

// TestLivePageInBrowser drives the live page in headless Chrome
// (testdata/live_browser.js): the URL round-trip and history, chart clicks
// as clauses, the query bar, an addon view as a tab, and Copy as Markdown
// under the page's CSP, with no console errors. It needs node and Chrome,
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
	srv := newServerOpts(t, HandlerOptions{Source: StaticSource{V: v}, Addons: []string{"web/live-probe.js"},
		AddonFS: fstest.MapFS{"web/live-probe.js": {Data: []byte(probeAddon)}}})
	out, err := exec.Command(node, filepath.Join("testdata", "live_browser.js"), chrome, srv.URL+"/", os.Getenv("WGO_SHOTS")).CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "ok ") {
		t.Fatalf("live_browser.js: %v\n%s", err, out)
	}
	t.Logf("browser: %s", strings.TrimSpace(string(out)))
}
