package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient builds a *CLIClient pointed at a test server, with a static
// token and a slugResolver that returns a fixed slug for every call.
func newTestClient(t *testing.T, srv *httptest.Server, slug string) *CLIClient {
	t.Helper()
	return NewClient(
		WithBaseURL(srv.URL),
		WithToken("test-token"),
		WithSlugResolver(func(string) (string, error) { return slug, nil }),
	)
}

// assertCommonHeaders verifies every request to the test server carries the
// headers we promised.
func assertCommonHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "Authorization header")
	assert.Equal(t, headerAccept, r.Header.Get("Accept"), "Accept header")
	assert.Equal(t, headerAPIVersion, r.Header.Get("X-GitHub-Api-Version"), "X-GitHub-Api-Version header")
	assert.True(t, strings.HasPrefix(r.Header.Get("User-Agent"), "wgo/"),
		"User-Agent header (got %q)", r.Header.Get("User-Agent"))
}

func TestGetPRStatus_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCommonHeaders(t, r)
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/repos/virtru/wgo/pulls", r.URL.Path)
		assert.Equal(t, "virtru:feature", r.URL.Query().Get("head"))
		assert.Equal(t, "open", r.URL.Query().Get("state"))
		w.Header().Set("ETag", `"abc123"`)
		_, _ = w.Write([]byte(`[{
			"number": 7,
			"state": "open",
			"title": "feat: add stuff",
			"body": "PR body",
			"html_url": "https://github.com/virtru/wgo/pull/7",
			"merged_at": null,
			"head": {"ref": "feature", "sha": "deadbeef", "repo": {"full_name": "virtru/wgo"}},
			"user": {"login": "alice"}
		}]`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "virtru/wgo")

	pr, err := c.GetPRStatus("/tmp/repo", "feature")
	require.NoError(t, err)
	require.NotNil(t, pr)
	assert.Equal(t, 7, pr.Number)
	assert.Equal(t, "open", pr.State)
	assert.Equal(t, "feature", pr.Branch)
	assert.Equal(t, "deadbeef", pr.HeadSHA)
	assert.Equal(t, "https://github.com/virtru/wgo/pull/7", pr.URL)
	assert.Equal(t, "alice", pr.Author)
	assert.False(t, pr.IsMerged())
	assert.False(t, pr.IsClosed())
}

func TestGetPRStatus_NoPR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "virtru/wgo")

	pr, err := c.GetPRStatus("/tmp/repo", "feature")
	require.NoError(t, err)
	assert.Nil(t, pr)
}

func TestGetPRStatus_MergedSetsState(t *testing.T) {
	merged := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{
			"number":    9,
			"state":     "closed",
			"title":     "merged PR",
			"merged_at": merged.Format(time.RFC3339),
			"head":      map[string]any{"ref": "branch", "sha": "abc", "repo": map[string]any{"full_name": "virtru/wgo"}},
			"user":      map[string]any{"login": "bob"},
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{body})
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "virtru/wgo")

	pr, err := c.GetPRStatus("/tmp/repo", "branch")
	require.NoError(t, err)
	require.NotNil(t, pr)
	assert.Equal(t, "merged", pr.State, "merged_at being non-nil should normalize state to 'merged'")
	assert.True(t, pr.IsMerged())
	assert.False(t, pr.IsClosed())
}

func TestGetPRStatus_NoTokenReturnsNil(t *testing.T) {
	// Build a client whose token resolution will fail and no gh fallback;
	// Available() returns false → GetPRStatus returns (nil, nil).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be called when token is unavailable")
	}))
	defer srv.Close()
	c := NewClient(
		WithBaseURL(srv.URL),
		// no token, no gh on PATH guarantee — make resolver fail explicitly
		WithSlugResolver(func(string) (string, error) { return "virtru/wgo", nil }),
	)
	// Force token failure.
	c.tokens.SetToken("")
	c.tokens.err = fmt.Errorf("no token")
	c.tokens.resolved = true

	pr, err := c.GetPRStatus("/tmp/repo", "x")
	assert.NoError(t, err)
	assert.Nil(t, pr)
}

