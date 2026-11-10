package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// timelinePages are two pages of a PR's history: every event type, a team
// request answered on the team's behalf, and items PRTimeline drops (a
// pending review, a request for a reviewer the token cannot see).
var timelinePages = []string{
	`{"data": {"repository": {"pullRequest": {
	  "state": "MERGED", "isDraft": false, "createdAt": "2026-10-20T09:00:00Z", "updatedAt": "2026-10-29T14:10:00Z",
	  "author": {"login": "alice"},
	  "timelineItems": {"pageInfo": {"hasNextPage": true, "endCursor": "c1"}, "nodes": [
	    {"__typename": "ReadyForReviewEvent", "createdAt": "2026-10-21T15:30:00Z"},
	    {"__typename": "ReviewRequestedEvent", "createdAt": "2026-10-21T15:30:00Z", "requestedReviewer": {"__typename": "User", "login": "bob"}},
	    {"__typename": "ReviewRequestedEvent", "createdAt": "2026-10-21T15:31:00Z", "requestedReviewer": {"__typename": "Team", "slug": "web"}},
	    {"__typename": "ReviewRequestedEvent", "createdAt": "2026-10-21T15:32:00Z", "requestedReviewer": null},
	    {"__typename": "PullRequestReview", "state": "PENDING", "createdAt": "2026-10-22T09:00:00Z", "author": {"login": "carol"}},
	    {"__typename": "PullRequestReview", "state": "CHANGES_REQUESTED", "submittedAt": "2026-10-23T10:00:00Z", "createdAt": "2026-10-23T09:00:00Z",
	     "author": {"login": "bob"}, "onBehalfOf": {"nodes": []}}
	  ]}}}}}`,
	`{"data": {"repository": {"pullRequest": {
	  "state": "MERGED", "isDraft": false, "createdAt": "2026-10-20T09:00:00Z", "updatedAt": "2026-10-29T14:10:00Z",
	  "author": {"login": "alice"},
	  "timelineItems": {"pageInfo": {"hasNextPage": false, "endCursor": "c2"}, "nodes": [
	    {"__typename": "ConvertToDraftEvent", "createdAt": "2026-10-24T08:00:00Z"},
	    {"__typename": "ReviewRequestRemovedEvent", "createdAt": "2026-10-24T08:01:00Z", "requestedReviewer": {"__typename": "User", "login": "bob"}},
	    {"__typename": "PullRequestReview", "state": "APPROVED", "submittedAt": "2026-10-28T10:00:00Z", "author": {"login": "carol"},
	     "onBehalfOf": {"nodes": [{"slug": "web"}]}},
	    {"__typename": "ClosedEvent", "createdAt": "2026-10-28T11:00:00Z"},
	    {"__typename": "ReopenedEvent", "createdAt": "2026-10-28T12:00:00Z"},
	    {"__typename": "MergedEvent", "createdAt": "2026-10-29T14:00:00Z"}
	  ]}}}}}`,
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestPRTimelineReadsEveryPage(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &req))
		assert.Equal(t, "acme", req.Variables["owner"])
		assert.Equal(t, "api", req.Variables["name"])
		assert.EqualValues(t, 7, req.Variables["number"])
		assert.Contains(t, req.Query, "timelineItems(first: 100")
		page := 0
		if req.Variables["after"] == "c1" {
			page = 1
		}
		_, _ = w.Write([]byte(timelinePages[page]))
	}))
	defer srv.Close()
	tl, err := newTestClient(t, srv, "o/r").PRTimeline(context.Background(), "acme/api", 7)
	require.NoError(t, err)
	assert.EqualValues(t, 2, calls.Load())
	assert.Equal(t, "merged", tl.State)
	assert.Equal(t, "alice", tl.Author)
	assert.Equal(t, ts("2026-10-20T09:00:00Z"), tl.CreatedAt)
	assert.Equal(t, ts("2026-10-29T14:10:00Z"), tl.UpdatedAt)
	assert.False(t, tl.Truncated)
	assert.Equal(t, []TimelineEvent{
		{Type: TimelineReady, At: ts("2026-10-21T15:30:00Z")},
		{Type: TimelineReviewRequested, At: ts("2026-10-21T15:30:00Z"), Reviewer: "bob"},
		{Type: TimelineReviewRequested, At: ts("2026-10-21T15:31:00Z"), Reviewer: "team:web"},
		// A review's time is when it was submitted, not started.
		{Type: TimelineReview, At: ts("2026-10-23T10:00:00Z"), Reviewer: "bob", State: "CHANGES_REQUESTED"},
		{Type: TimelineDraft, At: ts("2026-10-24T08:00:00Z")},
		{Type: TimelineReviewRequestRemoved, At: ts("2026-10-24T08:01:00Z"), Reviewer: "bob"},
		{Type: TimelineReview, At: ts("2026-10-28T10:00:00Z"), Reviewer: "carol", State: "APPROVED", Teams: []string{"team:web"}},
		{Type: TimelineClosed, At: ts("2026-10-28T11:00:00Z")},
		{Type: TimelineReopened, At: ts("2026-10-28T12:00:00Z")},
		{Type: TimelineMerged, At: ts("2026-10-29T14:00:00Z")},
	}, tl.Events)
}

