package github

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// SearchLimit is the most results GitHub search returns for one query.
const SearchLimit = 1000

// PRSearchPageSize is the page size of SearchPullRequests. Pages of 50 PRs
// with nested reviews and checks intermittently time out (HTTP 502); pages of
// 20 with retries succeed.
const PRSearchPageSize = 20

// MaxCheckContexts bounds the check runs and status contexts read per PR;
// beyond it PRFacts.ChecksTruncated is set.
const MaxCheckContexts = 100

// PRFacts is a pull request with the facts the dash's PR inventory keeps
// (gh-97). Its JSON form is the inventory cache's. A fact GitHub did not
// report is left unset: an empty string or time, a nil pointer or a nil list,
// so unknown is never mistaken for zero. A list GitHub reported as empty is
// non-nil and empty.
type PRFacts struct {
	// Repo is the base repository's owner/repo.
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	URL    string `json:"url"`
	Title  string `json:"title,omitempty"`
	// State is open, closed or merged.
	State   string `json:"state"`
	IsDraft bool   `json:"is_draft,omitempty"`
	Author  string `json:"author,omitempty"`

	CreatedAt time.Time `json:"created_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	MergedAt  time.Time `json:"merged_at,omitzero"`
	ClosedAt  time.Time `json:"closed_at,omitzero"`
	// ReadyAt is the latest ReadyForReviewEvent, else CreatedAt for a PR
	// that never was a draft; unset while the PR is a draft.
	ReadyAt time.Time `json:"ready_at,omitzero"`

	Additions    *int `json:"additions,omitempty"`
	Deletions    *int `json:"deletions,omitempty"`
	ChangedFiles *int `json:"changed_files,omitempty"`
	// Mergeable is mergeable, conflicting or unknown.
	Mergeable string `json:"mergeable,omitempty"`
	BaseRef   string `json:"base_ref,omitempty"`
	HeadRef   string `json:"head_ref,omitempty"`
	// ReviewDecision is GitHub's: APPROVED, CHANGES_REQUESTED or
	// REVIEW_REQUIRED. Where GitHub computes none (the base branch does
	// not require reviews), it is rolled up from Reviews as the REST path
	// does: CHANGES_REQUESTED, else APPROVED, else unset.
	ReviewDecision string `json:"review_decision,omitempty"`
	// ReviewRequests are the outstanding review requests.
	ReviewRequests []ReviewRequestFact `json:"review_requests,omitzero"`
	// Reviews holds the latest review per reviewer.
	Reviews []ReviewFact `json:"reviews,omitzero"`
	// Checks is the head commit's CI rollup, as models.CIStatus.State
	// names it: success, failure, pending or none.
	Checks string `json:"checks,omitempty"`
	// CheckDetails are the head commit's check runs and status contexts,
	// at most MaxCheckContexts.
	CheckDetails    []CheckFact `json:"check_details,omitzero"`
	ChecksTruncated bool        `json:"checks_truncated,omitempty"`

	// Body is the description, read for ticket references; never cached.
	Body string `json:"-"`
}

// ReviewRequestFact is one outstanding review request.
type ReviewRequestFact struct {
	// Reviewer is a login, or "team:<slug>" for a team.
	Reviewer string `json:"reviewer"`
	// Kind is "user" or "team".
	Kind string `json:"kind"`
	// RequestedAt is the latest ReviewRequestedEvent for Reviewer; unset
	// when none was found.
	RequestedAt time.Time `json:"requested_at,omitzero"`
}

// ReviewFact is a reviewer's latest review.
type ReviewFact struct {
	Author string `json:"author"`
	// State is APPROVED, CHANGES_REQUESTED, COMMENTED or DISMISSED.
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at,omitzero"`
}

// CheckFact is one check run or status context.
type CheckFact struct {
	Name string `json:"name"`
	// State is success, failure, pending, skipped or neutral.
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
}

// PRSearchResult is every PR one search matched, up to SearchLimit.
type PRSearchResult struct {
	PRs []PRFacts
	// IssueCount is how many PRs GitHub says matched.
	IssueCount int
	// Truncated is set when IssueCount exceeds SearchLimit: GitHub serves
	// no more, so the rest are missing.
	Truncated bool
}

// PRSearchError is a PR search that failed on one page: after its retries
// for a transient failure (see Transient), or at once for anything else.
type PRSearchError struct {
	Page    int
	Retries int
	Err     error
}

