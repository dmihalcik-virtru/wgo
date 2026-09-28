package cmd

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/jjtest"
	"github.com/virtru/wgo/internal/stack"
)

// fakeBookmarkLister is a tiny bookmarkLister for exercising the tracking
// decision helpers without a full jj.Client. It records the repo and opts of
// the last query so a test can assert the lookup itself was right, not just
// the branch taken off its result.
type fakeBookmarkLister struct {
	bookmarks []jj.Bookmark
	listErr   error

	listedRepo string
	listedOpts jj.BookmarkListOpts
}

func (f *fakeBookmarkLister) BookmarkList(repo string, opts jj.BookmarkListOpts) ([]jj.Bookmark, error) {
	f.listedRepo, f.listedOpts = repo, opts
	if f.listErr != nil {
		return nil, f.listErr
	}
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

	mustConflict := func(t *testing.T, f *fakeBookmarkLister) bool {
		t.Helper()
		got, err := localBookmarkConflicts(f, "/repo", name, oid)
		require.NoError(t, err)
		return got
	}

	// No local bookmark: no conflict.
	f := &fakeBookmarkLister{}
	assert.False(t, mustConflict(t, f))
	// The lookup must ask about this name on every remote, or the checks below
	// would pass even if the helper queried the wrong thing.
	assert.Equal(t, "/repo", f.listedRepo)
	assert.Equal(t, jj.BookmarkListOpts{AllRemotes: true, Names: []string{name}}, f.listedOpts)

	// Local bookmark at the same OID: no conflict (idempotent re-track).
	f = &fakeBookmarkLister{bookmarks: []jj.Bookmark{
		{Name: name, Present: true, CommitID: oid},
	}}
	assert.False(t, mustConflict(t, f))

	// Local bookmark at a different OID: conflict — don't clobber.
	f = &fakeBookmarkLister{bookmarks: []jj.Bookmark{
		{Name: name, Present: true, CommitID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}}
	assert.True(t, mustConflict(t, f))

	// Conflicted local bookmark: conflict.
	f = &fakeBookmarkLister{bookmarks: []jj.Bookmark{
		{Name: name, Present: true, Conflict: true},
	}}
	assert.True(t, mustConflict(t, f))

	// A remote entry of the same name is not a local conflict.
	f = &fakeBookmarkLister{bookmarks: []jj.Bookmark{
		{Name: name, Remote: "origin", Present: true, CommitID: "cccccccccccccccccccccccccccccccccccccccc"},
	}}
	assert.False(t, mustConflict(t, f))
}

// A failed lookup must not be reported as "no conflict": that answer routes
// the caller into `jj bookmark track`, which merges a genuinely colliding
// local bookmark into a conflicted one at exit 0.
func TestLocalBookmarkConflictsPropagatesListError(t *testing.T) {
	f := &fakeBookmarkLister{listErr: errors.New("snapshot failed")}

	got, err := localBookmarkConflicts(f, "/repo", "feat", "aaaa")

	require.Error(t, err)
	assert.ErrorContains(t, err, "snapshot failed")
	assert.False(t, got, "a failed check must not masquerade as a clean one")
}

// fakeMemberBookmarker records the mutating bookmark calls trackOrPinMember
// makes, so a test can assert *how* a bookmark was established, not just that
// the call succeeded.
type fakeMemberBookmarker struct {
	fakeBookmarkLister
	trackErr error
	setErr   error

	tracked []trackCall
	sets    []setCall
}

// Both calls record repo: it is adjacent to same-typed arguments at the call
// site, so a transposition would otherwise pass every fake-based test.
type trackCall struct {
	repo   string
	name   string
	remote string
}

type setCall struct {
	repo           string
	name           string
	revset         string
	allowBackwards bool
}

func (f *fakeMemberBookmarker) BookmarkTrack(repo, name, remote string) error {
	f.tracked = append(f.tracked, trackCall{repo, name, remote})
	return f.trackErr
}

func (f *fakeMemberBookmarker) BookmarkSet(repo, name, revset string, allowBackwards bool) error {
	f.sets = append(f.sets, setCall{repo, name, revset, allowBackwards})
	return f.setErr
}

func TestTrackOrPinMemberTracks(t *testing.T) {
	const oid = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	m := &stack.StackMember{Branch: "feat", PRNumber: 7, HeadOID: oid}

	f := &fakeMemberBookmarker{}
	bm, err := trackOrPinMember(f, protectedCfg(), "/repo", "origin", m)

	require.NoError(t, err)
	assert.Equal(t, "feat", bm)
	assert.Equal(t, []trackCall{{repo: "/repo", name: "feat", remote: "origin"}}, f.tracked)
	assert.Empty(t, f.sets, "tracking should not pin a second bookmark")
}

// A fork PR's head lives on a per-PR remote, not origin; the caller passes it
// in and the helper must use it verbatim.
func TestTrackOrPinMemberTracksForkRemote(t *testing.T) {
	m := &stack.StackMember{Branch: "feat", PRNumber: 7, HeadOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}

	f := &fakeMemberBookmarker{}
	bm, err := trackOrPinMember(f, protectedCfg(), "/repo", "pr-7-fork", m)

	require.NoError(t, err)
	assert.Equal(t, "feat", bm)
	assert.Equal(t, []trackCall{{repo: "/repo", name: "feat", remote: "pr-7-fork"}}, f.tracked)
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
		assert.Equal(t, []setCall{{repo: "/repo", name: "pr-7-main", revset: oid, allowBackwards: true}}, f.sets)
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
		assert.Equal(t, []setCall{{repo: "/repo", name: "pr-7-feat", revset: oid, allowBackwards: true}}, f.sets)
	})
}

