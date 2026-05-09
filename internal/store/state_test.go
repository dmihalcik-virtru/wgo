package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetSpecPreservesPurpose(t *testing.T) {
	s := NewState()
	s.AddAnnotation("/repo", "main", "initial purpose")

	s.SetSpec("/repo", "main", "spec/FOO-1.md", "draft")

	ann := s.GetAnnotation("/repo", "main")
	require.NotNil(t, ann)
	assert.Equal(t, "initial purpose", ann.Purpose)
	assert.Equal(t, "spec/FOO-1.md", ann.SpecPath)
	assert.Equal(t, "draft", ann.SpecState)
}

func TestAddAnnotationPreservesSpecMetadata(t *testing.T) {
	s := NewState()
	s.AddAnnotation("/repo", "main", "first purpose")
	s.SetSpec("/repo", "main", "spec/FOO-1.md", "draft")

	s.AddAnnotation("/repo", "main", "updated purpose")

	ann := s.GetAnnotation("/repo", "main")
	require.NotNil(t, ann)
	assert.Equal(t, "updated purpose", ann.Purpose)
	assert.Equal(t, "spec/FOO-1.md", ann.SpecPath, "SetSpec metadata should survive AddAnnotation")
	assert.Equal(t, "draft", ann.SpecState, "SetSpec metadata should survive AddAnnotation")
}
