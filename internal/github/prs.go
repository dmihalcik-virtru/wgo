package github

import (
	"encoding/json"
	"fmt"
	"os/exec"
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
	CheckPassing   int  // number of passing checks
	CheckTotal     int  // total checks; -1 = not yet fetched
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

// searchPRItem is the JSON shape returned by `gh search prs --json ...`.
type searchPRItem struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	IsDraft   bool      `json:"isDraft"`
	URL       string    `json:"url"`
	UpdatedAt time.Time `json:"updatedAt"`
	Author    struct {
		Login string `json:"login"`
	} `json:"author"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

func (item searchPRItem) toExtended() ExtendedPRInfo {
	owner, repo := splitOwnerRepo(item.Repository.NameWithOwner)
	return ExtendedPRInfo{
		Number:     item.Number,
		Title:      item.Title,
		State:      item.State,
		IsDraft:    item.IsDraft,
		URL:        item.URL,
		UpdatedAt:  item.UpdatedAt,
		RepoOwner:  owner,
		RepoName:   repo,
		CheckTotal: -1,
	}
}

func splitOwnerRepo(slug string) (owner, repo string) {
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return slug, ""
}

// searchPRsRaw runs `gh search prs` with the given extra args and returns raw items.
func searchPRsRaw(args ...string) ([]searchPRItem, error) {
	cmdArgs := []string{
		"search", "prs",
		"--json", "number,title,state,isDraft,url,updatedAt,author,repository",
		"--limit", "50",
	}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command("gh", cmdArgs...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh search prs: %s", strings.TrimSpace(stderr.String()))
	}
	var items []searchPRItem
	if err := json.Unmarshal([]byte(stdout.String()), &items); err != nil {
		return nil, fmt.Errorf("parse search results: %w", err)
	}
	return items, nil
}

// CurrentUser returns the authenticated GitHub username.
func (c *CLIClient) CurrentUser() (string, error) {
	out, err := exec.Command("gh", "api", "user", "--jq", ".login").Output()
	if err != nil {
		return "", fmt.Errorf("gh api user: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ListMyOpenPRs returns all open (including draft) PRs authored by the current user.
func (c *CLIClient) ListMyOpenPRs() ([]ExtendedPRInfo, error) {
	if !c.Available() {
		return nil, nil
	}
	items, err := searchPRsRaw("--author", "@me", "--state", "open")
	if err != nil {
		return nil, err
	}
	result := make([]ExtendedPRInfo, len(items))
	for i, item := range items {
		result[i] = item.toExtended()
	}
	return result, nil
}

// ListInvolvedPRs returns open PRs where the user is involved (assigned, review-requested,
// or has commented) but is NOT the author. Pass the current user's login as excludeAuthor.
func (c *CLIClient) ListInvolvedPRs(excludeAuthor string) ([]ExtendedPRInfo, error) {
	if !c.Available() {
		return nil, nil
	}
	items, err := searchPRsRaw("--involves", "@me", "--state", "open")
	if err != nil {
		return nil, err
	}
	var result []ExtendedPRInfo
	for _, item := range items {
		if strings.EqualFold(item.Author.Login, excludeAuthor) {
			continue // already shown in "my PRs" section
		}
		result = append(result, item.toExtended())
	}
	return result, nil
}

// lastActivityQuery fetches the viewer's most recent comment and review timestamps on a PR.
const lastActivityQuery = `query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      comments(last: 100) {
        nodes { author { login } createdAt }
      }
      reviews(last: 100) {
        nodes { author { login } submittedAt }
      }
    }
  }
}`

type lastActivityResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				Comments struct {
					Nodes []struct {
						Author    struct{ Login string } `json:"author"`
						CreatedAt time.Time              `json:"createdAt"`
					} `json:"nodes"`
				} `json:"comments"`
				Reviews struct {
					Nodes []struct {
						Author      struct{ Login string } `json:"author"`
						SubmittedAt time.Time              `json:"submittedAt"`
					} `json:"nodes"`
				} `json:"reviews"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

// FetchMyLastActivityOnPR returns the time of the user's most recent comment or review
// on the specified PR. Returns zero time if the user has no recorded activity.
func (c *CLIClient) FetchMyLastActivityOnPR(owner, repo string, number int, myLogin string) (time.Time, error) {
	cmd := exec.Command("gh", "api", "graphql",
		"-f", "query="+lastActivityQuery,
		"-F", "owner="+owner,
		"-F", "repo="+repo,
		"-F", fmt.Sprintf("number=%d", number),
	)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return time.Time{}, fmt.Errorf("graphql: %s", strings.TrimSpace(stderr.String()))
	}

	var resp lastActivityResponse
	if err := json.Unmarshal([]byte(stdout.String()), &resp); err != nil {
		return time.Time{}, fmt.Errorf("parse graphql response: %w", err)
	}

	var latest time.Time
	pr := resp.Data.Repository.PullRequest
	for _, node := range pr.Comments.Nodes {
		if strings.EqualFold(node.Author.Login, myLogin) && node.CreatedAt.After(latest) {
			latest = node.CreatedAt
		}
	}
	for _, node := range pr.Reviews.Nodes {
		if strings.EqualFold(node.Author.Login, myLogin) && node.SubmittedAt.After(latest) {
			latest = node.SubmittedAt
		}
	}
	return latest, nil
}

