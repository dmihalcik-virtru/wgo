// Package inventory is the on-disk cache of the PRs `wgo dash` follows for
// each configured person (gh-97), whether or not a local bookmark leads to
// them: the open PRs they authored, the open PRs awaiting their review, and
// the PRs they authored that were merged or closed recently. Each (person,
// set) is one entry under ~/.wgo/cache/inventory/<login>/<set>.json, with
// the login lowercased, as GitHub treats logins case-insensitively.
//
// It follows internal/prcache: Read never makes a network call, an entry
// keeps its last successful fetch apart from its last attempt, so a failed
// refresh annotates good data instead of replacing it, and a lease file
// beside each entry stops concurrent wgo processes from fetching the same set
// at once. The cached login of the authenticated user lives here too
// (identity.go), since the inventory is what needs it.
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
	"sync"
	"time"

	"github.com/virtru/wgo/internal/atomicfile"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/store"
)

// Set names one of a person's inventory sets.
type Set string

// Inventory sets.
const (
	// OpenAuthored is every open PR, draft or not, the person authored.
	OpenAuthored Set = "open-authored"
	// ReviewRequested is every open PR on which the person, or a team they
	// belong to, is a requested reviewer. GitHub expands team membership.
	ReviewRequested Set = "review-requested"
	// ClosedRecent is the PRs the person authored that were merged or
	// closed within ClosedWindowDays.
	ClosedRecent Set = "closed-recent"
)

// Sets lists every set, in display order.
var Sets = []Set{OpenAuthored, ReviewRequested, ClosedRecent}

// Key identifies one person's set.
type Key struct {
	// Login is the person's GitHub login.
	Login string
	Set   Set
}

// String is the key as the dash names a github_inventory part:
// "<login>/<set>", with the login lowercased as GitHub compares logins, so
// every part name has one spelling.
func (k Key) String() string { return strings.ToLower(k.Login) + "/" + string(k.Set) }

// Query is the GitHub search query for k. since bounds ClosedRecent; the
// other sets ignore it. Archived repositories are excluded.
func (k Key) Query(since time.Time) string {
	switch k.Set {
	case OpenAuthored:
		return "is:pr is:open archived:false author:" + k.Login
	case ReviewRequested:
		return "is:pr is:open archived:false review-requested:" + k.Login
	case ClosedRecent:
		return "is:pr is:closed archived:false author:" + k.Login + " closed:>=" + since.UTC().Format("2006-01-02")
	}
	return ""
}

// ClosedWindowDays is how far back ClosedRecent reaches for an activity
// window of days: max(2 × days, 28), so a period can be compared with the
// one before it.
func ClosedWindowDays(days int) int { return max(2*days, 28) }

