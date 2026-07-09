// Package github provides GitHub URL parsing and a REST API client.
//
// The client talks directly to https://api.github.com over HTTPS instead of
// shelling out to the gh CLI. Auth credentials come from the GITHUB_TOKEN
// environment variable, falling back to `gh auth token` for the credential
// bootstrap (the only retained shell-out to gh).
package github

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/virtru/wgo/models"
)

// URLType represents the type of GitHub URL.
type URLType int

const (
	URLTypePR     URLType = iota // Pull request URL
	URLTypeBranch                // Branch/tree URL
	URLTypeIssue                 // Issue URL
)

// ParsedURL contains the parsed components of a GitHub URL.
type ParsedURL struct {
	Owner      string
	Repo       string
	Type       URLType
	Identifier string // PR number, branch name, or issue number
}

// ParseGitHubURL parses a GitHub URL into its components.
func ParseGitHubURL(rawURL string) (*ParsedURL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	if u.Host != "github.com" && u.Host != "www.github.com" {
		return nil, fmt.Errorf("not a GitHub URL: %s", u.Host)
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return nil, fmt.Errorf("URL must include owner and repo: %s", rawURL)
	}

	owner := parts[0]
	repo := strings.TrimSuffix(parts[1], ".git")

	if len(parts) == 2 {
		return &ParsedURL{
			Owner:      owner,
			Repo:       repo,
			Type:       URLTypeBranch,
			Identifier: "", // empty means default branch
		}, nil
	}

	switch parts[2] {
	case "pull":
		if len(parts) < 4 {
			return nil, fmt.Errorf("PR URL missing number: %s", rawURL)
		}
		if _, err := strconv.Atoi(parts[3]); err != nil {
			return nil, fmt.Errorf("invalid PR number: %s", parts[3])
		}
		return &ParsedURL{
			Owner:      owner,
			Repo:       repo,
			Type:       URLTypePR,
			Identifier: parts[3],
		}, nil

	case "tree":
		if len(parts) < 4 {
			return nil, fmt.Errorf("branch URL missing branch name: %s", rawURL)
		}
		branch := strings.Join(parts[3:], "/")
		return &ParsedURL{
			Owner:      owner,
			Repo:       repo,
			Type:       URLTypeBranch,
			Identifier: branch,
		}, nil

	case "issues":
		if len(parts) < 4 {
			return nil, fmt.Errorf("issue URL missing number: %s", rawURL)
		}
		if _, err := strconv.Atoi(parts[3]); err != nil {
			return nil, fmt.Errorf("invalid issue number: %s", parts[3])
		}
		return &ParsedURL{
			Owner:      owner,
			Repo:       repo,
			Type:       URLTypeIssue,
			Identifier: parts[3],
		}, nil

	default:
		return nil, fmt.Errorf("unsupported GitHub URL type: %s", parts[2])
	}
}

// PRBranch returns the head branch name for a pull request.
func PRBranch(owner, repo string, number int) (string, error) {
	c := defaultClient()
	pr, err := c.fetchPR(owner+"/"+repo, number)
	if err != nil {
		return "", fmt.Errorf("get PR #%d head branch: %w", number, err)
	}
	if pr.Head.Ref == "" {
		return "", fmt.Errorf("github returned empty head branch for PR #%d", number)
	}
	return pr.Head.Ref, nil
}

// PRHeadInfo bundles the bits of a PR's head ref we need to fetch it into a
// local jj repo: the branch name on the head repo, the head commit OID, and
// the slug of the head repository (which differs from the base repo for
// fork PRs).
type PRHeadInfo struct {
	Ref      string // headRefName, the branch on the head repo
	OID      string // headRefOid, the 40-char commit id
	RepoSlug string // headRepository.FullName ("owner/repo")
}

