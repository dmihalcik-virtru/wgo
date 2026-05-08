package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/spec"
	"github.com/virtru/wgo/internal/store"
)

// EventConfig controls event processing behavior.
type EventConfig struct {
	AutoPlan            bool
	ExcludeBranches     []string
	SpecRequired        bool
	SpecRequiredMinLines int
}

// PreCommitContext carries the inputs needed to evaluate a pre-commit event.
type PreCommitContext struct {
	RepoRoot    string
	Branch      string
	StagedFiles []string
	MsgFile     string // path to .git/COMMIT_EDITMSG
}

// PreCommitDecision is the result of HandlePreCommit.
type PreCommitDecision struct {
	Allow  bool
	Reason string
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

// HandlePreCommit evaluates whether a commit should be allowed based on spec enforcement rules.
// Returns Allow=true immediately when spec_required is false or any allowance rule is satisfied.
func (p *EventProcessor) HandlePreCommit(ctx PreCommitContext) (PreCommitDecision, error) {
	allow := func(reason string) (PreCommitDecision, error) {
		return PreCommitDecision{Allow: true, Reason: reason}, nil
	}

	if !p.config.SpecRequired {
		return allow("spec_required=false")
	}
	if ctx.Branch == "HEAD" {
		return allow("detached HEAD")
	}
	if shouldExclude(ctx.Branch, p.config.ExcludeBranches) {
		return allow("excluded branch")
	}

	// Spec-only diff: all staged files are under spec/
	if len(ctx.StagedFiles) > 0 {
		allSpec := true
		for _, f := range ctx.StagedFiles {
			if !strings.HasPrefix(f, "spec/") {
				allSpec = false
				break
			}
		}
		if allSpec {
			return allow("spec-only diff")
		}
	}

	// Read commit message if available
	var commitMsg string
	if ctx.MsgFile != "" {
		if data, err := os.ReadFile(ctx.MsgFile); err == nil {
			commitMsg = string(data)
		}
	}

	// [no-spec] escape hatch anywhere in the message
	if strings.Contains(commitMsg, "[no-spec]") {
		return allow("[no-spec] in commit message")
	}

	// "Spec: spec/..." reference line in message
	for _, line := range strings.Split(commitMsg, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Spec: spec/") {
			return allow("Spec: reference in commit message")
		}
	}

	// Small diff: count total changed lines
	if p.config.SpecRequiredMinLines > 0 {
		if n, err := countStagedLines(ctx.RepoRoot); err == nil && n <= p.config.SpecRequiredMinLines {
			return allow(fmt.Sprintf("diff ≤ %d lines", p.config.SpecRequiredMinLines))
		}
	}

	// Annotation has SpecPath recorded
	if s, err := p.store.LoadState(); err == nil {
		if ann := s.GetAnnotation(ctx.RepoRoot, ctx.Branch); ann != nil && ann.SpecPath != "" {
			if _, statErr := os.Stat(ann.SpecPath); statErr == nil {
				return allow("spec path in annotation")
			}
		}
	}

	// Spec file discoverable on disk from branch name
	if _, err := spec.FindByBranch(ctx.RepoRoot, ctx.Branch); err == nil {
		return allow("spec file found on disk")
	}

	ticket := spec.ParseTicketFromBranch(ctx.Branch)
	ticketHint := ctx.Branch
	if ticket != "" {
		ticketHint = ticket
	}
	msg := fmt.Sprintf(`✗ commit blocked: branch %s has no spec.

Options:
  • Create one:  wgo spec new %s
  • Reference one in this commit message:
        Spec: spec/%s.md
  • Skip for this commit (e.g., emergency):
        git commit -m "your message [no-spec]"
  • Disable globally:  set [hooks] spec_required = false in ~/.wgo/config.toml
`, ctx.Branch, ticketHint, ticketHint)
	return PreCommitDecision{Allow: false, Reason: msg}, nil
}

// countStagedLines returns the total number of added+removed lines in the staged diff.
func countStagedLines(repoRoot string) (int, error) {
	cmd := exec.Command("git", "diff", "--cached", "--numstat")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	total := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		added, _ := strconv.Atoi(parts[0])
		removed, _ := strconv.Atoi(parts[1])
		total += added + removed
	}
	return total, nil
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
