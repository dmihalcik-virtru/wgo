package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// searchServer is a fake GraphQL search: it serves nodes PRSearchPageSize at
// a time, with cursors "c<page>", and can fail chosen attempts.
type searchServer struct {
	t     *testing.T
	nodes []any
	// count is issueCount; len(nodes) when zero.
	count int
	// fail returns the HTTP status to fail attempt n (1-based, per page)
	// of page p with, or 0 to serve it.
	fail func(page, attempt int) int
	// extra is merged into a served page's top level (say, an errors array).
	extra func(page int) map[string]any
	// cut, when it returns true for attempt n of page p, serves that
	// attempt with HTTP 200 and only the first half of its body.
	cut func(page, attempt int) bool

	mu       sync.Mutex
	attempts map[int]int
	queries  []string
}

func (s *searchServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		s.t.Errorf("bad request: %v", err)
	}
	page := 1
	if after, _ := req.Variables["after"].(string); after != "" {
		page, _ = strconv.Atoi(strings.TrimPrefix(after, "c"))
		page++
	}
	s.mu.Lock()
	if s.attempts == nil {
		s.attempts = map[int]int{}
	}
	s.attempts[page]++
	attempt := s.attempts[page]
	q, _ := req.Variables["q"].(string)
	s.queries = append(s.queries, q)
	s.mu.Unlock()
	if s.fail != nil {
		if code := s.fail(page, attempt); code != 0 {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`<html>Bad gateway</html>`))
			return
		}
	}
	lo := (page - 1) * PRSearchPageSize
	hi := min(lo+PRSearchPageSize, len(s.nodes))
	count := s.count
	if count == 0 {
		count = len(s.nodes)
	}
	resp := map[string]any{"data": map[string]any{"search": map[string]any{
		"issueCount": count,
		"pageInfo":   map[string]any{"hasNextPage": hi < len(s.nodes), "endCursor": fmt.Sprintf("c%d", page)},
		"nodes":      s.nodes[lo:hi],
	}}}
	if s.extra != nil {
		for k, v := range s.extra(page) {
			resp[k] = v
		}
	}
	if s.cut != nil && s.cut(page, attempt) {
		body, _ := json.Marshal(resp)
		_, _ = w.Write(body[:len(body)/2])
		return
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *searchServer) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.attempts {
		n += a
	}
	return n
}

// minimalPR is a search node with only the fields every PR has.
func minimalPR(repo string, n int) map[string]any {
	return map[string]any{
		"number": n, "title": fmt.Sprintf("PR %d", n), "url": fmt.Sprintf("https://github.com/%s/pull/%d", repo, n),
		"state": "OPEN", "repository": map[string]any{"nameWithOwner": repo, "isArchived": false},
	}
}

func newSearchClient(t *testing.T, s *searchServer) *CLIClient {
	t.Helper()
	s.t = t
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return NewClient(WithBaseURL(srv.URL), WithToken("test-token"), WithRetryBackoff(0, 0, 0))
}

// TestSearchPullRequestsPagesAndRetries: 195 PRs over 10 pages of 20, with a
// transient 502 on page 4, all come back.
func TestSearchPullRequestsPagesAndRetries(t *testing.T) {
	s := &searchServer{fail: func(page, attempt int) int {
		if page == 4 && attempt == 1 {
			return http.StatusBadGateway
		}
		return 0
	}}
	for i := 1; i <= 195; i++ {
		s.nodes = append(s.nodes, minimalPR("acme/api", i))
	}
	res, err := newSearchClient(t, s).SearchPullRequests(context.Background(), "is:pr is:open author:alice")
	require.NoError(t, err)
	require.Len(t, res.PRs, 195)
	assert.Equal(t, 195, res.IssueCount)
	assert.False(t, res.Truncated)
	assert.False(t, res.Incomplete, "every PR counted was read")
	assert.Equal(t, 1, res.PRs[0].Number)
	assert.Equal(t, 195, res.PRs[194].Number)
	assert.Equal(t, 11, s.requests(), "10 pages plus one retry")
	assert.Equal(t, 2, s.attempts[4])
	assert.Equal(t, "is:pr is:open author:alice", s.queries[0])
}

