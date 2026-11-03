package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// GraphQLError is one entry of a GraphQL response's errors array.
type GraphQLError struct {
	Message string `json:"message"`
	// Type is GitHub's error type, such as NOT_FOUND or FORBIDDEN.
	Type string `json:"type,omitempty"`
	// Path names the field that failed, such as ["search", "nodes", 3].
	Path []any `json:"path,omitempty"`
}

// GraphQLErrors is returned by GraphQL when the response carried an errors
// array. Whatever data came with it has still been decoded: GitHub answers a
// query that touches a repository the token cannot read with the rest of the
// data and an error for the hole, so the data is often worth keeping.
type GraphQLErrors struct {
	Errors []GraphQLError
}

// Error implements error, listing the first few messages.
func (e *GraphQLErrors) Error() string {
	const maxListed = 3
	var msgs []string
	for i, ge := range e.Errors {
		if i == maxListed {
			msgs = append(msgs, fmt.Sprintf("and %d more", len(e.Errors)-maxListed))
			break
		}
		msgs = append(msgs, ge.Message)
	}
	return "github graphql: " + strings.Join(msgs, "; ")
}

// GraphQL posts query with variables to GitHub's GraphQL endpoint on the
// authenticated client and decodes the response's data into data, which may
// be nil. A non-2xx response is an *APIError (or a *RateLimitError), like the
// REST helpers return; a transport failure is returned as is. A response with
// a non-empty errors array decodes its data anyway and returns
// *GraphQLErrors, so the caller can keep partial data.
func (c *CLIClient) GraphQL(ctx context.Context, query string, variables map[string]any, data any) error {
	body, err := json.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables,omitempty"`
	}{query, variables})
	if err != nil {
		return fmt.Errorf("github graphql: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphqlURL(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("github graphql: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("github graphql: reading body: %w", err)
	}
	if resp.StatusCode >= 300 {
		return graphQLHTTPError(resp, raw)
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []GraphQLError  `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("github graphql: parse response: %w", err)
	}
	if data != nil && len(env.Data) > 0 && !bytes.Equal(env.Data, []byte("null")) {
		if err := json.Unmarshal(env.Data, data); err != nil {
			return fmt.Errorf("github graphql: parse data: %w", err)
		}
	}
	if len(env.Errors) > 0 {
		return &GraphQLErrors{Errors: env.Errors}
	}
	return nil
}

// graphQLHTTPError is the typed error of a non-2xx GraphQL response. A rate
// limit (any 429, or a 403 saying so, as secondary limits do) is a
// *RateLimitError whose ResetAt comes from Retry-After, else from
// X-RateLimit-Reset, so callers can wait as long as GitHub asks.
func graphQLHTTPError(resp *http.Response, body []byte) error {
	err := apiErrorFromResp(http.MethodPost, "/graphql", resp.StatusCode, body)
	rl, ok := err.(*RateLimitError)
	if !ok && resp.StatusCode == http.StatusTooManyRequests {
		rl, ok = &RateLimitError{Message: string(body)}, true
	}
	if !ok {
		return err
	}
	if secs, perr := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); perr == nil && secs >= 0 {
		rl.ResetAt = time.Now().Add(time.Duration(secs) * time.Second)
	} else if reset, perr := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); perr == nil {
		rl.ResetAt = time.Unix(reset, 0)
	}
	return rl
}

// graphqlURL is the GraphQL endpoint beside the REST base URL: /graphql on
// api.github.com, /api/graphql beside a GitHub Enterprise /api/v3.
func (c *CLIClient) graphqlURL() string {
	if base, ok := strings.CutSuffix(c.baseURL, "/api/v3"); ok {
		return base + "/api/graphql"
	}
	return c.baseURL + "/graphql"
}