// BookmarkTrack repairs a missing local bookmark itself, so an error from it
// means `m.Branch` almost certainly does not resolve. Returning it anyway once
// surfaced as an unrelated "workspace add failed" several steps later.
func TestTrackOrPinMemberFallsBackToPinWhenTrackFails(t *testing.T) {
	const oid = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	m := &stack.StackMember{Branch: "feat", PRNumber: 7, HeadOID: oid}
	f := &fakeMemberBookmarker{trackErr: errors.New("no such remote bookmark")}

	bm, err := trackOrPinMember(f, protectedCfg(), "/repo", "origin", m)

	require.NoError(t, err, "a failed track should degrade to the pin, not abort")
	assert.Equal(t, "pr-7-feat", bm, "must not hand back a branch that failed to track")
	assert.Len(t, f.tracked, 1, "the track should still have been attempted")
	assert.Equal(t, []setCall{{repo: "/repo", name: "pr-7-feat", revset: oid, allowBackwards: true}}, f.sets)
}

// `jj bookmark set` with no -r defaults to @, so an empty head would pin the
// PR to whatever the main clone's working copy happens to be.
func TestTrackOrPinMemberRejectsEmptyHead(t *testing.T) {
	m := &stack.StackMember{Branch: "main", PRNumber: 7} // protected: takes the pin path
	f := &fakeMemberBookmarker{}

	_, err := trackOrPinMember(f, protectedCfg(), "/repo", "origin", m)

	require.Error(t, err)
	assert.ErrorContains(t, err, "no head commit")
	assert.Empty(t, f.sets, "must not pin anything without a head commit")
}