func TestListPRsForBranch_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCommonHeaders(t, r)
		assert.Equal(t, "all", r.URL.Query().Get("state"))
		_, _ = w.Write([]byte(`[
			{"number": 1, "state": "open", "title": "A", "head":{"ref":"a","sha":"sha-a","repo":{"full_name":"o/r"}}, "user":{"login":"x"}},
			{"number": 2, "state": "closed", "title": "B", "head":{"ref":"a","sha":"sha-b","repo":{"full_name":"o/r"}}, "user":{"login":"y"}}
		]`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	prs, err := c.ListPRsForBranch("/tmp", "a")
	require.NoError(t, err)
	require.Len(t, prs, 2)
	assert.Equal(t, 1, prs[0].Number)
	assert.Equal(t, "open", prs[0].State)
	assert.Equal(t, 2, prs[1].Number)
}

func TestClosePR_SendsPatch(t *testing.T) {
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		assertCommonHeaders(t, r)
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/repos/o/r/pulls/42", r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"state":"closed"}`, string(body))
		_, _ = w.Write([]byte(`{"number":42,"state":"closed"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	require.NoError(t, c.ClosePR("/tmp", 42))
	assert.True(t, called.Load())
}

func TestUpdatePRBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		body, _ := io.ReadAll(r.Body)
		var got map[string]string
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, "new body", got["body"])
		_, _ = w.Write([]byte(`{"number":1}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	require.NoError(t, c.UpdatePRBody("/tmp", 1, "new body"))
}

func TestUpdatePRBase_RejectsEmpty(t *testing.T) {
	c := newTestClient(t, httptest.NewServer(http.NotFoundHandler()), "o/r")
	err := c.UpdatePRBase("/tmp", 1, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base branch is required")
}

func TestUpdatePRBase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		body, _ := io.ReadAll(r.Body)
		var got map[string]string
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, "main", got["base"])
		_, _ = w.Write([]byte(`{"number":1}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	require.NoError(t, c.UpdatePRBase("/tmp", 1, "main"))
}

func TestGetPRBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/o/r/pulls/42", r.URL.Path)
		_, _ = w.Write([]byte(`{"number":42,"body":"hello\nworld","head":{"ref":"b","sha":"s","repo":{"full_name":"o/r"}}}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	body, err := c.GetPRBody("/tmp", 42)
	require.NoError(t, err)
	assert.Equal(t, "hello\nworld", body)
}

func TestDeleteRemoteBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/repos/o/r/git/refs/heads/feature%2Fauth", r.URL.EscapedPath())
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	require.NoError(t, c.DeleteRemoteBranch("/tmp", "feature/auth"))
}

func TestDeleteRemoteBranch_AlreadyGone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Reference does not exist","documentation_url":""}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	require.NoError(t, c.DeleteRemoteBranch("/tmp", "stale"))
}

func TestDeleteRemoteBranch_404IsOk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")
	require.NoError(t, c.DeleteRemoteBranch("/tmp", "anything"))
}

func TestDeleteRemoteBranch_ErrorWrapsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"server exploded"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")
	err := c.DeleteRemoteBranch("/tmp", "b")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
	assert.Contains(t, err.Error(), "server exploded")
}

func TestCurrentUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/user", r.URL.Path)
		_, _ = w.Write([]byte(`{"login":"davem"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	user, err := c.CurrentUser()
	require.NoError(t, err)
	assert.Equal(t, "davem", user)
}

func TestCurrentUser_EmptyLoginIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"login":""}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")
	_, err := c.CurrentUser()
	require.Error(t, err)
}

func TestPRBranch_PackageLevel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/virtru/wgo/pulls/3", r.URL.Path)
		_, _ = w.Write([]byte(`{"number":3,"head":{"ref":"my-branch","sha":"s","repo":{"full_name":"virtru/wgo"}}}`))
	}))
	defer srv.Close()
	// Replace package-level client for this test.
	prev := defaultClientInstance
	defaultClientInstance = newTestClient(t, srv, "virtru/wgo")
	defer func() { defaultClientInstance = prev }()

	branch, err := PRBranch("virtru", "wgo", 3)
	require.NoError(t, err)
	assert.Equal(t, "my-branch", branch)
}

func TestIssueTitle_PackageLevel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/virtru/wgo/issues/12", r.URL.Path)
		_, _ = w.Write([]byte(`{"title":"My Issue"}`))
	}))
	defer srv.Close()
	prev := defaultClientInstance
	defaultClientInstance = newTestClient(t, srv, "virtru/wgo")
	defer func() { defaultClientInstance = prev }()

	title, err := IssueTitle("virtru", "wgo", 12)
	require.NoError(t, err)
	assert.Equal(t, "My Issue", title)
}

func TestRepoDefaultBranch_PackageLevel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/virtru/wgo", r.URL.Path)
		_, _ = w.Write([]byte(`{"default_branch":"main"}`))
	}))
	defer srv.Close()
	prev := defaultClientInstance
	defaultClientInstance = newTestClient(t, srv, "virtru/wgo")
	defer func() { defaultClientInstance = prev }()

	branch, err := RepoDefaultBranch("virtru", "wgo")
	require.NoError(t, err)
	assert.Equal(t, "main", branch)
}

func TestETagCaching(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = fmt.Fprintf(w, `{"login":"user","hit":%d}`, n)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "o/r")
	// Use a tiny TTL so the second call goes back to the server (and a 304 hits cache).
	c.tr = newTransport(http.DefaultTransport, c.tokens, 1*time.Millisecond)
	c.http = &http.Client{Transport: c.tr, Timeout: 5 * time.Second}

	user1, err := c.CurrentUser()
	require.NoError(t, err)
	assert.Equal(t, "user", user1)

	// Wait past the TTL so the cache forces a conditional request.
	time.Sleep(10 * time.Millisecond)

	user2, err := c.CurrentUser()
	require.NoError(t, err)
	assert.Equal(t, "user", user2, "cached body returned on 304")

	assert.Equal(t, int32(2), atomic.LoadInt32(&hits), "expected exactly 2 server hits (1 fresh + 1 conditional)")
}

func TestRateLimitError(t *testing.T) {
	resetAt := time.Now().Add(2 * time.Minute).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Limit", "60")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	_, err := c.CurrentUser()
	require.Error(t, err)
	var rl *RateLimitError
	if !asRateLimit(err, &rl) {
		t.Fatalf("expected RateLimitError, got %T: %v", err, err)
	}
	assert.Contains(t, rl.Error(), "rate limit")

	// Second call should short-circuit thanks to cached rate-exceeded state.
	_, err = c.CurrentUser()
	require.Error(t, err)
	assert.ErrorAs(t, err, &rl)
}

func asRateLimit(err error, target **RateLimitError) bool {
	for err != nil {
		if e, ok := err.(*RateLimitError); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		if u, ok := err.(unwrapper); ok {
			err = u.Unwrap()
			continue
		}
		return false
	}
	return false
}

func TestAPIError_4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	err := c.UpdatePRBody("/tmp", 1, "x")
	require.Error(t, err)
	assert.True(t, IsNotFound(err), "IsNotFound should report true")
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.StatusCode)
	assert.Equal(t, http.MethodPatch, apiErr.Method)
}

func TestAPIError_5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`bad gateway`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	err := c.ClosePR("/tmp", 9)
	require.Error(t, err)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
}

func TestGetSpecFrontmatter_404IsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	out, err := c.GetSpecFrontmatterForBranch("o/r", "main", "spec/foo.md")
	require.NoError(t, err)
	assert.Equal(t, "", out)
}

func TestGetSpecFrontmatter_Base64(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/o/r/contents/spec/foo.md", r.URL.Path)
		assert.Equal(t, "main", r.URL.Query().Get("ref"))
		// base64("hello\nworld")
		_, _ = w.Write([]byte(`{"type":"file","encoding":"base64","content":"aGVsbG8K\nd29ybGQ="}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	out, err := c.GetSpecFrontmatterForBranch("o/r", "main", "spec/foo.md")
	require.NoError(t, err)
	assert.Equal(t, "hello\nworld", out)
}

func TestSearchPRs_ListMyOpenPRs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCommonHeaders(t, r)
		assert.Equal(t, "/search/issues", r.URL.Path)
		assert.Contains(t, r.URL.Query().Get("q"), "author:@me")
		assert.Contains(t, r.URL.Query().Get("q"), "is:pr")
		_, _ = w.Write([]byte(`{
			"total_count": 1,
			"incomplete_results": false,
			"items": [{
				"number": 5,
				"title": "Open PR",
				"html_url": "https://github.com/o/r/pull/5",
				"state": "open",
				"draft": false,
				"updated_at": "2025-01-01T00:00:00Z",
				"user": {"login": "alice"},
				"repository_url": "https://api.github.com/repos/o/r"
			}]
		}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")
	prs, err := c.ListMyOpenPRs()
	require.NoError(t, err)
	require.Len(t, prs, 1)
	assert.Equal(t, 5, prs[0].Number)
	assert.Equal(t, "o", prs[0].RepoOwner)
	assert.Equal(t, "r", prs[0].RepoName)
}

func TestSearchPRs_ListMyCommentedPRs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/search/issues", r.URL.Path)
		q := r.URL.Query().Get("q")
		assert.Contains(t, q, "commenter:davem")
		assert.Contains(t, q, "updated:>=")
		_, _ = w.Write([]byte(`{
			"items":[{
				"number":7,"title":"hi","html_url":"https://github.com/o/r/issues/7","updated_at":"2025-01-01T00:00:00Z",
				"repository_url":"https://api.github.com/repos/o/r"
			}]
		}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")
	since := time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)
	out, err := c.ListMyCommentedPRs("davem", since)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, 7, out[0].Number)
	assert.Equal(t, "o/r", out[0].RepoSlug)
}

