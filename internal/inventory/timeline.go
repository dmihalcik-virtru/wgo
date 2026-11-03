package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/atomicfile"
	"github.com/virtru/wgo/internal/github"
)

// This file caches each followed PR's review history (gh-104): one entry per
// PR under ~/.wgo/cache/inventory/timelines.d/<owner>/<repo>/<number>.json,
// lowercased. A login cannot contain a dot, so the directory never collides
// with a person's. An entry is keyed by the PR's updated_at at the fetch and
// is refetched only once GitHub reports a later one; like the sets, it keeps
// its last good timeline beside a failed attempt, backs off after failures,
// and is fetched behind a lease. A rate limit pauses every timeline fetch
// until GitHub's reset, so a background cycle does not spend its budget on
// requests bound to fail.

// TimelineKey identifies one PR's timeline.
type TimelineKey struct {
	// Repo is the base repository's owner/repo.
	Repo   string
	Number int
}

// String is the PR's reference, "owner/repo#123".
func (k TimelineKey) String() string { return k.Repo + "#" + strconv.Itoa(k.Number) }

// TimelineSchemaVersion is the timeline entry layout. An entry of another
// version reads as never fetched.
const TimelineSchemaVersion = 1

// timelinesDir is the timelines' directory beside the people's.
const timelinesDir = "timelines.d"

type timelineEntry struct {
	Schema   int                `json:"schema"`
	Timeline *github.PRTimeline `json:"timeline,omitempty"`
	// FetchedAt is the last successful fetch, LastAttemptAt the last try.
	FetchedAt     time.Time `json:"fetched_at,omitzero"`
	LastAttemptAt time.Time `json:"last_attempt_at,omitzero"`
	LastError     string    `json:"last_error,omitempty"`
	// Failures counts the attempts that failed since the last success.
	Failures int       `json:"failures,omitempty"`
	RetryAt  time.Time `json:"retry_at,omitzero"`
}

// TimelineResult is a cached timeline and its provenance.
type TimelineResult struct {
	// Timeline is the last successfully fetched history; nil if never.
	Timeline      *github.PRTimeline
	FetchedAt     time.Time
	LastAttemptAt time.Time
	// Err is the last attempt's failure, which can sit beside an older
	// good Timeline.
	Err      error
	Failures int
	RetryAt  time.Time
}

// NotBefore is when the timeline should next be fetched in the background,
// by the sets' back-off (Result.NotBefore).
func (r TimelineResult) NotBefore() time.Time {
	return Result{Failures: r.Failures, LastAttemptAt: r.LastAttemptAt, RetryAt: r.RetryAt}.NotBefore()
}

// Current reports whether the cached timeline was fetched for a PR updated
// at updatedAt or later: GitHub bumps updated_at with every event, so the
// history cannot have changed since. A timeline is never current for a PR
// whose updated_at is unknown.
func (r TimelineResult) Current(updatedAt time.Time) bool {
	return r.Timeline != nil && !updatedAt.IsZero() && !r.Timeline.UpdatedAt.Before(updatedAt)
}

// ReadTimeline returns the cached timeline of k. It never makes a network
// call.
func ReadTimeline(k TimelineKey) TimelineResult {
	e, ok := readTimelineEntry(k)
	if !ok {
		return TimelineResult{}
	}
	r := TimelineResult{Timeline: e.Timeline, FetchedAt: e.FetchedAt, LastAttemptAt: e.LastAttemptAt, Failures: e.Failures, RetryAt: e.RetryAt}
	if e.LastError != "" {
		r.Err = errors.New(e.LastError)
	}
	return r
}

func readTimelineEntry(k TimelineKey) (timelineEntry, bool) {
	path, err := timelinePath(k)
	if err != nil {
		return timelineEntry{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logf("inventory cache: read timeline %s: %v", k, err)
		}
		return timelineEntry{}, false
	}
	var e timelineEntry
	if err := json.Unmarshal(data, &e); err != nil {
		logf("inventory cache: corrupt timeline for %s: %v", k, err)
		return timelineEntry{}, false
	}
	if e.Schema != TimelineSchemaVersion {
		return timelineEntry{}, false
	}
	return e, true
}

func writeTimelineEntry(k TimelineKey, e timelineEntry) error {
	path, err := timelinePath(k)
	if err != nil {
		return err
	}
	e.Schema = TimelineSchemaVersion
	data, err := json.Marshal(&e)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o644)
}

// TimelineFetcher reads PR timelines. *github.CLIClient implements it; tests
// substitute fakes.
type TimelineFetcher interface {
	// PRTimeline returns a PR's review history; see
	// github.CLIClient.PRTimeline.
	PRTimeline(ctx context.Context, repo string, number int) (*github.PRTimeline, error)
}

