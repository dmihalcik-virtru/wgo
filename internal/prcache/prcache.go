// Package prcache is a cross-invocation on-disk cache for a branch's pull
// request refs, living under ~/.wgo/cache/pr/<slug>/<branch>.json. It lets
// `wgo statusline` render PR status without a network call in the hot path:
// the default read path serves whatever is on disk (fresh or stale) and never
// blocks, while a background `--refresh` warms the entry.
//
// This is the minimal cache WGO-131 needs; WGO-132 generalizes it (shared use
// by `wgo status`/`wgo pr`, hardened leasing/atomicity).
package prcache

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/links"
	"github.com/virtru/wgo/internal/store"
	"github.com/virtru/wgo/models"
)

// State describes the freshness of a cache lookup.
type State int

const (
	// Miss means no usable cache entry exists (absent or unreadable).
	Miss State = iota
	// Stale means an entry exists but is older than the TTL.
	Stale
	// Fresh means an entry exists and is within the TTL.
	Fresh
)

// entry is the on-disk representation of a cached PR lookup.
//
// PRs/FetchedAt describe the last *successful* fetch and are only ever written
// by one; LastAttemptAt/LastError describe the most recent attempt, successful
// or not. Keeping them apart is what stops a transient GitHub failure from
// being recorded as the authoritative "this branch has no PRs" (WGO-137).
//
// LastError is persisted rather than held in memory because the failing fetch
// usually happens in the detached `_refresh-pr` child: the process that later
// renders the PR line is not the process that saw the error.
//
// The trailing two fields are omitempty, so entries written before WGO-137
// load unchanged, as a success with no recorded attempt.
type entry struct {
	PRs           []models.PRRef `json:"prs"`
	FetchedAt     time.Time      `json:"fetched_at"`
	LastAttemptAt time.Time      `json:"last_attempt_at,omitempty"`
	LastError     string         `json:"last_error,omitempty"`
}

// Result is what a cache lookup served, plus the provenance a renderer needs
// to explain it.
type Result struct {
	// PRs is the last successfully fetched list. An empty non-nil slice means
	// GitHub genuinely reported no PRs; nil means nothing was ever fetched.
	PRs []models.PRRef
	// State is the freshness of PRs, derived from FetchedAt.
	State State
	// FetchedAt is when PRs were last successfully fetched; zero if never.
	FetchedAt time.Time
	// LastAttemptAt is when a fetch was last attempted, successful or not.
	LastAttemptAt time.Time
	// Err is the most recent recorded fetch failure. It can be non-nil
	// alongside a populated PRs: a refresh failed, but last-known-good data
	// survived it.
	Err error
}

// Read returns the cached PR refs for a branch and their freshness. It never
// makes a network call and never blocks. A cached "no PRs" result is a valid
// Fresh/Stale hit (empty slice), so callers do not re-fetch a branch that
// genuinely has no PRs. An entry that only ever recorded failures reads as a
// Miss carrying Err, so callers can tell "no data" from "no PRs".
func Read(remoteURL, repoPath, branch string, ttl time.Duration) Result {
	e, ok := readEntry(remoteURL, repoPath, branch)
	if !ok {
		return Result{State: Miss}
	}
	r := Result{
		PRs:           e.PRs,
		FetchedAt:     e.FetchedAt,
		LastAttemptAt: e.LastAttemptAt,
	}
	if e.LastError != "" {
		r.Err = errors.New(e.LastError)
	}
	switch {
	case e.FetchedAt.IsZero():
		// Never successfully fetched: an error-only entry is not data.
		r.State = Miss
	case time.Since(e.FetchedAt) >= ttl:
		r.State = Stale
	default:
		r.State = Fresh
	}
	return r
}