// checkRollupItem represents one entry in `gh pr view --json statusCheckRollup`.
type checkRollupItem struct {
	TypeName   string `json:"__typename"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"` // StatusContext uses "state" instead of "conclusion"
}

// FetchPRChecks returns (passing, total) check counts for a PR.
// Returns (-1, -1) if checks are unavailable or the PR has none.
func (c *CLIClient) FetchPRChecks(owner, repo string, number int) (passing, total int) {
	cmd := exec.Command("gh", "pr", "view", fmt.Sprintf("%d", number),
		"--repo", owner+"/"+repo,
		"--json", "statusCheckRollup",
	)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return -1, -1
	}

	var obj struct {
		StatusCheckRollup []checkRollupItem `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &obj); err != nil || len(obj.StatusCheckRollup) == 0 {
		return -1, -1
	}

	total = len(obj.StatusCheckRollup)
	for _, check := range obj.StatusCheckRollup {
		// CheckRun uses conclusion; StatusContext uses state
		result := strings.ToUpper(check.Conclusion)
		if result == "" {
			result = strings.ToUpper(check.State)
		}
		if result == "SUCCESS" {
			passing++
		}
	}
	return passing, total
}

// EnrichWithActivity fetches last-activity timestamps and check statuses for all PRs
// in parallel and mutates the slice in place.
func (c *CLIClient) EnrichWithActivity(prs []ExtendedPRInfo, myLogin string) {
	var wg sync.WaitGroup
	for i := range prs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pr := &prs[i]

			// Fetch my last comment/review on this PR
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
func (c *CLIClient) ListMyCommentedPRs(myLogin string, since time.Time) ([]CommentedPR, error) {
	if !c.Available() {
		return nil, nil
	}

	dateStr := since.Format("2006-01-02")
	query := fmt.Sprintf(`{
  search(query: "commenter:%s updated:>=%s", type: ISSUE, first: 30) {
    nodes {
      ... on PullRequest {
        number
        title
        repository { nameWithOwner }
        url
        updatedAt
      }
      ... on Issue {
        number
        title
        repository { nameWithOwner }
        url
        updatedAt
      }
    }
  }
}`, myLogin, dateStr)

	cmd := exec.Command("gh", "api", "graphql", "-f", "query="+query)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("graphql: %s", strings.TrimSpace(stderr.String()))
	}

	var resp struct {
		Data struct {
			Search struct {
				Nodes []struct {
					Number     int       `json:"number"`
					Title      string    `json:"title"`
					Repository struct {
						NameWithOwner string `json:"nameWithOwner"`
					} `json:"repository"`
					URL       string    `json:"url"`
					UpdatedAt time.Time `json:"updatedAt"`
				} `json:"nodes"`
			} `json:"search"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &resp); err != nil {
		return nil, fmt.Errorf("parse graphql response: %w", err)
	}

	var result []CommentedPR
	for _, node := range resp.Data.Search.Nodes {
		if node.Number == 0 {
			continue
		}
		result = append(result, CommentedPR{
			Number:    node.Number,
			Title:     node.Title,
			RepoSlug:  node.Repository.NameWithOwner,
			URL:       node.URL,
			UpdatedAt: node.UpdatedAt,
		})
	}
	return result, nil
}

// ListMyReviewsToday returns PR reviews the user submitted since the given time.
func (c *CLIClient) ListMyReviewsToday(myLogin string, since time.Time) ([]ReviewSubmission, error) {
	if !c.Available() {
		return nil, nil
	}

	sinceStr := since.Format(time.RFC3339)
	query := fmt.Sprintf(`{
  viewer {
    contributionsCollection(from: "%s") {
      pullRequestReviewContributions(first: 30) {
        nodes {
          pullRequestReview {
            state
            createdAt
            pullRequest {
              number
              title
              url
              repository { nameWithOwner }
            }
          }
        }
      }
    }
  }
}`, sinceStr)

	cmd := exec.Command("gh", "api", "graphql", "-f", "query="+query)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("graphql: %s", strings.TrimSpace(stderr.String()))
	}

	var resp struct {
		Data struct {
			Viewer struct {
				ContributionsCollection struct {
					PullRequestReviewContributions struct {
						Nodes []struct {
							PullRequestReview struct {
								State     string    `json:"state"`
								CreatedAt time.Time `json:"createdAt"`
								PullRequest struct {
									Number     int    `json:"number"`
									Title      string `json:"title"`
									URL        string `json:"url"`
									Repository struct {
										NameWithOwner string `json:"nameWithOwner"`
									} `json:"repository"`
								} `json:"pullRequest"`
							} `json:"pullRequestReview"`
						} `json:"nodes"`
					} `json:"pullRequestReviewContributions"`
				} `json:"contributionsCollection"`
			} `json:"viewer"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &resp); err != nil {
		return nil, fmt.Errorf("parse graphql response: %w", err)
	}

	var result []ReviewSubmission
	for _, node := range resp.Data.Viewer.ContributionsCollection.PullRequestReviewContributions.Nodes {
		r := node.PullRequestReview
		result = append(result, ReviewSubmission{
			PRNumber: r.PullRequest.Number,
			PRTitle:  r.PullRequest.Title,
			RepoSlug: r.PullRequest.Repository.NameWithOwner,
			PRURL:    r.PullRequest.URL,
			State:    r.State,
			Time:     r.CreatedAt,
		})
	}
	return result, nil
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
