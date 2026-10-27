// Package cleanup provides candidate detection logic for wgo clean.
package cleanup

import (
	"fmt"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/rig"
)

// CandidateKind identifies what type of item is a cleanup candidate.
type CandidateKind int

const (
	KindWorktree        CandidateKind = iota // A non-main git worktree
	KindLocalBranch                          // A local branch
	KindRemoteBranch                         // A remote branch (via gh)
	KindRepo                                 // A full cloned repo directory
	KindOrphanWorkspace                      // A workspace jj lists but whose directory is gone
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
	case KindOrphanWorkspace:
		return "orphaned workspace"
	default:
		return "unknown"
	}
}

// Candidate represents a cleanup candidate.
type Candidate struct {
	Kind          CandidateKind
	Path          string         // filesystem path (worktree or repo); "" for a workspace whose directory is gone
	RepoPath      string         // main repo root
	WorkspaceName string         // jj workspace name, for worktrees and orphaned workspaces
	Branch        string         // branch name
	Reason        string         // human-readable reason, e.g. "merged PR #42"
	PRInfo        *github.PRInfo // non-nil if a PR was found
	// Class is why a workspace (KindWorktree) is offered: its Tier says
	// whether --yes may remove it or the user must decide.
	Class *Classification
}

// Scan is what FindCandidates found.
type Scan struct {
	// Candidates are offered for removal: every kind of item, including
	// workspaces whose Class.Tier is TierRemove or TierDecide.
	Candidates []Candidate
	// Kept are workspaces classified TierKeep, reported but never offered.
	Kept []Candidate
}

// classifyWorkers bounds how many workspaces are classified at once, across
// all repos. A classification is mostly one jj process at a time.
func classifyWorkers() int { return max(4, runtime.NumCPU()) }

// repoWorkers bounds how many repos are scanned at once.
const repoWorkers = 6

// NewScanClassifier returns the classifier FindCandidates uses: jj reads that
// never snapshot a workspace, GitHub when it is reachable, and the idle
// threshold from status.stale_days. It returns nil when jjc cannot serve the
// classifier's reads.
func NewScanClassifier(cfg *config.Config, jjc jj.Client, ghClient github.Client) *Classifier {
	var r Reader
	switch v := jjc.(type) {
	case *jj.CLIClient:
		r = v.ReadOnly()
	case Reader:
		r = v
	default:
		return nil
	}
	var prs PRSource
	if ghClient != nil && ghClient.Available() {
		prs, _ = ghClient.(PRSource)
	}
	return NewClassifier(r, prs, ClassifyOptions{
		StaleAfter: time.Duration(cfg.Status.StaleDays) * 24 * time.Hour,
	})
}

// FindCandidates discovers cleanup candidates across tracked repos. With kinds
// it scans only for those: workspaces (KindWorktree, KindOrphanWorkspace)
// and bookmarks (KindLocalBranch, KindRemoteBranch) are separate passes, and
// the bookmark pass costs GitHub calls per bookmark. No kinds means all.
func FindCandidates(cfg *config.Config, jjc jj.Client, ghClient github.Client, kinds ...CandidateKind) (Scan, error) {
	disc := discovery.FromConfig(cfg)
	repos, err := disc.DiscoverAll()
	if err != nil {
		return Scan{}, fmt.Errorf("discovery failed: %w", err)
	}

	wants := func(ks ...CandidateKind) bool {
		if len(kinds) == 0 {
			return true
		}
		for _, k := range ks {
			if slices.Contains(kinds, k) {
				return true
			}
		}
		return false
	}
	pass := repoPass{
		workspaces: wants(KindWorktree, KindOrphanWorkspace),
		bookmarks:  wants(KindLocalBranch, KindRemoteBranch),
		rigDir:     cfg.Rig.Dir,
	}
	if c := NewScanClassifier(cfg, jjc, ghClient); c != nil {
		pass.classify = c.Classify
	}

	// Only process main workspaces (not secondary workspaces found during discovery)
	var scan Scan
	seen := map[string]bool{}
	var mains []string
	for _, repo := range repos {
		if repo.IsWorktree || seen[repo.Path] {
			continue
		}
		seen[repo.Path] = true
		mains = append(mains, repo.Path)
	}

	// Repos are scanned concurrently — each spends most of its time waiting
	// on jj and GitHub — while their workspaces share one pool of classifier
	// workers. Results keep the discovery order.
	pass.workers = make(chan struct{}, classifyWorkers())
	type repoResult struct{ found, kept []Candidate }
	results := make([]repoResult, len(mains))
	repoSlots := make(chan struct{}, repoWorkers)
	var wg sync.WaitGroup
	for i, path := range mains {
		wg.Add(1)
		repoSlots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-repoSlots }()
			found, kept, err := findRepoCandidate(path, jjc, ghClient, pass)
			if err == nil {
				results[i] = repoResult{found, kept}
			}
		}()
	}
	wg.Wait()
	for _, r := range results {
		scan.Candidates = append(scan.Candidates, r.found...)
		scan.Kept = append(scan.Kept, r.kept...)
	}

	return scan, nil
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

