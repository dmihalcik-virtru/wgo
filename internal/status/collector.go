package status

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
	"github.com/virtru/wgo/models"
)

// Collector gathers status information from repositories in parallel.
type Collector struct {
	gitClient      git.Client
	plan           *plan.Plan
	state          *store.State
	since          time.Time
	staleThreshold time.Duration
	repoTimeout    time.Duration
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
func NewCollector(gitClient git.Client, p *plan.Plan, state *store.State, opts ...CollectorOption) *Collector {
	c := &Collector{
		gitClient:      gitClient,
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

		worktrees, err := c.gitClient.ListWorktrees(main.Path)
		if err != nil {
			continue
		}

		for _, wt := range worktrees {
			if wt.IsMain || wt.Path == main.Path {
				continue
			}
			// Skip if already in discovery list (will be handled as dedup)
			if discoveredPaths[wt.Path] {
				continue
			}
			name := filepath.Base(wt.Path)
			targets = append(targets, collectionTarget{
				repo: discovery.DiscoveredRepo{
					Path: wt.Path,
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

	// Branch
	branch, err := c.gitClient.CurrentBranch(repo.Path)
	if err != nil {
		activity.Branch = "?"
		activity.State = models.StateClean
		return activity
	}
	activity.Branch = branch

	// Status
	status, err := c.gitClient.Status(repo.Path)
	if err == nil {
		activity.Status = status
	}

	// Last commit
	commit, err := c.gitClient.LastCommit(repo.Path)
	if err == nil {
		activity.LastCommit = commit
	}

	// Recent commits (if since is set)
	if !c.since.IsZero() {
		count, err := c.gitClient.RecentCommitCount(repo.Path, c.since)
		if err == nil {
			activity.RecentCommits = count
		}
	}

	// Diff stat
	diffStat, err := c.gitClient.DiffStat(repo.Path)
	if err == nil {
		activity.DiffStat = diffStat
	}

	// Last activity: use last commit date, or check tracked files
	activity.LastActivity = c.determineLastActivity(ctx, repo.Path, commit)

	// State determination: conflict > staged > modified > stale > clean
	activity.State = c.determineState(status, activity.LastActivity)

	// Annotation from plan or state
	activity.Annotation = c.lookupAnnotation(repo.Name, repo.Path, branch)

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
