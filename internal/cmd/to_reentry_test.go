package cmd

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gh "github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
)

// A PR workspace can be sitting on either bookmark, and the re-entry paths
// (findExistingCheckout, createWorktree) have to accept both — the pinned half
// of them was invisible before.
func TestWorkspaceBookmarkNames(t *testing.T) {
	pr := &gh.ParsedURL{Owner: "o", Repo: "r", Type: gh.URLTypePR, Identifier: "7"}
	// Pin first: a local bookmark named after the head ref may be the divergent
	// one that forced the pin, so it is the weaker candidate. The pin carries
	// the sanitized slug, matching the name trackOrPinMember actually sets.
	assert.Equal(t, []string{"pr-7-feat-x", "feat/x"}, workspaceBookmarkNames(pr, "feat/x"))

	// No PR number, no pin to guess: the head ref is all there is.
	branch := &gh.ParsedURL{Owner: "o", Repo: "r", Type: gh.URLTypeBranch, Identifier: "feat/x"}
	assert.Equal(t, []string{"feat/x"}, workspaceBookmarkNames(branch, "feat/x"))

	issue := &gh.ParsedURL{Owner: "o", Repo: "r", Type: gh.URLTypeIssue, Identifier: "9"}
	assert.Equal(t, []string{"issue-9"}, workspaceBookmarkNames(issue, "issue-9"))

	// A PR whose identifier is not a number would build "pr-%!d(string=...)";
	// fall back to the head ref rather than inventing a bookmark name.
	bogus := &gh.ParsedURL{Owner: "o", Repo: "r", Type: gh.URLTypePR, Identifier: "abc"}
	assert.Equal(t, []string{"feat/x"}, workspaceBookmarkNames(bogus, "feat/x"))
}

func TestFirstLocalBookmark(t *testing.T) {
	want := []string{"pr-7-feat", "feat"}

	// Both exist: preference order decides, not jj's listing order.
	f := &fakeBookmarkLister{bookmarks: []jj.Bookmark{
		{Name: "feat", Present: true},
		{Name: "pr-7-feat", Present: true},
	}}
	got, err := firstLocalBookmark(f, "/repo", want)
	require.NoError(t, err)
	assert.Equal(t, "pr-7-feat", got)
	assert.Equal(t, "/repo", f.listedRepo)
	assert.Equal(t, jj.BookmarkListOpts{Local: true, Names: want}, f.listedOpts)

	// Only the head ref exists: the tracked path, and it is the right target.
	f = &fakeBookmarkLister{bookmarks: []jj.Bookmark{{Name: "feat", Present: true}}}
	got, err = firstLocalBookmark(f, "/repo", want)
	require.NoError(t, err)
	assert.Equal(t, "feat", got)

	// A deleted-but-unpushed bookmark is still listed, with Present=false. It
	// resolves to nothing, so `jj edit` on it would fail.
	f = &fakeBookmarkLister{bookmarks: []jj.Bookmark{{Name: "feat"}}}
	got, err = firstLocalBookmark(f, "/repo", want)
	require.NoError(t, err)
	assert.Empty(t, got)

	// Nothing local: "" and no error, so the caller can warn instead of
	// launching jj at a name that cannot resolve.
	f = &fakeBookmarkLister{}
	got, err = firstLocalBookmark(f, "/repo", want)
	require.NoError(t, err)
	assert.Empty(t, got)

	// A list failure must not read as "no bookmark exists" — that would
	// silently leave @ parked wherever a stale workspace happened to be.
	f = &fakeBookmarkLister{listErr: errors.New("jj exploded")}
	_, err = firstLocalBookmark(f, "/repo", want)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jj exploded")
	assert.Contains(t, err.Error(), "pr-7-feat, feat")
}
