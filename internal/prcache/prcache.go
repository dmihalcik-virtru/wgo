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
type entry struct {
	PRs       []models.PRRef `json:"prs"`
	FetchedAt time.Time      `json:"fetched_at"`
}

// Read returns the cached PR refs for a branch and their freshness. It never
// makes a network call and never blocks. A Miss returns nil refs. A cached
// "no PRs" result is a valid Fresh/Stale hit (empty slice), so callers do not
// re-fetch a branch that genuinely has no PRs.
func Read(remoteURL, repoPath, branch string, ttl time.Duration) ([]models.PRRef, State) {
	path, err := prPath(remoteURL, repoPath, branch)
	if err != nil {
		return nil, Miss
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, Miss
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, Miss
	}
	if time.Since(e.FetchedAt) >= ttl {
		return e.PRs, Stale
	}
	return e.PRs, Fresh
}

// Write stores the PR refs for a branch, stamping the current time. The write
// is atomic (temp file + rename) so a killed writer never leaves a truncated
// entry for a concurrent reader. The temp file gets a unique name so two
// concurrent writers never clobber each other's temp before the rename.
func Write(remoteURL, repoPath, branch string, refs []models.PRRef) error {
	path, err := prPath(remoteURL, repoPath, branch)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(&entry{PRs: refs, FetchedAt: time.Now()}, "", "  ")
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
