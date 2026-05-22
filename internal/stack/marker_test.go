package stack

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func linearStackMarker() Marker {
	return Marker{
		StackID: "abc123",
		Self:    "/repo:b",
		Nodes: []MarkerNode{
			{Key: "/repo:a", Branch: "refactor", PRNumber: 12},
			{Key: "/repo:b", Branch: "plumbing", PRNumber: 13, Parents: []string{"/repo:a"}},
			{Key: "/repo:c", Branch: "feature", PRNumber: 14, Parents: []string{"/repo:b"}},
		},
	}
}

func TestRenderContainsExpectedAnchors(t *testing.T) {
	out := linearStackMarker().Render()

	assert.True(t, strings.HasPrefix(out, "<!-- wgo-stack:abc123 -->"))
	assert.True(t, strings.HasSuffix(out, "<!-- /wgo-stack -->"))
	assert.Contains(t, out, "#12 refactor ← root")
	assert.Contains(t, out, "**#13 plumbing ↳ on #12** ← this PR")
	assert.Contains(t, out, "#14 feature ↳ on #13")
}

func TestRenderIsStable(t *testing.T) {
	first := linearStackMarker().Render()
	second := linearStackMarker().Render()
	assert.Equal(t, first, second, "render must be deterministic")
}

func TestRenderNoPRYet(t *testing.T) {
	m := Marker{
		StackID: "s1",
		Self:    "/repo:b",
		Nodes: []MarkerNode{
			{Key: "/repo:a", Branch: "refactor", PRNumber: 12},
			{Key: "/repo:b", Branch: "plumbing", PRNumber: 0, Parents: []string{"/repo:a"}},
		},
	}
	out := m.Render()
	assert.Contains(t, out, "plumbing _(no PR)_")
	assert.Contains(t, out, "↳ on #12")
}

func TestApplyMarkerAppendsWhenAbsent(t *testing.T) {
	body := "Original PR description.\n\nMore text."
	rendered := linearStackMarker().Render()

	out := ApplyMarker(body, rendered)
	assert.True(t, strings.HasPrefix(out, "Original PR description."))
	assert.Contains(t, out, "<!-- wgo-stack:abc123 -->")
	assert.Contains(t, out, "<!-- /wgo-stack -->")
}

func TestApplyMarkerReplacesExisting(t *testing.T) {
	rendered1 := linearStackMarker().Render()
	body := "Intro\n\n" + rendered1 + "\n\nOutro"

	// Build a different marker (replace stack id) and ensure the old block is gone.
	updated := linearStackMarker()
	updated.StackID = "newid"
	rendered2 := updated.Render()

	out := ApplyMarker(body, rendered2)
	assert.Contains(t, out, "Intro")
	assert.Contains(t, out, "Outro")
	assert.Contains(t, out, "<!-- wgo-stack:newid -->")
	assert.NotContains(t, out, "<!-- wgo-stack:abc123 -->")
}

func TestApplyMarkerRoundTrip(t *testing.T) {
	rendered := linearStackMarker().Render()
	body := ApplyMarker("Body.", rendered)

	out := ApplyMarker(body, rendered)
	// Idempotent: applying the same marker twice should not duplicate it.
	assert.Equal(t, 1, strings.Count(out, "<!-- wgo-stack:abc123 -->"))
	assert.Equal(t, 1, strings.Count(out, "<!-- /wgo-stack -->"))
}

func TestStripMarker(t *testing.T) {
	rendered := linearStackMarker().Render()
	body := "Intro\n\n" + rendered + "\n\nOutro"

	out := StripMarker(body)
	assert.NotContains(t, out, "wgo-stack")
	assert.Contains(t, out, "Intro")
	assert.Contains(t, out, "Outro")
}

func TestExtractStackID(t *testing.T) {
	rendered := linearStackMarker().Render()
	body := "PR description\n\n" + rendered + "\n"

	assert.Equal(t, "abc123", ExtractStackID(body))
	assert.Equal(t, "", ExtractStackID("no marker here"))
}

func TestApplyMarkerHandlesDollarSignsInRendered(t *testing.T) {
	// Body has no marker; the rendered string contains "$1" which would be
	// interpreted as a regex backreference if we didn't escape it.
	rendered := "<!-- wgo-stack:x -->\n$1 is fine\n<!-- /wgo-stack -->"
	body := "first version " + rendered

	out := ApplyMarker(body, "<!-- wgo-stack:y -->\nuses $2 too\n<!-- /wgo-stack -->")
	assert.Contains(t, out, "uses $2 too", "$N must not be treated as backref")
	assert.NotContains(t, out, "<!-- wgo-stack:x -->")
}

func TestMalformedMarkerTreatedAsAbsent(t *testing.T) {
	// Missing close marker → ExtractStackID returns empty, ApplyMarker appends.
	body := "PR body <!-- wgo-stack:foo --> orphan open"
	assert.Equal(t, "", ExtractStackID(body))

	rendered := linearStackMarker().Render()
	out := ApplyMarker(body, rendered)
	// Original orphan stays, new block is appended.
	assert.Contains(t, out, "orphan open")
	assert.Contains(t, out, "<!-- wgo-stack:abc123 -->")
}

func TestRenderEmptyNodes(t *testing.T) {
	out := Marker{StackID: "empty"}.Render()
	require.Contains(t, out, "_no members_")
	assert.Contains(t, out, "<!-- wgo-stack:empty -->")
}