// ClosedSince is the first day, in UTC, of ClosedRecent's window at now.
func ClosedSince(now time.Time, days int) time.Time {
	y, m, d := now.UTC().AddDate(0, 0, -ClosedWindowDays(days)).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// State describes the freshness of a cache lookup.
type State int

const (
	// Miss means no usable data: absent, unreadable, or failures only.
	Miss State = iota
	// Stale means data exists but is older than the TTL, or covers a
	// shorter closed-recent window than asked for.
	Stale
	// Fresh means data exists and is within the TTL.
	Fresh
)

// PR is one cached pull request: GitHub's facts plus the ticket references
// found in its title and body.
type PR struct {
	github.PRFacts
	// Refs are ticket references in the title, then the body, in order and
	// deduplicated: Jira-shaped keys as written ("TEAM-123", but also
	// "SHA-256"), and same-repository issues as "gh-<n>". Which Jira-shaped
	// keys count is the reader's choice (gh-97 requirement 5).
	Refs []string `json:"refs,omitempty"`
}

// SchemaVersion is the entry layout Write produces. An entry of another
// version reads as a Miss, so the next refresh rewrites it.
const SchemaVersion = 1

type entry struct {
	Schema int    `json:"schema"`
	Query  string `json:"query,omitempty"`
	// Since is the start of the window a ClosedRecent entry covers.
	Since      time.Time `json:"since,omitzero"`
	PRs        []PR      `json:"prs"`
	IssueCount int       `json:"issue_count,omitempty"`
	Truncated  bool      `json:"truncated,omitempty"`
	// Incomplete is set when the last successful fetch is known to have
	// missed PRs it should hold (github.PRSearchResult.Incomplete).
	Incomplete bool `json:"incomplete,omitempty"`
	// FetchedAt is when the last successful fetch started, so its PRs are
	// at least as new as FetchedAt; LastAttemptAt is when the last try
	// ended.
	FetchedAt     time.Time `json:"fetched_at,omitzero"`
	LastAttemptAt time.Time `json:"last_attempt_at,omitzero"`
	// LastError is the last attempt's failure, or the errors that came
	// with the last successful fetch's partial data.
	LastError string `json:"last_error,omitempty"`
	// Failures counts the attempts that failed since the last success;
	// RetryAt is when GitHub asked a rate-limited caller to come back.
	Failures int       `json:"failures,omitempty"`
	RetryAt  time.Time `json:"retry_at,omitzero"`
	// Dropped, in an OpenAuthored entry, are PRs a refresh no longer found
	// open that the person's ClosedRecent set has not been fetched since
	// DroppedAt (when that refresh ended) to account for: most merged or
	// closed in between. They are as last fetched, at DroppedFetchedAt.
	Dropped          []PR      `json:"dropped,omitempty"`
	DroppedAt        time.Time `json:"dropped_at,omitzero"`
	DroppedFetchedAt time.Time `json:"dropped_fetched_at,omitzero"`
}

// Result is what a lookup served plus its provenance.
type Result struct {
	// PRs is the last successfully fetched set: nil when never fetched,
	// non-nil and empty when GitHub found none.
	PRs   []PR
	State State
	// FetchedAt is when the fetch of PRs started; zero if never.
	FetchedAt     time.Time
	LastAttemptAt time.Time
	// IssueCount is how many PRs GitHub said matched.
	IssueCount int
	// Truncated is set when more matched than GitHub search returns.
	Truncated bool
	// Incomplete is set when the fetch is known to have missed PRs: GitHub
	// returned errors beside partial data, or fewer PRs than it counted.
	// Such a set does not cover what it lacks.
	Incomplete bool
	// Err is the most recent recorded failure: of the last attempt, which
	// can sit beside last-known-good PRs, or the partial-data errors of the
	// last successful fetch.
	Err error
	// Since is the start of the window a ClosedRecent result covers.
	Since time.Time
	// Failures and RetryAt drive NotBefore.
	Failures int
	RetryAt  time.Time
	// Dropped, DroppedAt and DroppedFetchedAt are the entry's (OpenAuthored
	// only): PRs no longer open whose fate ClosedRecent has yet to show.
	Dropped          []PR
	DroppedAt        time.Time
	DroppedFetchedAt time.Time
}

// Failure back-off: after n consecutive failures a set waits
// min(failureBackoff << (n-1), maxFailureBackoff) from its last attempt.
const (
	failureBackoff    = time.Minute
	maxFailureBackoff = 30 * time.Minute
)

// NotBefore is when the set should next be fetched in the background: zero
// after a success, else its last attempt plus an exponential back-off, or
// the time a rate limit named, whichever is later. A set failing on its
// tenth page would otherwise refetch the first nine every lease window.
func (r Result) NotBefore() time.Time {
	// A hand-edited entry may hold any count; only a positive one backs off.
	if r.Failures <= 0 || r.LastAttemptAt.IsZero() {
		return r.RetryAt
	}
	wait := maxFailureBackoff
	if r.Failures <= 6 {
		wait = min(failureBackoff<<(r.Failures-1), maxFailureBackoff)
	}
	if t := r.LastAttemptAt.Add(wait); t.After(r.RetryAt) {
		return t
	}
	return r.RetryAt
}

// Read returns the cached set k and its freshness at now. ttl is the set's
// freshness window. since is the ClosedRecent window start the caller wants;
// an entry covering less of it is Stale. Read never makes a network call.
func Read(k Key, ttl time.Duration, since, now time.Time) Result {
	e, ok := readEntry(k)
	if !ok {
		return Result{State: Miss}
	}
	r := Result{
		PRs:              e.PRs,
		FetchedAt:        e.FetchedAt,
		LastAttemptAt:    e.LastAttemptAt,
		IssueCount:       e.IssueCount,
		Truncated:        e.Truncated,
		Incomplete:       e.Incomplete,
		Since:            e.Since,
		Failures:         e.Failures,
		RetryAt:          e.RetryAt,
		Dropped:          e.Dropped,
		DroppedAt:        e.DroppedAt,
		DroppedFetchedAt: e.DroppedFetchedAt,
	}
	if e.LastError != "" {
		r.Err = errors.New(e.LastError)
	}
	switch {
	case e.FetchedAt.IsZero():
		r.State, r.PRs = Miss, nil
	case now.Sub(e.FetchedAt) >= ttl:
		r.State = Stale
	case k.Set == ClosedRecent && (e.Since.IsZero() || e.Since.After(since)):
		// No recorded window covers nothing in particular.
		r.State = Stale
	default:
		r.State = Fresh
	}
	if r.State != Miss && r.PRs == nil {
		// A fetch that found nothing found none: an entry missing its
		// list must not read as never fetched.
		r.PRs = []PR{}
	}
	return r
}

func readEntry(k Key) (entry, bool) {
	path, err := entryPath(k)
	if err != nil {
		return entry{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logf("inventory cache: read %s: %v", k, err)
		}
		return entry{}, false
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		logf("inventory cache: corrupt entry for %s: %v", k, err)
		return entry{}, false
	}
	if e.Schema != SchemaVersion {
		return entry{}, false
	}
	return e, true
}

// writeMu serialises this process's cache writes so writeFailure's
// read-modify-write cannot overwrite a concurrent successful write.
var writeMu sync.Mutex

func writeEntry(k Key, e entry) error {
	path, err := entryPath(k)
	if err != nil {
		return err
	}
	e.Schema = SchemaVersion
	data, err := json.Marshal(&e)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o644)
}