// Error implements error. An HTTP failure is named by its status alone, so
// the message stays short enough to show on the page.
func (e *PRSearchError) Error() string {
	cause := e.Err.Error()
	var apiErr *APIError
	if errors.As(e.Err, &apiErr) {
		cause = "HTTP " + strconv.Itoa(apiErr.StatusCode)
	}
	if e.Retries > 0 {
		return fmt.Sprintf("GitHub GraphQL search: %s on page %d after %d retries", cause, e.Page, e.Retries)
	}
	return fmt.Sprintf("GitHub GraphQL search: %s on page %d", cause, e.Page)
}

// Unwrap returns the page's last error.
func (e *PRSearchError) Unwrap() error { return e.Err }

// prSearchQuery reads one page of PRs with every fact PRFacts holds. Union
// members are selected through the Actor interface so that a member GitHub
// adds or retires cannot fail the whole query.
var prSearchQuery = fmt.Sprintf(`query($q: String!, $after: String) {
  search(query: $q, type: ISSUE, first: %d, after: $after) {
    issueCount
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on PullRequest {
        number title url state isDraft body
        createdAt updatedAt mergedAt closedAt
        additions deletions changedFiles mergeable
        baseRefName headRefName reviewDecision
        author { login }
        repository { nameWithOwner isArchived }
        reviewRequests(first: 100) {
          nodes { requestedReviewer { __typename ... on Actor { login } ... on Team { slug } } }
        }
        latestReviews(first: 100) { nodes { author { login } state submittedAt } }
        latestOpinionatedReviews(first: 100) { nodes { author { login } state submittedAt } }
        timelineItems(last: 100, itemTypes: [READY_FOR_REVIEW_EVENT, REVIEW_REQUESTED_EVENT]) {
          nodes {
            __typename
            ... on ReadyForReviewEvent { createdAt }
            ... on ReviewRequestedEvent {
              createdAt
              requestedReviewer { __typename ... on Actor { login } ... on Team { slug } }
            }
          }
        }
        commits(last: 1) {
          nodes { commit { statusCheckRollup {
            state
            contexts(first: %d) {
              totalCount
              nodes {
                __typename
                ... on CheckRun { name status conclusion detailsUrl }
                ... on StatusContext { context state targetUrl }
              }
            }
          } } }
        }
      }
    }
  }
}`, PRSearchPageSize, MaxCheckContexts)

type gqlActor struct {
	Login string `json:"login"`
}

type gqlReviewer struct {
	Typename string `json:"__typename"`
	Login    string `json:"login"`
	Slug     string `json:"slug"`
}

// id is the reviewer as PRInfo.RequestedReviewers spells it, and its kind.
func (r *gqlReviewer) id() (string, string) {
	if r == nil {
		return "", ""
	}
	if r.Typename == "Team" {
		if r.Slug == "" {
			return "", ""
		}
		return "team:" + r.Slug, "team"
	}
	return r.Login, "user"
}

type gqlReviews struct {
	Nodes []struct {
		Author      *gqlActor `json:"author"`
		State       string    `json:"state"`
		SubmittedAt time.Time `json:"submittedAt"`
	} `json:"nodes"`
}

type gqlContext struct {
	Typename string `json:"__typename"`
	// CheckRun.
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	DetailsURL string `json:"detailsUrl"`
	// StatusContext.
	Context   string `json:"context"`
	State     string `json:"state"`
	TargetURL string `json:"targetUrl"`
}

