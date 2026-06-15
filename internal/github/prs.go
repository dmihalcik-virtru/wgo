package github

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ExtendedPRInfo contains enriched pull request information for the PR dashboard.
type ExtendedPRInfo struct {
	Number         int
	Title          string
	State          string // "open", "closed", "merged"
	IsDraft        bool
	URL            string
	UpdatedAt      time.Time
	RepoOwner      string
	RepoName       string
	CheckPassing   int       // number of passing checks
	CheckTotal     int       // total checks; -1 = not yet fetched
	MyLastActivity time.Time // zero if I have no activity on this PR
	HasNewActivity bool      // true when UpdatedAt > MyLastActivity (and MyLastActivity non-zero)
}

// RepoSlug returns "owner/repo".
func (p *ExtendedPRInfo) RepoSlug() string {
	return p.RepoOwner + "/" + p.RepoName
}

// RepoURL returns the GitHub URL for the PR's repository.
func (p *ExtendedPRInfo) RepoURL() string {
	if p.RepoOwner == "" || p.RepoName == "" {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/%s", p.RepoOwner, p.RepoName)
}

// StateLabel returns a short display label for the PR state.
func (p *ExtendedPRInfo) StateLabel() string {
	if p.IsDraft {
		return "DRAFT"
	}
	return strings.ToUpper(p.State)
}

func splitOwnerRepo(slug string) (owner, repo string) {
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return slug, ""
}

// splitOwnerRepoFromAPIURL parses GitHub's repository_url shape
// ("https://api.github.com/repos/owner/repo") and returns (owner, repo).
func splitOwnerRepoFromAPIURL(repoURL string) (owner, repo string) {
	const marker = "/repos/"
	idx := strings.Index(repoURL, marker)
	if idx < 0 {
		return "", ""
	}
	return splitOwnerRepo(repoURL[idx+len(marker):])
}

// CurrentUser returns the authenticated GitHub username.
func (c *CLIClient) CurrentUser() (string, error) {
	var u apiUserInfo
	if err := c.getJSON("/user", &u); err != nil {
		return "", fmt.Errorf("get authenticated user: %w", err)
	}
	if u.Login == "" {
		return "", fmt.Errorf("github /user returned empty login")
	}
	return u.Login, nil
}

// searchPRs runs the GitHub Search API for issues/PRs and returns the items
// as ExtendedPRInfo. q is the search query body (without the type filter).
func (c *CLIClient) searchPRs(q string) ([]ExtendedPRInfo, error) {
	full := q + " is:pr"
	endpoint := fmt.Sprintf("/search/issues?q=%s&per_page=50", url.QueryEscape(full))
	var resp apiSearchResults
	if err := c.getJSON(endpoint, &resp); err != nil {
		return nil, err
	}
	out := make([]ExtendedPRInfo, 0, len(resp.Items))
	for i := range resp.Items {
		out = append(out, resp.Items[i].toExtendedPRInfo())
	}
	return out, nil
}

// ListMyOpenPRs returns all open (including draft) PRs authored by the current user.
func (c *CLIClient) ListMyOpenPRs() ([]ExtendedPRInfo, error) {
	if !c.Available() {
		return nil, nil
	}
	return c.searchPRs("author:@me state:open")
}

// ListInvolvedPRs returns open PRs where the user is involved (assigned,
// review-requested, or has commented) but is NOT the author.
func (c *CLIClient) ListInvolvedPRs(excludeAuthor string) ([]ExtendedPRInfo, error) {
	if !c.Available() {
		return nil, nil
	}
	items, err := c.searchPRs("involves:@me state:open")
	if err != nil {
		return nil, err
	}
	if excludeAuthor == "" {
		return items, nil
	}
	out := make([]ExtendedPRInfo, 0, len(items))
	for _, item := range items {
		// search-api items have user.Login on the item itself, but we already
		// stripped that during conversion. Re-query is not worth it; instead
		// the call sites already pass excludeAuthor matched against the search
		// item. We do a best-effort recheck via a second search if needed.
		if item.RepoOwner == excludeAuthor {
			continue
		}
		out = append(out, item)
	}
	// The pre-existing semantics rely on filtering by author; do an explicit
	// second pass using the search API to subtract authored PRs.
	mine, _ := c.searchPRs(fmt.Sprintf("author:%s state:open", excludeAuthor))
	if len(mine) == 0 {
		return out, nil
	}
	authored := map[string]bool{}
	for _, pr := range mine {
		authored[fmt.Sprintf("%s/%s#%d", pr.RepoOwner, pr.RepoName, pr.Number)] = true
	}
	filtered := out[:0]
	for _, pr := range out {
		if authored[fmt.Sprintf("%s/%s#%d", pr.RepoOwner, pr.RepoName, pr.Number)] {
			continue
		}
		filtered = append(filtered, pr)
	}
	return filtered, nil
}

// FetchMyLastActivityOnPR returns the time of the user's most recent comment
// or review on the specified PR. Returns zero time if the user has no
// recorded activity.
//
// Two calls are made: /repos/{slug}/issues/{n}/comments and
// /repos/{slug}/pulls/{n}/reviews. Only entries authored by myLogin are
// considered.
func (c *CLIClient) FetchMyLastActivityOnPR(owner, repo string, number int, myLogin string) (time.Time, error) {
	slug := owner + "/" + repo
	var latest time.Time

	var comments []apiIssueComment
	commentsEndpoint := fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100", slug, number)
	if err := c.getJSON(commentsEndpoint, &comments); err != nil {
		return time.Time{}, fmt.Errorf("fetch comments for #%d: %w", number, err)
	}
	for _, c := range comments {
		if !strings.EqualFold(c.User.Login, myLogin) {
			continue
		}
		if c.CreatedAt.After(latest) {
			latest = c.CreatedAt
		}
	}

	var reviews []apiReview
	reviewsEndpoint := fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", slug, number)
	if err := c.getJSON(reviewsEndpoint, &reviews); err != nil {
		return latest, fmt.Errorf("fetch reviews for #%d: %w", number, err)
	}
	for _, r := range reviews {
		if !strings.EqualFold(r.User.Login, myLogin) {
			continue
		}
		if r.SubmittedAt.After(latest) {
			latest = r.SubmittedAt
		}
	}
	return latest, nil
}

// FetchPRChecks returns (passing, total) check counts for a PR.
// Returns (-1, -1) if checks are unavailable or the PR has none.
//
// We combine two endpoints because GitHub split checks across Check Runs
// (GitHub Actions, etc.) and Commit Statuses (older integrations).
func (c *CLIClient) FetchPRChecks(owner, repo string, number int) (passing, total int) {
	slug := owner + "/" + repo
	pr, err := c.fetchPR(slug, number)
	if err != nil || pr == nil || pr.Head.SHA == "" {
		return -1, -1
	}

	checkRunsEndpoint := fmt.Sprintf("/repos/%s/commits/%s/check-runs", slug, pr.Head.SHA)
	var runs apiCheckRunsResponse
	if err := c.getJSON(checkRunsEndpoint, &runs); err != nil {
		runs = apiCheckRunsResponse{}
	}

	combinedEndpoint := fmt.Sprintf("/repos/%s/commits/%s/status", slug, pr.Head.SHA)
	var combined apiCombinedStatus
	if err := c.getJSON(combinedEndpoint, &combined); err != nil {
		combined = apiCombinedStatus{}
	}

	total = len(runs.CheckRuns) + len(combined.Statuses)
	if total == 0 {
		return -1, -1
	}
	for _, r := range runs.CheckRuns {
		if strings.EqualFold(r.Conclusion, "success") {
			passing++
		}
	}
	for _, s := range combined.Statuses {
		if strings.EqualFold(s.State, "success") {
			passing++
		}
	}
	return passing, total
}

// EnrichWithActivity fetches last-activity timestamps for all PRs in parallel
// and mutates the slice in place.
func (c *CLIClient) EnrichWithActivity(prs []ExtendedPRInfo, myLogin string) {
	var wg sync.WaitGroup
	for i := range prs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pr := &prs[i]
			if t, err := c.FetchMyLastActivityOnPR(pr.RepoOwner, pr.RepoName, pr.Number, myLogin); err == nil {
				pr.MyLastActivity = t
				if !t.IsZero() && pr.UpdatedAt.After(t) {
					pr.HasNewActivity = true
				}
			}
		}(i)
	}
	wg.Wait()
}

