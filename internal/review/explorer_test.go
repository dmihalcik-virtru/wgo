package review

import (
	"strings"
	"testing"

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
	_, err := RenderPage(Page{Mode: "other"})
	assert.Error(t, err)
	_, err = RenderPage(Page{Mode: ModeReview})
	assert.Error(t, err, "review mode needs a graph")
}
