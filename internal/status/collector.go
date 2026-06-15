package status

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/links"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/spec"
	"github.com/virtru/wgo/internal/store"
	"github.com/virtru/wgo/models"
)

// Collector gathers status information from repositories in parallel.
type Collector struct {
	jjc            jj.Client
	ghClient       *github.CLIClient
	currentUser    string // cached current GitHub user login
	plan           *plan.Plan
	state          *store.State
	since          time.Time
	staleThreshold time.Duration
	repoTimeout    time.Duration
	userMu         sync.Mutex // protects currentUser access
}

// CollectorOption configures a Collector.
type CollectorOption func(*Collector)

// WithSince sets the time window for recent commit counting.
func WithSince(t time.Time) CollectorOption {
	return func(c *Collector) { c.since = t }
}

// WithStaleThreshold sets the duration after which a repo is marked stale.
func WithStaleThreshold(d time.Duration) CollectorOption {
	return func(c *Collector) { c.staleThreshold = d }
}

// WithRepoTimeout sets the per-repo collection timeout.
func WithRepoTimeout(d time.Duration) CollectorOption {
	return func(c *Collector) { c.repoTimeout = d }
}

// NewCollector creates a new Collector.
func NewCollector(jjc jj.Client, p *plan.Plan, state *store.State, opts ...CollectorOption) *Collector {
	c := &Collector{
		jjc:            jjc,
		ghClient:       github.NewClient(),
		plan:           p,
		state:          state,
		staleThreshold: 14 * 24 * time.Hour,
		repoTimeout:    5 * time.Second,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// CollectAll gathers status for all discovered repos in parallel.
// It expands main repos to include all their worktrees via git worktree list.
func (c *Collector) CollectAll(ctx context.Context, repos []discovery.DiscoveredRepo) []models.RepoActivity {
	cwd, _ := os.Getwd()

	// Phase 1: Partition discovered repos into main repos vs already-discovered worktrees.
	// Build a set of discovered paths for dedup.
	discoveredPaths := make(map[string]bool, len(repos))
	var mainRepos []discovery.DiscoveredRepo
	var discoveredWorktrees []discovery.DiscoveredRepo

	for _, r := range repos {
		discoveredPaths[r.Path] = true
		if r.IsWorktree {
			discoveredWorktrees = append(discoveredWorktrees, r)
		} else {
			mainRepos = append(mainRepos, r)
		}
	}

	// Phase 2: Expand main repos to include their worktrees.
	type collectionTarget struct {
		repo         discovery.DiscoveredRepo
		isWorktree   bool
		mainRepoName string
		mainRepoPath string
	}

	var targets []collectionTarget

	for _, main := range mainRepos {
		targets = append(targets, collectionTarget{repo: main})

		workspaces, err := c.jjc.ListWorkspaces(main.Path)
		if err != nil {
			continue
		}

		for _, ws := range workspaces {
			if ws.Name == "default" || ws.Path == main.Path {
				continue
			}
			// Skip if already in discovery list (will be handled as dedup)
			if discoveredPaths[ws.Path] {
				continue
			}
			name := filepath.Base(ws.Path)
			targets = append(targets, collectionTarget{
				repo: discovery.DiscoveredRepo{
					Path: ws.Path,
					Name: name,
				},
				isWorktree:   true,
				mainRepoName: main.Name,
				mainRepoPath: main.Path,
			})
		}
	}

	// Phase 3: Append discovered worktrees that have a matching main repo,
	// marking them as worktrees. Orphans (main not found) are appended as-is.
	for _, dw := range discoveredWorktrees {
		found := false
		for _, main := range mainRepos {
			if dw.MainRepoPath == main.Path {
				targets = append(targets, collectionTarget{
					repo:         dw,
					isWorktree:   true,
					mainRepoName: main.Name,
					mainRepoPath: main.Path,
				})
				found = true
				break
			}
		}
		if !found {
			targets = append(targets, collectionTarget{repo: dw})
		}
	}

	// Phase 4: Collect in parallel.
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results = make([]models.RepoActivity, len(targets))
	)

	for i, target := range targets {
		wg.Add(1)
		go func(idx int, t collectionTarget) {
			defer wg.Done()

			repoCtx, cancel := context.WithTimeout(ctx, c.repoTimeout)
			defer cancel()

			activity := c.collectOne(repoCtx, t.repo, cwd)
			if t.isWorktree {
				activity.IsWorktree = true
				activity.MainRepoName = t.mainRepoName
				activity.MainRepoPath = t.mainRepoPath
			}

			mu.Lock()
			results[idx] = activity
			mu.Unlock()
		}(i, target)
	}

	wg.Wait()
	return results
}

// collectOne gathers status for a single repo.
func (c *Collector) collectOne(ctx context.Context, repo discovery.DiscoveredRepo, cwd string) models.RepoActivity {
	activity := models.RepoActivity{
		Path: repo.Path,
		Name: repo.Name,
	}

	// Check if current directory is in this repo
	if cwd != "" {
		rel, err := filepath.Rel(repo.Path, cwd)
		if err == nil && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, "..") {
			activity.IsCurrent = true
		}
	}

	// Bookmark on this workspace's @ (jj-side equivalent of "branch").
	curChange, err := c.jjc.CurrentChange(repo.Path)
	if err != nil {
		activity.Branch = "?"
		activity.State = models.StateClean
		return activity
	}
	branch := ""
	if len(curChange.Bookmarks) > 0 {
		branch = curChange.Bookmarks[0]
	}
	if branch == "" {
		branch = "(no bookmark)"
	}
	activity.Branch = branch

	// Remote URL and GitHub links (origin remote only).
	if remotes, err := c.jjc.RemoteURLs(repo.Path); err == nil {
		remoteURL := remotes["origin"]
		if remoteURL != "" {
			activity.RemoteURL = remoteURL
			activity.RepoURL = links.RepoURL(remoteURL)
			activity.BranchURL = links.BranchURL(remoteURL, branch)
		}
	}

	// Status — convert jj's slice-based representation into the count-based
	// models.GitStatus the renderers expect.
	status := models.GitStatus{}
	if jjStatus, err := c.jjc.Status(repo.Path); err == nil {
		status = models.GitStatus{
			Modified: len(jjStatus.Modified),
			Added:    len(jjStatus.Added),
			Deleted:  len(jjStatus.Deleted),
		}
	}
	activity.Status = status

	// Last commit — the workspace's @- (the last described change).
	commit := models.CommitInfo{}
	if entries, err := c.jjc.Log(repo.Path, "@-"); err == nil && len(entries) > 0 {
		commit = models.CommitInfo{
			Hash:    entries[0].CommitID,
			Message: firstLine(entries[0].Description),
			Author:  entries[0].AuthorEmail,
			Date:    entries[0].AuthorTimestamp,
		}
	}
	activity.LastCommit = commit

	// Recent commits (if since is set). Counts commits visible on the
	// current bookmark that were authored after `since`.
	if !c.since.IsZero() && branch != "" && branch != "(no bookmark)" {
		revset := fmt.Sprintf("(::%s) & author_date(after:%q)", branch, c.since.UTC().Format(time.RFC3339))
		if n, err := c.jjc.CountRevset(repo.Path, revset); err == nil {
			activity.RecentCommits = n
		}
	}

	// Diff stat on the working-copy commit.
	if added, deleted, err := c.jjc.DiffStat(repo.Path, "@"); err == nil {
		files, _ := c.jjc.ChangedFiles(repo.Path, "@")
		activity.DiffStat = models.DiffStat{
			FilesChanged: len(files),
			Insertions:   added,
			Deletions:    deleted,
		}
	}

	// Ahead/behind vs origin.
	if branch != "" && branch != "(no bookmark)" {
		if ahead, behind, err := c.jjc.AheadBehind(repo.Path, branch); err == nil {
			activity.Status.Ahead = ahead
			activity.Status.Behind = behind
		}
	}

	// Last activity: use last commit date, or check tracked files
	activity.LastActivity = c.determineLastActivity(ctx, repo.Path, commit)

	// State determination: conflict > staged > modified > stale > clean
	activity.State = c.determineState(status, activity.LastActivity)

	// Annotation from plan or state
	activity.Annotation = c.lookupAnnotation(repo.Name, repo.Path, branch)

	// Spec glyph
	activity.SpecGlyph = specGlyph(repo.Path, branch, c.state)

	// Detect default branch
	defaultBranch := c.getDefaultBranch(repo.Path)
	activity.IsDefaultBranch = (branch == defaultBranch)

	// Classify engagement level (after gathering PR info)
	activity.EngagementLevel = c.classifyEngagementLevel(&activity)

	// Check context cancellation
	if ctx.Err() != nil {
		return activity
	}

	return activity
}

