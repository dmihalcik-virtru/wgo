package inventory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/github"
)

// fakeFetcher serves canned results and counts calls.
type fakeFetcher struct {
	res     *github.PRSearchResult
	err     error
	login   string
	calls   int
	queries []string
}

func (f *fakeFetcher) SearchPullRequests(_ context.Context, q string) (*github.PRSearchResult, error) {
	f.calls++
	f.queries = append(f.queries, q)
	return f.res, f.err
}

func (f *fakeFetcher) Viewer(context.Context) (string, error) {
	f.calls++
	return f.login, f.err
}

var (
	alice = Key{Login: "Alice", Set: OpenAuthored}
	since = time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
)

func result(prs ...github.PRFacts) *github.PRSearchResult {
	return &github.PRSearchResult{PRs: prs, IssueCount: len(prs)}
}

func TestQueries(t *testing.T) {
	assert.Equal(t, "is:pr is:open archived:false author:alice", Key{"alice", OpenAuthored}.Query(since))
	assert.Equal(t, "is:pr is:open archived:false review-requested:alice", Key{"alice", ReviewRequested}.Query(since))
	assert.Equal(t, "is:pr is:closed archived:false author:alice closed:>=2026-10-02", Key{"alice", ClosedRecent}.Query(since))
	assert.Equal(t, "alice/closed-recent", Key{"alice", ClosedRecent}.String())
	assert.Equal(t, "alice/open-authored", Key{"Alice", OpenAuthored}.String(), "one spelling per part name")
}

func TestClosedWindow(t *testing.T) {
	assert.Equal(t, 28, ClosedWindowDays(14))
	assert.Equal(t, 28, ClosedWindowDays(3))
	assert.Equal(t, 60, ClosedWindowDays(30))
	now := time.Date(2026, 10, 30, 23, 30, 0, 0, time.FixedZone("x", -7*3600)) // 06:30 UTC on the 31st
	assert.Equal(t, time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), ClosedSince(now, 14))
}

func TestColdReadIsMiss(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := Read(alice, time.Hour, since, time.Now())
	assert.Equal(t, Miss, r.State)
	assert.Nil(t, r.PRs)
	assert.NoError(t, r.Err)
}

func TestRefreshWritesAndReadAges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fakeFetcher{res: result(github.PRFacts{Repo: "acme/api", Number: 1, State: "open", Title: "TEAM-1: fix", Body: "see #2"})}
	res, err := Refresh(context.Background(), f, alice, since)
	require.NoError(t, err)
	assert.Equal(t, Fresh, res.State)
	assert.Equal(t, []string{"is:pr is:open archived:false author:Alice"}, f.queries)

	now := time.Now()
	r := Read(alice, time.Hour, since, now)
	assert.Equal(t, Fresh, r.State)
	require.Len(t, r.PRs, 1)
	assert.Equal(t, []string{"TEAM-1", "gh-2"}, r.PRs[0].Refs)
	assert.Empty(t, r.PRs[0].Body, "bodies are not cached")
	assert.Equal(t, Stale, Read(alice, time.Hour, since, now.Add(2*time.Hour)).State)
	assert.Equal(t, Fresh, Read(Key{Login: "alice", Set: OpenAuthored}, time.Hour, since, now).State, "logins are case-insensitive")

	path, err := entryPath(alice)
	require.NoError(t, err)
	home, _ := os.UserHomeDir()
	assert.Equal(t, filepath.Join(home, ".wgo", "cache", "inventory", "alice", "open-authored.json"), path)
}

func TestEmptySetIsFreshAndEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := Refresh(context.Background(), &fakeFetcher{res: result()}, alice, since)
	require.NoError(t, err)
	r := Read(alice, time.Hour, since, time.Now())
	assert.Equal(t, Fresh, r.State)
	assert.NotNil(t, r.PRs, "an empty set is data, not unknown")
	assert.Empty(t, r.PRs)
}

// TestClosedRecentWindowMustCover: an entry fetched for a shorter window
// than now asked for (days went up) is stale.
func TestClosedRecentWindowMustCover(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	k := Key{Login: "alice", Set: ClosedRecent}
	_, err := Refresh(context.Background(), &fakeFetcher{res: result()}, k, since)
	require.NoError(t, err)
	now := time.Now()
	assert.Equal(t, Fresh, Read(k, time.Hour, since, now).State)
	assert.Equal(t, Fresh, Read(k, time.Hour, since.AddDate(0, 0, 1), now).State, "a later start is covered")
	assert.Equal(t, Stale, Read(k, time.Hour, since.AddDate(0, 0, -30), now).State)
}

