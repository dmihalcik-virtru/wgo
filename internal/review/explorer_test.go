package review

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRenderPageReviewIsRender pins that the mode hook leaves the offline
// export alone: a review page without Boot or Addons is Render's output.
func TestRenderPageReviewIsRender(t *testing.T) {
	g := renderFixture()
	a, err := Render(g)
	require.NoError(t, err)
	b, err := RenderPage(Page{Mode: ModeReview, Graph: g})
	require.NoError(t, err)
	assert.Equal(t, a, b)
	assert.NotContains(t, string(a), "wgo-boot")
}

// TestRenderPageBootIsAppended checks that a served review page is the
// offline page plus a bootstrap element inserted before </body>, and that
// the bootstrap cannot break out of its script element.
func TestRenderPageBootIsAppended(t *testing.T) {
	g := renderFixture()
	offline, err := Render(g)
	require.NoError(t, err)
	served, err := RenderPage(Page{Mode: ModeReview, Graph: g, Boot: map[string]any{"mode": "review", "x": "</script><b>"}})
	require.NoError(t, err)
	body := strings.LastIndex(string(offline), "</body>")
	require.Positive(t, body)
	s := string(served)
	assert.True(t, strings.HasPrefix(s, string(offline[:body])))
	assert.True(t, strings.HasSuffix(s, string(offline[body:])))
	assert.Contains(t, s, `<script id="wgo-boot" type="application/json">{"mode":"review","x":"\u003c/script\u003e\u003cb\u003e"}</script>`)
}

func TestRenderPageRejectsBadAddonsAndModes(t *testing.T) {
	g := renderFixture()
	for _, name := range []string{"web/vendor/d3force.min.js", "../go.mod", "web/style.css", "web/missing.js"} {
		_, err := RenderPage(Page{Mode: ModeReview, Graph: g, Addons: []string{name}})
		assert.Error(t, err, name)
	}
	for _, name := range []string{"web/vendor/x.css", "../go.mod", "web/live.js", "web/missing.css"} {
		_, err := RenderPage(Page{Mode: ModeLive, AddonStyles: []string{name}})
		assert.Error(t, err, name)
	}
	_, err := RenderPage(Page{Mode: ModeLive, AddonStyles: []string{"web/live-x.css"}, AddonFS: fstest.MapFS{"web/live-x.css": {Data: []byte("a { content: \"</STYLE>\" }")}}})
	assert.ErrorContains(t, err, "</style", "a stylesheet must not close the page's <style>")
	page, err := RenderPage(Page{Mode: ModeLive, AddonStyles: []string{"web/live-x.css", "web/live-y.css"},
		AddonFS: fstest.MapFS{"web/live-x.css": {Data: []byte(".x{}")}, "web/live-y.css": {Data: []byte(".y{}")}}})
	require.NoError(t, err)
	css, x, y := strings.Index(string(page), "wgo dash (live mode) additions"), strings.Index(string(page), "<style>\n.x{}\n</style>"), strings.Index(string(page), "<style>\n.y{}\n</style>")
	assert.True(t, css > 0 && x > css && y > x && y < strings.Index(string(page), "</head>"), "addon stylesheets follow live.css, in order, in the head")
	_, err = RenderPage(Page{Mode: "other"})
	assert.Error(t, err)
	_, err = RenderPage(Page{Mode: ModeReview})
	assert.Error(t, err, "review mode needs a graph")
}

// TestServedJSHooksExistInApp pins what web/served.js reaches into app.js
// for. served.js returns silently when a hook is missing, so an app.js
// refactor would otherwise drop every review-to-live link unnoticed.
func TestServedJSHooksExistInApp(t *testing.T) {
	app, err := webFS.ReadFile("web/app.js")
	require.NoError(t, err)
	for _, hook := range []string{
		"window.__wgoStore = store",            // the store handle
		"subscribe: function (fn)",             // store.subscribe
		"select: function (ids, add)",          // store.select
		"opts: { collapse:",                    // store.opts.collapse
		`"selection"`,                          // the notify reason served.js listens for
		`"hdr"`, `"bar"`, `"side"`, `"detail"`, // the elements it decorates
		`"Collapse PRs`, // the checkbox label it flips
	} {
		assert.Contains(t, string(app), hook, "served.js relies on app.js having %s", hook)
	}
}