// GetPRHeadRef returns the head-ref details for a pull request. Used by
// `wgo to <PR URL>` to decide whether to fetch from origin or add a fork
// remote first.
func GetPRHeadRef(slug string, number int) (*PRHeadInfo, error) {
	c := defaultClient()
	pr, err := c.fetchPR(slug, number)
	if err != nil {
		return nil, fmt.Errorf("get PR #%d head ref: %w", number, err)
	}
	if pr.Head.Ref == "" {
		return nil, fmt.Errorf("github returned empty head ref for PR #%d", number)
	}
	return &PRHeadInfo{
		Ref:      pr.Head.Ref,
		OID:      pr.Head.SHA,
		RepoSlug: pr.Head.Repo.FullName,
	}, nil
}

// IssueTitle returns the title of a GitHub issue.
func IssueTitle(owner, repo string, number int) (string, error) {
	c := defaultClient()
	endpoint := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number)
	var issue struct {
		Title string `json:"title"`
	}
	if err := c.getJSON(endpoint, &issue); err != nil {
		return "", fmt.Errorf("get issue #%d title: %w", number, err)
	}
	title := strings.TrimSpace(issue.Title)
	if title == "" {
		return "", fmt.Errorf("github returned empty title for issue #%d", number)
	}
	return title, nil
}

// IssueBranchName creates a branch name from an issue number and title.
func IssueBranchName(number int, title string) string {
	slug := slugify(title)
	return fmt.Sprintf("%d-%s", number, slug)
}

// SanitizeBranch converts a branch name into a filesystem-safe path component.
func SanitizeBranch(branch string) string {
	s := branch
	s = strings.ReplaceAll(s, "/", "-")
	re := regexp.MustCompile(`[^a-zA-Z0-9\-_.]`)
	s = re.ReplaceAllString(s, "-")
	re = regexp.MustCompile(`-{2,}`)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
		s = strings.TrimRight(s, "-")
	}
	return s
}

func slugify(s string) string {
	s = strings.ToLower(s)
	re := regexp.MustCompile(`[^a-z0-9]+`)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
		s = strings.TrimRight(s, "-")
	}
	return s
}

// RepoCloneURL returns the HTTPS clone URL for a GitHub repo.
func RepoCloneURL(owner, repo string) string {
	return fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
}

// RepoDefaultBranch returns the default branch of a GitHub repo.
func RepoDefaultBranch(owner, repo string) (string, error) {
	c := defaultClient()
	endpoint := fmt.Sprintf("/repos/%s/%s", owner, repo)
	var rep struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.getJSON(endpoint, &rep); err != nil {
		return "", fmt.Errorf("get default branch for %s/%s: %w", owner, repo, err)
	}
	branch := strings.TrimSpace(rep.DefaultBranch)
	if branch == "" {
		return "", fmt.Errorf("empty default_branch from GitHub API for %s/%s", owner, repo)
	}
	return branch, nil
}

// PRMergeCommit holds the OID of the commit that landed the PR on the target branch.
type PRMergeCommit struct {
	OID string `json:"oid"`
}

// PRInfo contains information about a pull request.
type PRInfo struct {
	Number      int            `json:"number"`
	State       string         `json:"state"`
	Branch      string         `json:"headRefName"`
	BaseRefName string         `json:"baseRefName"`
	HeadSHA     string         `json:"headRefOid"`
	MergeCommit *PRMergeCommit `json:"mergeCommit"`
	MergedAt    *time.Time     `json:"mergedAt"`
	ClosedAt    *time.Time     `json:"closedAt"`
	URL         string         `json:"url"`
	Title       string         `json:"title"`
	Author      string         `json:"author"`
	// ReviewDecision is APPROVED, CHANGES_REQUESTED, or "" (see models.PRRef).
	// Populated only by ListPRsForBranchEnriched.
	ReviewDecision string `json:"reviewDecision"`
	// IsDraft reports whether the PR is a draft. Set on the base list path.
	IsDraft bool `json:"isDraft"`
	// Checks is the CI rollup for HeadSHA. Populated only by ListPRsForBranchEnriched.
	Checks models.CIStatus `json:"checks"`
}

