package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/virtru/wgo/internal/stack"
)

func stackMembers() []stack.StackMember {
	return []stack.StackMember{
		{Branch: "a", PRNumber: 1, Base: "main"},
		{Branch: "b", PRNumber: 2, Base: "a"},
		{Branch: "c", PRNumber: 3, Base: "b"},
	}
}

func TestMemberByPR(t *testing.T) {
	members := stackMembers()
	require.NotNil(t, memberByPR(members, 2))
	assert.Equal(t, "b", memberByPR(members, 2).Branch)
	assert.Nil(t, memberByPR(members, 99))
}

func TestMemberByBranch(t *testing.T) {
	members := stackMembers()
	pn, ok := memberByBranch(members, "c")
	assert.True(t, ok)
	assert.Equal(t, 3, pn)
	_, ok = memberByBranch(members, "nope")
	assert.False(t, ok)
}

func TestChooseLandingNode(t *testing.T) {
	members := stackMembers()
	bmFor := map[int]string{1: "a", 2: "b", 3: "c"}
	named := memberByPR(members, 3) // user passed the leaf PR #3
	noExisting := func(string) bool { return false }

	// Default: land on the passed PR's bookmark.
	got, err := chooseLandingNode(members, bmFor, named, 3, "", noExisting)
	require.NoError(t, err)
	assert.Equal(t, "c", got)

	// --on an interior stack member (by branch) lands on that member's bookmark.
	got, err = chooseLandingNode(members, bmFor, named, 3, "a", noExisting)
	require.NoError(t, err)
	assert.Equal(t, "a", got)

	// --on a bookmark outside the stack but existing locally lands on it.
	got, err = chooseLandingNode(members, bmFor, named, 3, "other", func(n string) bool { return n == "other" })
	require.NoError(t, err)
	assert.Equal(t, "other", got)

	// --on that matches neither a stack member nor an existing bookmark errors.
	_, err = chooseLandingNode(members, bmFor, named, 3, "ghost", noExisting)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ghost")
}

func TestChooseLandingNode_PinnedBookmarks(t *testing.T) {
	// When members were pinned (fork / protected), bmFor maps to pr-N-* names.
	members := stackMembers()
	bmFor := map[int]string{1: "pr-1-a", 2: "pr-2-b", 3: "pr-3-c"}
	named := memberByPR(members, 2)

	got, err := chooseLandingNode(members, bmFor, named, 2, "", func(string) bool { return false })
	require.NoError(t, err)
	assert.Equal(t, "pr-2-b", got, "lands on the pinned bookmark of the passed PR")

	got, err = chooseLandingNode(members, bmFor, named, 2, "a", func(string) bool { return false })
	require.NoError(t, err)
	assert.Equal(t, "pr-1-a", got, "--on resolves via stack membership to the pinned bookmark")
}