type gqlPR struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	URL            string    `json:"url"`
	State          string    `json:"state"`
	IsDraft        bool      `json:"isDraft"`
	Body           string    `json:"body"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	MergedAt       time.Time `json:"mergedAt"`
	ClosedAt       time.Time `json:"closedAt"`
	Additions      *int      `json:"additions"`
	Deletions      *int      `json:"deletions"`
	ChangedFiles   *int      `json:"changedFiles"`
	Mergeable      string    `json:"mergeable"`
	BaseRefName    string    `json:"baseRefName"`
	HeadRefName    string    `json:"headRefName"`
	ReviewDecision string    `json:"reviewDecision"`
	Author         *gqlActor `json:"author"`
	Repository     *struct {
		NameWithOwner string `json:"nameWithOwner"`
		IsArchived    bool   `json:"isArchived"`
	} `json:"repository"`
	ReviewRequests *struct {
		Nodes []struct {
			RequestedReviewer *gqlReviewer `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	LatestReviews            *gqlReviews `json:"latestReviews"`
	LatestOpinionatedReviews *gqlReviews `json:"latestOpinionatedReviews"`
	TimelineItems            *struct {
		Nodes []struct {
			Typename          string       `json:"__typename"`
			CreatedAt         time.Time    `json:"createdAt"`
			RequestedReviewer *gqlReviewer `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"timelineItems"`
	Commits *struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State    string `json:"state"`
					Contexts struct {
						TotalCount int          `json:"totalCount"`
						Nodes      []gqlContext `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

type gqlSearchData struct {
	Search *struct {
		IssueCount int `json:"issueCount"`
		PageInfo   struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
		Nodes []*gqlPR `json:"nodes"`
	} `json:"search"`
}

// SearchPullRequests runs a GitHub search for pull requests through the
// GraphQL search connection, PRSearchPageSize at a time, following the cursor
// to the end. A page that fails with a 5xx, a timeout or an incomplete body
// is retried after each WithRetryBackoff wait; when the retries run out, or on any other failure,
// the search fails with a *PRSearchError and no result. PRs in archived
// repositories are dropped, and a PR listed twice (results shifting between
// pages) is kept once.
//
// GraphQL errors that come with data, such as a repository the token cannot
// read, do not stop the search: the result is returned along with a
// *GraphQLErrors listing them. A nil result always comes with an error.
func (c *CLIClient) SearchPullRequests(ctx context.Context, query string) (*PRSearchResult, error) {
	if !c.Available() {
		return nil, ErrNoAuth
	}
	res := &PRSearchResult{PRs: []PRFacts{}}
	seen := map[string]bool{}
	var partial []GraphQLError
	after := ""
	// GitHub serves at most SearchLimit results; the bound only guards
	// against a server that never stops saying there is a next page.
	for page := 1; page <= SearchLimit/PRSearchPageSize+1; page++ {
		vars := map[string]any{"q": query}
		if after != "" {
			vars["after"] = after
		}
		var data gqlSearchData
		retries, err := c.graphQLWithRetry(ctx, prSearchQuery, vars, &data)
		var gqlErrs *GraphQLErrors
		switch {
		case err == nil:
		case errors.As(err, &gqlErrs) && data.Search != nil:
			partial = append(partial, gqlErrs.Errors...)
		default:
			return nil, &PRSearchError{Page: page, Retries: retries, Err: err}
		}
		if data.Search == nil {
			return nil, &PRSearchError{Page: page, Err: errors.New("the response has no search results")}
		}
		res.IssueCount = data.Search.IssueCount
		for _, n := range data.Search.Nodes {
			pr, ok := n.facts()
			if !ok {
				continue
			}
			key := strings.ToLower(pr.Repo) + "#" + strconv.Itoa(pr.Number)
			if seen[key] {
				continue
			}
			seen[key] = true
			res.PRs = append(res.PRs, pr)
		}
		next := data.Search.PageInfo
		if !next.HasNextPage || next.EndCursor == "" {
			break
		}
		after = next.EndCursor
	}
	res.Truncated = res.IssueCount > SearchLimit
	if len(partial) > 0 {
		return res, &GraphQLErrors{Errors: partial}
	}
	return res, nil
}

// graphQLWithRetry runs GraphQL, retrying a transient failure (see
// Transient) after each c.retryBackoff wait. It returns how many retries it
// made.
func (c *CLIClient) graphQLWithRetry(ctx context.Context, query string, vars map[string]any, data any) (int, error) {
	for retries := 0; ; retries++ {
		err := c.GraphQL(ctx, query, vars, data)
		if err == nil || retries >= len(c.retryBackoff) || !retryable(ctx, err) {
			return retries, err
		}
		t := time.NewTimer(c.retryBackoff[retries])
		select {
		case <-ctx.Done():
			t.Stop()
			return retries, ctx.Err()
		case <-t.C:
		}
	}
}

// retryable reports whether a failed request is worth repeating: a
// transient failure, unless the caller's context is done.
func retryable(ctx context.Context, err error) bool {
	return ctx.Err() == nil && Transient(err)
}

// Transient reports whether a request failed in a way a later attempt may
// not: a server error, a timeout (GitHub reports a GraphQL query that ran too
// long as HTTP 200 with no data and a timeout error), or a successful status
// whose body was empty, cut off or not JSON.
func Transient(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode >= 500
	}
	var incomplete *IncompleteResponseError
	if errors.As(err, &incomplete) {
		return true
	}
	var gqlErrs *GraphQLErrors
	if errors.As(err, &gqlErrs) {
		return gqlErrs.timedOut()
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// facts converts a search node; ok is false for a node that is not a PR (or
// that the token could not read) and for a PR in an archived repository.
func (n *gqlPR) facts() (PRFacts, bool) {
	if n == nil || n.Number == 0 || n.Repository == nil || n.Repository.NameWithOwner == "" || n.Repository.IsArchived {
		return PRFacts{}, false
	}
	p := PRFacts{
		Repo:           n.Repository.NameWithOwner,
		Number:         n.Number,
		URL:            n.URL,
		Title:          n.Title,
		State:          strings.ToLower(n.State),
		IsDraft:        n.IsDraft,
		CreatedAt:      n.CreatedAt,
		UpdatedAt:      n.UpdatedAt,
		MergedAt:       n.MergedAt,
		ClosedAt:       n.ClosedAt,
		Additions:      n.Additions,
		Deletions:      n.Deletions,
		ChangedFiles:   n.ChangedFiles,
		BaseRef:        n.BaseRefName,
		HeadRef:        n.HeadRefName,
		ReviewDecision: n.ReviewDecision,
		Body:           n.Body,
	}
	if n.Author != nil {
		p.Author = n.Author.Login
	}
	switch n.Mergeable {
	case "MERGEABLE":
		p.Mergeable = "mergeable"
	case "CONFLICTING":
		p.Mergeable = "conflicting"
	case "UNKNOWN":
		p.Mergeable = "unknown"
	}

	// The latest ReadyForReviewEvent and ReviewRequestedEvent per reviewer.
	var readyAt time.Time
	requestedAt := map[string]time.Time{}
	if n.TimelineItems != nil {
		for _, ev := range n.TimelineItems.Nodes {
			switch ev.Typename {
			case "ReadyForReviewEvent":
				if ev.CreatedAt.After(readyAt) {
					readyAt = ev.CreatedAt
				}
			case "ReviewRequestedEvent":
				if id, _ := ev.RequestedReviewer.id(); id != "" && ev.CreatedAt.After(requestedAt[id]) {
					requestedAt[id] = ev.CreatedAt
				}
			}
		}
		switch {
		case p.IsDraft:
		case !readyAt.IsZero():
			p.ReadyAt = readyAt
		default:
			p.ReadyAt = p.CreatedAt
		}
	}

	if n.ReviewRequests != nil {
		p.ReviewRequests = []ReviewRequestFact{}
		for _, rr := range n.ReviewRequests.Nodes {
			id, kind := rr.RequestedReviewer.id()
			if id == "" {
				continue // a deleted user or team, or one the token cannot see
			}
			p.ReviewRequests = append(p.ReviewRequests, ReviewRequestFact{Reviewer: id, Kind: kind, RequestedAt: requestedAt[id]})
		}
	}

	p.Reviews = latestReviewPerReviewer(p.Author, n.LatestReviews, n.LatestOpinionatedReviews)

	if p.ReviewDecision == "" {
		p.ReviewDecision = rollupLatestReviews(p.Reviews)
	}

	if n.Commits != nil && len(n.Commits.Nodes) > 0 {
		p.CheckDetails = []CheckFact{}
		p.Checks = "none"
		if roll := n.Commits.Nodes[len(n.Commits.Nodes)-1].Commit.StatusCheckRollup; roll != nil {
			p.Checks = rollupState(roll.State)
			for _, cx := range roll.Contexts.Nodes {
				if len(p.CheckDetails) == MaxCheckContexts {
					break
				}
				if cf, ok := cx.fact(); ok {
					p.CheckDetails = append(p.CheckDetails, cf)
				}
			}
			p.ChecksTruncated = roll.Contexts.TotalCount > len(roll.Contexts.Nodes) || len(roll.Contexts.Nodes) > MaxCheckContexts
		}
	}
	return p, true
}

// latestReviewPerReviewer merges latestReviews and latestOpinionatedReviews
// into one review per reviewer: their latest opinionated review (APPROVED,
// CHANGES_REQUESTED or DISMISSED) if they have one, else their latest
// comment. A comment after an approval or a change request thus does not
// hide it, matching the REST rollup, which skips comments. The author's own
// reviews (replies to review threads) are left out, as are pending ones. It
// is nil when GitHub reported neither list.
func latestReviewPerReviewer(author string, lists ...*gqlReviews) []ReviewFact {
	var out []ReviewFact
	at := map[string]int{}
	known := false
	for _, list := range lists {
		if list == nil {
			continue
		}
		known = true
		for _, r := range list.Nodes {
			if r.Author == nil || r.Author.Login == "" || strings.EqualFold(r.Author.Login, author) {
				continue
			}
			rf := ReviewFact{Author: r.Author.Login, State: r.State, SubmittedAt: r.SubmittedAt}
			opinion := false
			switch r.State {
			case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
				opinion = true
			case "COMMENTED":
			default:
				continue // PENDING: a draft review nobody else can see
			}
			i, seen := at[rf.Author]
			if !seen {
				at[rf.Author] = len(out)
				out = append(out, rf)
				continue
			}
			prev := out[i]
			prevOpinion := prev.State != "COMMENTED"
			if (opinion && !prevOpinion) || (opinion == prevOpinion && rf.SubmittedAt.After(prev.SubmittedAt)) {
				out[i] = rf
			}
		}
	}
	if !known {
		return nil
	}
	if out == nil {
		out = []ReviewFact{}
	}
	return out
}

// rollupLatestReviews rolls the latest review per reviewer up into a
// decision like rollupReviewDecision: any CHANGES_REQUESTED wins, then any
// APPROVED; comments and dismissed reviews decide nothing.
func rollupLatestReviews(reviews []ReviewFact) string {
	decision := ""
	for _, r := range reviews {
		switch r.State {
		case "CHANGES_REQUESTED":
			return "CHANGES_REQUESTED"
		case "APPROVED":
			decision = "APPROVED"
		}
	}
	return decision
}

// fact normalizes a check run or status context.
func (cx gqlContext) fact() (CheckFact, bool) {
	switch cx.Typename {
	case "CheckRun":
		if cx.Name == "" {
			return CheckFact{}, false
		}
		return CheckFact{Name: cx.Name, State: checkRunState(cx.Status, cx.Conclusion), URL: cx.DetailsURL}, true
	case "StatusContext":
		if cx.Context == "" {
			return CheckFact{}, false
		}
		state := "pending"
		switch cx.State {
		case "SUCCESS":
			state = "success"
		case "FAILURE", "ERROR":
			state = "failure"
		}
		return CheckFact{Name: cx.Context, State: state, URL: cx.TargetURL}, true
	}
	return CheckFact{}, false
}

// checkRunState normalizes a check run's status and conclusion. Failures are
// the conclusions isFailureConclusion counts, so the list and the REST
// rollup agree.
func checkRunState(status, conclusion string) string {
	if !strings.EqualFold(status, "COMPLETED") {
		return "pending"
	}
	switch c := strings.ToUpper(conclusion); {
	case c == "SUCCESS":
		return "success"
	case c == "SKIPPED":
		return "skipped"
	case isFailureConclusion(c):
		return "failure"
	default: // NEUTRAL, STALE
		return "neutral"
	}
}

// rollupState maps a StatusCheckRollup state onto models.CIStatus.State.
func rollupState(s string) string {
	switch s {
	case "SUCCESS":
		return "success"
	case "FAILURE", "ERROR":
		return "failure"
	case "PENDING", "EXPECTED":
		return "pending"
	}
	return ""
}

// Viewer returns the authenticated user's login, read through GraphQL.
func (c *CLIClient) Viewer(ctx context.Context) (string, error) {
	if !c.Available() {
		return "", ErrNoAuth
	}
	var data struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
	}
	if err := c.GraphQL(ctx, `query { viewer { login } }`, nil, &data); err != nil {
		return "", fmt.Errorf("get authenticated user: %w", err)
	}
	if data.Viewer.Login == "" {
		return "", errors.New("get authenticated user: GitHub returned no login")
	}
	return data.Viewer.Login, nil
}