// IsMerged reports whether the PR was merged.
func (p *PRInfo) IsMerged() bool {
	return strings.EqualFold(p.State, "merged") || p.MergedAt != nil
}

// IsClosed reports whether the PR was closed without merging.
func (p *PRInfo) IsClosed() bool {
	return strings.EqualFold(p.State, "closed") && !p.IsMerged()
}

// Client is the interface for GitHub operations.
type Client interface {
	GetPRStatus(repoPath, branch string) (*PRInfo, error)
	ListPRsForBranch(repoPath, branch string) ([]PRInfo, error)
	ClosePR(repoPath string, prNumber int) error
	DeleteRemoteBranch(repoPath, branch string) error
	Available() bool
	// GetPRBody fetches the current markdown body of a pull request.
	GetPRBody(repoPath string, prNumber int) (string, error)
	// UpdatePRBody overwrites the PR's body with the given markdown.
	UpdatePRBody(repoPath string, prNumber int, body string) error
	// UpdatePRBase retargets the PR's base branch (e.g. when a parent has merged).
	UpdatePRBase(repoPath string, prNumber int, baseBranch string) error
}

// CLIClient is the canonical GitHub Client implementation. The historical
// name is preserved so callers don't need to change; the implementation now
// talks to https://api.github.com directly rather than shelling out to gh.
type CLIClient struct {
	baseURL string
	http    *http.Client
	tokens  *tokenSource
	tr      *transport

	// slugResolver lets tests inject a fake repo-path -> "owner/repo" lookup.
	// In production we shell out to `jj git remote list -R <path>`.
	slugResolver func(repoPath string) (string, error)
}

// ClientOption configures a CLIClient at construction time.
type ClientOption func(*CLIClient)

