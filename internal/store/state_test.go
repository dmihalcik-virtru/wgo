package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetSpecPreservesPurpose(t *testing.T) {
	state := NewState()
	state.AddAnnotation("/repo", "WGO-101-spec-scaffold", "WGO-101: scaffold specs")

	state.SetSpec("/repo", "WGO-101-spec-scaffold", "spec/WGO-101.md", "draft")

	ann := state.GetAnnotation("/repo", "WGO-101-spec-scaffold")
	require.NotNil(t, ann)
	assert.Equal(t, "WGO-101: scaffold specs", ann.Purpose)
	assert.Equal(t, "spec/WGO-101.md", ann.SpecPath)
	assert.Equal(t, "draft", ann.SpecState)
}

func TestAddAnnotationPreservesSpecMetadata(t *testing.T) {
	state := NewState()
	state.SetSpec("/repo", "WGO-101-spec-scaffold", "spec/WGO-101.md", "draft")

	state.AddAnnotation("/repo", "WGO-101-spec-scaffold", "updated reason")

	ann := state.GetAnnotation("/repo", "WGO-101-spec-scaffold")
	require.NotNil(t, ann)
	assert.Equal(t, "spec/WGO-101.md", ann.SpecPath)
	assert.Equal(t, "draft", ann.SpecState)
}
