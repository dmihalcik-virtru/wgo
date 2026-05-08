package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/git"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

// newPreCommitProcessor returns an EventProcessor with spec_required=true for pre-commit tests.
func newPreCommitProcessor(t *testing.T, repoPath string) (*EventProcessor, *store.FileStore) {
	t.Helper()
	s := newTestStore(t)
	g := git.New(repoPath)
	cfg := &EventConfig{
		SpecRequired:         true,
		SpecRequiredMinLines: 5,
		ExcludeBranches:      []string{"main", "master"},
	}
	return NewEventProcessor(s, g, cfg), s
}

func TestHandlePreCommit_SpecRequiredFalse_Allows(t *testing.T) {
	repoPath := initTestRepo(t)
	s := newTestStore(t)
	g := git.New(repoPath)
	cfg := &EventConfig{SpecRequired: false}
	p := NewEventProcessor(s, g, cfg)

	d, err := p.HandlePreCommit(PreCommitContext{RepoRoot: repoPath, Branch: "WGO-1-feature"})
	require.NoError(t, err)
	assert.True(t, d.Allow, "expected Allow=true when spec_required=false, got reason: %s", d.Reason)
}

func TestHandlePreCommit_DetachedHEAD_Allows(t *testing.T) {
	repoPath := initTestRepo(t)
	p, _ := newPreCommitProcessor(t, repoPath)

	d, err := p.HandlePreCommit(PreCommitContext{RepoRoot: repoPath, Branch: "HEAD"})
	require.NoError(t, err)
	assert.True(t, d.Allow, "expected Allow=true for detached HEAD, got: %s", d.Reason)
}

func TestHandlePreCommit_ExcludedBranch_Allows(t *testing.T) {
	repoPath := initTestRepo(t)
	p, _ := newPreCommitProcessor(t, repoPath)

	d, err := p.HandlePreCommit(PreCommitContext{RepoRoot: repoPath, Branch: "main"})
	require.NoError(t, err)
	assert.True(t, d.Allow, "expected Allow=true for excluded branch, got: %s", d.Reason)
}

func TestHandlePreCommit_SpecOnlyDiff_Allows(t *testing.T) {
	repoPath := initTestRepo(t)
	p, _ := newPreCommitProcessor(t, repoPath)

	d, err := p.HandlePreCommit(PreCommitContext{
		RepoRoot:    repoPath,
		Branch:      "WGO-1-feature",
		StagedFiles: []string{"spec/WGO-1.md", "spec/WGO-2.md"},
	})
	require.NoError(t, err)
	assert.True(t, d.Allow, "expected Allow=true for spec-only diff, got: %s", d.Reason)
}

func TestHandlePreCommit_NoSpecInMessage_Allows(t *testing.T) {
	repoPath := initTestRepo(t)
	p, _ := newPreCommitProcessor(t, repoPath)

	msgFile := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
	require.NoError(t, os.WriteFile(msgFile, []byte("fix something [no-spec]"), 0o644))

	d, err := p.HandlePreCommit(PreCommitContext{
		RepoRoot:    repoPath,
		Branch:      "WGO-1-feature",
		StagedFiles: []string{"main.go"},
		MsgFile:     msgFile,
	})
	require.NoError(t, err)
	assert.True(t, d.Allow, "expected Allow=true for [no-spec] in message, got: %s", d.Reason)
}

func TestHandlePreCommit_SpecRefInMessage_Allows(t *testing.T) {
	repoPath := initTestRepo(t)
	p, _ := newPreCommitProcessor(t, repoPath)

	msgFile := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
	require.NoError(t, os.WriteFile(msgFile, []byte("feat: add thing\n\nSpec: spec/WGO-1.md\n"), 0o644))

	d, err := p.HandlePreCommit(PreCommitContext{
		RepoRoot:    repoPath,
		Branch:      "WGO-1-feature",
		StagedFiles: []string{"main.go"},
		MsgFile:     msgFile,
	})
	require.NoError(t, err)
	assert.True(t, d.Allow, "expected Allow=true for Spec: reference in message, got: %s", d.Reason)
}

func TestHandlePreCommit_SpecFileOnDisk_Allows(t *testing.T) {
	repoPath := initTestRepo(t)
	p, _ := newPreCommitProcessor(t, repoPath)

	// Create spec file on disk
	specDir := filepath.Join(repoPath, "spec")
	require.NoError(t, os.MkdirAll(specDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(specDir, "WGO-42.md"), []byte("# spec"), 0o644))

	d, err := p.HandlePreCommit(PreCommitContext{
		RepoRoot:    repoPath,
		Branch:      "WGO-42-my-feature",
		StagedFiles: []string{"main.go"},
	})
	require.NoError(t, err)
	assert.True(t, d.Allow, "expected Allow=true when spec file exists on disk, got: %s", d.Reason)
}