// WithBaseURL overrides the API base URL. Useful in tests and for GHE.
func WithBaseURL(u string) ClientOption {
	return func(c *CLIClient) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient injects a custom *http.Client (e.g. with a test transport).
func WithHTTPClient(h *http.Client) ClientOption {
	return func(c *CLIClient) { c.http = h }
}

// WithSlugResolver lets callers/tests inject a custom repo-path -> slug
// resolver. The default uses `jj git remote list` to derive the slug.
func WithSlugResolver(fn func(repoPath string) (string, error)) ClientOption {
	return func(c *CLIClient) { c.slugResolver = fn }
}

// WithToken sets a fixed API token, bypassing GITHUB_TOKEN/gh resolution.
// Primarily for tests.
func WithToken(tok string) ClientOption {
	return func(c *CLIClient) {
		c.tokens = &tokenSource{}
		c.tokens.SetToken(tok)
	}
}

const (
	defaultBaseURL  = "https://api.github.com"
	defaultCacheTTL = 60 * time.Second
)

// NewClient creates a new GitHub API client.
func NewClient(opts ...ClientOption) *CLIClient {
	c := &CLIClient{
		baseURL: defaultBaseURL,
		tokens:  &tokenSource{},
	}
	c.slugResolver = c.defaultSlugResolver
	for _, opt := range opts {
		opt(c)
	}
	if c.http == nil {
		c.tr = newTransport(http.DefaultTransport, c.tokens, defaultCacheTTL)
		c.http = &http.Client{
			Timeout:   30 * time.Second,
			Transport: c.tr,
		}
	} else if c.tr == nil {
		// If a caller injected a *http.Client, leave its transport alone;
		// it's their responsibility to wire auth/cache as needed.
		c.tr = newTransport(http.DefaultTransport, c.tokens, defaultCacheTTL)
	}
	return c
}

// defaultClient is used by package-level functions (PRBranch, IssueTitle,
// RepoDefaultBranch). It memoizes a single client across calls so that
// caching/auth state is shared.
var defaultClientInstance *CLIClient

func defaultClient() *CLIClient {
	if defaultClientInstance == nil {
		defaultClientInstance = NewClient()
	}
	return defaultClientInstance
}

// Available reports whether the client can make authenticated requests.
// We retain the historical name so callers don't change. The semantics are
// the same as before: returns false if we can't reach GitHub at all.
//
// Implementation: returns true if a token can be resolved, or if gh is on
// PATH (so a token lookup would succeed). We avoid making an HTTP call on
// the hot path.
func (c *CLIClient) Available() bool {
	if tok, err := c.tokens.Token(); err == nil && tok != "" {
		return true
	}
	return ghAvailable()
}

// GetPRByNumber fetches PR details by number, resolving the repo slug
// from repoPath's "origin" remote. Used by `wgo pr --open N` to translate
// a PR number into a browser URL without shelling out to `gh`.
func (c *CLIClient) GetPRByNumber(repoPath string, number int) (*PRInfo, error) {
	slug, err := c.resolveSlug(repoPath)
	if err != nil {
		return nil, err
	}
	pr, err := c.fetchPR(slug, number)
	if err != nil {
		return nil, err
	}
	return pr.toPRInfo(), nil
}

// GetPRStatus fetches PR status for a branch. Returns (nil, nil) if no PR
// exists, mirroring the previous behavior. Authentication failures and
// transport errors are also collapsed to (nil, nil) for graceful degradation
// in the dashboard codepaths.
func (c *CLIClient) GetPRStatus(repoPath, branch string) (*PRInfo, error) {
	if !c.Available() {
		return nil, nil
	}
	slug, err := c.resolveSlug(repoPath)
	if err != nil || slug == "" {
		return nil, nil
	}
	pr, err := c.firstOpenPRForBranch(slug, branch)
	if err != nil || pr == nil {
		return nil, nil
	}
	return pr.toPRInfo(), nil
}

// firstOpenPRForBranch returns the first open PR with head == branch, or
// nil if none exists. The slug is "owner/repo".
func (c *CLIClient) firstOpenPRForBranch(slug, branch string) (*apiPullRequest, error) {
	owner, _ := splitOwnerRepo(slug)
	endpoint := fmt.Sprintf("/repos/%s/pulls?head=%s:%s&state=open&per_page=1",
		slug, owner, url.QueryEscape(branch))
	var list []apiPullRequest
	if err := c.getJSON(endpoint, &list); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	return &list[0], nil
}

// ListPRsForBranch returns all PRs (any state) whose head branch matches.
func (c *CLIClient) ListPRsForBranch(repoPath, branch string) ([]PRInfo, error) {
	if !c.Available() {
		return nil, nil
	}
	slug, err := c.resolveSlug(repoPath)
	if err != nil || slug == "" {
		return nil, nil
	}
	owner, _ := splitOwnerRepo(slug)
	endpoint := fmt.Sprintf("/repos/%s/pulls?head=%s:%s&state=all&per_page=5",
		slug, owner, url.QueryEscape(branch))
	var list []apiPullRequest
	if err := c.getJSON(endpoint, &list); err != nil {
		return nil, nil
	}
	out := make([]PRInfo, 0, len(list))
	for i := range list {
		out = append(out, *list[i].toPRInfo())
	}
	return out, nil
}

// ListPRsForBranchEnriched is ListPRsForBranch plus the per-PR review decision
// and CI rollup. It issues extra API calls (reviews + checks per PR), so it is
// used only by the PR cache fetcher off the statusline hot path; the enriched
// fields then round-trip through the cache. Enrichment degrades gracefully:
// any per-PR fetch error leaves that field empty rather than failing the call.
func (c *CLIClient) ListPRsForBranchEnriched(repoPath, branch string) ([]PRInfo, error) {
	prs, err := c.ListPRsForBranch(repoPath, branch)
	if err != nil || len(prs) == 0 {
		return prs, err
	}
	slug, err := c.resolveSlug(repoPath)
	if err != nil || slug == "" {
		return prs, nil
	}
	var wg sync.WaitGroup
	for i := range prs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pr := &prs[i]
			pr.ReviewDecision = c.fetchReviewDecision(slug, pr.Number)
			checks, failingURL := c.fetchCIStatus(slug, pr.HeadSHA)
			checks.URL = ciDeepLink(pr.URL, pr.Number, pr.BaseRefName, checks.State, failingURL)
			pr.Checks = checks
		}(i)
	}
	wg.Wait()
	return prs, nil
}

