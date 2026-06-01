package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/virtru/wgo/internal/store"
)

func TestStackAddExistingBranch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := setupCleanRepo(t)
	resolvedDir, _ := filepath.EvalSymlinks(dir)

	// Create branches a, b, m
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "a").Run())
	addCleanCommit(t, dir, "a commit")
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "b").Run())
	addCleanCommit(t, dir, "b commit")
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "main").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "m").Run())
	addCleanCommit(t, dir, "m commit")

	// Setup state with a stack containing a and b
	s, err := store.New()
	require.NoError(t, err)
	require.NoError(t, s.EnsureDir())

	state := store.NewState()
	stackID := "test-stack"
	state.AddStack(store.Stack{ID: stackID, Name: "test", RootRef: "origin/main"})
	state.AddAnnotation(resolvedDir, "a", "")
	state.SetStackID(resolvedDir, "a", stackID)
	state.AddAnnotation(resolvedDir, "b", "")
	state.SetStackID(resolvedDir, "b", stackID)
	state.SetParents(resolvedDir, "b", []string{store.AnnotationKey(resolvedDir, "a")})
	require.NoError(t, s.SaveState(state))

	// Change to repo and run stack add
	require.NoError(t, os.Chdir(dir))

	stackAddParents = []string{"b"}
	stackAddStackID = ""
	err = runStackAdd("m")
	require.NoError(t, err)

	// Verify m was added to stack with b as parent
	loaded, err := s.LoadState()
	require.NoError(t, err)

	m := loaded.GetAnnotation(resolvedDir, "m")
	require.NotNil(t, m)
	assert.Equal(t, stackID, m.StackID)
	assert.Equal(t, []string{store.AnnotationKey(resolvedDir, "b")}, m.Parents)
}

func TestStackAddMultiParent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := setupCleanRepo(t)
	resolvedDir, _ := filepath.EvalSymlinks(dir)

	// Create branches a, b, c, merge-node
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "a").Run())
	addCleanCommit(t, dir, "a commit")
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "main").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "b").Run())
	addCleanCommit(t, dir, "b commit")
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "main").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "c").Run())
	addCleanCommit(t, dir, "c commit")
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "main").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "merge-node").Run())
	addCleanCommit(t, dir, "merge commit")

	// Setup state with stack
	s, err := store.New()
	require.NoError(t, err)
	require.NoError(t, s.EnsureDir())

	state := store.NewState()
	stackID := "test-stack"
	state.AddStack(store.Stack{ID: stackID, Name: "test", RootRef: "origin/main"})
	state.AddAnnotation(resolvedDir, "a", "")
	state.SetStackID(resolvedDir, "a", stackID)
	state.AddAnnotation(resolvedDir, "b", "")
	state.SetStackID(resolvedDir, "b", stackID)
	state.AddAnnotation(resolvedDir, "c", "")
	state.SetStackID(resolvedDir, "c", stackID)
	require.NoError(t, s.SaveState(state))

	// Change to repo and add merge-node with multiple parents
	require.NoError(t, os.Chdir(dir))

	stackAddParents = []string{"b", "c"}
	stackAddStackID = ""
	err = runStackAdd("merge-node")
	require.NoError(t, err)

	// Verify merge-node has both parents
	loaded, err := s.LoadState()
	require.NoError(t, err)

	mergeNode := loaded.GetAnnotation(resolvedDir, "merge-node")
	require.NotNil(t, mergeNode)
	assert.Equal(t, stackID, mergeNode.StackID)
	assert.ElementsMatch(t, []string{
		store.AnnotationKey(resolvedDir, "b"),
		store.AnnotationKey(resolvedDir, "c"),
	}, mergeNode.Parents)
}

