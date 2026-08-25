package cmd

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/virtru/wgo/internal/jj"
)

type wsAddCall struct{ name, dest, revset string }
type bmCreateCall struct{ name, revset string }

// fakeWSClient records calls — including the revsets, which the start point
// depends on — and lets tests seed pre-existing workspaces and bookmarks to
// exercise ensureWorkspaceAndBookmark's idempotency.
type fakeWSClient struct {
	workspaces []jj.Workspace
	bookmarks  []jj.Bookmark

	workspaceAdds   []wsAddCall
	bookmarkCreates []bmCreateCall
}

func (f *fakeWSClient) ListWorkspaces(string) ([]jj.Workspace, error) {
	return f.workspaces, nil
}

func (f *fakeWSClient) WorkspaceAdd(_, dest string, opts jj.WorkspaceAddOpts) error {
	f.workspaceAdds = append(f.workspaceAdds, wsAddCall{name: opts.Name, dest: dest, revset: opts.Revset})
	f.workspaces = append(f.workspaces, jj.Workspace{Name: opts.Name, Path: dest})
	return nil
}

func (f *fakeWSClient) BookmarkList(string, jj.BookmarkListOpts) ([]jj.Bookmark, error) {
	return f.bookmarks, nil
}

func (f *fakeWSClient) BookmarkCreate(_, name, revset string) error {
	f.bookmarkCreates = append(f.bookmarkCreates, bmCreateCall{name: name, revset: revset})
	f.bookmarks = append(f.bookmarks, jj.Bookmark{Name: name})
	return nil
}

func TestEnsureWorkspaceAndBookmarkIdempotent(t *testing.T) {
	f := &fakeWSClient{}
	const branch = "DSPX-3636-audiotel"

	// First run: nothing exists yet, so both are created.
	err := ensureWorkspaceAndBookmark(f, "/repo", branch, "/wt", "main@origin", "owner/repo")
	assert.NoError(t, err)
	assert.Len(t, f.workspaceAdds, 1)
	assert.Len(t, f.bookmarkCreates, 1)

	// Second run: both now exist, so neither is created again.
	err = ensureWorkspaceAndBookmark(f, "/repo", branch, "/wt", "main@origin", "owner/repo")
	assert.NoError(t, err)
	assert.Len(t, f.workspaceAdds, 1, "workspace should not be re-created")
	assert.Len(t, f.bookmarkCreates, 1, "bookmark should not be re-created")
}

// A bookmark left over from a rolled-back run (workspace forgotten but bookmark
// not deleted) must not cause a re-run to fail: the workspace is created, the
// bookmark is skipped.
func TestEnsureWorkspaceAndBookmarkOrphanBookmark(t *testing.T) {
	const branch = "DSPX-3636-audiotel"
	f := &fakeWSClient{bookmarks: []jj.Bookmark{{Name: branch}}}

	err := ensureWorkspaceAndBookmark(f, "/repo", branch, "/wt", "main@origin", "owner/repo")
	assert.NoError(t, err)
	assert.Len(t, f.workspaceAdds, 1)
	assert.Empty(t, f.bookmarkCreates, "existing bookmark should be left as-is")
}

// The start point must reach both mutations verbatim. Nothing asserted this
// before, which is why the unresolvable "main@origin" on a commitless remote
// went unnoticed.
func TestEnsureWorkspaceAndBookmarkUsesStartPoint(t *testing.T) {
	f := &fakeWSClient{}

	err := ensureWorkspaceAndBookmark(f, "/repo", "WGO-1-x", "/wt", "main@origin", "owner/repo")
	assert.NoError(t, err)
	assert.Equal(t, "main@origin", f.workspaceAdds[0].revset)
	assert.Equal(t, "main@origin", f.bookmarkCreates[0].revset)

	// `wgo to --on <parent>` passes a bare bookmark name rather than a remote
	// revset; it must be forwarded unchanged too.
	g := &fakeWSClient{}
	err = ensureWorkspaceAndBookmark(g, "/repo", "WGO-2-y", "/wt", "WGO-1-x", "owner/repo")
	assert.NoError(t, err)
	assert.Equal(t, "WGO-1-x", g.workspaceAdds[0].revset)
	assert.Equal(t, "WGO-1-x", g.bookmarkCreates[0].revset)
}