// ClosePR closes a pull request.
func (c *CLIClient) ClosePR(repoPath string, prNumber int) error {
	slug, err := c.resolveSlug(repoPath)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("/repos/%s/pulls/%d", slug, prNumber)
	return c.patchJSON(endpoint, map[string]string{"state": "closed"}, nil)
}

// DeleteRemoteBranch deletes a remote branch via the GitHub Git Data API.
func (c *CLIClient) DeleteRemoteBranch(repoPath, branch string) error {
	slug, err := c.resolveSlug(repoPath)
	if err != nil {
		return err
	}
	// GitHub requires the ref path to be url-escaped per segment; branch
	// names with slashes need percent-escaping rather than path-joining.
	endpoint := fmt.Sprintf("/repos/%s/git/refs/heads/%s", slug, url.PathEscape(branch))
	resp, err := c.doRaw(http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusUnprocessableEntity, http.StatusNotFound:
		// 422 with "Reference does not exist" — already deleted; treat as success.
		body, _ := io.ReadAll(resp.Body)
		if strings.Contains(string(body), "Reference does not exist") || resp.StatusCode == http.StatusNotFound {
			return nil
		}
		return apiErrorFromResp(http.MethodDelete, endpoint, resp.StatusCode, body)
	default:
		body, _ := io.ReadAll(resp.Body)
		return apiErrorFromResp(http.MethodDelete, endpoint, resp.StatusCode, body)
	}
}

// GetPRBody fetches the current markdown body of a pull request.
func (c *CLIClient) GetPRBody(repoPath string, prNumber int) (string, error) {
	slug, err := c.resolveSlug(repoPath)
	if err != nil {
		return "", err
	}
	pr, err := c.fetchPR(slug, prNumber)
	if err != nil {
		return "", fmt.Errorf("get PR #%d body: %w", prNumber, err)
	}
	return pr.Body, nil
}

// UpdatePRBody overwrites the PR's body with the given markdown.
func (c *CLIClient) UpdatePRBody(repoPath string, prNumber int, body string) error {
	slug, err := c.resolveSlug(repoPath)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("/repos/%s/pulls/%d", slug, prNumber)
	return c.patchJSON(endpoint, map[string]string{"body": body}, nil)
}

// UpdatePRBase retargets the PR's base branch.
func (c *CLIClient) UpdatePRBase(repoPath string, prNumber int, baseBranch string) error {
	if baseBranch == "" {
		return fmt.Errorf("base branch is required")
	}
	slug, err := c.resolveSlug(repoPath)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("/repos/%s/pulls/%d", slug, prNumber)
	return c.patchJSON(endpoint, map[string]string{"base": baseBranch}, nil)
}

