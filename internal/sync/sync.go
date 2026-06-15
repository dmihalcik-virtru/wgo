// Package sync walks the per-repo jj DAG and aligns GitHub PR bases plus
// the wgo-stack marker block embedded in each PR body.
package sync

import (
	"fmt"
	"strings"

	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
)

// JJOps is the jj surface the sync algorithm needs. Mocked in tests.
type JJOps interface {
	GitFetch(repo, remote string, refs []string) error
	Log(repo, revset string) ([]jj.LogEntry, error)
	RemoteURLs(path string) (map[string]string, error)
}

// GitHubOps is the github surface the sync algorithm needs. Mocked in tests.
type GitHubOps interface {
	GetPRStatus(repoPath, branch string) (*github.PRInfo, error)
	UpdatePRBase(repoPath string, prNumber int, baseBranch string) error
	GetPRBody(repoPath string, prNumber int) (string, error)
	UpdatePRBody(repoPath string, prNumber int, body string) error
}

// Options controls a single Sync run.
type Options struct {
	// DefaultBase is the bookmark or remote ref new roots target when no
	// in-graph ancestor has an open PR. Typically the repo's default branch
	// (e.g. "main"). If empty, sync leaves the existing PR base alone.
	DefaultBase string
	// Fetch causes sync to run `jj git fetch` before reading the DAG. Set to
	// false in tests or when callers have already fetched.
	Fetch bool
	// DryRun reports what would change without calling GitHub mutators.
	DryRun bool
}

// Result describes the work sync performed (or would have performed in dry-run).
type Result struct {
	BaseChanges   []BaseChange
	MarkerUpdates []MarkerUpdate
	Skipped       []string // bookmarks with no open PR
}

// BaseChange records a PR base retarget.
type BaseChange struct {
	Bookmark string
	PR       int
	OldBase  string
	NewBase  string
}

// MarkerUpdate records a PR body marker refresh.
type MarkerUpdate struct {
	Bookmark string
	PR       int
}

// Sync runs the per-repo PR base alignment + marker regeneration algorithm
// described in spec/gh-21.md against `repo`. It mutates GitHub state (unless
// DryRun is set) but never touches local jj state — jj auto-restacks
// descendants whenever an ancestor commit changes, so there is no rebase
// work for sync to do.
func Sync(jjc JJOps, ghc GitHubOps, repo string, opts Options) (*Result, error) {
	if opts.Fetch {
		if err := jjc.GitFetch(repo, "origin", nil); err != nil {
			return nil, fmt.Errorf("jj git fetch: %w", err)
		}
	}

	entries, err := jjc.Log(repo, "bookmarks() & ::heads()")
	if err != nil {
		return nil, fmt.Errorf("jj log: %w", err)
	}
	graph, err := BuildFromLog(entries)
	if err != nil {
		return nil, err
	}

	prs := make(map[string]*github.PRInfo, len(graph.Nodes))
	for bm := range graph.Nodes {
		pr, err := ghc.GetPRStatus(repo, bm)
		if err != nil {
			continue
		}
		if pr != nil {
			prs[bm] = pr
		}
	}

	result := &Result{}
	hasPR := func(name string) bool { _, ok := prs[name]; return ok }

	order, err := graph.TopoSort()
	if err != nil {
		return nil, err
	}

	for _, bm := range order {
		pr := prs[bm]
		if pr == nil {
			result.Skipped = append(result.Skipped, bm)
			continue
		}

		desired := graph.NearestAncestorWith(bm, hasPR)
		if desired == "" {
			desired = opts.DefaultBase
		}
		if desired != "" && pr.BaseRefName != desired {
			result.BaseChanges = append(result.BaseChanges, BaseChange{
				Bookmark: bm, PR: pr.Number, OldBase: pr.BaseRefName, NewBase: desired,
			})
			if !opts.DryRun {
				if err := ghc.UpdatePRBase(repo, pr.Number, desired); err != nil {
					return result, fmt.Errorf("update PR #%d base: %w", pr.Number, err)
				}
			}
		}
	}

	for _, bm := range order {
		pr := prs[bm]
		if pr == nil {
			continue
		}
		newBody, changed, err := refreshMarker(ghc, repo, pr, bm, graph, prs)
		if err != nil {
			return result, fmt.Errorf("refresh marker for #%d: %w", pr.Number, err)
		}
		if !changed {
			continue
		}
		result.MarkerUpdates = append(result.MarkerUpdates, MarkerUpdate{Bookmark: bm, PR: pr.Number})
		if !opts.DryRun {
			if err := ghc.UpdatePRBody(repo, pr.Number, newBody); err != nil {
				return result, fmt.Errorf("update PR #%d body: %w", pr.Number, err)
			}
		}
	}

	return result, nil
}

// refreshMarker computes the new PR body for `bookmark` with an up-to-date
// stack marker. Returns (newBody, changed, err); changed is false when the
// PR body already matches the rendered marker.
func refreshMarker(ghc GitHubOps, repo string, pr *github.PRInfo, bookmark string, g *Graph, prs map[string]*github.PRInfo) (string, bool, error) {
	body, err := ghc.GetPRBody(repo, pr.Number)
	if err != nil {
		return "", false, err
	}

	stackID := ExtractStackID(body)
	if stackID == "" {
		stackID = bookmark
	}

	nodes := make([]MarkerNode, 0, len(g.Nodes))
	branchByKey := map[string]string{}
	prByKey := map[string]int{}
	for _, n := range g.Nodes {
		key := n.Bookmark // single-repo: key == bookmark name
		nodes = append(nodes, MarkerNode{
			Key: key, Branch: n.Bookmark, Parents: n.Parents,
			PRNumber: prNumberOf(prs[n.Bookmark]),
		})
		branchByKey[key] = n.Bookmark
		if p, ok := prs[n.Bookmark]; ok {
			prByKey[key] = p.Number
		}
	}

	marker := Marker{StackID: stackID, Self: bookmark, Nodes: nodes}
	rendered := marker.Render()
	updated := ApplyMarker(body, rendered)
	return updated, !strings.EqualFold(strings.TrimSpace(updated), strings.TrimSpace(body)), nil
}

func prNumberOf(pr *github.PRInfo) int {
	if pr == nil {
		return 0
	}
	return pr.Number
}
