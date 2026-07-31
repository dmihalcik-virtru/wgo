// Package stack reconstructs the pull-request stack a given PR belongs to by
// walking base refs via the GitHub API. It depends on neither GitHub's native
// Stack object nor wgo's own wgo-stack marker, so it works regardless of how
// the author built the stack (wgo, gh stack, or plain git/gh). It is shared by
// the consumer side (`wgo to`, which checks out the whole stack) and can be
// reused by the publish side.
package stack

import (
	"sort"

	"github.com/virtru/wgo/internal/github"
)

// PRRef seeds stack resolution. Number is preferred; when it is zero, Branch is
// resolved to the first open PR whose head matches.
type PRRef struct {
	Number int
	Branch string
}

// StackMember is one PR in a resolved stack, carrying enough to fetch and check
// out its head branch.
type StackMember struct {
	Branch   string // head ref name
	PRNumber int
	Base     string // base ref name
	HeadSlug string // head repo "owner/repo"; differs from base repo for fork PRs
	HeadOID  string // head commit sha, for the pin-vs-track fallback on checkout
}

// GitHub is the narrow API surface ResolveStack needs. *github.CLIClient
// satisfies it.
type GitHub interface {
	// GetPRByNumber fetches a PR by number.
	GetPRByNumber(repoPath string, number int) (*github.PRInfo, error)
	// GetPRStatus returns the first OPEN PR whose head branch matches, or nil.
	GetPRStatus(repoPath, branch string) (*github.PRInfo, error)
	// ListPRsByBase returns OPEN PRs whose base branch matches (stack children).
	ListPRsByBase(repoPath, base string) ([]github.PRInfo, error)
}

// ResolveStack reconstructs the stack (bottom→top) containing the seed PR.
//
// It walks DOWN from the seed via base refs toward trunk (each ancestor is the
// open PR whose head equals the child's base), and UP via a breadth-first search
// for open PRs whose base equals a member's head. A lone PR yields a
// single-element slice. If the seed resolves to no PR, it returns (nil, nil) so
// callers can fall back to a single-PR path.
func ResolveStack(gh GitHub, repoPath string, seed PRRef) ([]StackMember, error) {
	seedPR, err := resolveSeed(gh, repoPath, seed)
	if err != nil {
		return nil, err
	}
	if seedPR == nil {
		return nil, nil
	}

	visited := map[int]bool{}

	// Walk down: seed first, then trunkward ancestors.
	var down []StackMember
	for cur := seedPR; cur != nil && !visited[cur.Number]; {
		visited[cur.Number] = true
		down = append(down, memberOf(cur))
		base := cur.BaseRefName
		if base == "" || base == cur.Branch { // self-reference guard
			break
		}
		parent, perr := gh.GetPRStatus(repoPath, base)
		if perr != nil || parent == nil { // base is trunk or has no open PR
			break
		}
		cur = parent
	}

	// Walk up: descendants whose base == a member's head, BFS over the subtree.
	var up []StackMember
	queue := []string{seedPR.Branch}
	for len(queue) > 0 {
		head := queue[0]
		queue = queue[1:]
		kids, kerr := gh.ListPRsByBase(repoPath, head)
		if kerr != nil {
			continue
		}
		sort.Slice(kids, func(i, j int) bool { return kids[i].Number < kids[j].Number })
		for i := range kids {
			k := &kids[i]
			if visited[k.Number] {
				continue // cycle / diamond guard
			}
			visited[k.Number] = true
			up = append(up, memberOf(k))
			queue = append(queue, k.Branch)
		}
	}

	// Assemble bottom→top: reverse the trunkward down-list, then children.
	out := make([]StackMember, 0, len(down)+len(up))
	for i := len(down) - 1; i >= 0; i-- {
		out = append(out, down[i])
	}
	out = append(out, up...)
	return out, nil
}

func resolveSeed(gh GitHub, repoPath string, seed PRRef) (*github.PRInfo, error) {
	if seed.Number != 0 {
		return gh.GetPRByNumber(repoPath, seed.Number)
	}
	if seed.Branch != "" {
		return gh.GetPRStatus(repoPath, seed.Branch)
	}
	return nil, nil
}

func memberOf(p *github.PRInfo) StackMember {
	return StackMember{
		Branch:   p.Branch,
		PRNumber: p.Number,
		Base:     p.BaseRefName,
		HeadSlug: p.HeadRepoSlug,
		HeadOID:  p.HeadSHA,
	}
}
