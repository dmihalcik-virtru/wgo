package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

// newTestStore creates a FileStore in a temp directory.
func newTestStore(t *testing.T) *store.FileStore {
	t.Helper()
	dir := t.TempDir()
	wgoDir := filepath.Join(dir, ".wgo")
	// We need to create a FileStore pointing at our temp dir.
	// Since store.New() hardcodes ~/.wgo, we'll construct one manually
	// by using a helper approach: create state dir and files.
	if err := os.MkdirAll(wgoDir, 0o755); err != nil {
		t.Fatalf("failed to create test wgo dir: %v", err)
	}
	return store.NewWithDir(wgoDir)
}

// initTestRepo creates a real git repo in a temp directory.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "initial"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestHandlePostCheckout_BranchCheckout_AddsToplan(t *testing.T) {
	s := newTestStore(t)
	repoPath := initTestRepo(t)

	// Create a branch
	cmd := exec.Command("git", "checkout", "-b", "feat/test-branch")
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkout failed: %v\n%s", err, out)
	}

	gitClient := git.New(repoPath)
	cfg := &EventConfig{
		AutoPlan:        true,
		ExcludeBranches: []string{"main", "master"},
	}

	processor := NewEventProcessor(s, gitClient, cfg)
	if err := processor.HandlePostCheckout(repoPath, "abc123", "def456", "1"); err != nil {
		t.Fatalf("HandlePostCheckout failed: %v", err)
	}

	// Verify state was updated
	state, err := s.LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if _, ok := state.Repos[repoPath]; !ok {
		t.Error("repo not added to state")
	}

	// Verify plan was updated
	content, err := s.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan failed: %v", err)
	}
	if !strings.Contains(content, "feat/test-branch") {
		t.Errorf("branch not added to plan, got:\n%s", content)
	}
	if !strings.Contains(content, "(auto-tracked)") {
		t.Errorf("expected auto-tracked reason, got:\n%s", content)
	}
}

func TestHandlePostCheckout_ExcludedBranch_NotAddedToPlan(t *testing.T) {
	s := newTestStore(t)
	repoPath := initTestRepo(t)

	gitClient := git.New(repoPath)
	cfg := &EventConfig{
		AutoPlan:        true,
		ExcludeBranches: []string{"main", "master"},
	}

	processor := NewEventProcessor(s, gitClient, cfg)
	// Simulate checkout of main (excluded)
	if err := processor.HandlePostCheckout(repoPath, "abc123", "def456", "1"); err != nil {
		t.Fatalf("HandlePostCheckout failed: %v", err)
	}

	content, err := s.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan failed: %v", err)
	}

	p, _ := plan.Parse(content)
	if len(p.ActiveBranches) > 0 {
		t.Errorf("excluded branch should not be in plan, got %d branches", len(p.ActiveBranches))
	}
}

func TestHandlePostCheckout_FileCheckout_NoAutoAdd(t *testing.T) {
	s := newTestStore(t)
	repoPath := initTestRepo(t)

	// Create a branch that would normally be added
	cmd := exec.Command("git", "checkout", "-b", "feat/should-not-add")
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkout failed: %v\n%s", err, out)
	}

	gitClient := git.New(repoPath)
	cfg := &EventConfig{
		AutoPlan:        true,
		ExcludeBranches: []string{"main"},
	}

	processor := NewEventProcessor(s, gitClient, cfg)
	// branchFlag "0" = file checkout, should not add to plan
	if err := processor.HandlePostCheckout(repoPath, "abc", "def", "0"); err != nil {
		t.Fatalf("HandlePostCheckout failed: %v", err)
	}

	content, err := s.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan failed: %v", err)
	}

	p, _ := plan.Parse(content)
	if len(p.ActiveBranches) > 0 {
		t.Errorf("file checkout should not add branches to plan, got %d", len(p.ActiveBranches))
	}
}

