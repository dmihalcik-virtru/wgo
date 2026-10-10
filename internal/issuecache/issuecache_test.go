package issuecache

import (
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

var key = Key{Owner: "acme", Repo: "widgets", Number: 70}

type countingFetcher struct {
	calls int
	info  Info
	err   error
}

func (f *countingFetcher) FetchIssue(Key) (Info, error) {
	f.calls++
	return f.info, f.err
}

func TestColdReadIsMiss(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := Read(key, time.Hour)
	assert.Equal(t, Miss, r.State)
	assert.NoError(t, r.Err)
}

func TestWriteReadFreshAndStale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Write(key, Info{Number: 70, State: "open", Title: "dash"}))
	r := Read(key, time.Hour)
	assert.Equal(t, Fresh, r.State)
	assert.Equal(t, "open", r.Info.State)
	assert.Equal(t, Stale, Read(key, 0).State)
}

// TestKeyedByRepo: the same number in another repository is a different issue.
func TestKeyedByRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Write(key, Info{Number: 70, State: "closed"}))
	other := Key{Owner: "acme", Repo: "gadgets", Number: 70}
	assert.Equal(t, Miss, Read(other, time.Hour).State)
}

// TestFailureKeepsGoodDataAndFailureOnlyIsMiss mirrors prcache (WGO-137).
func TestFailureKeepsGoodDataAndFailureOnlyIsMiss(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &countingFetcher{err: errors.New("rate limited")}
	r := Refresh(f, key)
	assert.Equal(t, Miss, r.State, "failure-only entry is unknown, not closed")
	assert.Error(t, r.Err)
	assert.Equal(t, Miss, Read(key, time.Hour).State)

	f.err, f.info = nil, Info{Number: 70, State: "open"}
	assert.Equal(t, Fresh, Refresh(f, key).State)

	f.err = errors.New("offline")
	r = Refresh(f, key)
	assert.Equal(t, "open", r.Info.State, "good data survives a failure")
	assert.Error(t, r.Err)
	assert.Equal(t, 3, f.calls)
}

func TestLockRefreshBacksOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	assert.True(t, LockRefresh(key, time.Hour))
	assert.False(t, LockRefresh(key, time.Hour))
	assert.True(t, LockRefresh(key, 0), "an aged lease is reclaimed")
}

// brokenCache makes ~/.wgo/cache a regular file, so nothing can be written
// under it, and captures Logf output.
func brokenCache(t *testing.T) *[]string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".wgo"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".wgo", "cache"), []byte("x"), 0o644))
	var logs []string
	old := Logf
	Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { Logf = old })
	return &logs
}

func TestRefreshReportsCacheWriteFailure(t *testing.T) {
	logs := brokenCache(t)
	f := &countingFetcher{info: Info{Number: 70, State: "open"}}
	res := Refresh(f, key)
	// The fetched data is still served, but the failed write is visible.
	assert.Equal(t, Fresh, res.State)
	assert.Equal(t, "open", res.Info.State)
	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "issue cache write")
	assert.NotEmpty(t, *logs)

	*logs = nil
	fetchErr := errors.New("rate limited")
	res = Refresh(&countingFetcher{err: fetchErr}, key)
	require.ErrorIs(t, res.Err, fetchErr)
	assert.Contains(t, res.Err.Error(), "issue cache write")
	assert.NotEmpty(t, *logs)
}

func TestLockRefreshLogsFilesystemFaults(t *testing.T) {
	logs := brokenCache(t)
	assert.False(t, LockRefresh(key, time.Minute))
	assert.NotEmpty(t, *logs, "a filesystem fault must not look like silent contention")
}

// TestHyphenatedOwnerRepoDoNotCollide: a-b/c and a/b-c are different repos.
func TestHyphenatedOwnerRepoDoNotCollide(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := Key{Owner: "a-b", Repo: "c", Number: 1}
	b := Key{Owner: "a", Repo: "b-c", Number: 1}
	require.NoError(t, Write(a, Info{Number: 1, State: "open"}))
	assert.Equal(t, Miss, Read(b, time.Hour).State)
	assert.Equal(t, Fresh, Read(a, time.Hour).State)
}

// TestOwnerRepoCaseInsensitive: GitHub slugs are case-insensitive.
func TestOwnerRepoCaseInsensitive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Write(Key{Owner: "Acme", Repo: "Widgets", Number: 7}, Info{Number: 7, State: "open"}))
	assert.Equal(t, Fresh, Read(Key{Owner: "acme", Repo: "widgets", Number: 7}, time.Hour).State)
}

func TestTraversalKeyRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	assert.Error(t, Write(Key{Owner: "..", Repo: "x", Number: 1}, Info{}))
}

// TestNoAuthIsNotCached: a missing token must not linger as a cached failure.
func TestNoAuthIsNotCached(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := Refresh(&countingFetcher{err: github.ErrNoAuth}, key)
	assert.ErrorIs(t, r.Err, github.ErrNoAuth)
	after := Read(key, time.Hour)
	assert.Equal(t, Miss, after.State)
	assert.NoError(t, after.Err, "no failure recorded on disk")
}

// TestCorruptEntryIsLoggedMiss: an unreadable entry reads as a miss, visibly.
func TestCorruptEntryIsLoggedMiss(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Write(key, Info{Number: 70, State: "open"}))
	path, err := issuePath(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))
	var logged []string
	old := Logf
	Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	t.Cleanup(func() { Logf = old })
	assert.Equal(t, Miss, Read(key, time.Hour).State)
	require.Len(t, logged, 1)
	assert.Contains(t, logged[0], "corrupt")
}

// TestLossyNamesDoNotCollide: names that a sanitizer would merge stay apart.
func TestLossyNamesDoNotCollide(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, Write(Key{Owner: "o", Repo: "foo--bar", Number: 1}, Info{Number: 1, State: "open"}))
	assert.Equal(t, Miss, Read(Key{Owner: "o", Repo: "foo-bar", Number: 1}, time.Hour).State)
}
