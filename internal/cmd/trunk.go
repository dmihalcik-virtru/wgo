package cmd

import (
	"strings"

	"github.com/virtru/wgo/internal/jj"
)

// rootCommitID is the all-zeros commit jj uses as the DAG root. A clone of a
// commitless remote has trunk() resolve to it, which is not a real trunk.
const rootCommitID = "0000000000000000000000000000000000000000"

// canonicalTrunkNames are the bookmark names jj's own trunk() alias considers,
// in its preference order. Mirroring that order matters when several origin
// bookmarks sit on the same commit — e.g. a freshly branched develop@origin
// equal to main@origin. Without the tie-break, `wgo to .../tree/develop` could
// be mistaken for a trunk target and lose its own workspace.
var canonicalTrunkNames = []string{"main", "master", "trunk"}

// trunkLister is the slice of jj.Client the trunk helpers need. Taking the
// narrow interface keeps them unit-testable with a small fake, following the
// pattern of bookmarkLister in to.go.
type trunkLister interface {
	Log(repo, revset string) ([]jj.LogEntry, error)
	BookmarkList(repo string, opts jj.BookmarkListOpts) ([]jj.Bookmark, error)
	Resolve(repo, revset string) (string, error)
}

// localTrunkBookmark names the bookmark at jj's trunk() in repoPath, resolved
// entirely from local state so `wgo to` keeps working offline.
//
// Returns "" when there is no usable answer: trunk() is the root commit (a
// clone of a commitless remote), or no bookmark points at it. Callers treat ""
// as "ask GitHub" rather than "no trunk".
func localTrunkBookmark(jjc trunkLister, repoPath string) string {
	entries, err := jjc.Log(repoPath, "trunk()")
	if err != nil || len(entries) == 0 {
		return ""
	}
	head := entries[0]
	if head.CommitID == "" || head.CommitID == rootCommitID {
		return ""
	}

	bms, err := jjc.BookmarkList(repoPath, jj.BookmarkListOpts{AllRemotes: true})
	if err == nil {
		atTrunk := make(map[string]bool, len(bms))
		for _, b := range bms {
			if b.Remote == "origin" && b.Present && b.CommitID == head.CommitID {
				atTrunk[b.Name] = true
			}
		}
		for _, name := range canonicalTrunkNames {
			if atTrunk[name] {
				return name
			}
		}
	}

	// No origin bookmark at trunk (a local-only repo, or an unusual remote
	// layout); fall back to whatever local bookmark sits there.
	if len(head.Bookmarks) > 0 {
		return head.Bookmarks[0]
	}
	return ""
}

// isTrunkTarget reports whether branch is repoPath's trunk — the branch whose
// checkout the mains clone already is.
//
// localTrunk (from localTrunkBookmark) answers without touching the network.
// Only when jj cannot name a trunk does this fall back to the GitHub
// default-branch API via defaultBranch, and a failed fallback returns false so
// the caller keeps today's create-a-workspace behaviour rather than guessing.
//
// Deliberately not implemented against doctor.exclude_bookmarks: that list
// contains release/*, and .../tree/release/1.2 is a real feature branch that
// must get its own workspace.
func isTrunkTarget(branch, localTrunk string, defaultBranch func() (string, error)) bool {
	if branch == "" {
		return false
	}
	if localTrunk != "" {
		return branch == localTrunk
	}
	if defaultBranch == nil {
		return false
	}
	def, err := defaultBranch()
	if err != nil {
		return false
	}
	return branch == def
}

// trunkRevset returns a revset naming repoPath's trunk commit, preferring jj's
// own trunk() and falling back to the branch's origin bookmark. Returns "" when
// neither resolves, which callers must treat as "there is no trunk to return to".
func trunkRevset(jjc trunkLister, repoPath, branch string) string {
	if _, err := jjc.Resolve(repoPath, "trunk()"); err == nil {
		return "trunk()"
	}
	if branch == "" {
		return ""
	}
	rev := remoteBookmarkRevset(branch, "origin")
	if id, err := jjc.Resolve(repoPath, rev); err == nil && strings.TrimSpace(id) != "" {
		return rev
	}
	return ""
}