func TestHandlePostCheckout_AutoPlanDisabled(t *testing.T) {
	s := newTestStore(t)
	repoPath := initTestRepo(t)

	cmd := exec.Command("git", "checkout", "-b", "feat/no-auto")
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkout failed: %v\n%s", err, out)
	}

	gitClient := git.New(repoPath)
	cfg := &EventConfig{
		AutoPlan:        false,
		ExcludeBranches: []string{"main"},
	}

	processor := NewEventProcessor(s, gitClient, cfg)
	if err := processor.HandlePostCheckout(repoPath, "abc", "def", "1"); err != nil {
		t.Fatalf("HandlePostCheckout failed: %v", err)
	}

	content, err := s.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan failed: %v", err)
	}

	p, _ := plan.Parse(content)
	if len(p.ActiveBranches) > 0 {
		t.Errorf("auto_plan=false should not add branches, got %d", len(p.ActiveBranches))
	}
}

func TestHandlePostCommit_UpdatesLastSeen(t *testing.T) {
	s := newTestStore(t)
	repoPath := initTestRepo(t)

	gitClient := git.New(repoPath)
	cfg := &EventConfig{AutoPlan: true}

	processor := NewEventProcessor(s, gitClient, cfg)
	if err := processor.HandlePostCommit(repoPath); err != nil {
		t.Fatalf("HandlePostCommit failed: %v", err)
	}

	state, err := s.LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if _, ok := state.Repos[repoPath]; !ok {
		t.Error("repo not added to state after post-commit")
	}
}

func TestHandlePostMerge_UpdatesLastSeen(t *testing.T) {
	s := newTestStore(t)
	repoPath := initTestRepo(t)

	gitClient := git.New(repoPath)
	cfg := &EventConfig{AutoPlan: true}

	processor := NewEventProcessor(s, gitClient, cfg)
	if err := processor.HandlePostMerge(repoPath, "0"); err != nil {
		t.Fatalf("HandlePostMerge failed: %v", err)
	}

	state, err := s.LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if _, ok := state.Repos[repoPath]; !ok {
		t.Error("repo not added to state after post-merge")
	}
}

func TestHandlePostRewrite_UpdatesLastSeen(t *testing.T) {
	s := newTestStore(t)
	repoPath := initTestRepo(t)

	gitClient := git.New(repoPath)
	cfg := &EventConfig{AutoPlan: true}

	processor := NewEventProcessor(s, gitClient, cfg)
	if err := processor.HandlePostRewrite(repoPath, "rebase"); err != nil {
		t.Fatalf("HandlePostRewrite failed: %v", err)
	}

	state, err := s.LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if _, ok := state.Repos[repoPath]; !ok {
		t.Error("repo not added to state after post-rewrite")
	}
}

func TestShouldExclude(t *testing.T) {
	patterns := []string{"main", "master", "develop", "release/*"}

	tests := []struct {
		branch string
		want   bool
	}{
		{"main", true},
		{"master", true},
		{"develop", true},
		{"release/1.0", true},
		{"feat/new-thing", false},
		{"fix/bug-123", false},
		{"release", false},
	}

	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			got := shouldExclude(tt.branch, patterns)
			if got != tt.want {
				t.Errorf("shouldExclude(%q) = %v, want %v", tt.branch, got, tt.want)
			}
		})
	}
}

func TestHandlePostCheckout_DuplicateBranch_NotDuplicated(t *testing.T) {
	s := newTestStore(t)
	repoPath := initTestRepo(t)

	cmd := exec.Command("git", "checkout", "-b", "feat/dup-test")
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkout failed: %v\n%s", err, out)
	}

	gitClient := git.New(repoPath)
	cfg := &EventConfig{
		AutoPlan:        true,
		ExcludeBranches: []string{"main"},
	}

	processor := NewEventProcessor(s, gitClient, cfg)

	// First checkout
	if err := processor.HandlePostCheckout(repoPath, "a", "b", "1"); err != nil {
		t.Fatalf("first HandlePostCheckout failed: %v", err)
	}

	// Second checkout of same branch
	if err := processor.HandlePostCheckout(repoPath, "a", "b", "1"); err != nil {
		t.Fatalf("second HandlePostCheckout failed: %v", err)
	}

	content, err := s.LoadPlan()
	if err != nil {
		t.Fatalf("LoadPlan failed: %v", err)
	}

	// Count occurrences of the branch
	count := strings.Count(content, "feat/dup-test")
	if count != 1 {
		t.Errorf("branch appears %d times, want 1:\n%s", count, content)
	}
}
