package jj_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/jjtest"
)

// The comparison helpers address files by fileset; a path with spaces and
// quotes must still match exactly that file.
func TestDiffBetweenQuotesPaths(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	odd := `dir/a "quoted" name.txt`
	jjtest.Commit(t, repo, "add files", map[string]string{odd: "one\n", "plain.txt": "x\n"})
	jjtest.Commit(t, repo, "edit both", map[string]string{odd: "two\n", "plain.txt": "y\n"})

	names, err := c.DiffNamesBetween(repo, "@--", "@-", []string{odd})
	require.NoError(t, err)
	assert.Equal(t, []string{odd}, names)

	out, err := c.DiffGitBetween(repo, "@--", "@-", []string{odd})
	require.NoError(t, err)
	assert.Contains(t, out, "-one")
	assert.Contains(t, out, "+two")
	assert.NotContains(t, out, "plain.txt")

	changes, err := c.DiffSummaryBetween(repo, "root()", "@-")
	require.NoError(t, err)
	assert.Len(t, changes, 2)
	added, deleted, err := c.DiffStatBetween(repo, "@--", "@-")
	require.NoError(t, err)
	assert.Equal(t, [2]int{2, 2}, [2]int{added, deleted})
}

func TestLogMembershipAndRefNames(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	jjtest.Commit(t, repo, "base", map[string]string{"a.txt": "a\n"})
	jjtest.Bookmark(t, repo, "feature", "@-")
	jjtest.Commit(t, repo, "on top", map[string]string{"b.txt": "b\n"})

	got, err := c.LogMembership(repo, "@ | @- | @--", []string{"bookmarks()", "::feature", "@"})
	require.NoError(t, err)
	require.Len(t, got, 3)
	byDesc := map[string][]bool{}
	for _, m := range got {
		// First line only: a contributor's jj config may add trailers.
		desc, _, _ := strings.Cut(m.Description, "\n")
		byDesc[desc] = m.In
		if desc == "base" {
			assert.Equal(t, []string{"feature"}, m.LocalBookmarks)
		}
	}
	assert.Equal(t, []bool{true, true, false}, byDesc["base"])
	assert.Equal(t, []bool{false, false, false}, byDesc["on top"])
	assert.Equal(t, []bool{false, false, true}, byDesc[""], "the working copy")

	names, err := c.RefNames(repo, "::@", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"feature"}, names)
}