func TestSearchPullRequestsGivesUpAfterThreeRetries(t *testing.T) {
	s := &searchServer{fail: func(page, _ int) int {
		if page == 2 {
			return http.StatusBadGateway
		}
		return 0
	}}
	for i := 1; i <= 30; i++ {
		s.nodes = append(s.nodes, minimalPR("acme/api", i))
	}
	res, err := newSearchClient(t, s).SearchPullRequests(context.Background(), "q")
	assert.Nil(t, res, "a failed set returns no partial result")
	var se *PRSearchError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, "GitHub GraphQL search: HTTP 502 on page 2 after 3 retries", err.Error())
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 4, s.attempts[2], "one attempt and three retries")
}

// TestSearchPullRequestsRetriesCutOffPages: a page served with HTTP 200 but
// a body cut off part way is retried like a 502, rather than failing the
// whole set.
func TestSearchPullRequestsRetriesCutOffPages(t *testing.T) {
	s := &searchServer{cut: func(page, attempt int) bool { return page == 5 && attempt == 1 }}
	for i := 1; i <= 110; i++ {
		s.nodes = append(s.nodes, minimalPR("acme/api", i))
	}
	res, err := newSearchClient(t, s).SearchPullRequests(context.Background(), "q")
	require.NoError(t, err)
	require.Len(t, res.PRs, 110)
	assert.Equal(t, 2, s.attempts[5], "the cut-off page was fetched again")
	assert.Equal(t, 7, s.requests(), "6 pages plus one retry")
}

// TestSearchPullRequestsGivesUpOnPagesAlwaysCutOff: a page cut off on every
// attempt fails the set after the usual three retries, naming the cause.
func TestSearchPullRequestsGivesUpOnPagesAlwaysCutOff(t *testing.T) {
	s := &searchServer{cut: func(page, _ int) bool { return page == 2 }}
	for i := 1; i <= 30; i++ {
		s.nodes = append(s.nodes, minimalPR("acme/api", i))
	}
	res, err := newSearchClient(t, s).SearchPullRequests(context.Background(), "q")
	assert.Nil(t, res)
	var incomplete *IncompleteResponseError
	require.ErrorAs(t, err, &incomplete)
	assert.Equal(t, http.StatusOK, incomplete.StatusCode)
	assert.Contains(t, err.Error(), "unexpected end of JSON input")
	assert.Contains(t, err.Error(), "on page 2 after 3 retries")
	assert.Equal(t, 4, s.attempts[2])
}