// TestFailedRefreshKeepsData: a failure is recorded beside the last good
// set, which stays served; a failure with no earlier data is a Miss carrying
// the error.
func TestFailedRefreshKeepsData(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	boom := errors.New("GitHub GraphQL search: HTTP 502 on page 2 after 3 retries")
	res, err := Refresh(context.Background(), &fakeFetcher{err: boom}, alice, since)
	require.ErrorIs(t, err, boom)
	assert.Equal(t, Miss, res.State)
	r := Read(alice, time.Hour, since, time.Now())
	assert.Equal(t, Miss, r.State)
	assert.EqualError(t, r.Err, boom.Error())

	_, err = Refresh(context.Background(), &fakeFetcher{res: result(github.PRFacts{Repo: "acme/api", Number: 1, State: "open"})}, alice, since)
	require.NoError(t, err)
	res, err = Refresh(context.Background(), &fakeFetcher{err: boom}, alice, since)
	require.ErrorIs(t, err, boom)
	require.Len(t, res.PRs, 1, "the last good set is returned")
	r = Read(alice, 0, since, time.Now())
	assert.Equal(t, Stale, r.State)
	require.Len(t, r.PRs, 1)
	assert.EqualError(t, r.Err, boom.Error())
	assert.True(t, r.LastAttemptAt.After(r.FetchedAt) || r.LastAttemptAt.Equal(r.FetchedAt))
}

// TestPartialDataIsKeptWithItsErrors: errors that came with data are recorded
// but are not a failed lookup; the set is kept, fresh, and marked incomplete,
// since GitHub left out what the token could not read.
func TestPartialDataIsKeptWithItsErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	partial := &github.GraphQLErrors{Errors: []github.GraphQLError{{Message: "Resource not accessible by integration"}}}
	res := result(github.PRFacts{Repo: "acme/api", Number: 1, State: "open"})
	res.IssueCount, res.Incomplete = 2, true // as github.CLIClient reports a null node
	got, err := Refresh(context.Background(), &fakeFetcher{res: res, err: partial}, alice, since)
	require.NoError(t, err)
	assert.ErrorIs(t, got.Err, partial)
	assert.True(t, got.Incomplete)
	r := Read(alice, time.Hour, since, time.Now())
	assert.Equal(t, Fresh, r.State)
	assert.True(t, r.Incomplete, "the cache keeps the set's incompleteness")
	require.Len(t, r.PRs, 1)
	assert.Contains(t, r.Err.Error(), "Resource not accessible")

	_, err = Refresh(context.Background(), &fakeFetcher{res: result()}, alice, since)
	require.NoError(t, err)
	assert.False(t, Read(alice, time.Hour, since, time.Now()).Incomplete, "a complete fetch clears it")
}

// slowFetcher answers after a delay, like a search of many pages.
type slowFetcher struct {
	fakeFetcher
	delay time.Duration
}

func (f *slowFetcher) SearchPullRequests(ctx context.Context, q string) (*github.PRSearchResult, error) {
	time.Sleep(f.delay)
	return f.fakeFetcher.SearchPullRequests(ctx, q)
}

// TestFetchedAtIsWhenTheSearchStarted: a PR can merge while the pages are
// read, so a fetch claims only the time it started; the attempt ends later.
func TestFetchedAtIsWhenTheSearchStarted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	before := time.Now()
	res, err := Refresh(context.Background(), &slowFetcher{fakeFetcher: fakeFetcher{res: result()}, delay: 30 * time.Millisecond}, alice, since)
	require.NoError(t, err)
	assert.False(t, res.FetchedAt.Before(before))
	assert.GreaterOrEqual(t, res.LastAttemptAt.Sub(res.FetchedAt), 30*time.Millisecond)
	r := Read(alice, time.Hour, since, time.Now())
	assert.True(t, r.FetchedAt.Equal(res.FetchedAt))
}

// writeRaw writes an entry for k as a hand edit or an older wgo might have.
func writeRaw(t *testing.T, k Key, body string) {
	t.Helper()
	path, err := entryPath(k)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// TestOddEntriesAreTolerated: entries the cache never writes itself still
// read safely and never claim more than they hold.
func TestOddEntriesAreTolerated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	at := now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)

	writeRaw(t, alice, `{"schema":1,"fetched_at":"`+at+`","last_attempt_at":"`+at+`","failures":-1}`)
	r := Read(alice, time.Hour, since, now)
	assert.NotPanics(t, func() { r.NotBefore() }, "a negative failure count must not shift negatively")
	assert.True(t, r.NotBefore().IsZero())
	assert.NotNil(t, r.PRs, "fetched with no list means none found, not never fetched")
	assert.Empty(t, r.PRs)

	closed := Key{Login: "alice", Set: ClosedRecent}
	writeRaw(t, closed, `{"schema":1,"prs":[],"fetched_at":"`+at+`"}`)
	assert.Equal(t, Stale, Read(closed, time.Hour, since, now).State, "closed-recent with no recorded window covers none")
}

