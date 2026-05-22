// Package cleanup provides candidate detection logic for wgo clean.
package cleanup

import (
	"fmt"
	"time"

	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/store"
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
func FindCandidates(cfg *config.Config, gitClient git.Client, ghClient github.Client) ([]Candidate, error) {
	disc := discovery.New(cfg.Discovery.BaseDirs, cfg.Discovery.ScanDepth, cfg.Discovery.ExcludePatterns)
	repos, err := disc.DiscoverAll()
	if err != nil {
		return nil, fmt.Errorf("discovery failed: %w", err)
	}

	// Only process main repos (not worktrees found during discovery)
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

		found, err := findRepoCandidate(repo.Path, gitClient, ghClient, cfg.Status.StaleDays)
		if err != nil {
			continue
		}
		candidates = append(candidates, found...)
	}

	return candidates, nil
}

func findRepoCandidate(repoPath string, gitClient git.Client, ghClient github.Client, staleDays int) ([]Candidate, error) {
	var candidates []Candidate

	defaultBranch, err := gitClient.DefaultBranch(repoPath)
	if err != nil {
		defaultBranch = "main"
	}

	// --- Worktrees ---
	worktrees, err := gitClient.ListWorktrees(repoPath)
	if err == nil {
		for _, wt := range worktrees {
			if wt.IsMain {
				continue
			}
			c := Candidate{
				Kind:     KindWorktree,
				Path:     wt.Path,
				RepoPath: repoPath,
				Branch:   wt.Branch,
			}

			// Check if dirty
			status, err := gitClient.Status(wt.Path)
			if err == nil {
				c.IsDirty = status.Modified > 0 || status.Staged > 0 || status.Added > 0
			}

			// Check PR status
			if ghClient != nil && ghClient.Available() && wt.Branch != "" {
				pr, _ := ghClient.GetPRStatus(repoPath, wt.Branch)
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

			// Check if branch is locally merged into default
			if wt.Branch != "" && wt.Branch != defaultBranch {
				merged, err := gitClient.IsBranchMerged(repoPath, wt.Branch, defaultBranch)
				if err == nil && merged {
					c.Reason = fmt.Sprintf("merged into %s", defaultBranch)
					candidates = append(candidates, c)
				}
			}
		}
	}

	// --- Local branches ---
	branches, err := gitClient.ListLocalBranches(repoPath)
	if err != nil {
		return candidates, nil
	}

	staleThreshold := time.Now().AddDate(0, 0, -staleDays)

	for _, branch := range branches {
		if branch == defaultBranch {
			continue
		}
		if isWorktreeBranch(branch, worktrees) {
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

		// Check local merge
		merged, err := gitClient.IsBranchMerged(repoPath, branch, defaultBranch)
		if err == nil && merged {
			c.Reason = fmt.Sprintf("merged into %s", defaultBranch)
			candidates = append(candidates, c)
			continue
		}

		// Check staleness by last commit
		_ = staleThreshold
		// (commit time check could be added here via LastCommit)
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

// isWorktreeBranch returns true if the branch is checked out in a non-main worktree.
func isWorktreeBranch(branch string, worktrees []git.WorktreeInfo) bool {
	for _, wt := range worktrees {
		if !wt.IsMain && wt.Branch == branch {
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

// StackParentBlock describes why a candidate was held back by FilterStackParents:
// the candidate is a parent of one or more in-stack children that are not yet
// retargeted (`wgo stack sync` removes a parent link once its PR has merged).
type StackParentBlock struct {
	Candidate Candidate
	Children  []string // annotation keys ("repoPath:branch") that still record this candidate as a parent
}

// FilterStackParents partitions candidates into (safe, blocked). A candidate is
// blocked when its (repoPath, branch) appears in some other annotation's Parents
// list. The blocked candidates are returned separately so callers can surface
// the reason; they are NOT included in `safe`.
//
// The rule is intentionally simple: the existence of a child link is treated as
// "still needed". `wgo stack sync` is responsible for removing parent links once
// the parent has merged, so anything still linked here is by definition unsafe
// to delete without breaking a child's record of its base.
func FilterStackParents(candidates []Candidate, state *store.State) (safe []Candidate, blocked []StackParentBlock) {
	if state == nil {
		return candidates, nil
	}
	// Index: parent key -> list of child keys that record it.
	childrenOf := map[string][]string{}
	for childKey, ann := range state.Annotations {
		for _, parentKey := range ann.Parents {
			childrenOf[parentKey] = append(childrenOf[parentKey], childKey)
		}
	}

	for _, c := range candidates {
		if c.Branch == "" || c.RepoPath == "" {
			safe = append(safe, c)
			continue
		}
		key := store.AnnotationKey(c.RepoPath, c.Branch)
		if children, ok := childrenOf[key]; ok && len(children) > 0 {
			blocked = append(blocked, StackParentBlock{Candidate: c, Children: children})
			continue
		}
		safe = append(safe, c)
	}
	return safe, blocked
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