// determineState determines the RepoState from status and activity time.
func (c *Collector) determineState(status models.GitStatus, lastActivity time.Time) models.RepoState {
	if status.Conflicts > 0 {
		return models.StateConflict
	}
	if status.Staged > 0 {
		return models.StateStaged
	}
	if status.Modified > 0 || status.Added > 0 || status.Deleted > 0 || status.Untracked > 0 {
		return models.StateModified
	}
	if !lastActivity.IsZero() && time.Since(lastActivity) > c.staleThreshold {
		return models.StateStale
	}
	return models.StateClean
}

// getCurrentUser retrieves and caches the current GitHub user login.
func (c *Collector) getCurrentUser() string {
	c.userMu.Lock()
	defer c.userMu.Unlock()

	if c.currentUser != "" {
		return c.currentUser
	}

	if c.ghClient == nil || !c.ghClient.Available() {
		return ""
	}

	user, err := c.ghClient.CurrentUser()
	if err != nil {
		return ""
	}

	c.currentUser = user
	return c.currentUser
}

// getDefaultBranch retrieves the GitHub default branch for a repo by
// resolving its origin remote URL and querying the GitHub API.
func (c *Collector) getDefaultBranch(repoPath string) string {
	remotes, err := c.jjc.RemoteURLs(repoPath)
	if err != nil {
		return ""
	}
	url := remotes["origin"]
	if url == "" {
		return ""
	}
	slug := github.SlugFromRemoteURL(url)
	if slug == "" {
		return ""
	}
	owner, repo, ok := strings.Cut(slug, "/")
	if !ok {
		return ""
	}
	branch, err := github.RepoDefaultBranch(owner, repo)
	if err != nil {
		return ""
	}
	return branch
}