func TestIsJiraTicket(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"DSPX-2674", true},
		{"A-1", true},
		{"FOO-123", true},
		{"dspx-2674", false},
		{"DSPX-", false},
		{"2674", false},
		{"DSPX2674", false},
		{"", false},
		{"DSPX-abc", false},
		{"dspx-ABC", false},
	}
	for _, tt := range tests {
		got := isJiraTicket(tt.input)
		assert.Equal(t, tt.want, got, "isJiraTicket(%q)", tt.input)
	}
}

func TestSlugTicketBranch(t *testing.T) {
	tests := []struct {
		ticket string
		desc   string
		want   string
	}{
		{"DSPX-123", "", "DSPX-123"},
		{"DSPX-123", "remove volume directive", "DSPX-123-remove-volume-directive"},
		{"DSPX-123", "fix the login bug", "DSPX-123-fix-the-login-bug"},
		// result must never end in a dash, capped at 60 chars
		{"DSPX-123", "a very long description that will be truncated at the sixty character limit", "DSPX-123-a-very-long-description-that-will-be-truncated-at-t"},
		// special characters are sanitized
		{"DSPX-1", "hello world!", "DSPX-1-hello-world"},
	}
	for _, tt := range tests {
		got := slugTicketBranch(tt.ticket, tt.desc)
		assert.Equal(t, tt.want, got, "slugTicketBranch(%q, %q)", tt.ticket, tt.desc)
		if len(got) > 0 {
			assert.NotEqual(t, byte('-'), got[len(got)-1], "slugTicketBranch(%q, %q) = %q ends in dash", tt.ticket, tt.desc, got)
		}
	}
}

func TestTruncateSlug(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 30, "short"},
		{"exactly-ten", 11, "exactly-ten"},
		{"DSPX-123-remove-volume-directive", 20, "DSPX-123-remove"},
		// truncates at last dash boundary
		{"abc-def-ghi", 8, "abc-def"},
		// no dash in range: raw truncation
		{"abcdefghij", 5, "abcde"},
		// trailing dash trimmed after raw truncation
		{"abc-defgh", 4, "abc"},
	}
	for _, tt := range tests {
		got := truncateSlug(tt.input, tt.maxLen)
		assert.Equal(t, tt.want, got, "truncateSlug(%q, %d)", tt.input, tt.maxLen)
	}
}

// fakeTrunkClient scripts CountRevset results and records the mutations
// ensureTrunk performs, so the bootstrap sequence can be asserted without a jj
// binary.
type fakeTrunkClient struct {
	count    int
	countErr error
	clean    bool
	pushErr  error

	probed      []string
	describedAs []string
	newCalls    int
	bookmarkSet []bmCreateCall
	pushed      []jj.PushOpts
}

func newFakeTrunkClient(count int) *fakeTrunkClient {
	return &fakeTrunkClient{count: count, clean: true}
}

func (f *fakeTrunkClient) CountRevset(_, revset string) (int, error) {
	f.probed = append(f.probed, revset)
	return f.count, f.countErr
}

func (f *fakeTrunkClient) IsClean(string) (bool, []string, error) {
	return f.clean, nil, nil
}

func (f *fakeTrunkClient) Describe(_, msg string) error {
	f.describedAs = append(f.describedAs, msg)
	return nil
}

func (f *fakeTrunkClient) CurrentChange(string) (jj.Change, error) {
	return jj.Change{CommitID: "deadbeef"}, nil
}

func (f *fakeTrunkClient) New(string, string, string) error {
	f.newCalls++
	return nil
}

func (f *fakeTrunkClient) BookmarkSet(_, name, revset string, _ bool) error {
	f.bookmarkSet = append(f.bookmarkSet, bmCreateCall{name: name, revset: revset})
	return nil
}

func (f *fakeTrunkClient) GitPush(_ string, opts jj.PushOpts) (jj.PushResult, error) {
	f.pushed = append(f.pushed, opts)
	return jj.PushResult{}, f.pushErr
}

