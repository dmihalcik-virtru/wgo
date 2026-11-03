package inventory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/github"
)

// fakeTimelines serves a canned timeline or error and counts calls.
type fakeTimelines struct {
	tl    *github.PRTimeline
	err   error
	calls int
}

func (f *fakeTimelines) PRTimeline(_ context.Context, repo string, number int) (*github.PRTimeline, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	tl := *f.tl
	tl.Repo, tl.Number = repo, number
	return &tl, nil
}

var pr7 = TimelineKey{Repo: "Acme/API", Number: 7}

func TestTimelineCacheRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cold := ReadTimeline(pr7)
	assert.Nil(t, cold.Timeline)
	assert.False(t, cold.Current(time.Now()))

	updated := time.Date(2026, 10, 29, 14, 10, 0, 0, time.UTC)
	f := &fakeTimelines{tl: &github.PRTimeline{State: "open", UpdatedAt: updated,
		Events: []github.TimelineEvent{{Type: github.TimelineReviewRequested, At: updated.Add(-time.Hour), Reviewer: "bob"}}}}
	res, err := RefreshTimeline(context.Background(), f, pr7)
	require.NoError(t, err)
	assert.Equal(t, 1, f.calls)
	assert.False(t, res.FetchedAt.IsZero())

	r := ReadTimeline(pr7)
	require.NotNil(t, r.Timeline)
	assert.Equal(t, "bob", r.Timeline.Events[0].Reviewer)
	// Current for the updated_at it was fetched at or an earlier one; not
	// once GitHub reports a later one, nor for an unknown one.
	assert.True(t, r.Current(updated))
	assert.True(t, r.Current(updated.Add(-time.Minute)))
	assert.False(t, r.Current(updated.Add(time.Second)))
	assert.False(t, r.Current(time.Time{}))

	// Lowercased under timelines.d, beside the people's directories.
	dir, err := Dir()
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "timelines.d", "acme", "api", "7.json"))
	assert.NoError(t, err)
}

// TestTimelineFailureKeepsTheLastGoodHistory: a failed refresh records the
// error and backs off, keeping the previous timeline.
func TestTimelineFailureKeepsTheLastGoodHistory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &fakeTimelines{tl: &github.PRTimeline{State: "open", UpdatedAt: time.Now(), Events: []github.TimelineEvent{}}}
	_, err := RefreshTimeline(context.Background(), f, pr7)
	require.NoError(t, err)
	f.err = errors.New("GitHub GraphQL timeline of acme/api#7: HTTP 502 on page 1 after 3 retries")
	res, err := RefreshTimeline(context.Background(), f, pr7)
	require.ErrorIs(t, err, f.err)
	require.NotNil(t, res.Timeline, "the last good timeline is returned")
	r := ReadTimeline(pr7)
	require.NotNil(t, r.Timeline)
	assert.EqualError(t, r.Err, f.err.Error())
	assert.Equal(t, 1, r.Failures)
	assert.True(t, r.NotBefore().After(time.Now()), "a failure backs off")
	assert.True(t, TimelinesPausedUntil().IsZero(), "an ordinary failure pauses nothing")

	// A missing token says nothing about the PR: not recorded.
	f.err = github.ErrNoAuth
	_, err = RefreshTimeline(context.Background(), f, pr7)
	require.ErrorIs(t, err, github.ErrNoAuth)
	assert.Equal(t, 1, ReadTimeline(pr7).Failures)

	// A success clears the failures.
	f.err = nil
	_, err = RefreshTimeline(context.Background(), f, pr7)
	require.NoError(t, err)
	r = ReadTimeline(pr7)
	assert.Zero(t, r.Failures)
	assert.NoError(t, r.Err)
	assert.True(t, r.NotBefore().IsZero())
}

// TestTimelineRateLimitPausesEveryFetch: a rate limit records its reset for
// every timeline, so the rest of a batch waits instead of failing too.
func TestTimelineRateLimitPausesEveryFetch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	reset := time.Now().Add(20 * time.Minute).Truncate(time.Second)
	f := &fakeTimelines{err: &github.RateLimitError{ResetAt: reset, Message: "API rate limit exceeded"}}
	_, err := RefreshTimeline(context.Background(), f, pr7)
	require.Error(t, err)
	assert.True(t, TimelinesPausedUntil().Equal(reset), "paused until %v", TimelinesPausedUntil())
	assert.True(t, ReadTimeline(pr7).RetryAt.Equal(reset))

	// Without a reset time, a short default pause.
	t.Setenv("HOME", t.TempDir())
	f.err = &github.RateLimitError{Message: "secondary rate limit"}
	_, _ = RefreshTimeline(context.Background(), f, pr7)
	until := TimelinesPausedUntil()
	assert.True(t, until.After(time.Now()) && until.Before(time.Now().Add(defaultTimelinePause+time.Second)), "paused until %v", until)
}

func TestTimelineKeysAreSafePaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, k := range []TimelineKey{{"acme/..", 1}, {"../etc", 1}, {"acme/api/x", 1}, {"acme/api", 0}, {"acme", 1}, {"", 1}} {
		_, err := timelinePath(k)
		assert.Error(t, err, "%v", k)
		_, err = RefreshTimeline(context.Background(), &fakeTimelines{tl: &github.PRTimeline{}}, k)
		assert.Error(t, err, "%v", k)
		assert.False(t, LockTimelineRefresh(k, time.Minute), "%v", k)
	}
	assert.True(t, LockTimelineRefresh(pr7, time.Minute))
	assert.False(t, LockTimelineRefresh(pr7, time.Minute), "the lease is held")
}
