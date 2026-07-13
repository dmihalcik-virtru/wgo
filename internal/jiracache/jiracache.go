// Package jiracache is a cross-invocation on-disk cache for a ticket's live
// Jira status, living under ~/.wgo/cache/jira/<ticket>.json. It lets `wgo .`
// and `wgo statusline` show a ticket's Jira status without a synchronous acli
// call in the hot path: the read path serves whatever is on disk (fresh or
// stale) and never blocks, while a background `_refresh-jira` warms the entry.
//
// It deliberately mirrors internal/prcache; the only structural difference is
// that a Jira status is repo-independent, so entries are keyed by ticket alone
// rather than by remote/repo/branch.
package jiracache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/store"
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

// Info is the cached Jira data projected onto the context.
type Info struct {
	Status   string `json:"status"`             // status name, e.g. "In Review"
	Assignee string `json:"assignee,omitempty"` // assignee display name, if any
}

// entry is the on-disk representation of a cached Jira lookup.
type entry struct {
	Info      Info      `json:"info"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Read returns the cached Jira info for a ticket and its freshness. It never
// makes a network call and never blocks. A Miss returns a zero Info. A cached
// empty status is a valid Fresh/Stale hit, so callers do not re-fetch a ticket
// that genuinely has no mappable status.
func Read(ticket string, ttl time.Duration) (Info, State) {
	path, err := jiraPath(ticket)
	if err != nil {
		return Info{}, Miss
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Info{}, Miss
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return Info{}, Miss
	}
	if time.Since(e.FetchedAt) >= ttl {
		return e.Info, Stale
	}
	return e.Info, Fresh
}

// Write stores the Jira info for a ticket, stamping the current time. The write
// is atomic (temp file + rename) so a killed writer never leaves a truncated
// entry for a concurrent reader. The temp file gets a unique name so two
// concurrent writers never clobber each other's temp before the rename.
func Write(ticket string, info Info) error {
	path, err := jiraPath(ticket)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(&entry{Info: info, FetchedAt: time.Now()}, "", "  ")
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

// LockRefresh reports whether a background refresh should be started now for
// this ticket. Acquisition is atomic (O_CREATE|O_EXCL) so that when no lease
// exists exactly one concurrent caller wins; rapid re-renders back off for
// `window`, and a stale lease (older than window) is reclaimed so a killed
// refresher never wedges the key permanently. Best-effort: on any error it
// returns false (skip the refresh) rather than risk a stampede.
func LockRefresh(ticket string, window time.Duration) bool {
	path, err := jiraPath(ticket)
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

// jiraPath returns the cache file path for a ticket:
// ~/.wgo/cache/jira/<sanitized-ticket>.json.
func jiraPath(ticket string) (string, error) {
	s, err := store.New()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(s.BaseDir(), "cache", "jira")
	return filepath.Join(dir, github.SanitizeBranch(ticket)+".json"), nil
}
