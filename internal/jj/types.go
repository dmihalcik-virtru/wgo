// Package jj wraps the Jujutsu (jj) CLI to provide structured access to
// workspaces, bookmarks, the change DAG, and the git interop subcommands wgo
// needs. All types are jj-native and do not mirror git equivalents.
package jj

import (
	"errors"
	"time"
)

// Workspace is a single jj workspace attached to a repository. A repository
// can have many workspaces; each has its own working copy and an associated
// commit (the workspace's `@`).
type Workspace struct {
	// Name is the workspace symbol (e.g. "default", "ticket-123").
	Name string
	// Path is the absolute filesystem root of the workspace.
	Path string
	// ChangeID is the change-id of the workspace's working-copy commit.
	ChangeID string
	// CommitID is the commit-id (40-char hex) of the working-copy commit.
	CommitID string
}

// Bookmark is a named ref pointing at a change. May be local-only, or paired
// with a remote (then Remote is non-empty).
type Bookmark struct {
	// Name is the bookmark name (without remote prefix).
	Name string
	// ChangeID is the change-id of the bookmark's target, or empty when the
	// bookmark is conflicted / has no target.
	ChangeID string
	// CommitID is the commit-id (40-char hex) of the bookmark's target, or
	// empty when conflicted / unset.
	CommitID string
	// Remote is the remote name when this entry represents a remote bookmark,
	// or "" for a purely local bookmark.
	Remote string
	// Tracked is true when this bookmark is tracking a remote counterpart.
	Tracked bool
	// Conflict is true when the bookmark has conflicting targets.
	Conflict bool
	// Present is true when the bookmark resolves to a commit. After
	// `jj bookmark delete` of a tracked bookmark, jj keeps a tombstone
	// entry with Present=false until the deletion is pushed to the remote.
	Present bool
}

// Change is a single node in the jj change DAG.
type Change struct {
	// ChangeID is the stable, anonymous change identifier.
	ChangeID string
	// CommitID is the underlying git-compatible commit id (40-char hex).
	CommitID string
	// Description is the change description, including trailing newline if
	// jj rendered one.
	Description string
	// Bookmarks are the local bookmark names pointing at this change.
	Bookmarks []string
	// Parents are the change-ids of this change's parents.
	Parents []string
	// AuthorEmail is the change author's email address.
	AuthorEmail string
	// AuthorTimestamp is the change author timestamp parsed from `%+`.
	AuthorTimestamp time.Time
	// Empty indicates the change touches no files (e.g. fresh `jj new`).
	Empty bool
	// CurrentWorkingCopy is true when this change is the `@` of the current
	// workspace.
	CurrentWorkingCopy bool
}

// LogEntry is the parsed line-delimited JSON record produced by a `jj log`
// invocation with LogEntryTemplate. It mirrors Change.
type LogEntry = Change

// Status is a snapshot of a workspace's working-copy state.
type Status struct {
	// Clean reports whether the working copy matches its parent commit.
	Clean bool
	// Modified files reported as `M` by jj status.
	Modified []string
	// Added files reported as `A`. jj does not distinguish "untracked" from
	// "added"; brand-new files appear here.
	Added []string
	// Deleted files reported as `D`.
	Deleted []string
	// Untracked is always empty in jj's model and exists only so callers
	// written against a git-shaped API compile cleanly. jj auto-tracks new
	// files into Added.
	Untracked []string
	// CurrentChange describes the workspace's `@` change.
	CurrentChange Change
}

// PushOpts configures a `jj git push` invocation.
type PushOpts struct {
	// Bookmarks names the bookmarks to push. Empty means push all tracked
	// bookmarks (i.e. invoke `jj git push --tracked`).
	Bookmarks []string
	// AllowNew permits creating new remote bookmarks via --allow-new.
	AllowNew bool
	// AllowEmptyDescription forwards --allow-empty-description.
	AllowEmptyDescription bool
	// DryRun forwards --dry-run.
	DryRun bool
	// Remote selects the remote, falling back to jj's default when "".
	Remote string
}

// PushResult summarises the outcome of a push.
type PushResult struct {
	// Pushed is the list of bookmark names that were updated remotely.
	Pushed []string
	// Failed maps bookmark name -> error message for any partial failures
	// jj surfaced per-bookmark.
	Failed map[string]string
}

// InitOpts configures `jj git init`. By default the repo is colocated with
// Git (see GitInit). When GitRepo is set, jj will use the supplied git
// directory as the backing store instead, and colocation stays off.
type InitOpts struct {
	// GitRepo, when non-empty, is passed as --git-repo and points at an
	// existing git repository to use as backing store.
	GitRepo string
	// RemoteName names the remote created during init (jj's default is
	// "origin"; empty preserves that default).
	RemoteName string
}

// BookmarkListOpts filters a `jj bookmark list` invocation.
type BookmarkListOpts struct {
	// Local restricts to local bookmarks only.
	Local bool
	// Remote, when non-empty, filters to bookmarks belonging to this remote
	// (passes --remote <pattern>).
	Remote string
	// Conflicted restricts to conflicted bookmarks.
	Conflicted bool
	// Tracked restricts to tracked remote bookmarks.
	Tracked bool
	// AllRemotes includes synchronized remote bookmarks (--all-remotes).
	AllRemotes bool
	// Names optionally filters to bookmarks matching these patterns.
	Names []string
}

// FileChange is a single per-file entry from `jj diff --summary`, used by
// callers that need to distinguish added vs modified vs deleted paths
// (e.g. counting "newly created spec files" in the pilot summary).
type FileChange struct {
	// Status is the diff-status character emitted by jj: 'A' (added),
	// 'M' (modified), or 'D' (deleted).
	Status rune
	// Path is the file path relative to the repo root.
	Path string
}

// ErrLeaseFailed is returned by GitPush when jj's safety check refused to
// move a remote bookmark because the remote had advanced since the last
// fetch.
var ErrLeaseFailed = errors.New("jj git push: remote bookmark moved unexpectedly")

// ErrNothingToPush is returned by GitPush when jj reports there is nothing
// to push (no matching bookmarks, no changes).
var ErrNothingToPush = errors.New("jj git push: no bookmarks to push")
