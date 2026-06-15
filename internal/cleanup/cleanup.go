// Package cleanup provides candidate detection logic for wgo clean.
package cleanup

import (
	"fmt"
	"time"

	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
)

// CandidateKind identifies what type of item is a cleanup candidate.
type CandidateKind int

const (
	KindWorktree     CandidateKind = iota // A non-main git worktree
	KindLocalBranch                       // A local branch
	KindRemoteBranch                      // A remote branch (via gh)
	KindRepo                              // A full cloned repo directory
)

func (k CandidateKind) String() string {
	switch k {
	case KindWorktree:
		return "worktree"
	case KindLocalBranch:
		return "local branch"
	case KindRemoteBranch:
		return "remote branch"
	case KindRepo:
		return "repo"
	default:
		return "unknown"
	}
}

// Candidate represents a cleanup candidate.
type Candidate struct {
	Kind     CandidateKind
	Path     string         // filesystem path (worktree or repo)
	RepoPath string         // main repo root
	Branch   string         // branch name
	Reason   string         // human-readable reason, e.g. "merged PR #42"
	IsDirty  bool           // has uncommitted changes
	PRInfo   *github.PRInfo // non-nil if a PR was found
}

// FindCandidates discovers all cleanup candidates across tracked repos.
func FindCandidates(cfg *config.Config, jjc jj.Client, ghClient github.Client) ([]Candidate, error) {
	disc := discovery.New(cfg.Discovery.BaseDirs, cfg.Discovery.ScanDepth, cfg.Discovery.ExcludePatterns)
	repos, err := disc.DiscoverAll()
	if err != nil {
		return nil, fmt.Errorf("discovery failed: %w", err)
	}

	// Only process main workspaces (not secondary workspaces found during discovery)
	var candidates []Candidate
	seen := map[string]bool{}
	for _, repo := range repos {
		if repo.IsWorktree {
			continue
		}
		if seen[repo.Path] {
			continue
		}
		seen[repo.Path] = true

		found, err := findRepoCandidate(repo.Path, jjc, ghClient, cfg.Status.StaleDays)
		if err != nil {
			continue
		}
		candidates = append(candidates, found...)
	}

	return candidates, nil
}

// repoDefaultBranch derives the GitHub default branch for a jj repo path,
// falling back to "main" on any error.
func repoDefaultBranch(jjc jj.Client, repoPath string) string {
	remotes, err := jjc.RemoteURLs(repoPath)
	if err != nil {
		return "main"
	}
	url := remotes["origin"]
	if url == "" {
		return "main"
	}
	slug := github.SlugFromRemoteURL(url)
	if slug == "" {
		return "main"
	}
	parts := splitSlug(slug)
	if parts == nil {
		return "main"
	}
	branch, err := github.RepoDefaultBranch(parts[0], parts[1])
	if err != nil || branch == "" {
		return "main"
	}
	return branch
}

func splitSlug(slug string) []string {
	for i := 0; i < len(slug); i++ {
		if slug[i] == '/' {
			return []string{slug[:i], slug[i+1:]}
		}
	}
	return nil
}

// workspaceBookmark returns the first local bookmark on the workspace's @,
// or "" when the @ has no bookmark.
func workspaceBookmark(jjc jj.Client, wsPath string) string {
	ch, err := jjc.CurrentChange(wsPath)
	if err != nil || len(ch.Bookmarks) == 0 {
		return ""
	}
	return ch.Bookmarks[0]
}