func TestTruncatedIsRecorded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fakeFetcher{res: &github.PRSearchResult{PRs: []github.PRFacts{}, IssueCount: 1500, Truncated: true}}
	_, err := Refresh(context.Background(), f, alice, since)
	require.NoError(t, err)
	r := Read(alice, time.Hour, since, time.Now())
	assert.True(t, r.Truncated)
	assert.Equal(t, 1500, r.IssueCount)
}

func TestNoAuthIsNotCached(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := Refresh(context.Background(), &fakeFetcher{err: github.ErrNoAuth}, alice, since)
	assert.ErrorIs(t, err, github.ErrNoAuth)
	r := Read(alice, time.Hour, since, time.Now())
	assert.Equal(t, Miss, r.State)
	assert.NoError(t, r.Err, "no failure recorded on disk")
}

// TestCancelledRefreshIsNotRecorded: a fetch stopped by shutdown says
// nothing about the set.
func TestCancelledRefreshIsNotRecorded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Refresh(ctx, &fakeFetcher{err: context.Canceled}, alice, since)
	assert.ErrorIs(t, err, context.Canceled)
	assert.NoError(t, Read(alice, time.Hour, since, time.Now()).Err, "no failure recorded on disk")
	_, err = RefreshIdentity(ctx, &fakeFetcher{err: context.Canceled})
	assert.ErrorIs(t, err, context.Canceled)
	path, _ := identityPath()
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "no identity failure recorded")
}

// TestManagedUserLogins: Enterprise Managed Users' logins have underscores.
func TestManagedUserLogins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := Refresh(context.Background(), &fakeFetcher{res: result()}, Key{Login: "alice_acme", Set: OpenAuthored}, since)
	require.NoError(t, err)
}

// TestDroppedOpenPRsAreCarried: PRs an open-authored refresh no longer finds
// are kept until closed-recent has been fetched since.
func TestDroppedOpenPRsAreCarried(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pr := func(n int) github.PRFacts { return github.PRFacts{Repo: "acme/api", Number: n, State: "open"} }
	refresh := func(k Key, prs ...github.PRFacts) Result {
		t.Helper()
		res, err := Refresh(context.Background(), &fakeFetcher{res: result(prs...)}, k, since)
		require.NoError(t, err)
		time.Sleep(2 * time.Millisecond) // keep fetch times apart
		return res
	}
	closed := Key{Login: "alice", Set: ClosedRecent}
	refresh(closed)
	first := refresh(alice, pr(1), pr(2), pr(3))
	assert.Empty(t, first.Dropped)

	second := refresh(alice, pr(1))
	require.Len(t, second.Dropped, 2)
	// They dropped some time during the refresh, so closed-recent must start
	// after it ended to account for them.
	assert.True(t, second.LastAttemptAt.Equal(second.DroppedAt))
	assert.False(t, second.DroppedAt.Before(second.FetchedAt))
	assert.True(t, first.FetchedAt.Equal(second.DroppedFetchedAt), "dropped PRs are as last fetched")
	r := Read(alice, time.Hour, since, time.Now())
	assert.Len(t, r.Dropped, 2)

	// Still unaccounted for: closed-recent was not refetched.
	third := refresh(alice, pr(1), pr(3))
	require.Len(t, third.Dropped, 1, "#3 is open again; #2 is carried")
	assert.Equal(t, 2, third.Dropped[0].Number)
	assert.True(t, second.DroppedAt.Equal(third.DroppedAt), "nothing newly dropped")

	refresh(closed, pr(2))
	assert.Empty(t, refresh(alice, pr(1), pr(3)).Dropped, "closed-recent caught up")
}

func TestFailureBackoff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	boom := errors.New("HTTP 502")
	var last Result
	for i := 1; i <= 8; i++ {
		_, _ = Refresh(context.Background(), &fakeFetcher{err: boom}, alice, since)
		last = Read(alice, time.Hour, since, time.Now())
		want := min(time.Minute<<(i-1), 30*time.Minute)
		assert.Equal(t, i, last.Failures)
		assert.True(t, last.LastAttemptAt.Add(want).Equal(last.NotBefore()), "after %d failures", i)
	}
	rl := &github.RateLimitError{ResetAt: time.Now().Add(2 * time.Hour), Message: "secondary rate limit"}
	_, _ = Refresh(context.Background(), &fakeFetcher{err: rl}, alice, since)
	assert.WithinDuration(t, rl.ResetAt, Read(alice, time.Hour, since, time.Now()).NotBefore(), time.Second, "Retry-After wins when later")

	_, err := Refresh(context.Background(), &fakeFetcher{res: result()}, alice, since)
	require.NoError(t, err)
	r := Read(alice, time.Hour, since, time.Now())
	assert.Zero(t, r.Failures)
	assert.True(t, r.NotBefore().IsZero(), "a success clears the back-off")
}