func TestHandlePreCommit_NoSpec_Blocks(t *testing.T) {
	repoPath := initTestRepo(t)
	p, _ := newPreCommitProcessor(t, repoPath)

	// Stage a file with > 5 lines so the min-lines escape hatch doesn't trigger
	content := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\n"
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "main.go"), []byte(content), 0o644))
	cmd := exec.Command("git", "add", "main.go")
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", out)

	d, err := p.HandlePreCommit(PreCommitContext{
		RepoRoot:    repoPath,
		Branch:      "WGO-999-no-spec-branch",
		StagedFiles: []string{"main.go"},
	})
	require.NoError(t, err)
	assert.False(t, d.Allow, "expected Allow=false when no spec and no escape, got reason: %s", d.Reason)
	assert.Contains(t, d.Reason, "commit blocked")
	assert.Contains(t, d.Reason, "[no-spec]")
}

func TestHandlePreCommit_AnnotationSpecPath_Allows(t *testing.T) {
	repoPath := initTestRepo(t)
	s := newTestStore(t)
	g := git.New(repoPath)
	cfg := &EventConfig{
		SpecRequired:         true,
		SpecRequiredMinLines: 5,
		ExcludeBranches:      []string{"main"},
	}
	p := NewEventProcessor(s, g, cfg)

	// Create spec file and record it in annotation
	specDir := filepath.Join(repoPath, "spec")
	require.NoError(t, os.MkdirAll(specDir, 0o755))
	specPath := filepath.Join(specDir, "WGO-55.md")
	require.NoError(t, os.WriteFile(specPath, []byte("# spec"), 0o644))

	state, _ := s.LoadState()
	state.SetSpec(repoPath, "WGO-55-feat", specPath, "active")
	_ = s.SaveState(state)

	d, err := p.HandlePreCommit(PreCommitContext{
		RepoRoot:    repoPath,
		Branch:      "WGO-55-feat",
		StagedFiles: []string{"main.go", "a.go", "b.go", "c.go", "d.go", "e.go"},
	})
	require.NoError(t, err)
	assert.True(t, d.Allow, "expected Allow=true when annotation has SpecPath, got: %s", d.Reason)
}

// newTestStore creates a FileStore in a temp directory.
func newTestStore(t *testing.T) *store.FileStore {
	t.Helper()
	dir := t.TempDir()
	wgoDir := filepath.Join(dir, ".wgo")
	// We need to create a FileStore pointing at our temp dir.
	// Since store.New() hardcodes ~/.wgo, we'll construct one manually
	// by using a helper approach: create state dir and files.
	require.NoError(t, os.MkdirAll(wgoDir, 0o755), "failed to create test wgo dir")
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
		{"git", "config", "commit.gpgsign", "false"},
		{"git", "commit", "--allow-empty", "-m", "initial"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "command %v failed: %s", args, out)
	}
	return dir
}

func TestHandlePostCheckout_BranchCheckout_AddsToplan(t *testing.T) {
	s := newTestStore(t)
	repoPath := initTestRepo(t)

	// Create a branch
	cmd := exec.Command("git", "checkout", "-b", "feat/test-branch")
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "checkout failed: %s", out)

	gitClient := git.New(repoPath)
	cfg := &EventConfig{
		AutoPlan:        true,
		ExcludeBranches: []string{"main", "master"},
	}

	processor := NewEventProcessor(s, gitClient, cfg)
	require.NoError(t, processor.HandlePostCheckout(repoPath, "abc123", "def456", "1"), "HandlePostCheckout failed")

	// Verify state was updated
	state, err := s.LoadState()
	require.NoError(t, err, "LoadState failed")
	assert.Contains(t, state.Repos, repoPath, "repo not added to state")

	// Verify plan was updated
	content, err := s.LoadPlan()
	require.NoError(t, err, "LoadPlan failed")
	assert.Contains(t, content, "feat/test-branch", "branch not added to plan")
	assert.Contains(t, content, "(auto-tracked)")
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
	require.NoError(t, processor.HandlePostCheckout(repoPath, "abc123", "def456", "1"), "HandlePostCheckout failed")

	content, err := s.LoadPlan()
	require.NoError(t, err, "LoadPlan failed")

	p, _ := plan.Parse(content)
	assert.Empty(t, p.ActiveBranches, "excluded branch should not be in plan")
}