// mutated reports whether ensureTrunk authored or pushed anything.
func (f *fakeTrunkClient) mutated() bool {
	return len(f.describedAs) > 0 || f.newCalls > 0 || len(f.bookmarkSet) > 0 || len(f.pushed) > 0
}

func TestEnsureTrunkNoopWhenTrunkExists(t *testing.T) {
	f := newFakeTrunkClient(1)

	assert.NoError(t, ensureTrunk(f, "/repo", "main", "owner/repo"))
	assert.False(t, f.mutated(), "a repo that already has a trunk must be left alone")
	assert.Equal(t, []string{`remote_bookmarks(exact:"main", exact:"origin")`}, f.probed)
}

// A probe that errors is a jj problem, not evidence of an empty remote. Degrade
// to the pre-existing behaviour so jj's own diagnostic surfaces downstream
// rather than being masked by an unexpected bootstrap commit.
func TestEnsureTrunkProbeErrorIsNoop(t *testing.T) {
	f := newFakeTrunkClient(0)
	f.countErr = errors.New("jj exploded")

	assert.NoError(t, ensureTrunk(f, "/repo", "main", "owner/repo"))
	assert.False(t, f.mutated())
}

func TestEnsureTrunkBootstrapsEmptyRepo(t *testing.T) {
	f := newFakeTrunkClient(0)

	assert.NoError(t, ensureTrunk(f, "/repo", "main", "owner/repo"))
	assert.Equal(t, []string{trunkBootstrapMessage}, f.describedAs)
	assert.Equal(t, 1, f.newCalls, "working copy must be left clean")
	assert.Equal(t, []bmCreateCall{{name: "main", revset: "deadbeef"}}, f.bookmarkSet)
	assert.Equal(t, []jj.PushOpts{{Bookmarks: []string{"main"}, AllowNew: true}}, f.pushed)
}

// The default branch is whatever GitHub reports; do not hard-code "main".
func TestEnsureTrunkHonoursNonMainDefaultBranch(t *testing.T) {
	f := newFakeTrunkClient(0)

	assert.NoError(t, ensureTrunk(f, "/repo", "trunk", "owner/repo"))
	assert.Equal(t, []string{`remote_bookmarks(exact:"trunk", exact:"origin")`}, f.probed)
	assert.Equal(t, "trunk", f.bookmarkSet[0].name)
	assert.Equal(t, []string{"trunk"}, f.pushed[0].Bookmarks)
}

// `jj describe` would silently fold uncommitted user work into the bootstrap
// commit, so refuse rather than risk it.
func TestEnsureTrunkRefusesDirtyWorkingCopy(t *testing.T) {
	f := newFakeTrunkClient(0)
	f.clean = false

	err := ensureTrunk(f, "/repo", "main", "owner/repo")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dirty")
	assert.False(t, f.mutated())
}

func TestEnsureTrunkToleratesNothingToPush(t *testing.T) {
	f := newFakeTrunkClient(0)
	f.pushErr = jj.ErrNothingToPush

	assert.NoError(t, ensureTrunk(f, "/repo", "main", "owner/repo"))
}

func TestEnsureTrunkPushFailureIsFatal(t *testing.T) {
	f := newFakeTrunkClient(0)
	f.pushErr = errors.New("403 Forbidden")

	err := ensureTrunk(f, "/repo", "main", "owner/repo")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "git push --bookmark main", "error should name the retry command")
}

func TestRemoteBookmarkRevset(t *testing.T) {
	tests := []struct {
		branch, remote, want string
	}{
		{"main", "origin", `remote_bookmarks(exact:"main", exact:"origin")`},
		{"release/1.0", "origin", `remote_bookmarks(exact:"release/1.0", exact:"origin")`},
		{"main", "upstream", `remote_bookmarks(exact:"main", exact:"upstream")`},
		{`we"ird`, "origin", `remote_bookmarks(exact:"we\"ird", exact:"origin")`},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, remoteBookmarkRevset(tt.branch, tt.remote))
	}
}
