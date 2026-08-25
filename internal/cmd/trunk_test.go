package cmd

import (
	"errors"
	"testing"

	"github.com/virtru/wgo/internal/jj"
)

// fakeTrunkLister is a narrow stand-in for jj.Client covering only what the
// trunk helpers call, following the fakeBookmarkLister pattern in to_track_test.go.
type fakeTrunkLister struct {
	log       map[string][]jj.LogEntry
	logErr    error
	bookmarks []jj.Bookmark
	bmErr     error
	resolve   map[string]string
}

func (f *fakeTrunkLister) Log(_, revset string) ([]jj.LogEntry, error) {
	if f.logErr != nil {
		return nil, f.logErr
	}
	return f.log[revset], nil
}

func (f *fakeTrunkLister) BookmarkList(_ string, _ jj.BookmarkListOpts) ([]jj.Bookmark, error) {
	if f.bmErr != nil {
		return nil, f.bmErr
	}
	return f.bookmarks, nil
}

func (f *fakeTrunkLister) Resolve(_, revset string) (string, error) {
	if id, ok := f.resolve[revset]; ok {
		return id, nil
	}
	return "", errors.New("no matching commit")
}

const testTrunkCommit = "abc1234567890abc1234567890abc1234567890a"

// TestLocalTrunkBookmarkPrefersCanonicalName pins the tie-break that keeps a
// develop branch momentarily equal to main from being mistaken for trunk —
// which would cost `wgo to .../tree/develop` its own workspace.
func TestLocalTrunkBookmarkPrefersCanonicalName(t *testing.T) {
	f := &fakeTrunkLister{
		log: map[string][]jj.LogEntry{
			"trunk()": {{CommitID: testTrunkCommit, Bookmarks: []string{"develop", "main"}}},
		},
		bookmarks: []jj.Bookmark{
			{Name: "develop", Remote: "origin", Present: true, CommitID: testTrunkCommit},
			{Name: "main", Remote: "origin", Present: true, CommitID: testTrunkCommit},
		},
	}
	if got := localTrunkBookmark(f, "/repo"); got != "main" {
		t.Errorf("localTrunkBookmark = %q, want %q", got, "main")
	}
}

// TestLocalTrunkBookmarkRootCommit covers a clone of a commitless remote, where
// trunk() resolves to the all-zeros root and there is no real trunk to report.
func TestLocalTrunkBookmarkRootCommit(t *testing.T) {
	f := &fakeTrunkLister{
		log: map[string][]jj.LogEntry{
			"trunk()": {{CommitID: rootCommitID}},
		},
	}
	if got := localTrunkBookmark(f, "/repo"); got != "" {
		t.Errorf("localTrunkBookmark = %q, want empty for the root commit", got)
	}
}

// TestLocalTrunkBookmarkFallsBackToLocal covers a repo with no origin bookmark
// at trunk — a local-only repo, or an unusual remote layout.
func TestLocalTrunkBookmarkFallsBackToLocal(t *testing.T) {
	f := &fakeTrunkLister{
		log: map[string][]jj.LogEntry{
			"trunk()": {{CommitID: testTrunkCommit, Bookmarks: []string{"mainline"}}},
		},
		bookmarks: []jj.Bookmark{
			{Name: "other", Remote: "origin", Present: true, CommitID: "deadbeef"},
		},
	}
	if got := localTrunkBookmark(f, "/repo"); got != "mainline" {
		t.Errorf("localTrunkBookmark = %q, want %q", got, "mainline")
	}
}

func TestLocalTrunkBookmarkLogError(t *testing.T) {
	f := &fakeTrunkLister{logErr: errors.New("boom")}
	if got := localTrunkBookmark(f, "/repo"); got != "" {
		t.Errorf("localTrunkBookmark = %q, want empty when jj log fails", got)
	}
}

func TestIsTrunkTarget(t *testing.T) {
	apiErr := func() (string, error) { return "", errors.New("offline") }
	apiDevelop := func() (string, error) { return "develop", nil }

	tests := []struct {
		name        string
		branch      string
		localTrunk  string
		defaultFn   func() (string, error)
		want        bool
		explanation string
	}{
		{"trunk matches", "main", "main", nil, true, ""},
		{"feature branch", "feature/x", "main", nil, false, ""},
		{
			name: "release branch is not trunk", branch: "release/1.2", localTrunk: "main",
			want:        false,
			explanation: "release/* is in doctor.exclude_bookmarks but is still a real branch needing its own workspace",
		},
		{"empty branch", "", "main", nil, false, ""},
		{"api fallback matches", "develop", "", apiDevelop, true, ""},
		{"api fallback differs", "feature", "", apiDevelop, false, ""},
		{
			name: "api unreachable", branch: "main", localTrunk: "", defaultFn: apiErr,
			want:        false,
			explanation: "offline with no local trunk must fall back to today's worktree behaviour, not guess",
		},
		{"no fallback provided", "main", "", nil, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTrunkTarget(tt.branch, tt.localTrunk, tt.defaultFn)
			if got != tt.want {
				t.Errorf("isTrunkTarget(%q, %q) = %v, want %v. %s",
					tt.branch, tt.localTrunk, got, tt.want, tt.explanation)
			}
		})
	}
}

func TestTrunkRevset(t *testing.T) {
	t.Run("prefers trunk()", func(t *testing.T) {
		f := &fakeTrunkLister{resolve: map[string]string{"trunk()": testTrunkCommit}}
		if got := trunkRevset(f, "/repo", "main"); got != "trunk()" {
			t.Errorf("trunkRevset = %q, want %q", got, "trunk()")
		}
	})

	t.Run("falls back to origin bookmark", func(t *testing.T) {
		rev := remoteBookmarkRevset("main", "origin")
		f := &fakeTrunkLister{resolve: map[string]string{rev: testTrunkCommit}}
		if got := trunkRevset(f, "/repo", "main"); got != rev {
			t.Errorf("trunkRevset = %q, want %q", got, rev)
		}
	})

	t.Run("no trunk at all", func(t *testing.T) {
		f := &fakeTrunkLister{resolve: map[string]string{}}
		if got := trunkRevset(f, "/repo", "main"); got != "" {
			t.Errorf("trunkRevset = %q, want empty", got)
		}
	})
}
