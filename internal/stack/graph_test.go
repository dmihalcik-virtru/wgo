package stack

import (
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/virtru/wgo/internal/store"
)

// stateFromEdges builds a State where every entry in `edges` maps a child
// branch name to its parent names (all in /repo). All branches end up in
// stack "s1". Empty parent list == root.
func stateFromEdges(edges map[string][]string) *store.State {
	s := store.NewState()
	s.AddStack(store.Stack{ID: "s1", Name: "test"})
	for child, parents := range edges {
		s.AddAnnotation("/repo", child, "")
		s.SetStackID("/repo", child, "s1")
		var parentKeys []string
		for _, p := range parents {
			parentKeys = append(parentKeys, store.AnnotationKey("/repo", p))
		}
		s.SetParents("/repo", child, parentKeys)
	}
	return s
}

func TestBuildLinearStack(t *testing.T) {
	// a <- b <- c
	s := stateFromEdges(map[string][]string{
		"a": nil,
		"b": {"a"},
		"c": {"b"},
	})

	g, err := Build(s, "s1")
	require.NoError(t, err)

	assert.Equal(t, []string{"/repo:a"}, g.Roots())
	assert.Equal(t, []string{"/repo:c"}, g.Leaves())

	order, err := g.TopoSort()
	require.NoError(t, err)
	assert.Equal(t, []string{"/repo:a", "/repo:b", "/repo:c"}, order)
}

func TestBuildFanOut(t *testing.T) {
	// a is the root; b and c both depend on a
	s := stateFromEdges(map[string][]string{
		"a": nil,
		"b": {"a"},
		"c": {"a"},
	})

	g, err := Build(s, "s1")
	require.NoError(t, err)

	assert.Equal(t, []string{"/repo:a"}, g.Roots())
	leaves := g.Leaves()
	slices.Sort(leaves)
	assert.Equal(t, []string{"/repo:b", "/repo:c"}, leaves)

	desc, err := g.AffectedDescendants("/repo:a")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"/repo:b", "/repo:c"}, desc)
}

func TestBuildMergeNode(t *testing.T) {
	// a and b are roots; c has both as parents (merge node).
	s := stateFromEdges(map[string][]string{
		"a": nil,
		"b": nil,
		"c": {"a", "b"},
	})

	g, err := Build(s, "s1")
	require.NoError(t, err)

	roots := g.Roots()
	slices.Sort(roots)
	assert.Equal(t, []string{"/repo:a", "/repo:b"}, roots)
	assert.Equal(t, []string{"/repo:c"}, g.Leaves())

	order, err := g.TopoSort()
	require.NoError(t, err)
	// c must come after both a and b.
	indexOf := func(s string) int {
		for i, n := range order {
			if n == s {
				return i
			}
		}
		return -1
	}
	assert.Less(t, indexOf("/repo:a"), indexOf("/repo:c"))
	assert.Less(t, indexOf("/repo:b"), indexOf("/repo:c"))

	// Changing a should require rebasing only c (not b — b is independent).
	desc, err := g.AffectedDescendants("/repo:a")
	require.NoError(t, err)
	assert.Equal(t, []string{"/repo:c"}, desc)
}

func TestBuildCycleRejected(t *testing.T) {
	// a -> b -> c -> a
	s := stateFromEdges(map[string][]string{
		"a": {"c"},
		"b": {"a"},
		"c": {"b"},
	})

	_, err := Build(s, "s1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCycle), "expected ErrCycle, got %v", err)
}

func TestWouldCreateCycle(t *testing.T) {
	// a <- b. Adding b as parent of a closes the loop.
	s := stateFromEdges(map[string][]string{
		"a": nil,
		"b": {"a"},
	})

	assert.True(t, WouldCreateCycle(s, "s1", "/repo:a", "/repo:b"),
		"a depending on b (which already depends on a) is a cycle")
	assert.False(t, WouldCreateCycle(s, "s1", "/repo:c", "/repo:b"),
		"new node c depending on b is fine")
	assert.True(t, WouldCreateCycle(s, "s1", "/repo:a", "/repo:a"),
		"self-parent is always a cycle")
}

func TestAffectedDescendantsTopoOrdered(t *testing.T) {
	// a <- b <- d; a <- c <- d. Changing a must rebase b, c, then d.
	s := stateFromEdges(map[string][]string{
		"a": nil,
		"b": {"a"},
		"c": {"a"},
		"d": {"b", "c"},
	})

	g, err := Build(s, "s1")
	require.NoError(t, err)

	desc, err := g.AffectedDescendants("/repo:a")
	require.NoError(t, err)
	// d must come after both b and c.
	indexOf := func(s string) int {
		for i, n := range desc {
			if n == s {
				return i
			}
		}
		return -1
	}
	assert.Less(t, indexOf("/repo:b"), indexOf("/repo:d"))
	assert.Less(t, indexOf("/repo:c"), indexOf("/repo:d"))
	assert.NotContains(t, desc, "/repo:a", "root itself is excluded from descendants")
}

func TestAffectedDescendantsUnknownNode(t *testing.T) {
	s := stateFromEdges(map[string][]string{"a": nil})
	g, err := Build(s, "s1")
	require.NoError(t, err)

	_, err = g.AffectedDescendants("/repo:nope")
	require.Error(t, err)
}

func TestExternalParentIsRootOfSubgraph(t *testing.T) {
	// b's parent is "/repo:external" which is not in stack s1. b should still
	// be treated as a root of the stack (it rebases onto something outside).
	s := store.NewState()
	s.AddStack(store.Stack{ID: "s1"})
	s.AddAnnotation("/repo", "b", "")
	s.SetStackID("/repo", "b", "s1")
	s.SetParents("/repo", "b", []string{"/repo:external"})

	g, err := Build(s, "s1")
	require.NoError(t, err)

	assert.Equal(t, []string{"/repo:b"}, g.Roots(),
		"a parent outside the stack still makes the node a stack-root")
}

func TestBuildDedupesDuplicateParents(t *testing.T) {
	// State written by an older wgo (or hand-edited state.json) might have
	// duplicate parent entries. Build must dedup so TopoSort doesn't fail
	// with ErrCycle from inflated indegree counts.
	s := store.NewState()
	s.AddStack(store.Stack{ID: "s1"})
	s.AddAnnotation("/repo", "a", "")
	s.AddAnnotation("/repo", "b", "")
	s.SetStackID("/repo", "a", "s1")
	s.SetStackID("/repo", "b", "s1")
	// Bypass SetParents' dedup to simulate dirty state on disk.
	ann := s.Annotations["/repo:b"]
	ann.Parents = []string{"/repo:a", "/repo:a", "/repo:a"}
	s.Annotations["/repo:b"] = ann

	g, err := Build(s, "s1")
	require.NoError(t, err)
	assert.Equal(t, []string{"/repo:a"}, g.Parents["/repo:b"],
		"duplicate parents must be normalized at Build time")

	order, err := g.TopoSort()
	require.NoError(t, err, "duplicate parents must not produce a false cycle")
	assert.Equal(t, []string{"/repo:a", "/repo:b"}, order)
}
