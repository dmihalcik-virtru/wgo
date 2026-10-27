package cleanup

import (
	"strconv"
	"sync"

	"github.com/virtru/wgo/internal/github"
)

// PRSource is the GitHub surface the classifier needs. *github.CLIClient
// satisfies it. A nil PRSource means no GitHub: PR states and the commit
// identity proof are skipped and everything else still runs.
type PRSource interface {
	ListPRsForBranch(repoPath, branch string) ([]github.PRInfo, error)
	GetPRByNumber(repoPath string, number int) (*github.PRInfo, error)
	ListPRCommits(repoPath string, number int) ([]github.PRCommit, error)
}

// memoPRs caches PRSource answers for one scan. Workspaces of one main clone
// share bookmarks and PRs, and a scan classifies them concurrently, so the
// same question is often asked more than once at the same time. Errors are
// cached too: a lookup that failed once fails the same way for the rest of
// the scan rather than costing another round trip per workspace.
type memoPRs struct {
	src     PRSource
	mu      sync.Mutex
	branch  map[string]prsResult
	number  map[string]prResult
	commits map[string]commitsResult
}

type prsResult struct {
	prs []github.PRInfo
	err error
}

type prResult struct {
	pr  *github.PRInfo
	err error
}

type commitsResult struct {
	commits []github.PRCommit
	err     error
}

func newMemoPRs(src PRSource) *memoPRs {
	return &memoPRs{
		src:     src,
		branch:  map[string]prsResult{},
		number:  map[string]prResult{},
		commits: map[string]commitsResult{},
	}
}

func (m *memoPRs) ListPRsForBranch(repoPath, branch string) ([]github.PRInfo, error) {
	key := repoPath + "\x00" + branch
	m.mu.Lock()
	r, ok := m.branch[key]
	m.mu.Unlock()
	if !ok {
		r.prs, r.err = m.src.ListPRsForBranch(repoPath, branch)
		m.mu.Lock()
		m.branch[key] = r
		m.mu.Unlock()
	}
	return r.prs, r.err
}

func (m *memoPRs) GetPRByNumber(repoPath string, number int) (*github.PRInfo, error) {
	key := repoPath + "\x00" + strconv.Itoa(number)
	m.mu.Lock()
	r, ok := m.number[key]
	m.mu.Unlock()
	if !ok {
		r.pr, r.err = m.src.GetPRByNumber(repoPath, number)
		m.mu.Lock()
		m.number[key] = r
		m.mu.Unlock()
	}
	return r.pr, r.err
}

func (m *memoPRs) ListPRCommits(repoPath string, number int) ([]github.PRCommit, error) {
	key := repoPath + "\x00" + strconv.Itoa(number)
	m.mu.Lock()
	r, ok := m.commits[key]
	m.mu.Unlock()
	if !ok {
		r.commits, r.err = m.src.ListPRCommits(repoPath, number)
		m.mu.Lock()
		m.commits[key] = r
		m.mu.Unlock()
	}
	return r.commits, r.err
}
