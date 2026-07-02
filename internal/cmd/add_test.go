package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/virtru/wgo/internal/jj"
)

// fakeWSClient records calls and lets tests seed pre-existing workspaces and
// bookmarks to exercise ensureWorkspaceAndBookmark's idempotency.
type fakeWSClient struct {
	workspaces []jj.Workspace
	bookmarks  []jj.Bookmark

	workspaceAddCalls   int
	bookmarkCreateCalls int
}

func (f *fakeWSClient) ListWorkspaces(string) ([]jj.Workspace, error) {
	return f.workspaces, nil
}

func (f *fakeWSClient) WorkspaceAdd(_, name, dest, _ string) error {
	f.workspaceAddCalls++
	f.workspaces = append(f.workspaces, jj.Workspace{Name: name, Path: dest})
	return nil
}

func (f *fakeWSClient) BookmarkList(string, jj.BookmarkListOpts) ([]jj.Bookmark, error) {
	return f.bookmarks, nil
}

func (f *fakeWSClient) BookmarkCreate(_, name, _ string) error {
	f.bookmarkCreateCalls++
	f.bookmarks = append(f.bookmarks, jj.Bookmark{Name: name})
	return nil
}

func TestEnsureWorkspaceAndBookmarkIdempotent(t *testing.T) {
	f := &fakeWSClient{}
	const branch = "DSPX-3636-audiotel"

	// First run: nothing exists yet, so both are created.
	err := ensureWorkspaceAndBookmark(f, "/repo", branch, "/wt", "main", "owner/repo")
	assert.NoError(t, err)
	assert.Equal(t, 1, f.workspaceAddCalls)
	assert.Equal(t, 1, f.bookmarkCreateCalls)

	// Second run: both now exist, so neither is created again.
	err = ensureWorkspaceAndBookmark(f, "/repo", branch, "/wt", "main", "owner/repo")
	assert.NoError(t, err)
	assert.Equal(t, 1, f.workspaceAddCalls, "workspace should not be re-created")
	assert.Equal(t, 1, f.bookmarkCreateCalls, "bookmark should not be re-created")
}

// A bookmark left over from a rolled-back run (workspace forgotten but bookmark
// not deleted) must not cause a re-run to fail: the workspace is created, the
// bookmark is skipped.
func TestEnsureWorkspaceAndBookmarkOrphanBookmark(t *testing.T) {
	const branch = "DSPX-3636-audiotel"
	f := &fakeWSClient{bookmarks: []jj.Bookmark{{Name: branch}}}

	err := ensureWorkspaceAndBookmark(f, "/repo", branch, "/wt", "main", "owner/repo")
	assert.NoError(t, err)
	assert.Equal(t, 1, f.workspaceAddCalls)
	assert.Equal(t, 0, f.bookmarkCreateCalls, "existing bookmark should be left as-is")
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