// writeFailure records a failed attempt, keeping any earlier good data.
func writeFailure(k Key, at time.Time, cause error) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	e, ok := readEntry(k)
	if !ok {
		e = entry{}
	}
	e.LastAttemptAt = at
	e.LastError = cause.Error()
	e.Failures++
	e.RetryAt = time.Time{}
	var rl *github.RateLimitError
	if errors.As(cause, &rl) && rl.ResetAt.After(at) {
		e.RetryAt = rl.ResetAt
	}
	return writeEntry(k, e)
}

// Fetcher performs the live lookups. *github.CLIClient implements it; tests
// substitute fakes.
type Fetcher interface {
	// SearchPullRequests returns every PR a search matches; see
	// github.CLIClient.SearchPullRequests for its error contract.
	SearchPullRequests(ctx context.Context, query string) (*github.PRSearchResult, error)
	// Viewer returns the authenticated user's login.
	Viewer(ctx context.Context) (string, error)
}

// Refresh fetches set k now, for a ClosedRecent window starting at since,
// and writes the result through the cache. It is for background workers and
// explicit refreshes only, never a read path.
//
// The returned error is what the caller should report as a failed lookup:
// the fetch failed (the previous data, if any, is kept and the failure
// recorded beside it) or the result could not be cached. A fetch that
// succeeded with partial-data errors is not a failure: the errors are
// recorded on the entry and returned in Result.Err only. A missing token,
// or a fetch stopped because ctx was cancelled, is returned but not
// recorded: neither says anything about the set.
func Refresh(ctx context.Context, f Fetcher, k Key, since time.Time) (Result, error) {
	if f == nil {
		return Read(k, 0, since, time.Now()), errors.New("inventory: no fetcher")
	}
	if _, err := entryPath(k); err != nil {
		return Result{State: Miss, Err: err}, err
	}
	// The PRs are as new as the search's start: a PR that merges while the
	// pages are read may be missing, so the fetch must not claim to be newer.
	started := time.Now()
	res, err := f.SearchPullRequests(ctx, k.Query(since))
	now := time.Now()
	if res == nil {
		if err == nil {
			err = errors.New("GitHub search returned no result")
		}
		prior := Read(k, 0, since, now)
		prior.Err = err
		if errors.Is(err, github.ErrNoAuth) || ctx.Err() != nil {
			return prior, err
		}
		if werr := writeFailure(k, now, err); werr != nil {
			logf("inventory cache: record failure for %s: %v", k, werr)
			err = errors.Join(err, fmt.Errorf("inventory cache write: %w", werr))
		}
		return prior, err
	}
	prs := make([]PR, 0, len(res.PRs))
	for _, p := range res.PRs {
		prs = append(prs, PR{PRFacts: p, Refs: Refs(p.Repo, p.Number, p.Title, p.Body)})
	}
	e := entry{
		Query:         k.Query(since),
		PRs:           prs,
		IssueCount:    res.IssueCount,
		Truncated:     res.Truncated,
		Incomplete:    res.Incomplete,
		FetchedAt:     started,
		LastAttemptAt: now,
	}
	if k.Set == ClosedRecent {
		e.Since = since
	}
	if err != nil {
		e.LastError = err.Error()
	}
	writeMu.Lock()
	if k.Set == OpenAuthored {
		if prior, ok := readEntry(k); ok {
			closed, _ := readEntry(Key{Login: k.Login, Set: ClosedRecent})
			carryDropped(&e, prior, closed.FetchedAt)
		}
	}
	werr := writeEntry(k, e)
	writeMu.Unlock()
	out := Result{PRs: prs, State: Fresh, FetchedAt: started, LastAttemptAt: now, IssueCount: res.IssueCount, Truncated: res.Truncated,
		Incomplete: res.Incomplete, Err: err, Since: e.Since, Dropped: e.Dropped, DroppedAt: e.DroppedAt, DroppedFetchedAt: e.DroppedFetchedAt}
	if werr != nil {
		// The fetched data is still returned; the error says it was not kept.
		logf("inventory cache: write %s: %v", k, werr)
		return out, fmt.Errorf("inventory cache write: %w", werr)
	}
	return out, nil
}