// repoPass says what findRepoCandidate scans in each repo.
type repoPass struct {
	workspaces bool // classify workspaces and find orphans
	bookmarks  bool // check local bookmarks and their remote branches
	// classify judges each live workspace; nil means workspaces cannot be
	// judged and are all kept.
	classify func(WorkspaceRef) Classification
	// workers is the classifier pool shared by every repo; nil means a pool
	// of this repo's own.
	workers chan struct{}
	// rigDir is needed here, and not just in discovery, because workspaces
	// are reached through ListWorkspaces on the main clone rather than
	// through a filesystem walk: keeping rig.dir out of discovery hides the
	// rig's directories but not its workspaces.
	rigDir string
}

// findRepoCandidate inspects one main repo.
func findRepoCandidate(repoPath string, jjc jj.Client, ghClient github.Client, pass repoPass) (candidates, kept []Candidate, err error) {
	defaultBranch := repoDefaultBranch(jjc, repoPath)

	// --- Workspaces (the jj equivalent of git worktrees) ---
	// The workspace list is read even for a bookmark-only pass: a bookmark on
	// a workspace's @ is left to the workspace.
	workspaces, err := jjc.ListWorkspaces(repoPath)
	// onWorkspace holds the commit each non-default workspace's @ sits on, so
	// a bookmark there is left to the workspace rather than offered alone.
	onWorkspace := map[string]bool{}
	if err == nil {
		var live []jj.Workspace
		for _, ws := range workspaces {
			if ws.Name == "default" {
				continue
			}
			if ws.CommitID != "" {
				onWorkspace[ws.CommitID] = true
			}
			if !pass.workspaces {
				continue
			}
			// A rig workspace is pinned to a released tag and deliberately
			// carries no bookmark, which is exactly the shape the checks below
			// read as an abandoned worktree. Rigs are owned by `wgo rig rm`.
			if rig.IsWorkspace(pass.rigDir, ws.Path, ws.Name) {
				continue
			}
			// An orphaned workspace has no working copy to read, so it must be
			// caught before it is classified: reading "" would read whichever
			// repo wgo was run from. One that cannot be checked at all is left
			// alone rather than guessed at.
			reason, orphaned, err := jj.OrphanReason(ws)
			if err != nil {
				continue
			}
			if orphaned {
				candidates = append(candidates, Candidate{
					Kind:          KindOrphanWorkspace,
					Path:          ws.Path,
					RepoPath:      repoPath,
					WorkspaceName: ws.Name,
					Reason:        reason,
				})
				continue
			}
			live = append(live, ws)
		}

		refs := make([]WorkspaceRef, len(live))
		for i, ws := range live {
			refs[i] = WorkspaceRef{Name: ws.Name, Path: ws.Path, RepoPath: repoPath, DefaultBranch: defaultBranch}
		}
		for i, cl := range classifyAll(pass.classify, refs, pass.workers) {
			c := Candidate{
				Kind:          KindWorktree,
				Path:          live[i].Path,
				RepoPath:      repoPath,
				WorkspaceName: live[i].Name,
				Reason:        cl.Reason(),
				PRInfo:        cl.PR,
				Class:         &cl,
			}
			if len(cl.Bookmarks) > 0 {
				c.Branch = cl.Bookmarks[0]
			}
			if cl.Tier == TierKeep {
				kept = append(kept, c)
			} else {
				candidates = append(candidates, c)
			}
		}
	}

	if !pass.bookmarks {
		return candidates, kept, nil
	}

	// --- Local bookmarks ---
	localBookmarks, err := jjc.BookmarkList(repoPath, jj.BookmarkListOpts{Local: true})
	if err != nil {
		return candidates, kept, nil
	}

	for _, bm := range localBookmarks {
		branch := bm.Name
		if branch == defaultBranch {
			continue
		}
		if bm.CommitID != "" && onWorkspace[bm.CommitID] {
			// The bookmark is on a workspace's @; the workspace decides.
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

	return candidates, kept, nil
}

// classifyAll classifies refs concurrently, at most cap(workers) at a time,
// keeping their order.
func classifyAll(classify func(WorkspaceRef) Classification, refs []WorkspaceRef, workers chan struct{}) []Classification {
	out := make([]Classification, len(refs))
	if classify == nil {
		for i := range out {
			out[i] = Classification{Type: TypeUnreadable, Tier: TierKeep,
				Evidence: []string{"this jj client cannot classify workspaces"}}
		}
		return out
	}
	sem := workers
	if sem == nil {
		sem = make(chan struct{}, classifyWorkers())
	}
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = classify(ref)
		}()
	}
	wg.Wait()
	return out
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

// FormatAge renders how long ago t was: "today", "yesterday", "3d ago",
// "5w ago".
func FormatAge(t time.Time) string { return formatAge(t) }

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
	if c.Kind == KindOrphanWorkspace {
		return c.RepoPath + " [workspace " + c.WorkspaceName + "]"
	}
	if c.RepoPath != "" && c.Branch != "" {
		return c.RepoPath + " [" + c.Branch + "]"
	}
	return c.Branch
}