// CommentedPR represents a PR/issue that the user commented on.
type CommentedPR struct {
	Number    int
	Title     string
	RepoSlug  string
	URL       string
	UpdatedAt time.Time
}

// ReviewSubmission represents a PR review the user submitted.
type ReviewSubmission struct {
	PRNumber int
	PRTitle  string
	RepoSlug string
	PRURL    string
	State    string // APPROVED, CHANGES_REQUESTED, COMMENTED
	Time     time.Time
}

// ListMyCommentedPRs returns PRs/issues the user commented on since the given time.
//
// REST equivalent of the old GraphQL search: q="commenter:{user} updated:>={date}".
func (c *CLIClient) ListMyCommentedPRs(myLogin string, since time.Time) ([]CommentedPR, error) {
	if !c.Available() {
		return nil, nil
	}
	dateStr := since.Format("2006-01-02")
	q := fmt.Sprintf("commenter:%s updated:>=%s", myLogin, dateStr)
	endpoint := fmt.Sprintf("/search/issues?q=%s&per_page=30", url.QueryEscape(q))
	var resp apiSearchResults
	if err := c.getJSON(endpoint, &resp); err != nil {
		return nil, fmt.Errorf("search commented: %w", err)
	}
	out := make([]CommentedPR, 0, len(resp.Items))
	for _, item := range resp.Items {
		owner, repo := splitOwnerRepoFromAPIURL(item.RepositoryURL)
		slug := owner + "/" + repo
		if slug == "/" {
			slug = ""
		}
		out = append(out, CommentedPR{
			Number:    item.Number,
			Title:     item.Title,
			RepoSlug:  slug,
			URL:       item.HTMLURL,
			UpdatedAt: item.UpdatedAt,
		})
	}
	return out, nil
}

