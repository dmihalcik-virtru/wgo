package cmd

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/virtru/wgo/internal/bujo"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/contrib"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/links"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

var (
	todayYesterday bool
	todaySyncPlan  bool
	todaySince     string
)

// todayCmd represents the `wgo today` command.
var todayCmd = &cobra.Command{
	Use:   "today",
	Short: "Daily review: commits, PRs, reviews, branches, and tasks",
	Long: `Show a comprehensive daily review of your development activity.

Sections:
  COMMITS        Git commits across all discovered repos
  FILES CHANGED  Files touched, grouped by repo
  PR REVIEWS     Reviews you submitted on other people's PRs
  PR COMMENTS    PRs/issues you commented on
  NEEDS ATTENTION  PRs with activity since your last comment
  ACTIVE BRANCHES  Current branch of each active worktree
  TASKS          Pending bujo tasks from your plan

Use --plan to auto-sync discovered branches into your plan file.
Use --since to change the time window (default: today).
Use --yesterday to review yesterday's activity.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return showToday()
	},
}

func init() {
	rootCmd.AddCommand(todayCmd)
	todayCmd.Flags().BoolVar(&todayYesterday, "yesterday", false, "Show yesterday's activity instead")
	todayCmd.Flags().BoolVar(&todaySyncPlan, "plan", false, "Sync discovered branches into the plan file")
	todayCmd.Flags().StringVar(&todaySince, "since", "", "Time window (e.g. today, yesterday, 2h, 3d)")
}

// todayData holds all the collected data for the daily review.
type todayData struct {
	since       time.Time
	now         time.Time
	repoCommits []contrib.RepoCommits
	commented   []github.CommentedPR
	reviews     []github.ReviewSubmission
	needsAttn   []github.ExtendedPRInfo
	branches    []activeBranch
	plan        *plan.Plan
	store       *store.FileStore
	ghLogin     string
}

type activeBranch struct {
	RepoName  string
	Branch    string
	Path      string
	GitHubURL string
	Changes   int
}

func showToday() error {
	if err := config.Init(); err != nil {
		return fmt.Errorf("failed to initialize config: %w", err)
	}
	cfg := config.Get()

	now := time.Now()
	var since time.Time

	// Determine time window
	switch {
	case todaySince != "":
		var err error
		since, err = parsePRSince(todaySince)
		if err != nil {
			return fmt.Errorf("invalid --since value: %w", err)
		}
	case todayYesterday:
		y, m, d := now.AddDate(0, 0, -1).Date()
		since = time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	default:
		y, m, d := now.Date()
		since = time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	}

	// Discover repos
	d := discovery.New(cfg.Discovery.BaseDirs, cfg.Discovery.ScanDepth, cfg.Discovery.ExcludePatterns)
	repos, err := d.DiscoverAll()
	if err != nil {
		return fmt.Errorf("failed to discover repositories: %w", err)
	}
	repoPaths := make([]string, len(repos))
	for i, r := range repos {
		repoPaths[i] = r.Path
	}

	author := cfg.Author

	// Collect everything in parallel
	data := &todayData{since: since, now: now}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []string

	// 1. Git commits with files
	wg.Add(1)
	go func() {
		defer wg.Done()
		rc, err := contrib.CollectCommitsWithFiles(repoPaths, since, author)
		if err != nil {
			mu.Lock()
			errs = append(errs, fmt.Sprintf("commits: %v", err))
			mu.Unlock()
			return
		}
		data.repoCommits = rc
	}()

	// 2. Active branches across worktrees (only repos with non-main branches or dirty state)
	wg.Add(1)
	go func() {
		defer wg.Done()
		data.branches = collectActiveBranches(repoPaths)
	}()

	// 3. GitHub data (PR comments, reviews, needs attention)
	gh := github.NewClient()
	if gh.Available() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			login, err := gh.CurrentUser()
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Sprintf("gh user: %v", err))
				mu.Unlock()
				return
			}
			data.ghLogin = login

			var ghWg sync.WaitGroup

			// PR comments
			ghWg.Add(1)
			go func() {
				defer ghWg.Done()
				commented, err := gh.ListMyCommentedPRs(login, since)
				if err != nil {
					mu.Lock()
					errs = append(errs, fmt.Sprintf("pr comments: %v", err))
					mu.Unlock()
					return
				}
				data.commented = commented
			}()

			// PR reviews
			ghWg.Add(1)
			go func() {
				defer ghWg.Done()
				reviews, err := gh.ListMyReviewsToday(login, since)
				if err != nil {
					mu.Lock()
					errs = append(errs, fmt.Sprintf("pr reviews: %v", err))
					mu.Unlock()
					return
				}
				data.reviews = reviews
			}()

			// Needs attention
			ghWg.Add(1)
			go func() {
				defer ghWg.Done()
				involved, err := gh.ListInvolvedPRs(login)
				if err != nil {
					mu.Lock()
					errs = append(errs, fmt.Sprintf("involved prs: %v", err))
					mu.Unlock()
					return
				}
				gh.EnrichWithActivity(involved, login)
				var attn []github.ExtendedPRInfo
				for _, pr := range involved {
					if pr.MyLastActivity.IsZero() || pr.HasNewActivity {
						attn = append(attn, pr)
					}
				}
				data.needsAttn = attn
			}()

			ghWg.Wait()
		}()
	}

	// 4. Plan and tasks
	wg.Add(1)
	go func() {
		defer wg.Done()
		s, err := store.New()
		if err != nil {
			return
		}
		data.store = s
		content, err := s.LoadPlan()
		if err != nil {
			return
		}
		p, err := plan.Parse(content)
		if err != nil {
			return
		}
		data.plan = p
	}()

	wg.Wait()

	// Render output
	isTTY := isTerminal()
	renderToday(data, isTTY)

	// Sync plan if requested
	if todaySyncPlan && data.plan != nil && data.store != nil {
		syncPlanBranches(data)
	}

	return nil
}

func renderToday(data *todayData, isTTY bool) {
	// Header
	if data.now.Day() == data.since.Day() && data.now.Month() == data.since.Month() {
		fmt.Printf("Daily Review — %s\n\n", data.now.Format("Monday, Jan 2 2006"))
	} else {
		fmt.Printf("Activity Review — since %s\n\n", data.since.Format("Mon Jan 2 15:04"))
	}

	sectionsRendered := 0

	// COMMITS
	if len(data.repoCommits) > 0 {
		totalCommits := 0
		for _, rc := range data.repoCommits {
			totalCommits += len(rc.Commits)
		}
		fmt.Printf("COMMITS  (%d across %d repos)\n", totalCommits, len(data.repoCommits))
		for _, rc := range data.repoCommits {
			label := links.Link(rc.GitHubURL, rc.Name, isTTY)
			if rc.Branch != "" && rc.Branch != "main" && rc.Branch != "master" {
				fmt.Printf("  %s [%s]\n", label, rc.Branch)
			} else {
				fmt.Printf("  %s\n", label)
			}
			for _, c := range rc.Commits {
				fmt.Printf("    %s %s\n", c.SHA, c.Message)
			}
		}
		fmt.Println()
		sectionsRendered++
	}

	// FILES CHANGED
	if len(data.repoCommits) > 0 {
		// Collect unique files per repo
		type repoFiles struct {
			name  string
			ghURL string
			files []string
		}
		var rfs []repoFiles
		for _, rc := range data.repoCommits {
			seen := map[string]bool{}
			var files []string
			for _, c := range rc.Commits {
				for _, f := range c.Files {
					if !seen[f] {
						seen[f] = true
						files = append(files, f)
					}
				}
			}
			if len(files) > 0 {
				rfs = append(rfs, repoFiles{name: rc.Name, ghURL: rc.GitHubURL, files: files})
			}
		}
		if len(rfs) > 0 {
			totalFiles := 0
			for _, rf := range rfs {
				totalFiles += len(rf.files)
			}
			fmt.Printf("FILES CHANGED  (%d unique)\n", totalFiles)
			for _, rf := range rfs {
				label := links.Link(rf.ghURL, rf.name, isTTY)
				fmt.Printf("  %s\n", label)
				show := rf.files
				overflow := 0
				if len(show) > 10 {
					overflow = len(show) - 10
					show = show[:10]
				}
				for _, f := range show {
					fmt.Printf("    %s\n", f)
				}
				if overflow > 0 {
					fmt.Printf("    ... and %d more\n", overflow)
				}
			}
			fmt.Println()
			sectionsRendered++
		}
	}

	// PR REVIEWS
	if len(data.reviews) > 0 {
		fmt.Printf("PR REVIEWS  (%d)\n", len(data.reviews))
		for _, r := range data.reviews {
			stateIcon := reviewStateIcon(r.State)
			prLabel := fmt.Sprintf("#%d", r.PRNumber)
			prLink := links.Link(r.PRURL, prLabel, isTTY)
			title := truncatePR(r.PRTitle, 50)
			fmt.Printf("  %s %s  %-22s  %s  %s\n",
				stateIcon, prLink, r.RepoSlug, title, r.Time.Local().Format("15:04"))
		}
		fmt.Println()
		sectionsRendered++
	}

	// PR COMMENTS
	if len(data.commented) > 0 {
		fmt.Printf("PR COMMENTS  (%d)\n", len(data.commented))
		for _, c := range data.commented {
			prLabel := fmt.Sprintf("#%d", c.Number)
			prLink := links.Link(c.URL, prLabel, isTTY)
			title := truncatePR(c.Title, 50)
			fmt.Printf("  %s  %-28s  %s\n", prLink, c.RepoSlug, title)
		}
		fmt.Println()
		sectionsRendered++
	}

	// NEEDS ATTENTION
	if len(data.needsAttn) > 0 {
		fmt.Printf("NEEDS ATTENTION  (%d)\n", len(data.needsAttn))
		for _, pr := range data.needsAttn {
			prLabel := fmt.Sprintf("#%-4d", pr.Number)
			prLink := links.Link(pr.URL, prLabel, isTTY)
			repoLabel := fmt.Sprintf("%-22s", pr.RepoSlug())
			title := truncatePR(pr.Title, 42)
			ago := formatTimeSincePR(pr.UpdatedAt)
			fmt.Printf("  %s  %s  %-42s  %s\n", prLink, repoLabel, title, ago)
		}
		fmt.Println()
		sectionsRendered++
	}

	// ACTIVE BRANCHES
	if len(data.branches) > 0 {
		fmt.Printf("ACTIVE BRANCHES  (%d)\n", len(data.branches))
		for _, b := range data.branches {
			label := links.Link(b.GitHubURL, b.RepoName, isTTY)
			dirtyMark := ""
			if b.Changes > 0 {
				dirtyMark = fmt.Sprintf(" (%d changes)", b.Changes)
			}
			fmt.Printf("  %-28s %s%s\n", label, b.Branch, dirtyMark)
		}
		fmt.Println()
		sectionsRendered++
	}

	// TASKS
	if data.plan != nil {
		active := make([]bujo.Task, 0)
		backlog := make([]bujo.Task, 0)
		for _, t := range data.plan.GetPendingTasks() {
			if t.Bullet == bujo.BulletInProgress || t.Bullet == bujo.BulletPriority {
				active = append(active, t)
			} else {
				backlog = append(backlog, t)
			}
		}
		if len(active)+len(backlog) > 0 {
			fmt.Printf("TASKS  (%d active, %d backlog)\n", len(active), len(backlog))
			for _, t := range active {
				printTaskLine(t)
			}
			show := backlog
			overflow := 0
			if len(show) > 5 {
				overflow = len(show) - 5
				show = show[:5]
			}
			for _, t := range show {
				printTaskLine(t)
			}
			if overflow > 0 {
				fmt.Printf("   ... and %d more\n", overflow)
			}
			fmt.Println()
			sectionsRendered++
		}
	}

	if sectionsRendered == 0 {
		fmt.Println("  No activity found.")
	}
}

func collectActiveBranches(repoPaths []string) []activeBranch {
	type result struct {
		branch activeBranch
		ok     bool
	}

	results := make([]result, len(repoPaths))
	var wg sync.WaitGroup

	for i, path := range repoPaths {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()

			branchOut, err := exec.Command("git", "-C", path, "branch", "--show-current").Output()
			if err != nil {
				return
			}
			branch := strings.TrimSpace(string(branchOut))
			if branch == "" || branch == "main" || branch == "master" || branch == "develop" {
				// Only include non-default branches, unless dirty
				statusOut, _ := exec.Command("git", "-C", path, "status", "--short").Output()
				changes := len(strings.Split(strings.TrimSpace(string(statusOut)), "\n"))
				if strings.TrimSpace(string(statusOut)) == "" {
					changes = 0
				}
				if changes == 0 {
					return
				}
				results[i] = result{
					branch: activeBranch{
						RepoName: repoDisplayName(path),
						Branch:   branch,
						Path:     path,
						Changes:  changes,
					},
					ok: true,
				}
				return
			}

			statusOut, _ := exec.Command("git", "-C", path, "status", "--short").Output()
			changes := 0
			if trimmed := strings.TrimSpace(string(statusOut)); trimmed != "" {
				changes = len(strings.Split(trimmed, "\n"))
			}

			ghURL := contrib.ResolveGitHubURL(path)

			results[i] = result{
				branch: activeBranch{
					RepoName:  repoDisplayName(path),
					Branch:    branch,
					Path:      path,
					GitHubURL: ghURL,
					Changes:   changes,
				},
				ok: true,
			}
		}(i, path)
	}
	wg.Wait()

	var branches []activeBranch
	for _, r := range results {
		if r.ok {
			branches = append(branches, r.branch)
		}
	}

	sort.Slice(branches, func(i, j int) bool {
		// Dirty branches first, then alphabetical
		if branches[i].Changes != branches[j].Changes {
			return branches[i].Changes > branches[j].Changes
		}
		return branches[i].RepoName < branches[j].RepoName
	})

	return branches
}

func repoDisplayName(path string) string {
	// Try to show owner/repo from remote
	out, err := exec.Command("git", "-C", path, "remote", "get-url", "origin").Output()
	if err == nil {
		remote := strings.TrimSpace(string(out))
		// SSH form
		if strings.HasPrefix(remote, "git@github.com:") {
			slug := strings.TrimPrefix(remote, "git@github.com:")
			return strings.TrimSuffix(slug, ".git")
		}
		// HTTPS form
		if idx := strings.Index(remote, "github.com/"); idx >= 0 {
			slug := remote[idx+len("github.com/"):]
			return strings.TrimSuffix(slug, ".git")
		}
	}
	// Fallback: last two path components
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return parts[len(parts)-1]
}

func syncPlanBranches(data *todayData) {
	if data.plan == nil || data.store == nil {
		return
	}

	added := 0
	for _, b := range data.branches {
		if b.Branch == "main" || b.Branch == "master" || b.Branch == "develop" {
			continue
		}
		existing := data.plan.GetBranch(b.RepoName, b.Branch)
		if existing != nil {
			continue
		}
		// Auto-generate reason from last commit on the branch
		reason := "active branch"
		out, err := exec.Command("git", "-C", b.Path, "log", "-1", "--format=%s").Output()
		if err == nil {
			msg := strings.TrimSpace(string(out))
			if msg != "" {
				reason = msg
			}
		}
		data.plan.AddBranch(b.RepoName, b.Branch, reason)
		added++
	}

	if added > 0 {
		content := data.plan.Render()
		if err := data.store.SavePlan(content); err != nil {
			fmt.Printf("  (failed to save plan: %v)\n", err)
			return
		}
		fmt.Printf("PLAN SYNC  +%d branches added to plan\n\n", added)
	} else {
		fmt.Println("PLAN SYNC  plan is up to date")
		fmt.Println()
	}
}

func reviewStateIcon(state string) string {
	switch strings.ToUpper(state) {
	case "APPROVED":
		return "✓"
	case "CHANGES_REQUESTED":
		return "✗"
	default:
		return "◦"
	}
}

func printTaskLine(t bujo.Task) {
	line := "   " + string(t.Bullet) + " " + t.Text
	if len(t.Refs) > 0 {
		ref := t.Refs[0]
		refStr := ""
		if ref.Branch != "" {
			refStr = ref.Repo + ":" + ref.Branch
		} else if ref.PR > 0 {
			refStr = fmt.Sprintf("%s PR #%d", ref.Repo, ref.PR)
		} else if ref.Issue > 0 {
			refStr = fmt.Sprintf("%s issue #%d", ref.Repo, ref.Issue)
		}
		if refStr != "" {
			line = fmt.Sprintf("%-60s %s", line, refStr)
		}
	}
	fmt.Println(line)
}