// readEntry loads and decodes the on-disk entry. ok is false when the entry is
// absent or unreadable, which callers treat as no cached knowledge at all.
func readEntry(remoteURL, repoPath, branch string) (entry, bool) {
	path, err := prPath(remoteURL, repoPath, branch)
	if err != nil {
		return entry{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return entry{}, false
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return entry{}, false
	}
	return e, true
}

// Write stores the PR refs from a successful fetch, stamping the current time
// and clearing any recorded failure.
func Write(remoteURL, repoPath, branch string, refs []models.PRRef) error {
	now := time.Now()
	return writeEntry(remoteURL, repoPath, branch, entry{
		PRs:           refs,
		FetchedAt:     now,
		LastAttemptAt: now,
	})
}

// WriteFailure records that a fetch was attempted and failed, preserving any
// previously cached PRs and their fetch time. This is the point of the split:
// a failure annotates the entry, it never replaces good data with an empty
// list.
//
// The read-modify-write is not locked. Two writers racing can drop a recorded
// error, which costs a warning line, never good data — Write only ever moves
// PRs forward to a newer successful fetch.
func WriteFailure(remoteURL, repoPath, branch string, cause error) error {
	e, _ := readEntry(remoteURL, repoPath, branch)
	e.LastAttemptAt = time.Now()
	e.LastError = cause.Error()
	return writeEntry(remoteURL, repoPath, branch, e)
}

// writeEntry persists e. The write is atomic (temp file + rename) so a killed
// writer never leaves a truncated entry for a concurrent reader. The temp file
// gets a unique name so two concurrent writers never clobber each other's temp
// before the rename.
func writeEntry(remoteURL, repoPath, branch string, e entry) error {
	path, err := prPath(remoteURL, repoPath, branch)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(&e, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// Invalidate removes the cached PR entry (and any refresh lease) for a branch so
// the next Read is a Miss. A missing entry is not an error. Callers invalidate
// after mutating PR state (wgo sync, remote-branch deletion) so stale review or
// merge state does not linger in the cache.
func Invalidate(remoteURL, repoPath, branch string) error {
	path, err := prPath(remoteURL, repoPath, branch)
	if err != nil {
		return err
	}
	lock := strings.TrimSuffix(path, ".json") + ".lock"
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(lock); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LockRefresh reports whether a background refresh should be started now for
// this repo/branch. Acquisition is atomic (O_CREATE|O_EXCL) so that when no
// lease exists exactly one concurrent caller wins; rapid re-renders back off
// for `window`, and a stale lease (older than window) is reclaimed so a killed
// refresher never wedges the key permanently. Best-effort: on any error it
// returns false (skip the refresh) rather than risk a stampede.
func LockRefresh(remoteURL, repoPath, branch string, window time.Duration) bool {
	path, err := prPath(remoteURL, repoPath, branch)
	if err != nil {
		return false
	}
	lock := strings.TrimSuffix(path, ".json") + ".lock"
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		return false
	}
	return acquireLease(lock, window)
}

// acquireLease atomically creates the lock file, returning true only for the
// single caller that created it. If the lock already exists it is reclaimed
// (and re-acquired) only when older than window.
func acquireLease(lock string, window time.Duration) bool {
	if f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644); err == nil {
		_ = f.Close()
		return true
	} else if !os.IsExist(err) {
		return false
	}
	// Lock exists: reclaim only once it has aged past the back-off window.
	fi, err := os.Stat(lock)
	if err != nil || time.Since(fi.ModTime()) < window {
		return false
	}
	if err := os.Remove(lock); err != nil {
		return false
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return false // lost the reclaim race to another caller
	}
	_ = f.Close()
	return true
}

// prPath returns the cache file path for a repo/branch:
// ~/.wgo/cache/pr/<slug>/<sanitized-branch>.json.
func prPath(remoteURL, repoPath, branch string) (string, error) {
	s, err := store.New()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(s.BaseDir(), "cache", "pr", slug(remoteURL, repoPath))
	return filepath.Join(dir, github.SanitizeBranch(branch)+".json"), nil
}

// slug derives a stable, filesystem-safe cache namespace for a repo: the
// GitHub "owner-repo" when the origin remote is a GitHub URL, otherwise the
// repo's base directory name. Using owner-repo lets worktrees of the same repo
// share cache entries.
func slug(remoteURL, repoPath string) string {
	if u := links.RepoURL(remoteURL); u != "" {
		ownerRepo := strings.TrimPrefix(u, "https://github.com/")
		if ownerRepo != "" {
			return github.SanitizeBranch(strings.ReplaceAll(ownerRepo, "/", "-"))
		}
	}
	return github.SanitizeBranch(filepath.Base(repoPath))
}
