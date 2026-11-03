package github

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// TimelinePageSize is how many timeline items PRTimeline reads per request.
const TimelinePageSize = 100

// MaxTimelinePages bounds the pages PRTimeline follows for one PR; beyond it
// the timeline is Truncated. With the item types it asks for, even a long
// review takes a few pages.
const MaxTimelinePages = 20

// Timeline event types (TimelineEvent.Type).
const (
	TimelineReady                = "ready"                  // ReadyForReviewEvent
	TimelineDraft                = "draft"                  // ConvertToDraftEvent
	TimelineReviewRequested      = "review_requested"       // ReviewRequestedEvent
	TimelineReviewRequestRemoved = "review_request_removed" // ReviewRequestRemovedEvent
	TimelineReview               = "review"                 // PullRequestReview
	TimelineMerged               = "merged"                 // MergedEvent
	TimelineClosed               = "closed"                 // ClosedEvent
	TimelineReopened             = "reopened"               // ReopenedEvent
	TimelineReviewDismissed      = "review_dismissed"       // ReviewDismissedEvent
)

// TimelineEvent is one review-history event of a pull request (gh-104).
type TimelineEvent struct {
	// Type is one of the Timeline* constants.
	Type string    `json:"type"`
	At   time.Time `json:"at"`
	// Reviewer is the login, or "team:<slug>", a review was requested from
	// or a request removed for; for a review or a dismissal, the
	// reviewer's login.
	Reviewer string `json:"reviewer,omitempty"`
	// State is a review's state: APPROVED, CHANGES_REQUESTED, COMMENTED or
	// DISMISSED. GitHub reports a review's current state, so a dismissed
	// review reads DISMISSED; when its dismissal is in the history, the
	// review keeps the state it had, and the dismissal event carries it
	// too. A dismissal whose review is not found leaves the review
	// DISMISSED.
	State string `json:"state,omitempty"`
	// Teams are the teams a review was submitted on behalf of, as
	// "team:<slug>": it answers their review requests too.
	Teams []string `json:"teams,omitempty"`
}

// PRTimeline is a pull request's review history as GitHub reported it at
// one fetch: the PR's own times and state then, and its events in order.
type PRTimeline struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Author string `json:"author,omitempty"`
	// State is open, closed or merged, and IsDraft the draft flag, at the
	// fetch.
	State     string    `json:"state,omitempty"`
	IsDraft   bool      `json:"is_draft,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
	// UpdatedAt is the PR's updated_at at the fetch: the cache refetches
	// once GitHub reports a later one.
	UpdatedAt time.Time       `json:"updated_at,omitzero"`
	Events    []TimelineEvent `json:"events"`
	// Truncated is set when the PR had more than MaxTimelinePages pages of
	// events; the newest are then missing.
	Truncated bool `json:"truncated,omitempty"`
}

// timelineQuery reads one page of a PR's review history. Reviewers are
// selected through the Actor interface, like prSearchQuery's, so that a
// union member GitHub adds cannot fail the query.
var timelineQuery = fmt.Sprintf(`query($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      state isDraft createdAt updatedAt
      author { login }
      timelineItems(first: %d, after: $after, itemTypes: [READY_FOR_REVIEW_EVENT, CONVERT_TO_DRAFT_EVENT,
          REVIEW_REQUESTED_EVENT, REVIEW_REQUEST_REMOVED_EVENT, PULL_REQUEST_REVIEW, REVIEW_DISMISSED_EVENT,
          MERGED_EVENT, CLOSED_EVENT, REOPENED_EVENT]) {
        pageInfo { hasNextPage endCursor }
        nodes {
          __typename
          ... on ReadyForReviewEvent { createdAt }
          ... on ConvertToDraftEvent { createdAt }
          ... on ReviewRequestedEvent {
            createdAt
            requestedReviewer { __typename ... on Actor { login } ... on Team { slug } }
          }
          ... on ReviewRequestRemovedEvent {
            createdAt
            requestedReviewer { __typename ... on Actor { login } ... on Team { slug } }
          }
          ... on PullRequestReview {
            id state submittedAt createdAt
            author { login }
            onBehalfOf(first: 10) { nodes { slug } }
          }
          ... on ReviewDismissedEvent {
            createdAt previousReviewState
            review { id author { login } }
          }
          ... on MergedEvent { createdAt }
          ... on ClosedEvent { createdAt }
          ... on ReopenedEvent { createdAt }
        }
      }
    }
  }
}`, TimelinePageSize)

type gqlTimelineItem struct {
	Typename          string       `json:"__typename"`
	ID                string       `json:"id"`
	CreatedAt         time.Time    `json:"createdAt"`
	SubmittedAt       time.Time    `json:"submittedAt"`
	State             string       `json:"state"`
	Author            *gqlActor    `json:"author"`
	RequestedReviewer *gqlReviewer `json:"requestedReviewer"`
	OnBehalfOf        *struct {
		Nodes []struct {
			Slug string `json:"slug"`
		} `json:"nodes"`
	} `json:"onBehalfOf"`
	// ReviewDismissedEvent.
	PreviousReviewState string `json:"previousReviewState"`
	Review              *struct {
		ID     string    `json:"id"`
		Author *gqlActor `json:"author"`
	} `json:"review"`
}

type gqlTimelineData struct {
	Repository *struct {
		PullRequest *struct {
			State         string    `json:"state"`
			IsDraft       bool      `json:"isDraft"`
			CreatedAt     time.Time `json:"createdAt"`
			UpdatedAt     time.Time `json:"updatedAt"`
			Author        *gqlActor `json:"author"`
			TimelineItems struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []*gqlTimelineItem `json:"nodes"`
			} `json:"timelineItems"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

