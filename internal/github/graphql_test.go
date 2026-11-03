package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGraphQLPostsQueryAndDecodesData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertCommonHeaders(t, r)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/graphql", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &req))
		assert.Equal(t, "query($n: Int!) { x }", req.Query)
		assert.EqualValues(t, 3, req.Variables["n"])
		_, _ = w.Write([]byte(`{"data": {"x": 42}}`))
	}))
	defer srv.Close()
	var out struct{ X int }
	require.NoError(t, newTestClient(t, srv, "o/r").GraphQL(context.Background(), "query($n: Int!) { x }", map[string]any{"n": 3}, &out))
	assert.Equal(t, 42, out.X)
}

// TestGraphQLHTTPFailureIsAPIError: an HTTP failure is the typed APIError the
// REST helpers return, so callers can read the status.
func TestGraphQLHTTPFailureIsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"message": "Server Error"}`))
	}))
	defer srv.Close()
	err := newTestClient(t, srv, "o/r").GraphQL(context.Background(), "query { x }", nil, nil)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
	assert.Equal(t, http.MethodPost, apiErr.Method)
}

// TestGraphQLErrorsKeepPartialData: an errors array comes back as
// GraphQLErrors, and the data beside it is still decoded.
func TestGraphQLErrorsKeepPartialData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data": {"a": 1, "b": null},
			"errors": [{"message": "Could not resolve to a Repository", "type": "NOT_FOUND", "path": ["b"]},
			           {"message": "second"}, {"message": "third"}, {"message": "fourth"}]}`))
	}))
	defer srv.Close()
	var out struct {
		A int
		B *int
	}
	err := newTestClient(t, srv, "o/r").GraphQL(context.Background(), "query { a b }", nil, &out)
	var gqlErrs *GraphQLErrors
	require.ErrorAs(t, err, &gqlErrs)
	assert.Equal(t, "NOT_FOUND", gqlErrs.Errors[0].Type)
	assert.Equal(t, []any{"b"}, gqlErrs.Errors[0].Path)
	assert.Equal(t, "github graphql: Could not resolve to a Repository; second; third; and 1 more", err.Error())
	assert.Equal(t, 1, out.A, "partial data is decoded")
	assert.Nil(t, out.B)
}

func TestGraphQLURL(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.github.com":         "https://api.github.com/graphql",
		"https://ghe.example.com/api/v3": "https://ghe.example.com/api/graphql",
		"http://127.0.0.1:9999":          "http://127.0.0.1:9999/graphql",
	} {
		assert.Equal(t, want, NewClient(WithBaseURL(base), WithToken("t")).graphqlURL(), base)
	}
}

func TestGraphQLTransportErrorIsReturned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	c := NewClient(WithBaseURL(url), WithToken("t"))
	err := c.GraphQL(context.Background(), "query { x }", nil, nil)
	require.Error(t, err)
	var apiErr *APIError
	assert.False(t, errors.As(err, &apiErr))
}