func TestStackAddCycleDetection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := setupCleanRepo(t)
	resolvedDir, _ := filepath.EvalSymlinks(dir)

	// Create branches a, b, c in a chain
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "a").Run())
	addCleanCommit(t, dir, "a commit")
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "b").Run())
	addCleanCommit(t, dir, "b commit")
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "c").Run())
	addCleanCommit(t, dir, "c commit")

	// Setup state with stack a → b → c
	s, err := store.New()
	require.NoError(t, err)
	require.NoError(t, s.EnsureDir())

	state := store.NewState()
	stackID := "test-stack"
	state.AddStack(store.Stack{ID: stackID, Name: "test", RootRef: "origin/main"})
	state.AddAnnotation(resolvedDir, "a", "")
	state.SetStackID(resolvedDir, "a", stackID)
	state.AddAnnotation(resolvedDir, "b", "")
	state.SetStackID(resolvedDir, "b", stackID)
	state.SetParents(resolvedDir, "b", []string{store.AnnotationKey(resolvedDir, "a")})
	state.AddAnnotation(resolvedDir, "c", "")
	state.SetStackID(resolvedDir, "c", stackID)
	state.SetParents(resolvedDir, "c", []string{store.AnnotationKey(resolvedDir, "b")})
	require.NoError(t, s.SaveState(state))

	// Change to repo and try to create cycle a → c
	require.NoError(t, os.Chdir(dir))

	stackAddParents = []string{"c"}
	stackAddStackID = stackID
	err = runStackAdd("a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle")
}

func TestStackAddNoParentStack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := setupCleanRepo(t)

	// Create branches b and m (neither in a stack)
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "b").Run())
	addCleanCommit(t, dir, "b commit")
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "main").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "m").Run())
	addCleanCommit(t, dir, "m commit")

	// Setup empty state
	s, err := store.New()
	require.NoError(t, err)
	require.NoError(t, s.EnsureDir())
	require.NoError(t, s.SaveState(store.NewState()))

	// Change to repo and try to add m with b as parent (b not in stack)
	require.NoError(t, os.Chdir(dir))

	stackAddParents = []string{"b"}
	stackAddStackID = ""
	err = runStackAdd("m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no parent is part of a stack")
}

func TestStackAddExplicitStack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := setupCleanRepo(t)
	resolvedDir, _ := filepath.EvalSymlinks(dir)

	// Create branches b and m
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "b").Run())
	addCleanCommit(t, dir, "b commit")
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "main").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "m").Run())
	addCleanCommit(t, dir, "m commit")

	// Setup state with stack (b not in stack initially)
	s, err := store.New()
	require.NoError(t, err)
	require.NoError(t, s.EnsureDir())

	state := store.NewState()
	stackID := "explicit-stack"
	state.AddStack(store.Stack{ID: stackID, Name: "test", RootRef: "origin/main"})
	require.NoError(t, s.SaveState(state))

	// Add b to stack first
	state.AddAnnotation(resolvedDir, "b", "")
	state.SetStackID(resolvedDir, "b", stackID)
	require.NoError(t, s.SaveState(state))

	// Change to repo and add m with explicit stack
	require.NoError(t, os.Chdir(dir))

	stackAddParents = []string{"b"}
	stackAddStackID = stackID
	err = runStackAdd("m")
	require.NoError(t, err)

	// Verify m was added with explicit stack
	loaded, err := s.LoadState()
	require.NoError(t, err)

	m := loaded.GetAnnotation(resolvedDir, "m")
	require.NotNil(t, m)
	assert.Equal(t, stackID, m.StackID)
}

func TestStackAddBranchNotFound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := setupCleanRepo(t)

	// Setup state
	s, err := store.New()
	require.NoError(t, err)
	require.NoError(t, s.EnsureDir())
	require.NoError(t, s.SaveState(store.NewState()))

	// Change to repo and try to add nonexistent branch
	require.NoError(t, os.Chdir(dir))

	stackAddParents = []string{"parent"}
	stackAddStackID = ""
	err = runStackAdd("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStackAddDifferentStack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := setupCleanRepo(t)
	resolvedDir, _ := filepath.EvalSymlinks(dir)

	// Create branches
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "m").Run())
	addCleanCommit(t, dir, "m commit")
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "main").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "b").Run())
	addCleanCommit(t, dir, "b commit")

	// Setup state with m in stack1, b in stack2
	s, err := store.New()
	require.NoError(t, err)
	require.NoError(t, s.EnsureDir())

	state := store.NewState()
	stack1 := "stack-1"
	stack2 := "stack-2"
	state.AddStack(store.Stack{ID: stack1, Name: "stack1", RootRef: "origin/main"})
	state.AddStack(store.Stack{ID: stack2, Name: "stack2", RootRef: "origin/main"})
	state.AddAnnotation(resolvedDir, "m", "")
	state.SetStackID(resolvedDir, "m", stack1)
	state.AddAnnotation(resolvedDir, "b", "")
	state.SetStackID(resolvedDir, "b", stack2)
	require.NoError(t, s.SaveState(state))

	// Change to repo and try to add m to stack2
	require.NoError(t, os.Chdir(dir))

	stackAddParents = []string{"b"}
	stackAddStackID = stack2
	err = runStackAdd("m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already in stack")
	assert.Contains(t, err.Error(), stack1)
}

