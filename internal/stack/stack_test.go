package stack

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/virtru/wgo/internal/github"
)

// fakeGitHub serves PRs from in-memory fixtures keyed by branch and number.
type fakeGitHub struct {
	byNumber map[int]*github.PRInfo
	byHead   map[string]*github.PRInfo  // first OPEN pr whose head == branch
	byBase   map[string][]github.PRInfo // OPEN prs whose base == branch
}

func (f *fakeGitHub) GetPRByNumber(_ string, n int) (*github.PRInfo, error) {
	return f.byNumber[n], nil
}
func (f *fakeGitHub) GetPRStatus(_, branch string) (*github.PRInfo, error) {
	return f.byHead[branch], nil
}
func (f *fakeGitHub) ListPRsByBase(_, base string) ([]github.PRInfo, error) {
	return f.byBase[base], nil
}

// linearStack builds trunk(main) ← #1 a ← #2 b ← #3 c.
func linearStack() *fakeGitHub {
	pr := func(n int, head, base string) *github.PRInfo {
		return &github.PRInfo{Number: n, State: "open", Branch: head, BaseRefName: base,
			HeadSHA: "sha-" + head, HeadRepoSlug: "o/r"}
	}
	a, b, c := pr(1, "a", "main"), pr(2, "b", "a"), pr(3, "c", "b")
	return &fakeGitHub{
		byNumber: map[int]*github.PRInfo{1: a, 2: b, 3: c},
		byHead:   map[string]*github.PRInfo{"a": a, "b": b, "c": c},
		byBase: map[string][]github.PRInfo{
			"a": {*b}, "b": {*c},
		},
	}
}

func branches(members []StackMember) []string {
	out := make([]string, len(members))
	for i, m := range members {
		out[i] = m.Branch
	}
	return out
}

func TestResolveStack_LonePR(t *testing.T) {
	pr := &github.PRInfo{Number: 5, State: "open", Branch: "solo", BaseRefName: "main", HeadRepoSlug: "o/r"}
	gh := &fakeGitHub{
		byNumber: map[int]*github.PRInfo{5: pr},
		byHead:   map[string]*github.PRInfo{"solo": pr},
		byBase:   map[string][]github.PRInfo{},
	}
	members, err := ResolveStack(gh, "/tmp", PRRef{Number: 5})
	require.NoError(t, err)
	require.Len(t, members, 1)
	assert.Equal(t, "solo", members[0].Branch)
	assert.Equal(t, 5, members[0].PRNumber)
}

func TestResolveStack_SeedFromBottom(t *testing.T) {
	members, err := ResolveStack(linearStack(), "/tmp", PRRef{Number: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, branches(members))
}

func TestResolveStack_SeedFromMiddle(t *testing.T) {
	members, err := ResolveStack(linearStack(), "/tmp", PRRef{Number: 2})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, branches(members))
}

func TestResolveStack_SeedFromTop(t *testing.T) {
	members, err := ResolveStack(linearStack(), "/tmp", PRRef{Number: 3})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, branches(members))
	// Bases chain correctly.
	assert.Equal(t, "main", members[0].Base)
	assert.Equal(t, "a", members[1].Base)
	assert.Equal(t, "b", members[2].Base)
}

func TestResolveStack_SeedByBranch(t *testing.T) {
	members, err := ResolveStack(linearStack(), "/tmp", PRRef{Branch: "b"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, branches(members))
}

func TestResolveStack_StopsAtTrunk(t *testing.T) {
	// #1 a's base is "main", which has no open PR (not in byHead) → stop.
	members, err := ResolveStack(linearStack(), "/tmp", PRRef{Number: 1})
	require.NoError(t, err)
	require.NotEmpty(t, members)
	assert.Equal(t, "a", members[0].Branch, "does not descend past trunk")
}

func TestResolveStack_NoSeedPR(t *testing.T) {
	gh := &fakeGitHub{byNumber: map[int]*github.PRInfo{}, byHead: map[string]*github.PRInfo{}, byBase: map[string][]github.PRInfo{}}
	members, err := ResolveStack(gh, "/tmp", PRRef{Number: 99})
	require.NoError(t, err)
	assert.Nil(t, members)
}

func TestResolveStack_CycleGuard(t *testing.T) {
	// a → b → a (malformed): base refs form a cycle. Must terminate.
	a := &github.PRInfo{Number: 1, State: "open", Branch: "a", BaseRefName: "b", HeadRepoSlug: "o/r"}
	b := &github.PRInfo{Number: 2, State: "open", Branch: "b", BaseRefName: "a", HeadRepoSlug: "o/r"}
	gh := &fakeGitHub{
		byNumber: map[int]*github.PRInfo{1: a, 2: b},
		byHead:   map[string]*github.PRInfo{"a": a, "b": b},
		byBase:   map[string][]github.PRInfo{"a": {*b}, "b": {*a}},
	}
	members, err := ResolveStack(gh, "/tmp", PRRef{Number: 1})
	require.NoError(t, err)
	// Both nodes visited exactly once; no infinite loop.
	assert.Len(t, members, 2)
}

func TestResolveStack_ForkChildCarriesSlug(t *testing.T) {
	base := &github.PRInfo{Number: 1, State: "open", Branch: "a", BaseRefName: "main", HeadRepoSlug: "o/r"}
	fork := &github.PRInfo{Number: 2, State: "open", Branch: "b", BaseRefName: "a", HeadRepoSlug: "contributor/r", HeadSHA: "fsha"}
	gh := &fakeGitHub{
		byNumber: map[int]*github.PRInfo{1: base, 2: fork},
		byHead:   map[string]*github.PRInfo{"a": base, "b": fork},
		byBase:   map[string][]github.PRInfo{"a": {*fork}},
	}
	members, err := ResolveStack(gh, "/tmp", PRRef{Number: 1})
	require.NoError(t, err)
	require.Len(t, members, 2)
	assert.Equal(t, "contributor/r", members[1].HeadSlug)
	assert.Equal(t, "fsha", members[1].HeadOID)
}

func TestResolveStack_MultiChildBFS(t *testing.T) {
	// a ← {b, c}: one node with two open children (fan-out).
	a := &github.PRInfo{Number: 1, State: "open", Branch: "a", BaseRefName: "main", HeadRepoSlug: "o/r"}
	b := &github.PRInfo{Number: 2, State: "open", Branch: "b", BaseRefName: "a", HeadRepoSlug: "o/r"}
	c := &github.PRInfo{Number: 3, State: "open", Branch: "c", BaseRefName: "a", HeadRepoSlug: "o/r"}
	gh := &fakeGitHub{
		byNumber: map[int]*github.PRInfo{1: a, 2: b, 3: c},
		byHead:   map[string]*github.PRInfo{"a": a, "b": b, "c": c},
		byBase:   map[string][]github.PRInfo{"a": {*c, *b}}, // unsorted on purpose
	}
	members, err := ResolveStack(gh, "/tmp", PRRef{Number: 1})
	require.NoError(t, err)
	// a first (parent), then children by ascending PR number (deterministic).
	assert.Equal(t, []string{"a", "b", "c"}, branches(members))
}