// RefreshTimeline fetches k's timeline now and writes it through the cache.
// It is for background workers and explicit refreshes only. A failure keeps
// the previous timeline and is recorded beside it, except a missing token or
// a cancelled ctx, which say nothing about the PR. A rate limit also pauses
// every timeline fetch until GitHub's reset (TimelinesPausedUntil).
func RefreshTimeline(ctx context.Context, f TimelineFetcher, k TimelineKey) (TimelineResult, error) {
	if f == nil {
		return ReadTimeline(k), errors.New("inventory: no timeline fetcher")
	}
	if _, err := timelinePath(k); err != nil {
		return TimelineResult{Err: err}, err
	}
	tl, err := f.PRTimeline(ctx, k.Repo, k.Number)
	now := time.Now()
	if err == nil && tl == nil {
		err = errors.New("GitHub returned no timeline")
	}
	if err != nil {
		prior := ReadTimeline(k)
		prior.Err = err
		if errors.Is(err, github.ErrNoAuth) || ctx.Err() != nil {
			return prior, err
		}
		var rl *github.RateLimitError
		if errors.As(err, &rl) {
			pauseTimelines(now, rl.ResetAt)
		}
		writeMu.Lock()
		e, _ := readTimelineEntry(k)
		e.LastAttemptAt, e.LastError, e.RetryAt = now, err.Error(), time.Time{}
		e.Failures++
		if rl != nil && rl.ResetAt.After(now) {
			e.RetryAt = rl.ResetAt
		}
		werr := writeTimelineEntry(k, e)
		writeMu.Unlock()
		if werr != nil {
			logf("inventory cache: record timeline failure for %s: %v", k, werr)
			err = errors.Join(err, fmt.Errorf("inventory cache write: %w", werr))
		}
		return prior, err
	}
	e := timelineEntry{Timeline: tl, FetchedAt: now, LastAttemptAt: now}
	writeMu.Lock()
	werr := writeTimelineEntry(k, e)
	writeMu.Unlock()
	out := TimelineResult{Timeline: tl, FetchedAt: now, LastAttemptAt: now}
	if werr != nil {
		logf("inventory cache: write timeline %s: %v", k, werr)
		return out, fmt.Errorf("inventory cache write: %w", werr)
	}
	return out, nil
}

// LockTimelineRefresh takes k's refresh lease, like LockRefresh.
func LockTimelineRefresh(k TimelineKey, window time.Duration) bool {
	path, err := timelinePath(k)
	if err != nil {
		logf("inventory cache: timeline lease for %s: %v", k, err)
		return false
	}
	return acquireLease(strings.TrimSuffix(path, ".json")+".lock", window, k.String())
}

// timelinePauseFile records a rate limit's reset for every timeline fetch.
const timelinePauseFile = "paused.json"

// defaultTimelinePause is how long a rate limit that names no reset pauses
// the timeline fetches.
const defaultTimelinePause = 5 * time.Minute

func pauseTimelines(now, resetAt time.Time) {
	if !resetAt.After(now) {
		resetAt = now.Add(defaultTimelinePause)
	}
	dir, err := Dir()
	if err != nil {
		return
	}
	data, _ := json.Marshal(struct {
		Until time.Time `json:"until"`
	}{resetAt})
	if err := atomicfile.Write(filepath.Join(dir, timelinesDir, timelinePauseFile), data, 0o644); err != nil {
		logf("inventory cache: pause timelines: %v", err)
	}
}

// TimelinesPausedUntil is when the last rate limit a timeline fetch met
// resets: no timeline should be fetched before then. Zero when none is
// recorded. It never makes a network call.
func TimelinesPausedUntil() time.Time {
	dir, err := Dir()
	if err != nil {
		return time.Time{}
	}
	data, err := os.ReadFile(filepath.Join(dir, timelinesDir, timelinePauseFile))
	if err != nil {
		return time.Time{}
	}
	var p struct {
		Until time.Time `json:"until"`
	}
	if json.Unmarshal(data, &p) != nil {
		return time.Time{}
	}
	return p.Until
}

// ownerRe and repoRe are a lowercased GitHub owner and repository name; a
// repository may not be named "." or "..".
var (
	ownerRe = regexp.MustCompile(`^[a-z0-9_-]{1,100}$`)
	repoRe  = regexp.MustCompile(`^[a-z0-9._-]{1,100}$`)
)

func timelinePath(k TimelineKey) (string, error) {
	owner, name, _ := strings.Cut(strings.ToLower(k.Repo), "/")
	if !ownerRe.MatchString(owner) || !repoRe.MatchString(name) || name == "." || name == ".." || k.Number <= 0 {
		return "", fmt.Errorf("inventory: %q is not a pull request", k.String())
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, timelinesDir, owner, name, strconv.Itoa(k.Number)+".json"), nil
}