// firstLine returns the first newline-delimited line of s, suitable for
// use as a short commit subject.
func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return strings.TrimSpace(s)
}

// classifyEngagementLevel determines engagement level based on repo state.
// It also populates PR information in the activity struct.
func (c *Collector) classifyEngagementLevel(activity *models.RepoActivity) models.EngagementLevel {
	// If uncommitted changes, always active (unpushed)
	if activity.State != models.StateClean && activity.State != models.StateStale {
		return models.EngagementActiveUnpushed
	}

	// If commits ahead of remote, active (unpushed)
	if activity.Status.Ahead > 0 {
		return models.EngagementActiveUnpushed
	}

	// Try to get PR info if gh is available
	if c.ghClient != nil && c.ghClient.Available() {
		prInfo, err := c.ghClient.GetPRStatus(activity.Path, activity.Branch)
		if err == nil && prInfo != nil {
			// Populate PR fields
			activity.PRNumber = prInfo.Number
			activity.PRAuthor = prInfo.Author
			activity.PRURL = prInfo.URL

			// Check if PR is open
			if strings.EqualFold(prInfo.State, "open") {
				// Check if it's authored by the current user
				currentUser := c.getCurrentUser()
				if currentUser != "" && strings.EqualFold(prInfo.Author, currentUser) {
					return models.EngagementActivePR
				}
				// PR authored by someone else
				return models.EngagementReviewing
			}
		}
	}

	// On default branch with no commits ahead = observer
	if activity.IsDefaultBranch && activity.Status.Ahead == 0 {
		return models.EngagementObserver
	}

	// Default to observer for anything else
	return models.EngagementObserver
}

// determineLastActivity finds the most recent activity time for a repo.
func (c *Collector) determineLastActivity(_ context.Context, repoPath string, commit models.CommitInfo) time.Time {
	// Start with the last commit date
	latest := commit.Date

	// Check if tracked files have been modified more recently
	// Use a lightweight check on common files
	for _, name := range []string{".", "src", "lib", "internal", "cmd", "pkg"} {
		p := filepath.Join(repoPath, name)
		info, err := os.Stat(p)
		if err == nil && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}

	return latest
}

// specGlyph returns the single-character spec status glyph for a branch.
// It checks the state annotation cache first, then falls back to parsing the spec file.
func specGlyph(repoPath, branch string, state *store.State) string {
	ticket := spec.ParseTicketFromBranch(branch)
	if ticket == "" {
		return " "
	}

	// Cache hit: use annotation SpecState.
	if state != nil {
		if ann := state.GetAnnotation(repoPath, branch); ann != nil && ann.SpecState != "" {
			return specStatusGlyph(spec.Status(ann.SpecState))
		}
	}

	// Cache miss: parse the spec file directly.
	specPath, err := spec.FindByTicket(repoPath, ticket)
	if err != nil {
		return "⚠"
	}
	sf, err := spec.Parse(specPath)
	if err != nil || sf.Frontmatter.Ticket == "" {
		return "⚠"
	}
	return specStatusGlyph(sf.Frontmatter.Status)
}

func specStatusGlyph(s spec.Status) string {
	switch s {
	case spec.StatusDraft:
		return "●"
	case spec.StatusInProgress:
		return "◐"
	case spec.StatusShipped:
		return "✓"
	case spec.StatusAbandoned:
		return "−"
	default:
		return "●"
	}
}

// lookupAnnotation checks plan then state for a branch annotation.
func (c *Collector) lookupAnnotation(repoName, repoPath, branch string) string {
	if c.plan != nil {
		if entry := c.plan.GetBranch(repoName, branch); entry != nil {
			return entry.Reason
		}
	}
	if c.state != nil {
		if ann := c.state.GetAnnotation(repoPath, branch); ann != nil {
			return ann.Purpose
		}
	}
	return ""
}
