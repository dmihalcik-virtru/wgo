// Package issuecache is a cross-invocation on-disk cache for GitHub issue
// status, used for `gh-N` tickets. Entries live under
// ~/.wgo/cache/ghissue/<owner>/<repo>/<number>.json (owner and repo lowercased,
// as GitHub treats them case-insensitively), keyed by repository and issue
// number, because an issue number only means something within its repository.
//
// It is modelled on internal/jiracache, with internal/prcache's split between
// the last successful fetch and the last attempt: Read never makes a network
// call, a fetch failure annotates an entry instead of replacing good data,
// and an entry that only ever recorded failures reads as a Miss carrying the
// error, so callers can tell "unknown" from a real issue state.
package issuecache

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/virtru/wgo/internal/atomicfile"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/store"
)

// State describes the freshness of a cache lookup.
type State int

const (
	// Miss means no usable data: absent, unreadable, or failures only.
	Miss State = iota
	// Stale means data exists but is older than the TTL.
	Stale
	// Fresh means data exists and is within the TTL.
	Fresh
)

// Info is the cached issue data.
type Info struct {
	Number      int       `json:"number"`
	Title       string    `json:"title,omitempty"`
	State       string    `json:"state"` // "open" or "closed"
	StateReason string    `json:"state_reason,omitempty"`
	URL         string    `json:"url,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
	IsPR        bool      `json:"is_pr,omitempty"`
}

type entry struct {
	Info          Info      `json:"info"`
	FetchedAt     time.Time `json:"fetched_at,omitzero"`
	LastAttemptAt time.Time `json:"last_attempt_at,omitzero"`
	LastError     string    `json:"last_error,omitempty"`
}

// Result is what a lookup served plus its provenance.
type Result struct {
	Info          Info
	State         State
	FetchedAt     time.Time
	LastAttemptAt time.Time
	// Err is the most recent recorded fetch failure, possibly alongside
	// last-known-good Info. After Refresh it can also accompany fresh data,
	// meaning the fetch worked but the result could not be cached.
	Err error
}

// Key identifies an issue.
type Key struct {
	Owner  string
	Repo   string
	Number int
}

func (k Key) String() string { return fmt.Sprintf("%s/%s#%d", k.Owner, k.Repo, k.Number) }

// Read returns the cached issue and its freshness. It never blocks on the
// network.
func Read(k Key, ttl time.Duration) Result {
	e, ok := readEntry(k)
	if !ok {
		return Result{State: Miss}
	}
	r := Result{Info: e.Info, FetchedAt: e.FetchedAt, LastAttemptAt: e.LastAttemptAt}
	if e.LastError != "" {
		r.Err = errors.New(e.LastError)
	}
	switch {
	case e.FetchedAt.IsZero():
		r.State = Miss
	case time.Since(e.FetchedAt) >= ttl:
		r.State = Stale
	default:
		r.State = Fresh
	}
	return r
}

func readEntry(k Key) (entry, bool) {
	path, err := issuePath(k)
	if err != nil {
		return entry{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logf("issue cache: read %s: %v", k, err)
		}
		return entry{}, false
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		logf("issue cache: corrupt entry for %s: %v", k, err)
		return entry{}, false
	}
	return e, true
}

// writeMu serialises this process's cache writes so WriteFailure's
// read-modify-write cannot overwrite a concurrent successful Write with a
// stale copy. atomicfile keeps each file whole; this keeps it current.
var writeMu sync.Mutex

// Write stores a successful fetch, clearing any recorded failure.
func Write(k Key, info Info) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	now := time.Now()
	return writeEntry(k, entry{Info: info, FetchedAt: now, LastAttemptAt: now})
}

// WriteFailure records a failed attempt, keeping any earlier good data.
func WriteFailure(k Key, cause error) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	e, _ := readEntry(k)
	e.LastAttemptAt = time.Now()
	e.LastError = cause.Error()
	return writeEntry(k, e)
}

func writeEntry(k Key, e entry) error {
	path, err := issuePath(k)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(&e, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o644)
}

// Logf, when set, receives cache faults (an unwritable cache dir, a lease
// that could not be taken for a reason other than contention). It is nil by
// default; cmd wires it to the WGO_DEBUG logger. Refresh and LockRefresh still
// never fail their caller: these faults are made observable, not fatal.
var Logf func(format string, args ...any)

func logf(format string, args ...any) {
	if Logf != nil {
		Logf(format, args...)
	}
}

// Fetcher performs the live lookup. cmd supplies a GitHub-backed one; tests
// count calls.
type Fetcher interface {
	FetchIssue(k Key) (Info, error)
}

// Refresh fetches the issue now and writes the result through the cache. It
// is for background workers and explicit refreshes only, never a read path.
//
// Result.Err can be set in two shapes: with State == Fresh, when the fetch
// succeeded but the cache write failed (the data is valid, not persisted); or
// alongside prior stale data, when the fetch itself failed.
func Refresh(f Fetcher, k Key) Result {
	if f == nil {
		return Read(k, 0)
	}
	info, err := f.FetchIssue(k)
	if err != nil {
		prior := Read(k, 0)
		prior.Err = err
		if errors.Is(err, github.ErrNoAuth) {
			// A missing token is not an answer about the issue: caching it
			// would keep reporting an error after the user logs in.
			return prior
		}
		if werr := WriteFailure(k, err); werr != nil {
			logf("issue cache: record failure for %s: %v", k, werr)
			prior.Err = errors.Join(err, fmt.Errorf("issue cache write: %w", werr))
		}
		return prior
	}
	now := time.Now()
	res := Result{Info: info, State: Fresh, FetchedAt: now, LastAttemptAt: now}
	if werr := Write(k, info); werr != nil {
		// The fetched data is still served; the error says it was not kept.
		logf("issue cache: write %s: %v", k, werr)
		res.Err = fmt.Errorf("issue cache write: %w", werr)
	}
	return res
}

// LockRefresh takes the refresh lease for k, returning false while another
// caller holds one younger than window. The lease file is left in place as a
// back-off marker and reclaimed once it ages past window, so a killed
// refresher never wedges the key.
//
// A filesystem fault (as opposed to contention) also returns false, since a
// cache that cannot hold a lease cannot hold the result either, but it is
// logged through Logf so a broken cache dir is diagnosable.
func LockRefresh(k Key, window time.Duration) bool {
	path, err := issuePath(k)
	if err != nil {
		logf("issue cache: lease for %s: %v", k, err)
		return false
	}
	lock := strings.TrimSuffix(path, ".json") + ".lock"
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		logf("issue cache: lease for %s: %v", k, err)
		return false
	}
	if f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644); err == nil {
		_ = f.Close()
		return true
	} else if !os.IsExist(err) {
		logf("issue cache: lease for %s: %v", k, err)
		return false
	}
	fi, err := os.Stat(lock)
	if err != nil {
		if !os.IsNotExist(err) {
			logf("issue cache: lease for %s: %v", k, err)
		}
		return false
	}
	if time.Since(fi.ModTime()) < window {
		return false // held: genuine contention
	}
	if err := os.Remove(lock); err != nil && !os.IsNotExist(err) {
		logf("issue cache: reclaim lease for %s: %v", k, err)
		return false
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if !os.IsExist(err) {
			logf("issue cache: lease for %s: %v", k, err)
		}
		return false
	}
	_ = f.Close()
	return true
}

// slugPart is what GitHub allows in an owner or repo name. Using it as a
// directory name verbatim (after lowercasing) keeps distinct repos distinct,
// which a lossy sanitizer would not.
var slugPart = regexp.MustCompile(`^[a-z0-9._-]{1,100}$`)

func issuePath(k Key) (string, error) {
	if k.Owner == "" || k.Repo == "" || k.Number <= 0 {
		return "", fmt.Errorf("issuecache: incomplete key %s", k)
	}
	s, err := store.New()
	if err != nil {
		return "", err
	}
	owner, repo := strings.ToLower(k.Owner), strings.ToLower(k.Repo)
	for _, part := range []string{owner, repo} {
		if !slugPart.MatchString(part) || part == "." || part == ".." {
			return "", fmt.Errorf("issuecache: unusable key %s", k)
		}
	}
	return filepath.Join(s.BaseDir(), "cache", "ghissue", owner, repo, fmt.Sprintf("%d.json", k.Number)), nil
}
