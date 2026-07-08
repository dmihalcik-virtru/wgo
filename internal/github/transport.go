package github

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HTTP headers and constants used for every GitHub API request.
const (
	headerAccept     = "application/vnd.github+json"
	headerAPIVersion = "2022-11-28"
)

// userAgent is the value of the User-Agent header on every request. The
// `wgo/<version>` form follows GitHub's recommended pattern.
var userAgent = "wgo/" + clientVersion()

// clientVersion returns a User-Agent version. It mirrors what cmd/root.go
// exposes via the cobra Version, but the github package must not depend on
// cobra, so we keep a small local default and let the binary's build
// metadata be reflected at runtime if a caller calls SetUserAgentVersion.
func clientVersion() string { return uaVersion }

var uaVersion = "dev"

// SetUserAgentVersion overrides the client's reported version. Call from
// cmd/root.go (or any entry point with build info) so requests carry the
// real binary version. Safe to call before constructing any Client.
func SetUserAgentVersion(v string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return
	}
	uaVersion = v
	userAgent = "wgo/" + v
}

// RateLimitError indicates the GitHub API rate limit has been exhausted.
// Returned when X-RateLimit-Remaining=0 or HTTP 429.
type RateLimitError struct {
	ResetAt time.Time // when the limit resets, may be zero if unknown
	Message string
}

// Error implements error.
func (e *RateLimitError) Error() string {
	if !e.ResetAt.IsZero() {
		return fmt.Sprintf("github rate limit exhausted: %s (resets at %s)", e.Message, e.ResetAt.Format(time.RFC3339))
	}
	return fmt.Sprintf("github rate limit exhausted: %s", e.Message)
}

// APIError is the structured HTTP error returned for non-2xx responses.
type APIError struct {
	StatusCode int
	URL        string
	Body       string // truncated response body for context
	Method     string
}

// Error implements error.
func (e *APIError) Error() string {
	body := e.Body
	const maxBody = 400
	if len(body) > maxBody {
		body = body[:maxBody] + "...(truncated)"
	}
	if body == "" {
		return fmt.Sprintf("github api: %s %s -> %d", e.Method, e.URL, e.StatusCode)
	}
	return fmt.Sprintf("github api: %s %s -> %d: %s", e.Method, e.URL, e.StatusCode, body)
}

// IsNotFound reports whether the error is a 404 from the GitHub API.
func IsNotFound(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusNotFound
	}
	return false
}

// cachedResponse is a stored response keyed by URL for conditional requests.
type cachedResponse struct {
	etag       string
	body       []byte
	statusCode int
	expiresAt  time.Time
}

// etagCache stores per-URL cached responses and their ETags. Concurrent-safe.
type etagCache struct {
	mu      sync.Mutex
	entries map[string]*cachedResponse
	ttl     time.Duration
}

func newEtagCache(ttl time.Duration) *etagCache {
	return &etagCache{
		entries: map[string]*cachedResponse{},
		ttl:     ttl,
	}
}

func (c *etagCache) lookup(key string) (*cachedResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	return r, true
}

func (c *etagCache) store(key, etag string, statusCode int, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &cachedResponse{
		etag:       etag,
		body:       body,
		statusCode: statusCode,
		expiresAt:  time.Now().Add(c.ttl),
	}
}

func (c *etagCache) refreshTTL(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.entries[key]; ok {
		r.expiresAt = time.Now().Add(c.ttl)
	}
}

// transport is the http.RoundTripper used by all GitHub API requests.
// It injects auth + standard headers, handles rate limiting, and routes
// GET requests through the ETag cache for conditional requests.
type transport struct {
	base   http.RoundTripper
	tokens *tokenSource
	cache  *etagCache

	// rateMu guards rate-limit state; populated from response headers.
	rateMu       sync.Mutex
	rateRemain   int
	rateLimit    int
	rateResetAt  time.Time
	rateExceeded bool
}

func newTransport(base http.RoundTripper, tokens *tokenSource, cacheTTL time.Duration) *transport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &transport{
		base:       base,
		tokens:     tokens,
		cache:      newEtagCache(cacheTTL),
		rateRemain: -1,
	}
}

// RoundTrip implements http.RoundTripper. It mutates the request to add
// required headers and, for GETs with a cached response, sends an
// If-None-Match. On 304 it returns the cached body.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.rateMu.Lock()
	rateExceeded := t.rateExceeded
	resetAt := t.rateResetAt
	t.rateMu.Unlock()
	if rateExceeded && time.Now().Before(resetAt) {
		return nil, &RateLimitError{
			ResetAt: resetAt,
			Message: "remaining=0 (cached)",
		}
	}

	tok, err := t.tokens.Token()
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", headerAccept)
	req.Header.Set("X-GitHub-Api-Version", headerAPIVersion)
	req.Header.Set("User-Agent", userAgent)

	cacheKey := req.URL.String()
	cacheable := req.Method == http.MethodGet

	if cacheable {
		if entry, ok := t.cache.lookup(cacheKey); ok {
			if time.Now().Before(entry.expiresAt) {
				// Fresh cache hit; no network.
				return responseFromCache(req, entry), nil
			}
			if entry.etag != "" {
				req.Header.Set("If-None-Match", entry.etag)
			}
		}
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	t.recordRateLimit(resp.Header)

	if cacheable && resp.StatusCode == http.StatusNotModified {
		entry, ok := t.cache.lookup(cacheKey)
		_ = resp.Body.Close()
		if !ok {
			// We sent If-None-Match without having a cached body — shouldn't
			// happen, but surface a useful error rather than crashing.
			return nil, fmt.Errorf("github api: 304 Not Modified with no cached response for %s", cacheKey)
		}
		t.cache.refreshTTL(cacheKey)
		return responseFromCache(req, entry), nil
	}

	if cacheable && resp.StatusCode == http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("github api: reading response body: %w", readErr)
		}
		etag := resp.Header.Get("ETag")
		t.cache.store(cacheKey, etag, resp.StatusCode, body)
		resp.Body = io.NopCloser(strings.NewReader(string(body)))
		return resp, nil
	}

	return resp, nil
}

func (t *transport) recordRateLimit(h http.Header) {
	rem := h.Get("X-RateLimit-Remaining")
	if rem == "" {
		return
	}
	t.rateMu.Lock()
	defer t.rateMu.Unlock()
	if n, err := strconv.Atoi(rem); err == nil {
		t.rateRemain = n
		t.rateExceeded = n == 0
	}
	if lim := h.Get("X-RateLimit-Limit"); lim != "" {
		if n, err := strconv.Atoi(lim); err == nil {
			t.rateLimit = n
		}
	}
	if reset := h.Get("X-RateLimit-Reset"); reset != "" {
		if ts, err := strconv.ParseInt(reset, 10, 64); err == nil {
			t.rateResetAt = time.Unix(ts, 0)
		}
	}
}

// responseFromCache constructs an http.Response from a cached body so callers
// can drain it through the normal path.
func responseFromCache(req *http.Request, entry *cachedResponse) *http.Response {
	return &http.Response{
		Status:        http.StatusText(entry.statusCode),
		StatusCode:    entry.statusCode,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"X-Wgo-Cache": []string{"hit"}},
		Body:          io.NopCloser(strings.NewReader(string(entry.body))),
		ContentLength: int64(len(entry.body)),
		Request:       req,
	}
}