// ListMyReviewsToday returns PR reviews the user submitted since the given time.
//
// REST equivalent: for each authored review event we need to query the user's
// reviews across PRs. The cheap approximation is to use the search API for
// PRs the user reviewed since `since`, then list reviews on each.
func (c *CLIClient) ListMyReviewsToday(myLogin string, since time.Time) ([]ReviewSubmission, error) {
	if !c.Available() {
		return nil, nil
	}
	dateStr := since.Format("2006-01-02")
	q := fmt.Sprintf("reviewed-by:%s updated:>=%s is:pr", myLogin, dateStr)
	endpoint := fmt.Sprintf("/search/issues?q=%s&per_page=30", url.QueryEscape(q))
	var resp apiSearchResults
	if err := c.getJSON(endpoint, &resp); err != nil {
		return nil, fmt.Errorf("search reviewed: %w", err)
	}
	var out []ReviewSubmission
	for _, item := range resp.Items {
		owner, repo := splitOwnerRepoFromAPIURL(item.RepositoryURL)
		slug := owner + "/" + repo
		if slug == "/" {
			slug = ""
		}
		reviews, err := c.listReviewsForPR(slug, item.Number)
		if err != nil {
			continue
		}
		for _, r := range reviews {
			if !strings.EqualFold(r.User.Login, myLogin) {
				continue
			}
			if r.SubmittedAt.Before(since) {
				continue
			}
			out = append(out, ReviewSubmission{
				PRNumber: item.Number,
				PRTitle:  item.Title,
				RepoSlug: slug,
				PRURL:    item.HTMLURL,
				State:    r.State,
				Time:     r.SubmittedAt,
			})
		}
	}
	return out, nil
}

func (c *CLIClient) listReviewsForPR(slug string, number int) ([]apiReview, error) {
	endpoint := fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", slug, number)
	var out []apiReview
	if err := c.getJSON(endpoint, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListPRsByAuthor returns open PRs authored by the given GitHub handle.
func (c *CLIClient) ListPRsByAuthor(author string) ([]ExtendedPRInfo, error) {
	if !c.Available() {
		return nil, nil
	}
	return c.searchPRs(fmt.Sprintf("author:%s state:open", author))
}

// ListPRsReviewRequestedFor returns open PRs where the given GitHub handle has
// a pending review request.
func (c *CLIClient) ListPRsReviewRequestedFor(reviewer string) ([]ExtendedPRInfo, error) {
	if !c.Available() {
		return nil, nil
	}
	return c.searchPRs(fmt.Sprintf("review-requested:%s state:open", reviewer))
}

// GetSpecFrontmatterForBranch fetches a raw spec file from a remote branch
// via the GitHub Contents API and returns the YAML frontmatter block
// (between --- delimiters). Returns ("", nil) when the file does not exist
// on that branch.
func (c *CLIClient) GetSpecFrontmatterForBranch(slug, branch, specPath string) (string, error) {
	if !c.Available() {
		return "", nil
	}
	endpoint := fmt.Sprintf("/repos/%s/contents/%s?ref=%s",
		slug, specPath, url.QueryEscape(branch))
	var contents apiContents
	err := c.getJSON(endpoint, &contents)
	if err != nil {
		if IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("github contents %s: %w", endpoint, err)
	}
	if !strings.EqualFold(contents.Encoding, "base64") {
		return contents.Content, nil
	}
	decoded, err := decodeBase64Content(contents.Content)
	if err != nil {
		return "", nil
	}
	return string(decoded), nil
}

// EnrichWithChecks fetches CI check status for all PRs in parallel.
func (c *CLIClient) EnrichWithChecks(prs []ExtendedPRInfo) {
	var wg sync.WaitGroup
	for i := range prs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pr := &prs[i]
			passing, total := c.FetchPRChecks(pr.RepoOwner, pr.RepoName, pr.Number)
			pr.CheckPassing = passing
			pr.CheckTotal = total
		}(i)
	}
	wg.Wait()
}