// TestSearchPullRequestsRetriesGraphQLTimeouts: GitHub answers a query that
// ran too long with HTTP 200, no data and a timeout error; that is retried.
func TestSearchPullRequestsRetriesGraphQLTimeouts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"data": null, "errors": [{"message": "Something went wrong while executing your query. This may be the result of a timeout, or it could be a GitHub bug."}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"search":{"issueCount":1,"pageInfo":{"hasNextPage":false},"nodes":[` +
			mustJSON(t, minimalPR("acme/api", 7)) + `]}}}`))
	}))
	defer srv.Close()
	c := NewClient(WithBaseURL(srv.URL), WithToken("t"), WithRetryBackoff(0))
	res, err := c.SearchPullRequests(context.Background(), "q")
	require.NoError(t, err)
	require.Len(t, res.PRs, 1)
	assert.EqualValues(t, 2, calls.Load())
}

func TestSearchPullRequestsDoesNotRetryClientErrors(t *testing.T) {
	s := &searchServer{fail: func(int, int) int { return http.StatusUnauthorized }}
	_, err := newSearchClient(t, s).SearchPullRequests(context.Background(), "q")
	assert.EqualError(t, err, "GitHub GraphQL search: HTTP 401 on page 1")
	assert.Equal(t, 1, s.requests())
}

// TestSearchPullRequestsRetriesTimeouts: a page that times out is retried.
func TestSearchPullRequestsRetriesTimeouts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			time.Sleep(300 * time.Millisecond)
		}
		_, _ = w.Write([]byte(`{"data":{"search":{"issueCount":1,"pageInfo":{"hasNextPage":false},"nodes":[` +
			mustJSON(t, minimalPR("acme/api", 7)) + `]}}}`))
	}))
	defer srv.Close()
	c := NewClient(WithBaseURL(srv.URL), WithToken("t"), WithRetryBackoff(0),
		WithHTTPClient(&http.Client{Timeout: 100 * time.Millisecond}))
	res, err := c.SearchPullRequests(context.Background(), "q")
	require.NoError(t, err)
	require.Len(t, res.PRs, 1)
	assert.EqualValues(t, 2, calls.Load(), "the timed-out page was retried")
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestDefaultRetryBackoff(t *testing.T) {
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}, NewClient().retryBackoff)
}

// TestSearchPullRequestsTruncatedArchivedAndDuplicates: more than 1,000
// matches marks the result truncated; archived repositories, unreadable
// (null) nodes and repeats are dropped.
func TestSearchPullRequestsTruncatedArchivedAndDuplicates(t *testing.T) {
	archived := minimalPR("acme/old", 9)
	archived["repository"] = map[string]any{"nameWithOwner": "acme/old", "isArchived": true}
	s := &searchServer{count: 1500, nodes: []any{minimalPR("acme/api", 1), archived, nil, map[string]any{}, minimalPR("ACME/api", 1), minimalPR("acme/web", 1)}}
	res, err := newSearchClient(t, s).SearchPullRequests(context.Background(), "q")
	require.NoError(t, err)
	assert.True(t, res.Truncated)
	assert.Equal(t, 1500, res.IssueCount)
	var got []string
	for _, p := range res.PRs {
		got = append(got, fmt.Sprintf("%s#%d", p.Repo, p.Number))
	}
	assert.Equal(t, []string{"acme/api#1", "acme/web#1"}, got)
}

// TestSearchPullRequestsKeepsPartialData: GraphQL errors that come with data
// (a repository the token cannot read) are returned beside the result.
func TestSearchPullRequestsKeepsPartialData(t *testing.T) {
	s := &searchServer{nodes: []any{minimalPR("acme/api", 1), nil}, extra: func(int) map[string]any {
		return map[string]any{"errors": []any{map[string]any{"message": "Resource not accessible by integration", "path": []any{"search", "nodes", 1}}}}
	}}
	res, err := newSearchClient(t, s).SearchPullRequests(context.Background(), "q")
	require.NotNil(t, res)
	require.Len(t, res.PRs, 1)
	var gqlErrs *GraphQLErrors
	require.ErrorAs(t, err, &gqlErrs)
	assert.Contains(t, err.Error(), "Resource not accessible")
	assert.True(t, res.Incomplete, "the unreadable PR is missing from the result")
}

// TestSearchPullRequestsNoticesSkippedPRs: reading fewer PRs than GitHub
// counted, short of the search limit, marks the result incomplete: results
// shifted between pages and one was skipped. Past the limit, Truncated says
// so instead.
func TestSearchPullRequestsNoticesSkippedPRs(t *testing.T) {
	s := &searchServer{count: 3, nodes: []any{minimalPR("acme/api", 1), minimalPR("acme/api", 2)}}
	res, err := newSearchClient(t, s).SearchPullRequests(context.Background(), "q")
	require.NoError(t, err)
	assert.True(t, res.Incomplete)
	assert.False(t, res.Truncated)

	s = &searchServer{count: SearchLimit + 1, nodes: []any{minimalPR("acme/api", 1)}}
	res, err = newSearchClient(t, s).SearchPullRequests(context.Background(), "q")
	require.NoError(t, err)
	assert.True(t, res.Truncated)
	assert.False(t, res.Incomplete, "a truncated result is reported as truncated")
}

func TestSearchPullRequestsWithoutSearchDataFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data": null, "errors": [{"message": "Parse error"}]}`))
	}))
	defer srv.Close()
	res, err := NewClient(WithBaseURL(srv.URL), WithToken("t")).SearchPullRequests(context.Background(), "q")
	assert.Nil(t, res)
	assert.EqualError(t, err, "GitHub GraphQL search: github graphql: Parse error on page 1")
}

func TestSearchPullRequestsWithoutCredentials(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PATH", t.TempDir())
	c := NewClient(WithBaseURL("http://127.0.0.1:1"))
	_, err := c.SearchPullRequests(context.Background(), "q")
	assert.ErrorIs(t, err, ErrNoAuth)
	_, err = c.Viewer(context.Background())
	assert.ErrorIs(t, err, ErrNoAuth)
}

