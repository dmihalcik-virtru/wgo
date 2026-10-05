package github

import (
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