func findRepoCandidate(repoPath string, jjc jj.Client, ghClient github.Client, staleDays int) ([]Candidate, error) {
	var candidates []Candidate

	defaultBranch := repoDefaultBranch(jjc, repoPath)

	// --- Workspaces (the jj equivalent of git worktrees) ---
	workspaces, err := jjc.ListWorkspaces(repoPath)
	if err == nil {
		for _, ws := range workspaces {
			if ws.Name == "default" {
				continue
			}
			bookmark := workspaceBookmark(jjc, ws.Path)
			c := Candidate{
				Kind:     KindWorktree,
				Path:     ws.Path,
				RepoPath: repoPath,
				Branch:   bookmark,
			}

			// Check if dirty
			if st, err := jjc.Status(ws.Path); err == nil {
				c.IsDirty = !st.Clean
			}

			// Check PR status
			if ghClient != nil && ghClient.Available() && bookmark != "" {
				pr, _ := ghClient.GetPRStatus(repoPath, bookmark)
				if pr != nil {
					c.PRInfo = pr
					if pr.IsMerged() {
						c.Reason = fmt.Sprintf("merged PR #%d", pr.Number)
						candidates = append(candidates, c)
						continue
					}
					if pr.IsClosed() {
						c.Reason = fmt.Sprintf("closed PR #%d", pr.Number)
						candidates = append(candidates, c)
						continue
					}
				}
			}

			// Check if bookmark is locally merged into default.
			if bookmark != "" && bookmark != defaultBranch {
				if isMergedInto(jjc, repoPath, bookmark, defaultBranch+"@origin") {
					c.Reason = fmt.Sprintf("merged into %s", defaultBranch)
					candidates = append(candidates, c)
				}
			}
		}
	}

	// --- Local bookmarks ---
	localBookmarks, err := jjc.BookmarkList(repoPath, jj.BookmarkListOpts{Local: true})
	if err != nil {
		return candidates, nil
	}

	staleThreshold := time.Now().AddDate(0, 0, -staleDays)

	for _, bm := range localBookmarks {
		branch := bm.Name
		if branch == defaultBranch {
			continue
		}
		if isWorkspaceBookmark(jjc, branch, workspaces) {
			// Already handled above
			continue
		}

		c := Candidate{
			Kind:     KindLocalBranch,
			RepoPath: repoPath,
			Branch:   branch,
		}

		// Check PR status
		if ghClient != nil && ghClient.Available() {
			pr, _ := ghClient.GetPRStatus(repoPath, branch)
			if pr != nil {
				c.PRInfo = pr
				if pr.IsMerged() {
					c.Reason = fmt.Sprintf("merged PR #%d", pr.Number)
					candidates = append(candidates, c)
					continue
				}
				if pr.IsClosed() {
					// Also add as remote branch candidate
					candidates = append(candidates, Candidate{
						Kind:     KindRemoteBranch,
						RepoPath: repoPath,
						Branch:   branch,
						Reason:   fmt.Sprintf("closed PR #%d (no remote branch needed)", pr.Number),
						PRInfo:   pr,
					})
					c.Reason = fmt.Sprintf("closed PR #%d", pr.Number)
					candidates = append(candidates, c)
					continue
				}
			}
		}

		// Check local merge into default@origin.
		if isMergedInto(jjc, repoPath, branch, defaultBranch+"@origin") {
			c.Reason = fmt.Sprintf("merged into %s", defaultBranch)
			candidates = append(candidates, c)
			continue
		}

		// Check staleness by last commit
		_ = staleThreshold
		// (commit time check could be added here via Log)
	}

	branches := make([]string, 0, len(localBookmarks))
	for _, bm := range localBookmarks {
		branches = append(branches, bm.Name)
	}

	// --- Stale remote branches that have merged PRs ---
	if ghClient != nil && ghClient.Available() {
		for _, branch := range branches {
			if branch == defaultBranch {
				continue
			}
			pr, _ := ghClient.GetPRStatus(repoPath, branch)
			if pr != nil && pr.IsMerged() {
				// Check if we already have this as a local branch candidate
				alreadyLocal := false
				for _, c := range candidates {
					if c.Kind == KindLocalBranch && c.Branch == branch {
						alreadyLocal = true
						break
					}
				}
				if !alreadyLocal {
					candidates = append(candidates, Candidate{
						Kind:     KindRemoteBranch,
						RepoPath: repoPath,
						Branch:   branch,
						Reason:   fmt.Sprintf("merged PR #%d", pr.Number),
						PRInfo:   pr,
					})
				}
			}
		}
	}

	return candidates, nil
}

// isMergedInto reports whether all commits reachable from branch are also
// reachable from target. Implemented via the jj revset `(target)..(branch)`
// — zero result means branch is fully contained in target's history.
func isMergedInto(jjc jj.Client, repoPath, branch, target string) bool {
	n, err := jjc.CountRevset(repoPath, fmt.Sprintf("(%s)..(%s)", target, branch))
	if err != nil {
		return false
	}
	return n == 0
}

// isWorkspaceBookmark returns true if the named bookmark is currently
// attached to any non-default workspace's @.
func isWorkspaceBookmark(jjc jj.Client, branch string, workspaces []jj.Workspace) bool {
	for _, ws := range workspaces {
		if ws.Name == "default" {
			continue
		}
		if workspaceBookmark(jjc, ws.Path) == branch {
			return true
		}
	}
	return false
}

// SenescenceReason returns a human-readable reason string for a senescent PR.
func SenescenceReason(pr *github.PRInfo) string {
	if pr == nil {
		return ""
	}
	if pr.IsMerged() {
		if pr.MergedAt != nil {
			return fmt.Sprintf("PR #%d merged %s", pr.Number, formatAge(*pr.MergedAt))
		}
		return fmt.Sprintf("PR #%d merged", pr.Number)
	}
	if pr.IsClosed() {
		if pr.ClosedAt != nil {
			return fmt.Sprintf("PR #%d closed %s", pr.Number, formatAge(*pr.ClosedAt))
		}
		return fmt.Sprintf("PR #%d closed", pr.Number)
	}
	return ""
}

func formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 24*time.Hour:
		return "today"
	case d < 48*time.Hour:
		return "yesterday"
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	}
}

// GroupByRepo groups candidates by their repo path.
func GroupByRepo(candidates []Candidate) map[string][]Candidate {
	m := map[string][]Candidate{}
	for _, c := range candidates {
		m[c.RepoPath] = append(m[c.RepoPath], c)
	}
	return m
}

// FilterKind returns candidates of the given kind.
func FilterKind(candidates []Candidate, kind CandidateKind) []Candidate {
	var out []Candidate
	for _, c := range candidates {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

// FilterKinds returns candidates matching any of the given kinds.
func FilterKinds(candidates []Candidate, kinds ...CandidateKind) []Candidate {
	kindSet := map[CandidateKind]bool{}
	for _, k := range kinds {
		kindSet[k] = true
	}
	var out []Candidate
	for _, c := range candidates {
		if kindSet[c.Kind] {
			out = append(out, c)
		}
	}
	return out
}

// DisplayPath returns a short display path for a candidate.
func (c *Candidate) DisplayPath() string {
	if c.Path != "" {
		return c.Path
	}
	if c.RepoPath != "" && c.Branch != "" {
		return c.RepoPath + " [" + c.Branch + "]"
	}
	return c.Branch
}
