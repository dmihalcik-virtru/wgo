package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSummarizeChecks(t *testing.T) {
	run := func(status, conclusion, url string) apiCheckRun {
		return apiCheckRun{Status: status, Conclusion: conclusion, HTMLURL: url}
	}
	tests := []struct {
		name        string
		runs        []apiCheckRun
		statuses    []string // combined-status states
		wantState   string
		wantPassed  int
		wantFailed  int
		wantPending int
		wantTotal   int
		wantFailURL string
	}{
		{
			name:      "no checks at all is none",
			wantState: "none",
		},
		{
			name:       "all success",
			runs:       []apiCheckRun{run("completed", "success", ""), run("completed", "success", "")},
			wantState:  "success",
			wantPassed: 2,
			wantTotal:  2,
		},
		{
			name:        "any failure wins and captures first failing url",
			runs:        []apiCheckRun{run("completed", "success", ""), run("completed", "failure", "https://ci/job/1"), run("completed", "failure", "https://ci/job/2")},
			wantState:   "failure",
			wantPassed:  1,
			wantFailed:  2,
			wantTotal:   3,
			wantFailURL: "https://ci/job/1",
		},
		{
			name:        "pending beats success but not failure",
			runs:        []apiCheckRun{run("in_progress", "", ""), run("completed", "success", "")},
			wantState:   "pending",
			wantPassed:  1,
			wantPending: 1,
			wantTotal:   2,
		},
		{
			name:       "neutral and skipped count as passed",
			runs:       []apiCheckRun{run("completed", "neutral", ""), run("completed", "skipped", "")},
			wantState:  "success",
			wantPassed: 2,
			wantTotal:  2,
		},
		{
			name:        "terminal non-success conclusions are failures",
			runs:        []apiCheckRun{run("completed", "timed_out", "https://ci/t"), run("completed", "cancelled", ""), run("completed", "action_required", "")},
			wantState:   "failure",
			wantFailed:  3,
			wantTotal:   3,
			wantFailURL: "https://ci/t",
		},
		{
			name:       "legacy combined statuses fold in",
			statuses:   []string{"success", "failure", "pending"},
			wantState:  "failure",
			wantPassed: 1,
			wantFailed: 1,
			// pending status still counted
			wantPending: 1,
			wantTotal:   3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runs := apiCheckRunsResponse{CheckRuns: tt.runs}
			combined := apiCombinedStatus{}
			for _, s := range tt.statuses {
				combined.Statuses = append(combined.Statuses, apiStatusContext{State: s})
			}
			got, failURL := summarizeChecks(runs, combined)
			assert.Equal(t, tt.wantState, got.State, "state")
			assert.Equal(t, tt.wantPassed, got.Passed, "passed")
			assert.Equal(t, tt.wantFailed, got.Failed, "failed")
			assert.Equal(t, tt.wantPending, got.Pending, "pending")
			assert.Equal(t, tt.wantTotal, got.Total, "total")
			assert.Equal(t, tt.wantFailURL, failURL, "failing url")
		})
	}
}