// fullPRNode reports every fact. Times are distinct so each lands visibly.
const fullPRNode = `{
  "number": 101, "title": "Refresh tokens TEAM-101", "url": "https://github.com/acme/api/pull/101",
  "state": "OPEN", "isDraft": false, "body": "Fixes #12",
  "createdAt": "2026-10-20T09:00:00Z", "updatedAt": "2026-10-29T14:10:00Z", "mergedAt": null, "closedAt": null,
  "additions": 0, "deletions": 40, "changedFiles": 7, "mergeable": "CONFLICTING",
  "baseRefName": "main", "headRefName": "TEAM-101-token-refresh", "reviewDecision": "CHANGES_REQUESTED",
  "author": {"login": "alice"},
  "repository": {"nameWithOwner": "acme/api", "isArchived": false},
  "reviewRequests": {"nodes": [
    {"requestedReviewer": {"__typename": "User", "login": "bob"}},
    {"requestedReviewer": {"__typename": "Team", "slug": "web"}},
    {"requestedReviewer": {"__typename": "User", "login": "carol"}},
    {"requestedReviewer": null}
  ]},
  "latestReviews": {"nodes": [
    {"author": {"login": "bob"}, "state": "CHANGES_REQUESTED", "submittedAt": "2026-10-24T10:00:00Z"},
    {"author": {"login": "carol"}, "state": "PENDING", "submittedAt": null},
    {"author": null, "state": "COMMENTED", "submittedAt": "2026-10-24T11:00:00Z"},
    {"author": {"login": "dave"}, "state": "COMMENTED", "submittedAt": "2026-10-25T10:00:00Z"}
  ]},
  "timelineItems": {"nodes": [
    {"__typename": "ReadyForReviewEvent", "createdAt": "2026-10-21T15:30:00Z"},
    {"__typename": "ReviewRequestedEvent", "createdAt": "2026-10-21T15:31:00Z", "requestedReviewer": {"__typename": "User", "login": "bob"}},
    {"__typename": "ReviewRequestedEvent", "createdAt": "2026-10-26T10:00:00Z", "requestedReviewer": {"__typename": "User", "login": "bob"}},
    {"__typename": "ReviewRequestedEvent", "createdAt": "2026-10-22T08:00:00Z", "requestedReviewer": {"__typename": "Team", "slug": "web"}}
  ]},
  "closingIssuesReferences": {"nodes": [
    {"number": 12, "repository": {"nameWithOwner": "acme/api"}},
    {"number": 3, "repository": {"nameWithOwner": "acme/web"}},
    null
  ]},
  "commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "FAILURE", "contexts": {"totalCount": 7, "nodes": [
    {"__typename": "CheckRun", "name": "build", "status": "COMPLETED", "conclusion": "SUCCESS", "detailsUrl": "https://ci.example/1"},
    {"__typename": "CheckRun", "name": "vendor-hsm-tests", "status": "COMPLETED", "conclusion": "TIMED_OUT", "detailsUrl": "https://ci.example/2"},
    {"__typename": "CheckRun", "name": "docs", "status": "COMPLETED", "conclusion": "SKIPPED", "detailsUrl": ""},
    {"__typename": "CheckRun", "name": "e2e", "status": "IN_PROGRESS", "conclusion": null, "detailsUrl": "https://ci.example/3"},
    {"__typename": "CheckRun", "name": "stale", "status": "COMPLETED", "conclusion": "NEUTRAL", "detailsUrl": ""},
    {"__typename": "StatusContext", "context": "ci/legacy", "state": "ERROR", "targetUrl": "https://ci.example/4"}
  ]}}}}]}
}`