// carryDropped sets e's Dropped: the PRs prior listed open that e no longer
// does, plus those prior already carried that closedFetched (when the
// person's last ClosedRecent fetch started) has not caught up with. A merged PR thus stays
// visible, as last seen, until ClosedRecent shows it merged, instead of
// vanishing for up to ClosedRecent's TTL.
func carryDropped(e *entry, prior entry, closedFetched time.Time) {
	open := map[string]bool{}
	for _, p := range e.PRs {
		open[prKey(p)] = true
	}
	seen := map[string]bool{}
	newly := false
	for _, p := range prior.PRs {
		if !open[prKey(p)] && !seen[prKey(p)] {
			seen[prKey(p)] = true
			e.Dropped = append(e.Dropped, p)
			newly = true
		}
	}
	carried := false
	if closedFetched.Before(prior.DroppedAt) {
		for _, p := range prior.Dropped {
			if !open[prKey(p)] && !seen[prKey(p)] {
				seen[prKey(p)] = true
				e.Dropped = append(e.Dropped, p)
				carried = true
			}
		}
	}
	switch {
	case !newly && !carried:
		e.Dropped = nil
	case !newly:
		e.DroppedAt, e.DroppedFetchedAt = prior.DroppedAt, prior.DroppedFetchedAt
	default:
		// ClosedRecent must be fetched after this refresh ended to account
		// for the newly dropped PRs, which could have closed while its
		// pages were read; the oldest data carried sets the time.
		e.DroppedAt, e.DroppedFetchedAt = e.LastAttemptAt, prior.FetchedAt
		if carried && prior.DroppedFetchedAt.Before(prior.FetchedAt) {
			e.DroppedFetchedAt = prior.DroppedFetchedAt
		}
	}
}