// Switching from `bookmark create` to `bookmark set` gave up the accidental
// guard create provided against overwriting a name wgo does not own. A pin
// with a tracked remote counterpart was pushed by someone, so it is no longer
// wgo's to move.
func TestTrackOrPinMemberRefusesPublishedPin(t *testing.T) {
	m := &stack.StackMember{Branch: "main", PRNumber: 7, HeadOID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	f := &fakeMemberBookmarker{fakeBookmarkLister: fakeBookmarkLister{
		bookmarks: []jj.Bookmark{
			{Name: "pr-7-main", Present: true, CommitID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			{Name: "pr-7-main", Remote: "origin", Present: true, Tracked: true},
		},
	}}

	_, err := trackOrPinMember(f, protectedCfg(), "/repo", "origin", m)

	require.Error(t, err)
	assert.ErrorContains(t, err, "pr-7-main")
	assert.ErrorContains(t, err, "tracked on a remote")
	assert.Empty(t, f.sets, "must not move a bookmark it does not own")
}

// jj auto-exports local bookmarks to the colocated git repo's pseudo-remote
// and reports them Tracked. wgo always colocates, so treating that as
// "published" would refuse to re-pin every PR on the second run.
func TestTrackOrPinMemberIgnoresGitPseudoRemote(t *testing.T) {
	const oid = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	m := &stack.StackMember{Branch: "main", PRNumber: 7, HeadOID: oid}
	f := &fakeMemberBookmarker{fakeBookmarkLister: fakeBookmarkLister{
		bookmarks: []jj.Bookmark{
			{Name: "pr-7-main", Present: true, CommitID: oid},
			{Name: "pr-7-main", Remote: jj.GitPseudoRemote, Present: true, Tracked: true, CommitID: oid},
		},
	}}

	bm, err := trackOrPinMember(f, protectedCfg(), "/repo", "origin", m)

	require.NoError(t, err, "the colocated git remote is not a publication")
	assert.Equal(t, "pr-7-main", bm)
	assert.Len(t, f.sets, 1)
}

// An untracked local bookmark of the pin's name is wgo's own pin from an
// earlier run, so it moves — including backwards onto an unrelated commit.
func TestTrackOrPinMemberRepointsOwnPin(t *testing.T) {
	const oid = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	m := &stack.StackMember{Branch: "main", PRNumber: 7, HeadOID: oid}
	f := &fakeMemberBookmarker{fakeBookmarkLister: fakeBookmarkLister{
		bookmarks: []jj.Bookmark{
			{Name: "pr-7-main", Present: true, CommitID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	}}

	bm, err := trackOrPinMember(f, protectedCfg(), "/repo", "origin", m)

	require.NoError(t, err)
	assert.Equal(t, "pr-7-main", bm)
	assert.Equal(t, []setCall{{repo: "/repo", name: "pr-7-main", revset: oid, allowBackwards: true}}, f.sets)
}

// The pin is the only path that can abort the whole `wgo to`, so its error
// must name the bookmark and the commit it failed to reach.
func TestTrackOrPinMemberWrapsSetError(t *testing.T) {
	const oid = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	m := &stack.StackMember{Branch: "main", PRNumber: 7, HeadOID: oid}
	f := &fakeMemberBookmarker{setErr: errors.New("immutable commit")}

	bm, err := trackOrPinMember(f, protectedCfg(), "/repo", "origin", m)

	require.Error(t, err)
	assert.Empty(t, bm)
	assert.ErrorContains(t, err, "pin bookmark pr-7-main")
	assert.ErrorContains(t, err, oid)
	assert.ErrorContains(t, err, "immutable commit")
}

// Regression: `wgo to <PR-URL>` is a lookup users re-run freely. Against a
// real jj binary, pinning the same PR twice must be idempotent rather than
// failing with "Bookmark already exists", and a force-pushed head must drag
// the pin with it whether the new head is an ancestor of the old one or an
// unrelated sibling — `jj bookmark set` refuses both without --allow-backwards.
func TestTrackOrPinMemberPinIsRerunnable(t *testing.T) {
	repo, c := jjtest.NewRepo(t)

	// commit asserts it read back the change it just wrote: the helper resolves
	// "@-" positionally, so a shift in Log's ordering would silently hand the
	// test the wrong commits and every assertion below would still pass.
	commit := func(msg string) string {
		jjtest.Commit(t, repo, msg, map[string]string{msg + ".txt": msg + "\n"})
		got, err := c.Log(repo, "@-")
		require.NoError(t, err)
		require.NotEmpty(t, got)
		require.Contains(t, got[0].Description, msg, "@- is not the change just described")
		return got[0].CommitID
	}

	// A local "feat" bookmark parks on the first commit so the member's head
	// always diverges from it and the pin path is taken; the PR head starts at
	// the third and later rewinds to the second.
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

	// The commoner force-push shape: an amend/rebase replaces the head with a
	// commit that is neither ancestor nor descendant of where the pin sits.
	// Forking off `parked` makes it a true sibling of `rewound`, where the pin
	// currently is — a linear commit on top would just be a descendant, which
	// `jj bookmark set` allows even without --allow-backwards.
	require.NoError(t, c.New(repo, parked, "sibling"))
	forked, err := c.Log(repo, "@")
	require.NoError(t, err)
	require.NotEmpty(t, forked)
	sibling := forked[0].CommitID
	require.NotEqual(t, rewound, sibling)

	m.HeadOID = sibling
	_, err = trackOrPinMember(c, protectedCfg(), repo, "origin", m)
	require.NoError(t, err, "a sideways-moved PR head must move the pin")
	assert.Equal(t, sibling, pinnedAt())
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