func TestStackAddReparent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := setupCleanRepo(t)
	resolvedDir, _ := filepath.EvalSymlinks(dir)

	// Create branches a, b, m
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "a").Run())
	addCleanCommit(t, dir, "a commit")
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "main").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "b").Run())
	addCleanCommit(t, dir, "b commit")
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "main").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "m").Run())
	addCleanCommit(t, dir, "m commit")

	// Setup state with m on a
	s, err := store.New()
	require.NoError(t, err)
	require.NoError(t, s.EnsureDir())

	state := store.NewState()
	stackID := "test-stack"
	state.AddStack(store.Stack{ID: stackID, Name: "test", RootRef: "origin/main"})
	state.AddAnnotation(resolvedDir, "a", "")
	state.SetStackID(resolvedDir, "a", stackID)
	state.AddAnnotation(resolvedDir, "b", "")
	state.SetStackID(resolvedDir, "b", stackID)
	state.AddAnnotation(resolvedDir, "m", "")
	state.SetStackID(resolvedDir, "m", stackID)
	state.SetParents(resolvedDir, "m", []string{store.AnnotationKey(resolvedDir, "a")})
	require.NoError(t, s.SaveState(state))

	// Change to repo and reparent m to b
	require.NoError(t, os.Chdir(dir))

	stackAddParents = []string{"b"}
	stackAddStackID = ""
	err = runStackAdd("m")
	require.NoError(t, err)

	// Verify m's parent was updated to b
	loaded, err := s.LoadState()
	require.NoError(t, err)

	m := loaded.GetAnnotation(resolvedDir, "m")
	require.NotNil(t, m)
	assert.Equal(t, stackID, m.StackID)
	assert.Equal(t, []string{store.AnnotationKey(resolvedDir, "b")}, m.Parents)
}

func TestStackAddDefaultsToCurrentBranch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := setupCleanRepo(t)
	resolvedDir, _ := filepath.EvalSymlinks(dir)

	// Create branches b and m, checkout m
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "b").Run())
	addCleanCommit(t, dir, "b commit")
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "main").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "m").Run())
	addCleanCommit(t, dir, "m commit")

	// Setup state with b in stack
	s, err := store.New()
	require.NoError(t, err)
	require.NoError(t, s.EnsureDir())

	state := store.NewState()
	stackID := "test-stack"
	state.AddStack(store.Stack{ID: stackID, Name: "test", RootRef: "origin/main"})
	state.AddAnnotation(resolvedDir, "b", "")
	state.SetStackID(resolvedDir, "b", stackID)
	require.NoError(t, s.SaveState(state))

	// Change to repo (on branch m) and run without specifying branch
	require.NoError(t, os.Chdir(dir))

	stackAddParents = []string{"b"}
	stackAddStackID = ""
	err = runStackAdd("") // Empty string should default to current branch
	require.NoError(t, err)

	// Verify m was added
	loaded, err := s.LoadState()
	require.NoError(t, err)

	m := loaded.GetAnnotation(resolvedDir, "m")
	require.NotNil(t, m)
	assert.Equal(t, stackID, m.StackID)
}

func TestStackAddRequiresAtLeastOneParent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := setupCleanRepo(t)

	// Create branch m
	require.NoError(t, exec.Command("git", "-C", dir, "checkout", "-b", "m").Run())
	addCleanCommit(t, dir, "m commit")

	// Setup state
	s, err := store.New()
	require.NoError(t, err)
	require.NoError(t, s.EnsureDir())
	require.NoError(t, s.SaveState(store.NewState()))

	// Change to repo and try to add without parents
	require.NoError(t, os.Chdir(dir))

	stackAddParents = []string{} // No parents
	stackAddStackID = ""
	err = runStackAdd("m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one parent required")
}