func TestHandlePostCheckout_FileCheckout_NoAutoAdd(t *testing.T) {
	s := newTestStore(t)
	repoPath := initTestRepo(t)

	// Create a branch that would normally be added
	cmd := exec.Command("git", "checkout", "-b", "feat/should-not-add")
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "checkout failed: %s", out)

	gitClient := git.New(repoPath)
	cfg := &EventConfig{
		AutoPlan:        true,
		ExcludeBranches: []string{"main"},
	}

	processor := NewEventProcessor(s, gitClient, cfg)
	// branchFlag "0" = file checkout, should not add to plan
	require.NoError(t, processor.HandlePostCheckout(repoPath, "abc", "def", "0"), "HandlePostCheckout failed")

	content, err := s.LoadPlan()
	require.NoError(t, err, "LoadPlan failed")

	p, _ := plan.Parse(content)
	assert.Empty(t, p.ActiveBranches, "file checkout should not add branches to plan")
}

func TestHandlePostCheckout_AutoPlanDisabled(t *testing.T) {
	s := newTestStore(t)
	repoPath := initTestRepo(t)

	cmd := exec.Command("git", "checkout", "-b", "feat/no-auto")
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "checkout failed: %s", out)

	gitClient := git.New(repoPath)
	cfg := &EventConfig{
		AutoPlan:        false,
		ExcludeBranches: []string{"main"},
	}

	processor := NewEventProcessor(s, gitClient, cfg)
	require.NoError(t, processor.HandlePostCheckout(repoPath, "abc", "def", "1"), "HandlePostCheckout failed")

	content, err := s.LoadPlan()
	require.NoError(t, err, "LoadPlan failed")

	p, _ := plan.Parse(content)
	assert.Empty(t, p.ActiveBranches, "auto_plan=false should not add branches")
}

func TestHandlePostCommit_UpdatesLastSeen(t *testing.T) {
	s := newTestStore(t)
	repoPath := initTestRepo(t)

	gitClient := git.New(repoPath)
	cfg := &EventConfig{AutoPlan: true}

	processor := NewEventProcessor(s, gitClient, cfg)
	require.NoError(t, processor.HandlePostCommit(repoPath), "HandlePostCommit failed")

	state, err := s.LoadState()
	require.NoError(t, err, "LoadState failed")
	assert.Contains(t, state.Repos, repoPath, "repo not added to state after post-commit")
}

func TestHandlePostMerge_UpdatesLastSeen(t *testing.T) {
	s := newTestStore(t)
	repoPath := initTestRepo(t)

	gitClient := git.New(repoPath)
	cfg := &EventConfig{AutoPlan: true}

	processor := NewEventProcessor(s, gitClient, cfg)
	require.NoError(t, processor.HandlePostMerge(repoPath, "0"), "HandlePostMerge failed")

	state, err := s.LoadState()
	require.NoError(t, err, "LoadState failed")
	assert.Contains(t, state.Repos, repoPath, "repo not added to state after post-merge")
}

func TestHandlePostRewrite_UpdatesLastSeen(t *testing.T) {
	s := newTestStore(t)
	repoPath := initTestRepo(t)

	gitClient := git.New(repoPath)
	cfg := &EventConfig{AutoPlan: true}

	processor := NewEventProcessor(s, gitClient, cfg)
	require.NoError(t, processor.HandlePostRewrite(repoPath, "rebase"), "HandlePostRewrite failed")

	state, err := s.LoadState()
	require.NoError(t, err, "LoadState failed")
	assert.Contains(t, state.Repos, repoPath, "repo not added to state after post-rewrite")
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
			assert.Equal(t, tt.want, got, "shouldExclude(%q)", tt.branch)
		})
	}
}

func TestHandlePostCheckout_DuplicateBranch_NotDuplicated(t *testing.T) {
	s := newTestStore(t)
	repoPath := initTestRepo(t)

	cmd := exec.Command("git", "checkout", "-b", "feat/dup-test")
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "checkout failed: %s", out)

	gitClient := git.New(repoPath)
	cfg := &EventConfig{
		AutoPlan:        true,
		ExcludeBranches: []string{"main"},
	}

	processor := NewEventProcessor(s, gitClient, cfg)

	// First checkout
	require.NoError(t, processor.HandlePostCheckout(repoPath, "a", "b", "1"), "first HandlePostCheckout failed")

	// Second checkout of same branch
	require.NoError(t, processor.HandlePostCheckout(repoPath, "a", "b", "1"), "second HandlePostCheckout failed")

	content, err := s.LoadPlan()
	require.NoError(t, err, "LoadPlan failed")

	// Count occurrences of the branch
	count := strings.Count(content, "feat/dup-test")
	assert.Equal(t, 1, count, "branch appears %d times, want 1:\n%s", count, content)
}