func TestFetchMyLastActivityOnPR(t *testing.T) {
	t1 := time.Date(2025, 3, 10, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 3, 11, 9, 0, 0, 0, time.UTC) // later
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/comments"):
			body := []map[string]any{
				{"user": map[string]string{"login": "davem"}, "created_at": t1.Format(time.RFC3339)},
				{"user": map[string]string{"login": "someone-else"}, "created_at": t2.Format(time.RFC3339)},
			}
			_ = json.NewEncoder(w).Encode(body)
		case strings.Contains(r.URL.Path, "/reviews"):
			body := []map[string]any{
				{"user": map[string]string{"login": "davem"}, "state": "APPROVED", "submitted_at": t2.Format(time.RFC3339)},
			}
			_ = json.NewEncoder(w).Encode(body)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	got, err := c.FetchMyLastActivityOnPR("o", "r", 9, "davem")
	require.NoError(t, err)
	assert.Equal(t, t2, got, "latest activity is the review, since the comment from someone-else is filtered out")
}

func TestFetchPRChecks_Combined(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/pulls/12":
			_, _ = w.Write([]byte(`{"number":12,"head":{"ref":"b","sha":"abc","repo":{"full_name":"o/r"}}}`))
		case "/repos/o/r/commits/abc/check-runs":
			_, _ = w.Write([]byte(`{"total_count":2,"check_runs":[
				{"status":"completed","conclusion":"success"},
				{"status":"completed","conclusion":"failure"}
			]}`))
		case "/repos/o/r/commits/abc/status":
			_, _ = w.Write([]byte(`{"state":"success","statuses":[{"state":"success"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	passing, total := c.FetchPRChecks("o", "r", 12)
	assert.Equal(t, 2, passing, "2 successes total (1 check-run + 1 status)")
	assert.Equal(t, 3, total)
}

func TestFetchPRChecks_NoChecks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/pulls/12":
			_, _ = w.Write([]byte(`{"number":12,"head":{"ref":"b","sha":"abc","repo":{"full_name":"o/r"}}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")
	passing, total := c.FetchPRChecks("o", "r", 12)
	assert.Equal(t, -1, passing)
	assert.Equal(t, -1, total)
}

func TestSlugFromRemoteURL(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"https", "https://github.com/owner/repo.git", "owner/repo"},
		{"https no .git", "https://github.com/owner/repo", "owner/repo"},
		{"ssh scp-style", "git@github.com:owner/repo.git", "owner/repo"},
		{"ssh scp-style no .git", "git@github.com:owner/repo", "owner/repo"},
		{"www", "https://www.github.com/owner/repo", "owner/repo"},
		{"non-github", "https://gitlab.com/owner/repo", ""},
		{"empty", "", ""},
		{"missing", "not-a-url", ""},
		{"single segment", "https://github.com/owner", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SlugFromRemoteURL(tc.in)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseGitHubSlugFromRemoteList(t *testing.T) {
	out := `origin git@github.com:virtru/wgo.git (fetch)
origin git@github.com:virtru/wgo.git (push)
upstream https://github.com/upstream-org/wgo.git`
	got := parseGitHubSlugFromRemoteList(out)
	assert.Equal(t, "virtru/wgo", got, "origin wins over upstream")

	// no origin -> any URL counts
	out2 := `upstream https://github.com/upstream-org/wgo.git`
	got2 := parseGitHubSlugFromRemoteList(out2)
	assert.Equal(t, "upstream-org/wgo", got2)

	// no github at all -> empty
	out3 := `origin git@gitlab.com:upstream-org/wgo.git`
	got3 := parseGitHubSlugFromRemoteList(out3)
	assert.Equal(t, "", got3)
}

func TestSlugFromGitConfig(t *testing.T) {
	cfg := `[core]
	repositoryformatversion = 0
[remote "origin"]
	url = git@github.com:virtru/wgo.git
	fetch = +refs/heads/*:refs/remotes/origin/*
[remote "fork"]
	url = https://github.com/fork-org/wgo.git
`
	got := slugFromGitConfigContent(cfg)
	assert.Equal(t, "virtru/wgo", got, "origin wins")

	cfg2 := `[remote "fork"]
	url = https://github.com/fork-org/wgo.git
`
	got2 := slugFromGitConfigContent(cfg2)
	assert.Equal(t, "fork-org/wgo", got2, "fallback to any github remote when no origin")
}

func TestETagCacheBypassed_PostAndPatchAlwaysHit(t *testing.T) {
	var patches, gets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			atomic.AddInt32(&gets, 1)
			w.Header().Set("ETag", `"v"`)
			_, _ = w.Write([]byte(`{"login":"u"}`))
			return
		}
		atomic.AddInt32(&patches, 1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "o/r")

	require.NoError(t, c.UpdatePRBody("/tmp", 1, "x"))
	require.NoError(t, c.UpdatePRBody("/tmp", 1, "y"))
	assert.Equal(t, int32(2), atomic.LoadInt32(&patches), "patches never cached")
	assert.Equal(t, int32(0), atomic.LoadInt32(&gets))
}

func TestUserAgentVersionSetter(t *testing.T) {
	prev := uaVersion
	defer func() { SetUserAgentVersion(prev) }()
	SetUserAgentVersion("1.2.3")
	assert.Equal(t, "wgo/1.2.3", userAgent)
}

func TestAvailable_WithToken(t *testing.T) {
	c := NewClient(WithToken("xyz"))
	assert.True(t, c.Available())
}
