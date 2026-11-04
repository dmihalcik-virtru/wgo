package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	// NoData is set when the response carried no data at all, only errors.
	NoData bool
}

// timedOut reports whether GitHub gave up on the whole query: it answers a
// query that ran too long with HTTP 200, no data and an error saying "This
// may be the result of a timeout".
func (e *GraphQLErrors) timedOut() bool {
	if !e.NoData {
		return false
	}
	for _, ge := range e.Errors {
		if strings.Contains(strings.ToLower(ge.Message), "timeout") {
			return true
		}
	}
	return false
}

// IncompleteResponseError is a GraphQL response with a successful HTTP
// status whose body was empty, cut off or not JSON, or held neither data nor
// errors: a proxy or GitHub dropped the response part way. It is transient,
// so it is retried like a 5xx.
type IncompleteResponseError struct {
	StatusCode int
	// Received is how many bytes of body arrived.
	Received int
	Err      error
}

// Error implements error.
func (e *IncompleteResponseError) Error() string {
	return fmt.Sprintf("github graphql: incomplete response (HTTP %d, %d bytes): %v", e.StatusCode, e.Received, e.Err)
}

// Unwrap returns the read or parse error.
func (e *IncompleteResponseError) Unwrap() error { return e.Err }

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
// *GraphQLErrors, so the caller can keep partial data. A successful status
// with a body that is empty, cut off, not JSON, or without data and errors is
// an *IncompleteResponseError.
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
		if resp.StatusCode < 300 {
			return &IncompleteResponseError{StatusCode: resp.StatusCode, Received: len(raw), Err: err}
		}
		return fmt.Errorf("github graphql: reading body: %w", err)
	}
	if resp.StatusCode >= 300 {
		return graphQLHTTPError(resp, raw)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return &IncompleteResponseError{StatusCode: resp.StatusCode, Received: len(raw), Err: errors.New("empty body")}
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []GraphQLError  `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return &IncompleteResponseError{StatusCode: resp.StatusCode, Received: len(raw), Err: err}
	}
	noData := len(env.Data) == 0 || bytes.Equal(env.Data, []byte("null"))
	if noData && len(env.Errors) == 0 {
		// GraphQL always answers with data, errors or both.
		return &IncompleteResponseError{StatusCode: resp.StatusCode, Received: len(raw), Err: errors.New("neither data nor errors")}
	}
	if data != nil && !noData {
		if err := json.Unmarshal(env.Data, data); err != nil {
			return fmt.Errorf("github graphql: parse data: %w", err)
		}
	}
	if len(env.Errors) > 0 {
		return &GraphQLErrors{Errors: env.Errors, NoData: noData}
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