func prKey(p PR) string { return strings.ToLower(p.Repo) + "#" + strconv.Itoa(p.Number) }

// LockRefresh takes the refresh lease for k, returning false while another
// caller holds one younger than window. The lease file is left in place as a
// back-off marker and reclaimed once it ages past window, so a killed
// refresher never wedges the key. A filesystem fault also returns false and
// is logged.
func LockRefresh(k Key, window time.Duration) bool {
	path, err := entryPath(k)
	if err != nil {
		logf("inventory cache: lease for %s: %v", k, err)
		return false
	}
	return acquireLease(strings.TrimSuffix(path, ".json")+".lock", window, k.String())
}

// KeepLease renews k's lease every interval until the returned stop is
// called, so a fetch of many pages outlives a short lease window without
// another process reclaiming the lease and fetching the set again.
func KeepLease(k Key, interval time.Duration) (stop func()) {
	path, err := entryPath(k)
	if err != nil || interval <= 0 {
		return func() {}
	}
	lock := strings.TrimSuffix(path, ".json") + ".lock"
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-t.C:
				if err := os.Chtimes(lock, now, now); err != nil {
					logf("inventory cache: renew lease for %s: %v", k, err)
				}
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// acquireLease atomically creates lock, or reclaims it once older than
// window, returning true only for the caller that created it.
func acquireLease(lock string, window time.Duration, what string) bool {
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		logf("inventory cache: lease for %s: %v", what, err)
		return false
	}
	if f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644); err == nil {
		_ = f.Close()
		return true
	} else if !os.IsExist(err) {
		logf("inventory cache: lease for %s: %v", what, err)
		return false
	}
	fi, err := os.Stat(lock)
	if err != nil {
		if !os.IsNotExist(err) {
			logf("inventory cache: lease for %s: %v", what, err)
		}
		return false
	}
	if time.Since(fi.ModTime()) < window {
		return false // held: genuine contention
	}
	if err := os.Remove(lock); err != nil && !os.IsNotExist(err) {
		logf("inventory cache: reclaim lease for %s: %v", what, err)
		return false
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if !os.IsExist(err) {
			logf("inventory cache: lease for %s: %v", what, err)
		}
		return false
	}
	_ = f.Close()
	return true
}

// loginRe is a lowercased GitHub login, allowing the underscore of
// Enterprise Managed Users' logins ("alice_acme"). Using it as a directory
// name verbatim keeps distinct people distinct and paths safe.
var loginRe = regexp.MustCompile(`^[a-z0-9_-]{1,100}$`)

// Dir is the inventory cache directory, ~/.wgo/cache/inventory.
func Dir() (string, error) {
	s, err := store.New()
	if err != nil {
		return "", err
	}
	return filepath.Join(s.BaseDir(), "cache", "inventory"), nil
}

func entryPath(k Key) (string, error) {
	login := strings.ToLower(k.Login)
	if !loginRe.MatchString(login) {
		return "", fmt.Errorf("inventory: %q is not a GitHub login", k.Login)
	}
	switch k.Set {
	case OpenAuthored, ReviewRequested, ClosedRecent:
	default:
		return "", fmt.Errorf("inventory: unknown set %q", k.Set)
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, login, string(k.Set)+".json"), nil
}

// Logf, when set, receives cache faults (an unwritable cache dir, a corrupt
// entry, a lease that could not be taken for a reason other than
// contention). cmd wires it to the WGO_DEBUG logger.
var Logf func(format string, args ...any)

func logf(format string, args ...any) {
	if Logf != nil {
		Logf(format, args...)
	}
}