func intp(n int) *int { return &n }

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestSearchPullRequestsFacts(t *testing.T) {
	var full map[string]any
	require.NoError(t, json.Unmarshal([]byte(fullPRNode), &full))
	// Ready for review once, then converted back to draft.
	draft := minimalPR("acme/web", 7)
	draft["isDraft"] = true
	draft["createdAt"] = "2026-10-01T00:00:00Z"
	draft["timelineItems"] = map[string]any{"nodes": []any{map[string]any{"__typename": "ReadyForReviewEvent", "createdAt": "2026-10-03T00:00:00Z"}}}
	merged := minimalPR("acme/web", 8)
	merged["state"] = "MERGED"
	merged["createdAt"] = "2026-10-01T00:00:00Z"
	merged["mergedAt"] = "2026-10-02T00:00:00Z"
	merged["closedAt"] = "2026-10-02T00:00:00Z"
	merged["timelineItems"] = map[string]any{"nodes": []any{}}
	merged["commits"] = map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": nil}}}}
	merged["reviewRequests"] = map[string]any{"nodes": []any{}}
	merged["latestReviews"] = map[string]any{"nodes": []any{}}
	merged["closingIssuesReferences"] = map[string]any{"nodes": []any{}}
	s := &searchServer{nodes: []any{full, minimalPR("acme/api", 102), draft, merged}}
	res, err := newSearchClient(t, s).SearchPullRequests(context.Background(), "q")
	require.NoError(t, err)
	require.Len(t, res.PRs, 4)

	want := PRFacts{
		Repo: "acme/api", Number: 101, URL: "https://github.com/acme/api/pull/101", Title: "Refresh tokens TEAM-101",
		State: "open", Author: "alice",
		CreatedAt: at("2026-10-20T09:00:00Z"), UpdatedAt: at("2026-10-29T14:10:00Z"), ReadyAt: at("2026-10-21T15:30:00Z"),
		Additions: intp(0), Deletions: intp(40), ChangedFiles: intp(7), Mergeable: "conflicting",
		BaseRef: "main", HeadRef: "TEAM-101-token-refresh", ReviewDecision: "CHANGES_REQUESTED",
		ReviewRequests: []ReviewRequestFact{
			{Reviewer: "bob", Kind: "user", RequestedAt: at("2026-10-26T10:00:00Z")},
			{Reviewer: "team:web", Kind: "team", RequestedAt: at("2026-10-22T08:00:00Z")},
			{Reviewer: "carol", Kind: "user"},
		},
		Reviews: []ReviewFact{
			{Author: "bob", State: "CHANGES_REQUESTED", SubmittedAt: at("2026-10-24T10:00:00Z")},
			{Author: "dave", State: "COMMENTED", SubmittedAt: at("2026-10-25T10:00:00Z")},
		},
		Checks: "failure",
		CheckDetails: []CheckFact{
			{Name: "build", State: "success", URL: "https://ci.example/1"},
			{Name: "vendor-hsm-tests", State: "failure", URL: "https://ci.example/2"},
			{Name: "docs", State: "skipped"},
			{Name: "e2e", State: "pending", URL: "https://ci.example/3"},
			{Name: "stale", State: "neutral"},
			{Name: "ci/legacy", State: "failure", URL: "https://ci.example/4"},
		},
		ChecksTruncated: true, // totalCount 7, 6 served
		ClosingIssues:   []IssueRef{{Repo: "acme/api", Number: 12}, {Repo: "acme/web", Number: 3}},
		Body:            "Fixes #12",
	}
	assert.Equal(t, want, res.PRs[0])

	// Nothing reported: everything beyond identity and state is unknown.
	sparse := res.PRs[1]
	assert.Equal(t, PRFacts{Repo: "acme/api", Number: 102, URL: "https://github.com/acme/api/pull/102", Title: "PR 102", State: "open"}, sparse)
	b, err := json.Marshal(sparse)
	require.NoError(t, err)
	assert.JSONEq(t, `{"repo":"acme/api","number":102,"url":"https://github.com/acme/api/pull/102","title":"PR 102","state":"open"}`, string(b),
		"unknown facts are omitted, not zero")

	assert.True(t, res.PRs[2].ReadyAt.IsZero(), "no ready_at while draft")
	m := res.PRs[3]
	assert.Equal(t, "merged", m.State)
	assert.Equal(t, at("2026-10-02T00:00:00Z"), m.MergedAt)
	assert.Equal(t, m.CreatedAt, m.ReadyAt, "never a draft: ready at creation")
	assert.Equal(t, "none", m.Checks)
	assert.NotNil(t, m.CheckDetails)
	assert.Empty(t, m.CheckDetails, "no checks reported is an empty list")
	assert.NotNil(t, m.ReviewRequests)
	assert.NotNil(t, m.Reviews)
	b, err = json.Marshal(m)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"review_requests":[]`)
	assert.Contains(t, string(b), `"closing_issues":[]`, "GitHub links no issue: an empty list, not unknown")
	assert.NotContains(t, string(b), "body")
}

// TestSearchPullRequestsRollsUpReviewDecision: where the base branch needs
// no reviews GitHub reports no decision; it is rolled up from the reviews,
// as the REST path does.
func TestSearchPullRequestsRollsUpReviewDecision(t *testing.T) {
	review := func(login, state string) map[string]any {
		return map[string]any{"author": map[string]any{"login": login}, "state": state, "submittedAt": "2026-10-24T10:00:00Z"}
	}
	node := func(n int, decision any, reviews ...any) map[string]any {
		pr := minimalPR("acme/api", n)
		pr["reviewDecision"] = decision
		pr["latestReviews"] = map[string]any{"nodes": reviews}
		return pr
	}
	s := &searchServer{nodes: []any{
		node(1, nil, review("bob", "APPROVED"), review("carol", "COMMENTED")),
		node(2, nil, review("bob", "APPROVED"), review("carol", "CHANGES_REQUESTED")),
		node(3, nil, review("carol", "COMMENTED"), review("dave", "DISMISSED")),
		node(4, "REVIEW_REQUIRED", review("bob", "APPROVED")),
	}}
	res, err := newSearchClient(t, s).SearchPullRequests(context.Background(), "q")
	require.NoError(t, err)
	var got []string
	for _, p := range res.PRs {
		got = append(got, p.ReviewDecision)
	}
	assert.Equal(t, []string{"APPROVED", "CHANGES_REQUESTED", "", "REVIEW_REQUIRED"}, got)
}

// TestSearchPullRequestsOpinionatedReviewsWin: a comment after an approval
// or a change request does not hide it, from the reviews or the decision,
// and the author's own thread replies are not reviews.
func TestSearchPullRequestsOpinionatedReviewsWin(t *testing.T) {
	review := func(login, state, at string) map[string]any {
		return map[string]any{"author": map[string]any{"login": login}, "state": state, "submittedAt": at}
	}
	pr := func(n int, latest, opinionated []any) map[string]any {
		p := minimalPR("acme/api", n)
		p["author"] = map[string]any{"login": "alice"}
		p["reviewDecision"] = nil
		p["latestReviews"] = map[string]any{"nodes": latest}
		p["latestOpinionatedReviews"] = map[string]any{"nodes": opinionated}
		return p
	}
	s := &searchServer{nodes: []any{
		// bob approved, then commented.
		pr(1, []any{review("bob", "COMMENTED", "2026-10-25T10:00:00Z"), review("alice", "COMMENTED", "2026-10-25T11:00:00Z")},
			[]any{review("bob", "APPROVED", "2026-10-24T10:00:00Z")}),
		// carol requested changes, then commented; dave only commented.
		pr(2, []any{review("carol", "COMMENTED", "2026-10-26T10:00:00Z"), review("dave", "COMMENTED", "2026-10-26T11:00:00Z")},
			[]any{review("carol", "CHANGES_REQUESTED", "2026-10-25T10:00:00Z")}),
	}}
	res, err := newSearchClient(t, s).SearchPullRequests(context.Background(), "q")
	require.NoError(t, err)
	assert.Equal(t, []ReviewFact{{Author: "bob", State: "APPROVED", SubmittedAt: at("2026-10-24T10:00:00Z")}}, res.PRs[0].Reviews,
		"bob's approval survives his later comment; alice's replies to her own PR are not reviews")
	assert.Equal(t, "APPROVED", res.PRs[0].ReviewDecision)
	assert.Equal(t, []ReviewFact{
		{Author: "carol", State: "CHANGES_REQUESTED", SubmittedAt: at("2026-10-25T10:00:00Z")},
		{Author: "dave", State: "COMMENTED", SubmittedAt: at("2026-10-26T11:00:00Z")},
	}, res.PRs[1].Reviews)
	assert.Equal(t, "CHANGES_REQUESTED", res.PRs[1].ReviewDecision)
}

// TestGraphQLRateLimitHonoursRetryAfter: a secondary rate limit says when
// to come back.
func TestGraphQLRateLimitHonoursRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message": "slow down"}`))
	}))
	defer srv.Close()
	err := NewClient(WithBaseURL(srv.URL), WithToken("t")).GraphQL(context.Background(), "query { x }", nil, nil)
	var rl *RateLimitError
	require.ErrorAs(t, err, &rl)
	assert.WithinDuration(t, time.Now().Add(2*time.Minute), rl.ResetAt, 10*time.Second)
}

