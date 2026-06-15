package github

import "time"

// apiPullRequest mirrors the subset of fields we read from
// GET /repos/{owner}/{repo}/pulls/{number} (and the list endpoint).
// See https://docs.github.com/en/rest/pulls/pulls.
type apiPullRequest struct {
	Number    int        `json:"number"`
	State     string     `json:"state"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	HTMLURL   string     `json:"html_url"`
	URL       string     `json:"url"`
	Draft     bool       `json:"draft"`
	MergedAt  *time.Time `json:"merged_at"`
	ClosedAt  *time.Time `json:"closed_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	MergeSHA  *string    `json:"merge_commit_sha"`
	Head      apiPRRef   `json:"head"`
	Base      apiPRRef   `json:"base"`
	User      apiUser    `json:"user"`
	// MergedBy is non-nil when state == "closed" and merged_at is set; we
	// don't currently need it but the field is documented in the API.
}

type apiPRRef struct {
	Label string  `json:"label"` // e.g. "owner:branch"
	Ref   string  `json:"ref"`   // bare branch name
	SHA   string  `json:"sha"`
	Repo  apiRepo `json:"repo"`
}

type apiRepo struct {
	FullName      string `json:"full_name"`
	NameWithOwner string `json:"-"` // alias for FullName, populated for compatibility
	DefaultBranch string `json:"default_branch"`
}

type apiUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// toPRInfo converts an API pull request payload into the existing PRInfo
// shape used by callers.
func (p *apiPullRequest) toPRInfo() *PRInfo {
	info := &PRInfo{
		Number:   p.Number,
		State:    p.State,
		Branch:   p.Head.Ref,
		HeadSHA:  p.Head.SHA,
		MergedAt: p.MergedAt,
		ClosedAt: p.ClosedAt,
		URL:      p.HTMLURL,
		Title:    p.Title,
		Author:   p.User.Login,
	}
	if p.MergeSHA != nil && *p.MergeSHA != "" {
		info.MergeCommit = &PRMergeCommit{OID: *p.MergeSHA}
	}
	// GitHub's REST API reports state="closed" even when a PR was merged;
	// the gh CLI distinguishes "merged" as a distinct state. Mirror that
	// here so downstream IsMerged()/IsClosed() callers behave the same.
	if info.MergedAt != nil {
		info.State = "merged"
	}
	return info
}

// toExtendedPRInfo converts an API pull request payload into the dashboard
// ExtendedPRInfo shape.
func (p *apiPullRequest) toExtendedPRInfo() ExtendedPRInfo {
	owner, repo := splitOwnerRepo(p.Head.Repo.FullName)
	return ExtendedPRInfo{
		Number:     p.Number,
		Title:      p.Title,
		State:      p.State,
		IsDraft:    p.Draft,
		URL:        p.HTMLURL,
		UpdatedAt:  p.UpdatedAt,
		RepoOwner:  owner,
		RepoName:   repo,
		CheckTotal: -1,
	}
}

// apiSearchResults is GET /search/issues with q=is:pr ...
type apiSearchResults struct {
	TotalCount        int             `json:"total_count"`
	IncompleteResults bool            `json:"incomplete_results"`
	Items             []apiSearchItem `json:"items"`
}

type apiSearchItem struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	HTMLURL     string    `json:"html_url"`
	UpdatedAt   time.Time `json:"updated_at"`
	State       string    `json:"state"`
	Draft       bool      `json:"draft"`
	User        apiUser   `json:"user"`
	PullRequest *struct {
		HTMLURL  string     `json:"html_url"`
		MergedAt *time.Time `json:"merged_at"`
	} `json:"pull_request"`
	// repository_url is "https://api.github.com/repos/owner/repo"; we parse it
	// because the search payload doesn't include a structured repo object.
	RepositoryURL string `json:"repository_url"`
}

// toExtendedPRInfo converts a search payload into ExtendedPRInfo.
func (s *apiSearchItem) toExtendedPRInfo() ExtendedPRInfo {
	owner, repo := splitOwnerRepoFromAPIURL(s.RepositoryURL)
	state := s.State
	if s.PullRequest != nil && s.PullRequest.MergedAt != nil {
		state = "merged"
	}
	return ExtendedPRInfo{
		Number:     s.Number,
		Title:      s.Title,
		State:      state,
		IsDraft:    s.Draft,
		URL:        s.HTMLURL,
		UpdatedAt:  s.UpdatedAt,
		RepoOwner:  owner,
		RepoName:   repo,
		CheckTotal: -1,
	}
}

// apiCheckRun and apiStatus mirror the relevant fields from
// /repos/{slug}/commits/{ref}/check-runs and /commits/{ref}/status.
type apiCheckRunsResponse struct {
	TotalCount int          `json:"total_count"`
	CheckRuns  []apiCheckRun `json:"check_runs"`
}

type apiCheckRun struct {
	Status     string `json:"status"`     // queued, in_progress, completed
	Conclusion string `json:"conclusion"` // success, failure, neutral, cancelled, ...
}

type apiCombinedStatus struct {
	State    string             `json:"state"` // success, failure, pending
	Statuses []apiStatusContext `json:"statuses"`
}

type apiStatusContext struct {
	State string `json:"state"` // success, failure, pending
}

// apiIssueComment is /repos/{slug}/issues/{n}/comments item.
type apiIssueComment struct {
	User      apiUser   `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// apiReview is /repos/{slug}/pulls/{n}/reviews item.
type apiReview struct {
	User        apiUser   `json:"user"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// apiIssue is /repos/{slug}/issues/{n} (search results return similar shape).
type apiIssue struct {
	Number     int     `json:"number"`
	Title      string  `json:"title"`
	HTMLURL    string  `json:"html_url"`
	State      string  `json:"state"`
	Repository apiRepo `json:"repository"`
}

// apiUserInfo is /user.
type apiUserInfo struct {
	Login string `json:"login"`
}

// apiContents is /repos/{slug}/contents/{path}.
type apiContents struct {
	Type     string `json:"type"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}