// PRTimelineError is a timeline fetch that failed on one page: after its
// retries for a 5xx or a timeout, or at once for anything else.
type PRTimelineError struct {
	Repo    string
	Number  int
	Page    int
	Retries int
	Err     error
}

// Error implements error, naming an HTTP failure by its status alone so the
// message stays short enough to show on the page.
func (e *PRTimelineError) Error() string {
	cause := e.Err.Error()
	var apiErr *APIError
	if errors.As(e.Err, &apiErr) {
		cause = "HTTP " + strconv.Itoa(apiErr.StatusCode)
	}
	s := fmt.Sprintf("GitHub GraphQL timeline of %s#%d: %s on page %d", e.Repo, e.Number, cause, e.Page)
	if e.Retries > 0 {
		s += fmt.Sprintf(" after %d retries", e.Retries)
	}
	return s
}

// Unwrap returns the page's last error.
func (e *PRTimelineError) Unwrap() error { return e.Err }

// PRTimeline reads the review history of PR number in repo (owner/repo)
// through the GraphQL timelineItems connection, TimelinePageSize items at a
// time, following the cursor to the end or MaxTimelinePages. A page that
// fails transiently (see Transient) is retried after each WithRetryBackoff
// wait.
// Any failure, a GraphQL error included (a PR the token cannot read comes
// back as NOT_FOUND), fails the whole fetch with a *PRTimelineError: a
// history with a hole would misstate where the time went.
func (c *CLIClient) PRTimeline(ctx context.Context, repo string, number int) (*PRTimeline, error) {
	if !c.Available() {
		return nil, ErrNoAuth
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || number <= 0 {
		return nil, fmt.Errorf("github timeline: %q#%d is not a pull request", repo, number)
	}
	out := &PRTimeline{Repo: repo, Number: number, Events: []TimelineEvent{}}
	// reviews maps a review's ID to its event, and dismissed each dismissed
	// review's ID to the state it had, so a dismissed review keeps it.
	reviews, dismissed := map[string]int{}, map[string]string{}
	after := ""
	for page := 1; ; page++ {
		if page > MaxTimelinePages {
			out.Truncated = true
			break
		}
		vars := map[string]any{"owner": owner, "name": name, "number": number}
		if after != "" {
			vars["after"] = after
		}
		var data gqlTimelineData
		retries, err := c.graphQLWithRetry(ctx, timelineQuery, vars, &data)
		if err != nil {
			return nil, &PRTimelineError{Repo: repo, Number: number, Page: page, Retries: retries, Err: err}
		}
		if data.Repository == nil || data.Repository.PullRequest == nil {
			return nil, &PRTimelineError{Repo: repo, Number: number, Page: page, Err: errors.New("the response has no pull request")}
		}
		pr := data.Repository.PullRequest
		// The PR as of the first page: should it change while later pages
		// are read, the next refresh sees a newer updated_at and reads it
		// again.
		if page == 1 {
			out.State, out.IsDraft = strings.ToLower(pr.State), pr.IsDraft
			out.CreatedAt, out.UpdatedAt = pr.CreatedAt, pr.UpdatedAt
			if pr.Author != nil {
				out.Author = pr.Author.Login
			}
		}
		for _, it := range pr.TimelineItems.Nodes {
			ev, ok := it.event()
			if !ok {
				continue
			}
			switch {
			case ev.Type == TimelineReview && it.ID != "":
				reviews[it.ID] = len(out.Events)
			case ev.Type == TimelineReviewDismissed && it.Review.ID != "":
				dismissed[it.Review.ID] = ev.State
			}
			out.Events = append(out.Events, ev)
		}
		next := pr.TimelineItems.PageInfo
		if !next.HasNextPage || next.EndCursor == "" {
			break
		}
		after = next.EndCursor
	}
	for id, state := range dismissed {
		if i, ok := reviews[id]; ok && state != "" && out.Events[i].State == "DISMISSED" {
			out.Events[i].State = state
		}
	}
	return out, nil
}

// event converts a timeline item; ok is false for an item without a time,
// a request for a reviewer the token cannot see, and a pending review.
func (it *gqlTimelineItem) event() (TimelineEvent, bool) {
	if it == nil {
		return TimelineEvent{}, false
	}
	ev := TimelineEvent{At: it.CreatedAt}
	switch it.Typename {
	case "ReadyForReviewEvent":
		ev.Type = TimelineReady
	case "ConvertToDraftEvent":
		ev.Type = TimelineDraft
	case "MergedEvent":
		ev.Type = TimelineMerged
	case "ClosedEvent":
		ev.Type = TimelineClosed
	case "ReopenedEvent":
		ev.Type = TimelineReopened
	case "ReviewRequestedEvent", "ReviewRequestRemovedEvent":
		ev.Type = TimelineReviewRequested
		if it.Typename == "ReviewRequestRemovedEvent" {
			ev.Type = TimelineReviewRequestRemoved
		}
		if ev.Reviewer, _ = it.RequestedReviewer.id(); ev.Reviewer == "" {
			return TimelineEvent{}, false // a deleted user or team, or one the token cannot see
		}
	case "PullRequestReview":
		if it.State == "PENDING" || it.Author == nil || it.Author.Login == "" {
			return TimelineEvent{}, false
		}
		ev.Type, ev.Reviewer, ev.State = TimelineReview, it.Author.Login, it.State
		if !it.SubmittedAt.IsZero() {
			ev.At = it.SubmittedAt
		}
		if it.OnBehalfOf != nil {
			for _, t := range it.OnBehalfOf.Nodes {
				if t.Slug != "" {
					ev.Teams = append(ev.Teams, "team:"+t.Slug)
				}
			}
		}
	case "ReviewDismissedEvent":
		if it.Review == nil || it.Review.Author == nil || it.Review.Author.Login == "" {
			return TimelineEvent{}, false
		}
		ev.Type, ev.Reviewer, ev.State = TimelineReviewDismissed, it.Review.Author.Login, it.PreviousReviewState
	default:
		return TimelineEvent{}, false
	}
	if ev.At.IsZero() {
		return TimelineEvent{}, false
	}
	return ev, true
}
