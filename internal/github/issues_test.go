package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetIssue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCommonHeaders(t, r)
		assert.Equal(t, "/repos/o/r/issues/70", r.URL.Path)
		_, _ = w.Write([]byte(`{"number":70,"title":"dash","state":"closed","state_reason":"completed",
			"html_url":"https://github.com/o/r/issues/70","updated_at":"2026-10-01T00:00:00Z"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	is, err := c.GetIssue("o", "r", 70)
	require.NoError(t, err)
	assert.Equal(t, 70, is.Number)
	assert.Equal(t, "closed", is.State)
	assert.Equal(t, "completed", is.StateReason)
	assert.Equal(t, "https://github.com/o/r/issues/70", is.URL)
	assert.False(t, is.IsPR)
}

func TestGetIssueDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCommonHeaders(t, r)
		assert.Equal(t, "/repos/o/r/issues/42", r.URL.Path)
		_, _ = w.Write([]byte(`{"number":42,"title":"Improve coverage","body":"Acceptance criteria here.","html_url":"https://github.com/o/r/issues/42"}`))
	}))
	defer srv.Close()
	issue, err := newTestClient(t, srv, "o/r").GetIssueDetails("o", "r", 42)
	require.NoError(t, err)
	assert.Equal(t, "Improve coverage", issue.Title)
	assert.Equal(t, "Acceptance criteria here.", issue.Body)
	assert.Equal(t, "https://github.com/o/r/issues/42", issue.URL)
}

func TestGetIssueDetailsRejectsPR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"number":42,"title":"A PR","pull_request":{}}`))
	}))
	defer srv.Close()
	_, err := newTestClient(t, srv, "o/r").GetIssueDetails("o", "r", 42)
	require.ErrorContains(t, err, "pull request")
}

func TestCreateIssueWithLabelsAndMilestone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCommonHeaders(t, r)
		switch r.URL.Path {
		case "/repos/o/r/milestones":
			assert.Equal(t, http.MethodGet, r.Method)
			_, _ = w.Write([]byte(`[{"number":7,"title":"Next release"}]`))
		case "/repos/o/r/issues":
			assert.Equal(t, http.MethodPost, r.Method)
			var payload map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			assert.Equal(t, "Add a feature", payload["title"])
			assert.Equal(t, float64(7), payload["milestone"])
			assert.Equal(t, []any{"feature", "planned"}, payload["labels"])
			_, _ = w.Write([]byte(`{"number":104,"title":"Add a feature","html_url":"https://github.com/o/r/issues/104"}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	issue, err := newTestClient(t, srv, "o/r").CreateIssue("o", "r", "Add a feature", []string{"feature", "planned"}, "Next release")
	require.NoError(t, err)
	assert.Equal(t, 104, issue.Number)
	assert.Equal(t, "https://github.com/o/r/issues/104", issue.URL)
}