// TestPRTimelineRetriesAndFails: a 502 is retried; a GraphQL error (a PR the
// token cannot read) fails the fetch, naming the PR and the page.
func TestPRTimelineRetriesAndFails(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(strings.Replace(timelinePages[1], `"MERGED"`, `"OPEN"`, 1)))
	}))
	defer srv.Close()
	c := NewClient(WithBaseURL(srv.URL), WithToken("t"), WithRetryBackoff(0, 0, 0))
	tl, err := c.PRTimeline(context.Background(), "acme/api", 7)
	require.NoError(t, err)
	assert.EqualValues(t, 2, calls.Load())
	assert.Equal(t, "open", tl.State)

	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data": {"repository": null}, "errors": [{"type": "NOT_FOUND", "message": "Could not resolve to a Repository"}]}`))
	}))
	defer missing.Close()
	_, err = NewClient(WithBaseURL(missing.URL), WithToken("t")).PRTimeline(context.Background(), "acme/gone", 3)
	var te *PRTimelineError
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "GitHub GraphQL timeline of acme/gone#3: github graphql: Could not resolve to a Repository on page 1", err.Error())

	_, err = c.PRTimeline(context.Background(), "not-a-repo", 3)
	assert.ErrorContains(t, err, "is not a pull request")
}

// TestPRTimelineStopsAtThePageBound: a server that always has another page
// is read MaxTimelinePages times and the timeline marked truncated.
func TestPRTimelineStopsAtThePageBound(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(timelinePages[0]))
	}))
	defer srv.Close()
	tl, err := newTestClient(t, srv, "o/r").PRTimeline(context.Background(), "acme/api", 7)
	require.NoError(t, err)
	assert.EqualValues(t, MaxTimelinePages, calls.Load())
	assert.True(t, tl.Truncated)
}

// TestPRTimelineKeepsADismissedReviewsState: GitHub reports a dismissed
// review as DISMISSED; its ReviewDismissedEvent says what it was, so the
// review keeps that state and the dismissal is an event of its own.
func TestPRTimelineKeepsADismissedReviewsState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data": {"repository": {"pullRequest": {
		  "state": "OPEN", "isDraft": false, "createdAt": "2026-10-20T09:00:00Z", "updatedAt": "2026-10-25T09:00:00Z", "author": {"login": "alice"},
		  "timelineItems": {"pageInfo": {"hasNextPage": false}, "nodes": [
		    {"__typename": "PullRequestReview", "id": "R1", "state": "DISMISSED", "submittedAt": "2026-10-22T10:00:00Z", "author": {"login": "bob"}},
		    {"__typename": "PullRequestReview", "id": "R2", "state": "DISMISSED", "submittedAt": "2026-10-22T11:00:00Z", "author": {"login": "carol"}},
		    {"__typename": "ReviewDismissedEvent", "createdAt": "2026-10-24T08:00:00Z", "previousReviewState": "APPROVED", "review": {"id": "R1", "author": {"login": "bob"}}},
		    {"__typename": "ReviewDismissedEvent", "createdAt": "2026-10-24T09:00:00Z", "previousReviewState": "APPROVED", "review": null}
		  ]}}}}}`))
	}))
	defer srv.Close()
	tl, err := newTestClient(t, srv, "o/r").PRTimeline(context.Background(), "acme/api", 7)
	require.NoError(t, err)
	assert.Equal(t, []TimelineEvent{
		{Type: TimelineReview, At: ts("2026-10-22T10:00:00Z"), Reviewer: "bob", State: "APPROVED"},
		// No dismissal event names carol's review: it stays DISMISSED.
		{Type: TimelineReview, At: ts("2026-10-22T11:00:00Z"), Reviewer: "carol", State: "DISMISSED"},
		{Type: TimelineReviewDismissed, At: ts("2026-10-24T08:00:00Z"), Reviewer: "bob", State: "APPROVED"},
	}, tl.Events)
}
