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

func TestAddAnnotationPreservesStackMetadata(t *testing.T) {
	s := NewState()
	s.AddAnnotation("/repo", "child", "child branch")
	s.SetParents("/repo", "child", []string{"/repo:parent"})
	s.SetStackID("/repo", "child", "stack-1")

	s.AddAnnotation("/repo", "child", "updated child branch")

	ann := s.GetAnnotation("/repo", "child")
	require.NotNil(t, ann)
	assert.Equal(t, "updated child branch", ann.Purpose)
	assert.Equal(t, []string{"/repo:parent"}, ann.Parents)
	assert.Equal(t, "stack-1", ann.StackID)
}

func TestSetParentsCopiesInput(t *testing.T) {
	s := NewState()
	parents := []string{"/repo:a", "/repo:b"}
	s.SetParents("/repo", "child", parents)

	parents[0] = "MUTATED"
	ann := s.GetAnnotation("/repo", "child")
	require.NotNil(t, ann)
	assert.Equal(t, []string{"/repo:a", "/repo:b"}, ann.Parents, "SetParents must copy the slice")
}

func TestAddStackPreservesCreatedAt(t *testing.T) {
	s := NewState()
	s.AddStack(Stack{ID: "s1", Name: "first", RootRef: "origin/main"})
	created := s.GetStack("s1").CreatedAt

	s.AddStack(Stack{ID: "s1", Name: "renamed", RootRef: "origin/main"})

	got := s.GetStack("s1")
	require.NotNil(t, got)
	assert.Equal(t, "renamed", got.Name)
	assert.Equal(t, created, got.CreatedAt, "CreatedAt must be preserved across updates")
	assert.True(t, got.UpdatedAt.After(created) || got.UpdatedAt.Equal(created))
}

func TestRemoveStackClearsAnnotationStackID(t *testing.T) {
	s := NewState()
	s.AddStack(Stack{ID: "s1", Name: "first"})
	s.AddAnnotation("/repo", "a", "")
	s.AddAnnotation("/repo", "b", "")
	s.SetStackID("/repo", "a", "s1")
	s.SetStackID("/repo", "b", "s1")
	s.SetParents("/repo", "b", []string{"/repo:a"})

	s.RemoveStack("s1")

	assert.Nil(t, s.GetStack("s1"))
	assert.Equal(t, "", s.GetAnnotation("/repo", "a").StackID)
	assert.Equal(t, "", s.GetAnnotation("/repo", "b").StackID)
	assert.Equal(t, []string{"/repo:a"}, s.GetAnnotation("/repo", "b").Parents,
		"Parents links survive RemoveStack so the DAG stays queryable")
}

func TestAnnotationsInStackSortedAndFiltered(t *testing.T) {
	s := NewState()
	for _, b := range []string{"c", "a", "b"} {
		s.AddAnnotation("/repo", b, "")
		s.SetStackID("/repo", b, "s1")
	}
	s.AddAnnotation("/repo", "other", "")
	s.SetStackID("/repo", "other", "s2")

	got := s.AnnotationsInStack("s1")
	assert.Equal(t, []string{"/repo:a", "/repo:b", "/repo:c"}, got)
	assert.Nil(t, s.AnnotationsInStack(""))
}