func TestRollupReviewDecision(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }
	rev := func(login, state string, when time.Time) apiReview {
		return apiReview{User: apiUser{Login: login}, State: state, SubmittedAt: when}
	}
	tests := []struct {
		name    string
		reviews []apiReview
		want    string
	}{
		{name: "no reviews", want: ""},
		{name: "single approval", reviews: []apiReview{rev("a", "APPROVED", at(0))}, want: "APPROVED"},
		{name: "single changes requested", reviews: []apiReview{rev("a", "CHANGES_REQUESTED", at(0))}, want: "CHANGES_REQUESTED"},
		{
			name:    "latest per user wins: approved then changes",
			reviews: []apiReview{rev("a", "APPROVED", at(0)), rev("a", "CHANGES_REQUESTED", at(5))},
			want:    "CHANGES_REQUESTED",
		},
		{
			name:    "latest per user wins: changes then approved",
			reviews: []apiReview{rev("a", "CHANGES_REQUESTED", at(0)), rev("a", "APPROVED", at(5))},
			want:    "APPROVED",
		},
		{
			name:    "any changes-requested reviewer blocks",
			reviews: []apiReview{rev("a", "APPROVED", at(0)), rev("b", "CHANGES_REQUESTED", at(1))},
			want:    "CHANGES_REQUESTED",
		},
		{
			name:    "comment-only reviews are ignored",
			reviews: []apiReview{rev("a", "APPROVED", at(0)), rev("a", "COMMENTED", at(5))},
			want:    "APPROVED",
		},
		{
			name:    "dismissed clears a prior approval",
			reviews: []apiReview{rev("a", "APPROVED", at(0)), rev("a", "DISMISSED", at(5))},
			want:    "",
		},
		{
			name:    "two approvals",
			reviews: []apiReview{rev("a", "APPROVED", at(0)), rev("b", "APPROVED", at(1))},
			want:    "APPROVED",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, rollupReviewDecision(tt.reviews))
		})
	}
}

func TestCIDeepLink(t *testing.T) {
	const prURL = "https://github.com/o/r/pull/42"
	tests := []struct {
		name       string
		state      string
		base       string
		failingURL string
		want       string
	}{
		{name: "failure prefers failing job url", state: "failure", failingURL: "https://ci/job/9", want: "https://ci/job/9"},
		{name: "failure without job falls back to checks", state: "failure", want: prURL + "/checks"},
		{name: "success links to checks", state: "success", want: prURL + "/checks"},
		{name: "pending links to merge queue", state: "pending", base: "main", want: "https://github.com/o/r/queue/main"},
		{name: "pending without base falls back", state: "pending", want: prURL + "/checks"},
		{name: "none has no link", state: "none", want: ""},
		{name: "empty state has no link", state: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ciDeepLink(prURL, 42, tt.base, tt.state, tt.failingURL))
		})
	}
}

// TestCIDeepLinkQueueTrimGuard ensures a PR URL that doesn't end in the expected
// /pull/{n} suffix falls back to the checks tab rather than emitting a bad queue URL.
func TestCIDeepLinkQueueTrimGuard(t *testing.T) {
	got := ciDeepLink("https://github.com/o/r/pull/999", 42, "main", "pending", "")
	assert.Equal(t, "https://github.com/o/r/pull/999/checks", got)
}

func TestListPRsForBranchEnriched(t *testing.T) {
	submitted := time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/pulls":
			_, _ = w.Write([]byte(`[{
				"number":7,"state":"open","title":"Feature","draft":true,
				"html_url":"https://github.com/o/r/pull/7",
				"head":{"ref":"feature","sha":"deadbeef","repo":{"full_name":"o/r"}},
				"base":{"ref":"main"},"user":{"login":"me"}
			}]`))
		case "/repos/o/r/pulls/7/reviews":
			body := []map[string]any{
				{"user": map[string]string{"login": "rev"}, "state": "APPROVED", "submitted_at": submitted.Format(time.RFC3339)},
			}
			_ = json.NewEncoder(w).Encode(body)
		case "/repos/o/r/commits/deadbeef/check-runs":
			_, _ = w.Write([]byte(`{"total_count":1,"check_runs":[{"status":"completed","conclusion":"success"}]}`))
		case "/repos/o/r/commits/deadbeef/status":
			_, _ = w.Write([]byte(`{"state":"success","statuses":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	prs, err := c.ListPRsForBranchEnriched("/tmp", "feature")
	require.NoError(t, err)
	require.Len(t, prs, 1)
	pr := prs[0]
	assert.True(t, pr.IsDraft, "draft flag from base payload")
	assert.Equal(t, "APPROVED", pr.ReviewDecision)
	assert.Equal(t, "success", pr.Checks.State)
	assert.Equal(t, 1, pr.Checks.Passed)
	assert.Equal(t, 1, pr.Checks.Total)
	assert.Equal(t, "https://github.com/o/r/pull/7/checks", pr.Checks.URL, "success links to the checks tab")
}
