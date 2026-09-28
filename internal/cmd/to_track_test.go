package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/jjtest"
	"github.com/virtru/wgo/internal/stack"
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

// fakeMemberBookmarker records the mutating bookmark calls trackOrPinMember
// makes, so a test can assert *how* a bookmark was established, not just that
// the call succeeded.
type fakeMemberBookmarker struct {
	fakeBookmarkLister
	tracked []string // "name@remote"
	sets    []setCall
}

type setCall struct {
	name           string
	revset         string
	allowBackwards bool
}

func (f *fakeMemberBookmarker) BookmarkTrack(_, name, remote string) error {
	f.tracked = append(f.tracked, name+"@"+remote)
	return nil
}

func (f *fakeMemberBookmarker) BookmarkSet(_, name, revset string, allowBackwards bool) error {
	f.sets = append(f.sets, setCall{name, revset, allowBackwards})
	return nil
}

func TestTrackOrPinMemberTracks(t *testing.T) {
	const oid = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	m := &stack.StackMember{Branch: "feat", PRNumber: 7, HeadOID: oid}

	f := &fakeMemberBookmarker{}
	bm, err := trackOrPinMember(f, protectedCfg(), "/repo", "origin", m)

	require.NoError(t, err)
	assert.Equal(t, "feat", bm)
	assert.Equal(t, []string{"feat@origin"}, f.tracked)
	assert.Empty(t, f.sets, "tracking should not pin a second bookmark")
}

// A protected branch and a divergent local bookmark both route to the pin.
// The pin must be *set*, not created: `jj bookmark create` errors on an
// existing name, which made a second `wgo to` on the same PR fail outright.
func TestTrackOrPinMemberPins(t *testing.T) {
	const oid = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	t.Run("protected branch", func(t *testing.T) {
		m := &stack.StackMember{Branch: "main", PRNumber: 7, HeadOID: oid}
		f := &fakeMemberBookmarker{}

		bm, err := trackOrPinMember(f, protectedCfg(), "/repo", "origin", m)

		require.NoError(t, err)
		assert.Equal(t, "pr-7-main", bm)
		assert.Empty(t, f.tracked)
		assert.Equal(t, []setCall{{"pr-7-main", oid, true}}, f.sets)
	})

	t.Run("divergent local bookmark", func(t *testing.T) {
		m := &stack.StackMember{Branch: "feat", PRNumber: 7, HeadOID: oid}
		f := &fakeMemberBookmarker{fakeBookmarkLister: fakeBookmarkLister{
			bookmarks: []jj.Bookmark{{
				Name: "feat", Present: true,
				CommitID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			}},
		}}

		bm, err := trackOrPinMember(f, protectedCfg(), "/repo", "origin", m)

		require.NoError(t, err)
		assert.Equal(t, "pr-7-feat", bm)
		assert.Empty(t, f.tracked, "must not clobber the divergent local bookmark")
		assert.Equal(t, []setCall{{"pr-7-feat", oid, true}}, f.sets)
	})
}

// Regression: `wgo to <PR-URL>` is a lookup users re-run freely. Against a
// real jj binary, pinning the same PR twice must be idempotent rather than
// failing with "Bookmark already exists", and a force-pushed head that rewinds
// the PR must still drag the pin backwards.
func TestTrackOrPinMemberPinIsRerunnable(t *testing.T) {
	repo, c := jjtest.NewRepo(t)

	// Three commits. A local "feat" bookmark parks on the first so the
	// member's head always diverges from it and the pin path is taken; the
	// PR head starts at the third and later rewinds to the second.
	commit := func(msg string) string {
		jjtest.Commit(t, repo, msg, map[string]string{msg + ".txt": msg + "\n"})
		got, err := c.Log(repo, "@-")
		require.NoError(t, err)
		require.NotEmpty(t, got)
		return got[0].CommitID
	}
	parked, rewound, head := commit("one"), commit("two"), commit("three")
	jjtest.Bookmark(t, repo, "feat", parked)

	m := &stack.StackMember{Branch: "feat", PRNumber: 7, HeadOID: head}
	pinnedAt := func() string {
		bms, err := c.BookmarkList(repo, jj.BookmarkListOpts{Local: true, Names: []string{"pr-7-feat"}})
		require.NoError(t, err)
		require.Len(t, bms, 1)
		return bms[0].CommitID
	}

	bm, err := trackOrPinMember(c, protectedCfg(), repo, "origin", m)
	require.NoError(t, err)
	assert.Equal(t, "pr-7-feat", bm)
	assert.Equal(t, head, pinnedAt())

	// Second run, pin already present at the same commit: no-op, not an error.
	bm, err = trackOrPinMember(c, protectedCfg(), repo, "origin", m)
	require.NoError(t, err, "re-running wgo to on the same PR must not fail")
	assert.Equal(t, "pr-7-feat", bm)
	assert.Equal(t, head, pinnedAt())

	// Force-push rewinds the head to an ancestor: the pin follows it back.
	m.HeadOID = rewound
	_, err = trackOrPinMember(c, protectedCfg(), repo, "origin", m)
	require.NoError(t, err, "a rewound PR head must move the pin backwards")
	assert.Equal(t, rewound, pinnedAt())
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