// TestEnrichmentIsBoundedAndReportsRateLimits: enriching hundreds of PRs
// keeps at most enrichConcurrency lookups in flight, and a rate limit is
// returned rather than shown as PRs without checks.
func TestEnrichmentIsBoundedAndReportsRateLimits(t *testing.T) {
	var inFlight, peak atomic.Int32
	var limited atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		if limited.Load() {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message": "You have exceeded a secondary rate limit"}`))
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "/pulls/"):
			_, _ = w.Write([]byte(`{"number": 1, "head": {"sha": "abc"}}`))
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			_, _ = w.Write([]byte(`{"check_runs": [{"conclusion": "success", "status": "completed"}]}`))
		default:
			_, _ = w.Write([]byte(`{"statuses": [], "state": "success"}`))
		}
	}))
	defer srv.Close()
	prs := make([]ExtendedPRInfo, 60)
	for i := range prs {
		prs[i] = ExtendedPRInfo{Number: i + 1, RepoOwner: "acme", RepoName: fmt.Sprintf("repo%d", i), CheckTotal: -1}
	}
	c := NewClient(WithBaseURL(srv.URL), WithToken("t"))
	require.NoError(t, c.EnrichWithChecks(prs))
	assert.LessOrEqual(t, peak.Load(), int32(enrichConcurrency))
	for _, p := range prs {
		assert.Equal(t, 1, p.CheckTotal)
	}

	limited.Store(true)
	for i := range prs {
		prs[i].CheckTotal = -1
	}
	defer limited.Store(false)
	err := NewClient(WithBaseURL(srv.URL), WithToken("t")).EnrichWithChecks(prs)
	var rl *RateLimitError
	require.ErrorAs(t, err, &rl, "a rate limit is reported, not shown as unknown checks")
	err = NewClient(WithBaseURL(srv.URL), WithToken("t")).EnrichWithActivity(prs, "alice")
	require.ErrorAs(t, err, &rl)
}

