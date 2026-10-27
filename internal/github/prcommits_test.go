package github

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A PR's commit list is paged 100 at a time; the client follows the pages
// until a short one and keeps branch order.
func TestListPRCommits_Pages(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCommonHeaders(t, r)
		assert.Equal(t, "/repos/virtru/wgo/pulls/42/commits", r.URL.Path)
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		n := 100
		if page == "2" {
			n = 3
		}
		var items []string
		for i := range n {
			items = append(items, fmt.Sprintf(`{"sha":"p%s-%03d","commit":{"committer":{"date":"2026-10-0%sT12:00:00Z"}}}`, page, i, page))
		}
		_, _ = w.Write([]byte("[" + strings.Join(items, ",") + "]"))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "virtru/wgo")

	commits, err := c.ListPRCommits("/tmp/repo", 42)
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "2"}, pages, "stops after the short page")
	require.Len(t, commits, 103)
	assert.Equal(t, "p1-000", commits[0].SHA)
	assert.Equal(t, "p2-002", commits[102].SHA)
	assert.Equal(t, 2, commits[102].CommittedAt.Day())
}
