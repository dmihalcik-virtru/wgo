package stack

import (
	"errors"
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

func TestRenderEmbedsMachineParseableData(t *testing.T) {
	// The spec promises another machine can rebuild local state by reading
	// GitHub. That means the marker block must carry the topology in a
	// machine-parseable form, not just human-readable labels.
	out := linearStackMarker().Render()
	assert.Contains(t, out, "<!-- wgo-stack-data:")
	assert.Contains(t, out, " -->")

	data, err := ParseNodes(out)
	require.NoError(t, err)
	require.NotNil(t, data)
	assert.Equal(t, "abc123", data.StackID)
	require.Len(t, data.Nodes, 3)

	assert.Equal(t, "refactor", data.Nodes[0].Branch)
	assert.Equal(t, 12, data.Nodes[0].PRNumber)
	assert.Empty(t, data.Nodes[0].Parents, "root has no parents in the wire format")

	assert.Equal(t, "plumbing", data.Nodes[1].Branch)
	assert.Equal(t, []string{"refactor"}, data.Nodes[1].Parents,
		"parents are branch names, not annotation keys")

	assert.Equal(t, "feature", data.Nodes[2].Branch)
	assert.Equal(t, []string{"plumbing"}, data.Nodes[2].Parents)
}

func TestParseNodesNoSidecar(t *testing.T) {
	data, err := ParseNodes("just a PR body, no marker at all")
	require.NoError(t, err)
	assert.Nil(t, data, "absent sidecar must be a clean (nil, nil), not an error")
}

func TestParseNodesMalformedJSON(t *testing.T) {
	body := "<!-- wgo-stack-data:{this is not json -->"
	_, err := ParseNodes(body)
	require.Error(t, err, "malformed sidecar must surface a parse error")
	assert.True(t, errors.Is(err, ErrMalformedMarkerData),
		"malformed sidecar error must be ErrMalformedMarkerData")
}

func TestRenderRoundTripsThroughParseNodes(t *testing.T) {
	// Render → ParseNodes → rebuild a Marker → Render again. The two
	// renders won't match byte-for-byte (the rebuild has no Self) but
	// the ParseNodes wire data MUST match.
	original := linearStackMarker()
	body := original.Render()

	data, err := ParseNodes(body)
	require.NoError(t, err)
	require.NotNil(t, data)

	// Round-trip through Data(): same wire form.
	assert.Equal(t, original.Data(), *data)
}

func TestDataExcludesExternalParents(t *testing.T) {
	// A node whose parent is not in the stack (e.g. an external base like
	// origin/main) must have that parent dropped from the wire form, since
	// a consumer can't translate non-stack keys back to anything useful.
	m := Marker{
		StackID: "s",
		Nodes: []MarkerNode{
			{Key: "/repo:a", Branch: "a", PRNumber: 1, Parents: []string{"/repo:not-in-stack"}},
		},
	}
	data := m.Data()
	require.Len(t, data.Nodes, 1)
	assert.Empty(t, data.Nodes[0].Parents,
		"parents not resolvable to in-stack branches must be omitted")
}

func TestRenderLinksToDocs(t *testing.T) {
	out := linearStackMarker().Render()
	// The trailer must link `wgo stack` to the README section so reviewers
	// landing on a PR have a one-click path to the usage docs.
	assert.Contains(t, out, "[`wgo stack`](https://github.com/dmihalcik-virtru/wgo#stacked-pull-requests)",
		"marker trailer must link wgo stack to the README section")
}