func TestKeepLeaseRenews(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.True(t, LockRefresh(alice, time.Hour))
	path, _ := entryPath(alice)
	lock := path[:len(path)-len(".json")] + ".lock"
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(lock, old, old))
	stop := KeepLease(alice, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	stop()
	fi, err := os.Stat(lock)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), fi.ModTime(), time.Second, "the lease was renewed")
	assert.False(t, LockRefresh(alice, time.Minute), "a renewed lease is held")
}

func TestBadKeysAreRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fakeFetcher{res: result()}
	for _, k := range []Key{{Login: "../x", Set: OpenAuthored}, {Login: "team:web", Set: OpenAuthored}, {Login: "alice", Set: "everything"}} {
		_, err := Refresh(context.Background(), f, k, since)
		assert.Error(t, err, "%v", k)
		assert.False(t, LockRefresh(k, time.Minute))
	}
	assert.Zero(t, f.calls, "nothing is fetched for a key that cannot be cached")
}

func TestLockRefreshBacksOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	assert.True(t, LockRefresh(alice, time.Hour))
	assert.False(t, LockRefresh(alice, time.Hour))
	assert.True(t, LockRefresh(Key{Login: "alice", Set: ReviewRequested}, time.Hour), "leases are per set")
	assert.True(t, LockRefresh(alice, 0), "an aged lease is reclaimed")
}

func TestCorruptOrOldEntryIsMiss(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var logged []string
	old := Logf
	Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	t.Cleanup(func() { Logf = old })
	path, err := entryPath(alice)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))
	assert.Equal(t, Miss, Read(alice, time.Hour, since, time.Now()).State)
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "corrupt")
	require.NoError(t, os.WriteFile(path, []byte(`{"schema": 99, "prs": [], "fetched_at": "2026-10-30T00:00:00Z"}`), 0o644))
	assert.Equal(t, Miss, Read(alice, time.Hour, since, time.Now()).State, "another schema is refetched")
}

func TestIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	login, st := ReadIdentity(IdentityTTL, time.Now())
	assert.Equal(t, "", login)
	assert.Equal(t, Miss, st)

	f := &fakeFetcher{login: "alice"}
	got, err := RefreshIdentity(context.Background(), f)
	require.NoError(t, err)
	assert.Equal(t, "alice", got)
	login, st = ReadIdentity(IdentityTTL, time.Now())
	assert.Equal(t, "alice", login)
	assert.Equal(t, Fresh, st)
	login, st = ReadIdentity(IdentityTTL, time.Now().Add(25*time.Hour))
	assert.Equal(t, "alice", login, "a stale login is still served")
	assert.Equal(t, Stale, st)

	got, err = RefreshIdentity(context.Background(), &fakeFetcher{err: errors.New("HTTP 502")})
	assert.Error(t, err)
	assert.Equal(t, "alice", got, "a failure keeps the previous login")
	login, _ = ReadIdentity(IdentityTTL, time.Now())
	assert.Equal(t, "alice", login)

	assert.True(t, LockIdentityRefresh(time.Hour))
	assert.False(t, LockIdentityRefresh(time.Hour))
}

func TestRefs(t *testing.T) {
	for _, tc := range []struct {
		name, title, body string
		want              []string
	}{
		{name: "jira keys in title then body", title: "TEAM-12: fix", body: "Also OPS-3 and TEAM-12 again", want: []string{"TEAM-12", "OPS-3"}},
		{name: "false positives kept for the reader", title: "Use SHA-256 and P-384", want: []string{"SHA-256", "P-384"}},
		{name: "lowercase is not a key", title: "fix team-12 and utf-8", want: nil},
		{name: "hash refs", body: "Fixes #12, see (#13) and #12", want: []string{"gh-12", "gh-13"}},
		{name: "self is dropped", body: "Follow-up to #7, reverts #8", want: []string{"gh-8"}},
		{name: "gh refs any case", body: "GH-4 and gh-5 and Gh-6", want: []string{"gh-4", "gh-5", "gh-6"}},
		{name: "same repo full refs", body: "acme/api#20 and ACME/API#21 but not acme/web#22", want: []string{"gh-20", "gh-21"}},
		{name: "issue urls", body: "https://github.com/acme/api/issues/30 https://github.com/acme/web/issues/31", want: []string{"gh-30"}},
		{name: "not refs", body: "a#1 &#123; docs/x.md#5 ##6 #12abc color #fff", want: nil},
		{name: "leading zeros", body: "#012 and gh-12", want: []string{"gh-12"}},
		{name: "link text of other repositories' issues", body: `<a href="https://redirect.github.com/dep/lib/issues/456">#456</a> and [#457](https://redirect.github.com/dep/lib/issues/457)`, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Refs("acme/api", 7, tc.title, tc.body))
		})
	}
}