// fetchPR loads a single PR by number.
func (c *CLIClient) fetchPR(slug string, number int) (*apiPullRequest, error) {
	endpoint := fmt.Sprintf("/repos/%s/pulls/%d", slug, number)
	var pr apiPullRequest
	if err := c.getJSON(endpoint, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// resolveSlug derives "owner/repo" from a workspace path. The github package
// stays decoupled from internal/jj by shelling out to `jj git remote list`
// directly; tests can override via WithSlugResolver.
func (c *CLIClient) resolveSlug(repoPath string) (string, error) {
	if c.slugResolver != nil {
		return c.slugResolver(repoPath)
	}
	return c.defaultSlugResolver(repoPath)
}

// defaultSlugResolver tries, in order:
//  1. `jj git remote list -R <path>` for pure jj repos
//  2. reading .git/config for git-only (or colocated) repos
//
// Returns the first valid GitHub origin slug.
func (c *CLIClient) defaultSlugResolver(repoPath string) (string, error) {
	// Try jj first (this is the primary backend going forward).
	if out, err := exec.Command("jj", "git", "remote", "list", "-R", repoPath).Output(); err == nil {
		if slug := parseGitHubSlugFromRemoteList(string(out)); slug != "" {
			return slug, nil
		}
	}
	// Fall back to reading .git/config so callers in legacy/colocated repos
	// continue to work without requiring jj on PATH.
	if slug := readSlugFromGitConfig(repoPath); slug != "" {
		return slug, nil
	}
	return "", fmt.Errorf("could not determine GitHub repo slug for %s", repoPath)
}

// parseGitHubSlugFromRemoteList parses "<remote> <url>" lines, preferring
// the "origin" remote, and returns owner/repo if the URL points at GitHub.
func parseGitHubSlugFromRemoteList(out string) string {
	var originURL, anyURL string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		remote, urlStr := parts[0], parts[1]
		if remote == "origin" {
			originURL = urlStr
		} else if anyURL == "" {
			anyURL = urlStr
		}
	}
	for _, u := range []string{originURL, anyURL} {
		if u == "" {
			continue
		}
		if slug := SlugFromRemoteURL(u); slug != "" {
			return slug
		}
	}
	return ""
}

// SlugFromRemoteURL parses common git remote URL shapes (https, ssh, scp-style)
// and returns owner/repo if the host is github.com. Returns "" on no match.
func SlugFromRemoteURL(remote string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	// scp-style: git@github.com:owner/repo.git
	if strings.HasPrefix(remote, "git@") {
		rest := strings.TrimPrefix(remote, "git@")
		idx := strings.Index(rest, ":")
		if idx < 0 {
			return ""
		}
		host := rest[:idx]
		path := strings.TrimPrefix(rest[idx+1:], "/")
		path = strings.TrimSuffix(path, ".git")
		if !strings.EqualFold(host, "github.com") {
			return ""
		}
		return path
	}
	// URL form
	u, err := url.Parse(remote)
	if err != nil {
		return ""
	}
	if !strings.EqualFold(u.Host, "github.com") && !strings.EqualFold(u.Host, "www.github.com") {
		return ""
	}
	path := strings.Trim(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// readSlugFromGitConfig parses .git/config for a github.com origin URL.
// Returns "" on any failure or if no GitHub remote is found.
func readSlugFromGitConfig(repoPath string) string {
	candidates := []string{
		filepath.Join(repoPath, ".git", "config"),
		filepath.Join(repoPath, ".git"), // worktree case (.git is a file)
	}
	for _, p := range candidates {
		data, err := readGitConfig(p)
		if err != nil || data == "" {
			continue
		}
		if slug := slugFromGitConfigContent(data); slug != "" {
			return slug
		}
	}
	return ""
}

// slugFromGitConfigContent scans an INI-style .git/config content for
// [remote "origin"] (and any remote) url= and returns a GitHub slug.
func slugFromGitConfigContent(content string) string {
	var current string
	type remote struct{ url string }
	remotes := map[string]*remote{}
	scanner := strings.Split(content, "\n")
	for _, ln := range scanner {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "[remote ") {
			// e.g. [remote "origin"]
			start := strings.Index(ln, `"`)
			end := strings.LastIndex(ln, `"`)
			if start >= 0 && end > start {
				current = ln[start+1 : end]
				remotes[current] = &remote{}
			} else {
				current = ""
			}
			continue
		}
		if strings.HasPrefix(ln, "[") {
			current = ""
			continue
		}
		if current == "" {
			continue
		}
		if !strings.HasPrefix(ln, "url") {
			continue
		}
		_, value, ok := strings.Cut(ln, "=")
		if !ok {
			continue
		}
		remotes[current].url = strings.TrimSpace(value)
	}
	for _, name := range []string{"origin"} {
		if r, ok := remotes[name]; ok {
			if slug := SlugFromRemoteURL(r.url); slug != "" {
				return slug
			}
		}
	}
	for _, r := range remotes {
		if slug := SlugFromRemoteURL(r.url); slug != "" {
			return slug
		}
	}
	return ""
}

// readGitConfig handles both the directory case (.git/config exists) and the
// worktree case (.git is a file with "gitdir: <path>").
func readGitConfig(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		data, err := os.ReadFile(filepath.Join(path, "config"))
		return string(data), err
	}
	if strings.HasSuffix(path, "config") {
		data, err := os.ReadFile(path)
		return string(data), err
	}
	// .git file: "gitdir: <path>"
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(ln, "gitdir:") {
			gitdir := strings.TrimSpace(strings.TrimPrefix(ln, "gitdir:"))
			// gitdir may be a path to a worktree's .git directory (commondir's
			// config is elsewhere). For our purposes, try the same directory
			// first and bubble up to commondir.
			candidates := []string{
				filepath.Join(gitdir, "config"),
				filepath.Join(gitdir, "..", "..", "config"),
			}
			for _, c := range candidates {
				if d, err := os.ReadFile(c); err == nil {
					return string(d), nil
				}
			}
		}
	}
	return "", nil
}

