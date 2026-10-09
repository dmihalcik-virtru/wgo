package review

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renderFixture() *Graph {
	return &Graph{
		Label: "2026 <&> \"q\"",
		Me:    "person:me",
		Nodes: []Node{
			{ID: "person:me", Kind: KindPerson, Label: "me", Me: true},
			{ID: "pr:1", Kind: KindPR, Label: "evil </script><!-- \u2028\u2029 & title"},
		},
		Edges:  []Edge{{Source: "person:me", Target: "pr:1", Kind: "authored"}},
		Facets: map[string]map[string]int{},
	}
}

var graphDataRe = regexp.MustCompile(`(?s)<script id="graph-data" type="application/json">(.*?)</script>`)

func TestRenderStructure(t *testing.T) {
	out, err := Render(renderFixture())
	require.NoError(t, err)
	page := string(out)

	for _, ph := range []string{"/*WGO_STYLE*/", "/*WGO_VENDOR*/", "/*WGO_APP*/", "/*WGO_DATA*/", "/*WGO_TITLE*/"} {
		assert.NotContains(t, page, ph)
	}
	assert.True(t, strings.HasPrefix(page, "<!doctype html>"))
	assert.Contains(t, page, `<div id="app"></div>`)
	assert.Contains(t, page, `name="color-scheme"`)
	assert.Contains(t, page, `name="viewport"`)
	assert.Equal(t, 1, strings.Count(page, "</html>"))
	assert.NotRegexp(t, `(src|href)=["']https?:`, page)
	// 3 inline scripts: data, vendor, app.
	assert.Equal(t, 3, strings.Count(page, "<script"))
	assert.Equal(t, 3, strings.Count(page, "</script>"))
	assert.Equal(t, 1, strings.Count(page, "</style>"))
}

func TestRenderDataRoundTrip(t *testing.T) {
	g := renderFixture()
	out, err := Render(g)
	require.NoError(t, err)

	m := graphDataRe.FindSubmatch(out)
	require.Len(t, m, 2)
	var got Graph
	require.NoError(t, json.Unmarshal(m[1], &got))
	assert.Len(t, got.Nodes, len(g.Nodes))
	assert.Len(t, got.Edges, len(g.Edges))
	assert.Equal(t, g.Nodes[1].Label, got.Nodes[1].Label)
}

func TestRenderEscapesDataAndTitle(t *testing.T) {
	out, err := Render(renderFixture())
	require.NoError(t, err)
	page := string(out)

	m := graphDataRe.FindSubmatch(out)
	require.Len(t, m, 2)
	data := string(m[1])
	for _, bad := range []string{"</script", "<!--", "<", ">", "&", "\u2028", "\u2029"} {
		assert.NotContains(t, data, bad)
	}
	assert.Contains(t, data, `\u003c/script\u003e`)

	assert.Contains(t, page, "<title>wgo review — 2026 &lt;&amp;&gt; &#34;q&#34;</title>")
}

func TestRenderDoesNotReexpandPlaceholdersInData(t *testing.T) {
	g := renderFixture()
	g.Nodes[1].Label = "/*WGO_APP*/"
	out, err := Render(g)
	require.NoError(t, err)
	m := graphDataRe.FindSubmatch(out)
	require.Len(t, m, 2)
	assert.Contains(t, string(m[1]), "/*WGO_APP*/")
}

func TestRenderDeterministic(t *testing.T) {
	a, err := Render(renderFixture())
	require.NoError(t, err)
	b, err := Render(renderFixture())
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

func TestContainsFold(t *testing.T) {
	assert.True(t, containsFold("a </SCRIPT> b", "</script"))
	assert.False(t, containsFold("a <\\/script> b", "</script"))
}
