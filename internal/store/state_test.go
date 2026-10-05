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

func TestRemoveAnnotation(t *testing.T) {
	s := NewState()
	s.AddAnnotation("/repo", "foo", "purpose")
	require.NotNil(t, s.GetAnnotation("/repo", "foo"))

	s.RemoveAnnotation("/repo", "foo")
	assert.Nil(t, s.GetAnnotation("/repo", "foo"))
}

func TestUntrackRepoRemovesAnnotations(t *testing.T) {
	s := NewState()
	s.AddRepo("/repo", "git@example.com:org/repo.git")
	s.AddAnnotation("/repo", "main", "")
	s.AddAnnotation("/repo", "feature", "")
	s.AddAnnotation("/other", "keep", "")

	s.UntrackRepo("/repo")

	assert.Nil(t, s.GetRepo("/repo"))
	assert.Nil(t, s.GetAnnotation("/repo", "main"))
	assert.Nil(t, s.GetAnnotation("/repo", "feature"))
	assert.NotNil(t, s.GetAnnotation("/other", "keep"), "untracking one repo must not affect others")
}

func TestNewStateAtCurrentVersion(t *testing.T) {
	s := NewState()
	assert.Equal(t, StateVersion, s.Version)
}