// --- HTTP helpers ---

// getJSON performs a GET and unmarshals the JSON body into v.
func (c *CLIClient) getJSON(endpoint string, v any) error {
	resp, err := c.doRaw(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("github api: reading body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return apiErrorFromResp(http.MethodGet, endpoint, resp.StatusCode, body)
	}
	if v == nil {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("github api: parse response from %s: %w", endpoint, err)
	}
	return nil
}

// patchJSON performs a PATCH with body and (if v != nil) unmarshals the response.
func (c *CLIClient) patchJSON(endpoint string, body any, v any) error {
	return c.bodyJSON(http.MethodPatch, endpoint, body, v)
}

// postJSON performs a POST with body and (if v != nil) unmarshals the response.
func (c *CLIClient) bodyJSON(method, endpoint string, body, v any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("github api: marshal request body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	resp, err := c.doRaw(method, endpoint, rdr)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("github api: reading body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return apiErrorFromResp(method, endpoint, resp.StatusCode, respBody)
	}
	if v == nil {
		return nil
	}
	if len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, v); err != nil {
		return fmt.Errorf("github api: parse response from %s: %w", endpoint, err)
	}
	return nil
}

// doRaw performs the HTTP request and returns the raw response. The caller is
// responsible for closing the body.
func (c *CLIClient) doRaw(method, endpoint string, body io.Reader) (*http.Response, error) {
	u, err := c.absURL(endpoint)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, fmt.Errorf("github api: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

func (c *CLIClient) absURL(endpoint string) (string, error) {
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return endpoint, nil
	}
	if !strings.HasPrefix(endpoint, "/") {
		return "", fmt.Errorf("github api: endpoint must start with '/': %q", endpoint)
	}
	return c.baseURL + endpoint, nil
}

// apiErrorFromResp builds a typed APIError, including a rate-limit decode
// when GitHub returns 403 with X-RateLimit-Remaining=0 in the body's message.
func apiErrorFromResp(method, endpoint string, status int, body []byte) error {
	if status == http.StatusForbidden || status == http.StatusTooManyRequests {
		if strings.Contains(string(body), "API rate limit exceeded") ||
			strings.Contains(string(body), "rate limit") {
			return &RateLimitError{Message: string(body)}
		}
	}
	return &APIError{
		StatusCode: status,
		URL:        endpoint,
		Body:       string(body),
		Method:     method,
	}
}

// --- base64 contents helper (used by GetSpecFrontmatterForBranch) ---

func decodeBase64Content(s string) ([]byte, error) {
	// GitHub returns base64 with embedded newlines.
	clean := strings.ReplaceAll(s, "\n", "")
	clean = strings.TrimSpace(clean)
	return base64.StdEncoding.DecodeString(clean)
}
