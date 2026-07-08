package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/jj"
)

// fakeBookmarkLister is a tiny bookmarkLister for exercising the tracking
// decision helpers without a full jj.Client.
type fakeBookmarkLister struct {
	bookmarks []jj.Bookmark
}

func (f *fakeBookmarkLister) BookmarkList(string, jj.BookmarkListOpts) ([]jj.Bookmark, error) {
	return f.bookmarks, nil
}

func protectedCfg() *config.Config {
	return &config.Config{
		Doctor: config.DoctorConfig{
			ExcludeBookmarks: []string{"main", "master", "develop", "release/*"},
		},
	}
}

func TestShouldTrack(t *testing.T) {
	cfg := protectedCfg()
	cases := map[string]bool{
		"DSPX-3302-03-multi-instance": true,
		"feature/foo":                 true,
		"main":                        false,
		"master":                      false,
		"develop":                     false,
		"release/1.2":                 false,
	}
	for branch, want := range cases {
		if got := shouldTrack(cfg, branch); got != want {
			t.Errorf("shouldTrack(%q) = %v, want %v", branch, got, want)
		}
	}
}

func TestLocalBookmarkConflicts(t *testing.T) {
	const name, oid = "feat", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	// No local bookmark: no conflict.
	f := &fakeBookmarkLister{}
	assert.False(t, localBookmarkConflicts(f, "/repo", name, oid))

	// Local bookmark at the same OID: no conflict (idempotent re-track).
	f = &fakeBookmarkLister{bookmarks: []jj.Bookmark{
		{Name: name, Present: true, CommitID: oid},
	}}
	assert.False(t, localBookmarkConflicts(f, "/repo", name, oid))

	// Local bookmark at a different OID: conflict — don't clobber.
	f = &fakeBookmarkLister{bookmarks: []jj.Bookmark{
		{Name: name, Present: true, CommitID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}}
	assert.True(t, localBookmarkConflicts(f, "/repo", name, oid))

	// Conflicted local bookmark: conflict.
	f = &fakeBookmarkLister{bookmarks: []jj.Bookmark{
		{Name: name, Present: true, Conflict: true},
	}}
	assert.True(t, localBookmarkConflicts(f, "/repo", name, oid))

	// A remote entry of the same name is not a local conflict.
	f = &fakeBookmarkLister{bookmarks: []jj.Bookmark{
		{Name: name, Remote: "origin", Present: true, CommitID: "cccccccccccccccccccccccccccccccccccccccc"},
	}}
	assert.False(t, localBookmarkConflicts(f, "/repo", name, oid))
}

func TestRemoteBookmarkTrackable(t *testing.T) {
	const name = "feat"

	// Untracked remote bookmark on origin: trackable.
	f := &fakeBookmarkLister{bookmarks: []jj.Bookmark{
		{Name: name, Remote: "origin", Present: true, Tracked: false},
	}}
	assert.True(t, remoteBookmarkTrackable(f, "/repo", name, "origin"))

	// Already tracked: nothing useful to do.
	f = &fakeBookmarkLister{bookmarks: []jj.Bookmark{
		{Name: name, Remote: "origin", Present: true, Tracked: true},
	}}
	assert.False(t, remoteBookmarkTrackable(f, "/repo", name, "origin"))

	// Local-only branch (no remote counterpart): not trackable.
	f = &fakeBookmarkLister{bookmarks: []jj.Bookmark{
		{Name: name, Remote: "", Present: true},
	}}
	assert.False(t, remoteBookmarkTrackable(f, "/repo", name, "origin"))

	// Remote bookmark on a different remote: not trackable against origin.
	f = &fakeBookmarkLister{bookmarks: []jj.Bookmark{
		{Name: name, Remote: "upstream", Present: true, Tracked: false},
	}}
	assert.False(t, remoteBookmarkTrackable(f, "/repo", name, "origin"))
}
