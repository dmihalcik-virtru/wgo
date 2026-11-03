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
// but are not a failed lookup.
func TestPartialDataIsKeptWithItsErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	partial := &github.GraphQLErrors{Errors: []github.GraphQLError{{Message: "Resource not accessible by integration"}}}
	f := &fakeFetcher{res: result(github.PRFacts{Repo: "acme/api", Number: 1, State: "open"}), err: partial}
	res, err := Refresh(context.Background(), f, alice, since)
	require.NoError(t, err)
	assert.ErrorIs(t, res.Err, partial)
	r := Read(alice, time.Hour, since, time.Now())
	assert.Equal(t, Fresh, r.State)
	require.Len(t, r.PRs, 1)
	assert.Contains(t, r.Err.Error(), "Resource not accessible")
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
