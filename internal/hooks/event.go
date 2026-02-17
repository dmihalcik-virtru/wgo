package hooks

import (
	"path/filepath"

	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

// EventConfig controls event processing behavior.
type EventConfig struct {
	AutoPlan        bool
	ExcludeBranches []string
}

// EventProcessor handles git hook events by updating state and plan.
type EventProcessor struct {
	store  *store.FileStore
	git    git.Client
	config *EventConfig
}

// NewEventProcessor creates a new EventProcessor.
func NewEventProcessor(s *store.FileStore, g git.Client, cfg *EventConfig) *EventProcessor {
	return &EventProcessor{
		store:  s,
		git:    g,
		config: cfg,
	}
}

// HandlePostCheckout handles the post-checkout hook.
// branchFlag is "1" for branch checkout, "0" for file checkout.
func (p *EventProcessor) HandlePostCheckout(repoPath, prevRef, newRef, branchFlag string) error {
	lock := NewFileLock(p.store.BaseDir())
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()

	// Update LastSeen
	remoteURL, _ := p.git.RemoteURL(repoPath)
	state, err := p.store.LoadState()
	if err != nil {
		return err
	}
	state.AddRepo(repoPath, remoteURL)

	if err := p.store.SaveState(state); err != nil {
		return err
	}

	// Only auto-add to plan on branch checkout
	if branchFlag != "1" || !p.config.AutoPlan {
		return nil
	}

	branch, err := p.git.CurrentBranch(repoPath)
	if err != nil {
		return nil // silently skip
	}

	if shouldExclude(branch, p.config.ExcludeBranches) {
		return nil
	}

	repoName, err := p.git.RepoName(repoPath)
	if err != nil {
		return nil
	}

	// Load plan and check if branch already exists
	content, err := p.store.LoadPlan()
	if err != nil {
		return nil
	}

	planFile, err := plan.Parse(content)
	if err != nil {
		return nil
	}

	if planFile.GetBranch(repoName, branch) != nil {
		return nil // already tracked
	}

	planFile.AddBranch(repoName, branch, "(auto-tracked)")
	return p.store.SavePlan(planFile.Render())
}

// HandlePostCommit handles the post-commit hook.
func (p *EventProcessor) HandlePostCommit(repoPath string) error {
	return p.updateLastSeen(repoPath)
}

// HandlePostMerge handles the post-merge hook.
func (p *EventProcessor) HandlePostMerge(repoPath, squashFlag string) error {
	return p.updateLastSeen(repoPath)
}

// HandlePostRewrite handles the post-rewrite hook (rebase, amend).
func (p *EventProcessor) HandlePostRewrite(repoPath, command string) error {
	return p.updateLastSeen(repoPath)
}

// updateLastSeen updates LastSeen for the repo under lock.
func (p *EventProcessor) updateLastSeen(repoPath string) error {
	lock := NewFileLock(p.store.BaseDir())
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()

	remoteURL, _ := p.git.RemoteURL(repoPath)
	state, err := p.store.LoadState()
	if err != nil {
		return err
	}
	state.AddRepo(repoPath, remoteURL)
	return p.store.SaveState(state)
}

// shouldExclude returns true if branch matches any exclude pattern.
func shouldExclude(branch string, patterns []string) bool {
	for _, pattern := range patterns {
		if matched, _ := filepath.Match(pattern, branch); matched {
			return true
		}
	}
	return false
}
