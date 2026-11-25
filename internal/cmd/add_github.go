package cmd

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/virtru/wgo/internal/config"
	gh "github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
)

type githubIssueRef struct {
	owner, repo string
	number      int
}

var githubIssueRefRe = regexp.MustCompile(`^([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)#([0-9]+)$`)

func parseGitHubIssueRef(s string) (githubIssueRef, bool, error) {
	ref := githubIssueRef{}
	number := ""
	if strings.HasPrefix(s, "https://github.com/") || strings.HasPrefix(s, "https://www.github.com/") {
		parsed, err := gh.ParseGitHubURL(s)
		if err != nil || parsed.Type != gh.URLTypeIssue {
			return ref, false, fmt.Errorf("invalid GitHub issue URL %q", s)
		}
		ref.owner, ref.repo, number = parsed.Owner, parsed.Repo, parsed.Identifier
		if !validRepoSegment(ref.owner) || !validRepoSegment(ref.repo) {
			return ref, false, fmt.Errorf("invalid GitHub issue URL %q", s)
		}
	} else if m := githubIssueRefRe.FindStringSubmatch(s); m != nil {
		ref.owner, ref.repo, number = m[1], m[2], m[3]
		if !validRepoSegment(ref.owner) || !validRepoSegment(ref.repo) {
			return ref, false, fmt.Errorf("invalid GitHub issue reference %q", s)
		}
	} else if strings.HasPrefix(s, "gh-") {
		number = strings.TrimPrefix(s, "gh-")
	} else if strings.HasPrefix(s, "#") {
		number = strings.TrimPrefix(s, "#")
	} else if strings.Contains(s, "#") {
		return ref, false, fmt.Errorf("invalid GitHub issue reference %q: expected owner/repo#number or #number", s)
	} else if len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
		number = s
	} else {
		return ref, false, nil
	}
	if number == "" || strings.ContainsFunc(number, func(r rune) bool { return r < '0' || r > '9' }) {
		return ref, false, fmt.Errorf("invalid GitHub issue reference %q: expected a positive issue number", s)
	}
	n, err := strconv.Atoi(number)
	if err != nil || n < 1 {
		return ref, false, fmt.Errorf("invalid GitHub issue reference %q: expected a positive issue number", s)
	}
	ref.number = n
	return ref, true, nil
}

// issueRepository uses the explicit reference first, then the current jj repo.
// An -r value is useful when starting work from outside any checkout.
func issueRepository(ref githubIssueRef, repos []string) (repoSpec, error) {
	if ref.owner != "" {
		return repoSpec{owner: ref.owner, repo: ref.repo}, nil
	}
	if current, err := detectCurrentJJRepo(jj.NewCLI()); err == nil {
		return parseRepoSpec(current)
	}
	if len(repos) > 0 {
		return parseRepoSpec(repos[0])
	}
	return repoSpec{}, fmt.Errorf("cannot infer GitHub issue repository; run inside a jj repo or pass -r owner/repo")
}

func runAddWithGitHubIssue(ref githubIssueRef, desc string, repos []string, priority bool) error {
	if addRequireJira {
		return fmt.Errorf("--require-jira only applies to Jira tickets")
	}
	issueRepo, err := issueRepository(ref, repos)
	if err != nil {
		return err
	}
	issue, err := gh.NewClient().GetIssueDetails(issueRepo.owner, issueRepo.repo, ref.number)
	if err != nil {
		return fmt.Errorf("fetch GitHub issue %s#%d: %w", issueRepo, ref.number, err)
	}
	if strings.TrimSpace(issue.Title) == "" {
		return fmt.Errorf("GitHub issue %s#%d has no title", issueRepo, ref.number)
	}
	if desc == "" {
		desc = issue.Title
	}
	if len(repos) == 0 {
		if _, err := detectCurrentJJRepo(jj.NewCLI()); err != nil {
			repos = []string{issueRepo.String()}
		}
	}
	return addWithWorktreeSource(fmt.Sprintf("gh-%d", issue.Number), desc, repos, priority, &issue)
}

func runAddWithGitHubCreate(desc string, repos []string, priority bool) error {
	if addRequireJira {
		return fmt.Errorf("--require-jira only applies to Jira tickets")
	}
	if strings.TrimSpace(desc) == "" {
		return fmt.Errorf("--github needs an issue description")
	}
	if err := config.Init(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if config.Get().Worktree.WorktreesDir == "" {
		return fmt.Errorf("worktree.worktrees_dir not configured; set it in ~/.wgo/config.toml")
	}
	if len(repos) > 0 {
		if _, err := parseRepoSpecs(repos); err != nil {
			return err
		}
	}
	issueRepo := repoSpec{}
	var err error
	if len(repos) > 0 {
		issueRepo, err = parseRepoSpec(repos[0])
	} else {
		issueRepo, err = issueRepository(githubIssueRef{}, repos)
	}
	if err != nil {
		return err
	}
	if addSpecRepo != "" {
		found := addSpecRepo == issueRepo.String() && len(repos) == 0
		for _, repo := range repos {
			parsed, _ := parseRepoSpec(repo)
			found = found || parsed.String() == addSpecRepo
		}
		if !found {
			return fmt.Errorf("--spec-repo %q is not in the -r list", addSpecRepo)
		}
	}
	issue, err := gh.NewClient().CreateIssue(issueRepo.owner, issueRepo.repo, desc, addGitHubLabels, addGitHubMilestone)
	if err != nil {
		return fmt.Errorf("create GitHub issue in %s: %w", issueRepo, err)
	}
	fmt.Fprintf(os.Stderr, "created GitHub issue: %s\n", issue.URL)
	issue.Body = "" // the new issue's boilerplate is not the spec summary
	if len(repos) == 0 {
		repos = []string{issueRepo.String()}
	}
	return addWithWorktreeSource(fmt.Sprintf("gh-%d", issue.Number), desc, repos, priority, &issue)
}