func TestViewer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assert.Contains(t, string(body), "viewer")
		_, _ = w.Write([]byte(`{"data": {"viewer": {"login": "alice"}}}`))
	}))
	defer srv.Close()
	login, err := NewClient(WithBaseURL(srv.URL), WithToken("t")).Viewer(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "alice", login)
}

// TestListMyOpenPRsFollowsPages: the REST search follows every page, up to
// SearchLimit results.
func TestListMyOpenPRsFollowsPages(t *testing.T) {
	const total = 237
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/search/issues", r.URL.Path)
		q := r.URL.Query()
		pages = append(pages, q.Get("page"))
		per, _ := strconv.Atoi(q.Get("per_page"))
		page, _ := strconv.Atoi(q.Get("page"))
		var items []string
		for i := (page-1)*per + 1; i <= min(page*per, total); i++ {
			items = append(items, fmt.Sprintf(`{"number":%d,"title":"t","html_url":"https://github.com/o/r/pull/%d","state":"open",
				"updated_at":"2025-01-01T00:00:00Z","repository_url":"https://api.github.com/repos/o/r"}`, i, i))
		}
		fmt.Fprintf(w, `{"total_count":%d,"items":[%s]}`, total, strings.Join(items, ","))
	}))
	defer srv.Close()
	prs, err := newTestClient(t, srv, "o/r").ListMyOpenPRs()
	require.NoError(t, err)
	assert.Len(t, prs, total)
	assert.Equal(t, []string{"1", "2", "3"}, pages)
	assert.Equal(t, total, prs[total-1].Number)

	pages = nil
	involved, err := newTestClient(t, srv, "o/r").ListInvolvedPRs("")
	require.NoError(t, err)
	assert.Len(t, involved, total)
}

// TestChecksUnknownWhenAnEndpointFails: when one of the two check endpoints
// fails for a reason other than a rate limit (a token without the Checks
// permission, a 5xx), the PR's checks are unknown rather than counted from
// the other endpoint alone, and the failures are reported.
func TestChecksUnknownWhenAnEndpointFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/pulls/"):
			_, _ = w.Write([]byte(`{"number": 1, "head": {"sha": "abc"}}`))
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message": "Resource not accessible by personal access token"}`))
		default:
			_, _ = w.Write([]byte(`{"statuses": [{"state": "success"}], "state": "success"}`))
		}
	}))
	defer srv.Close()
	prs := []ExtendedPRInfo{{Number: 1, RepoOwner: "acme", RepoName: "api", CheckTotal: -1}, {Number: 2, RepoOwner: "acme", RepoName: "web", CheckTotal: -1}}
	err := NewClient(WithBaseURL(srv.URL), WithToken("t")).EnrichWithChecks(prs)
	require.Error(t, err)
	var rl *RateLimitError
	assert.False(t, errors.As(err, &rl))
	assert.Contains(t, err.Error(), "2 of 2 PRs could not be read")
	for _, p := range prs {
		assert.Equal(t, -1, p.CheckTotal, "one green status must not read as all checks passing")
	}
}
