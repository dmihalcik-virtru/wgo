package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/virtru/wgo/internal/git"
	gh "github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/store"
)

func writeCmdTestConfig(t *testing.T, home string) string {
	t.Helper()
	cfgDir := filepath.Join(home, ".wgo")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	worktreesDir := filepath.Join(home, "worktrees")
	content := `[discovery]
base_dirs = ["` + filepath.Join(home, "repos") + `"]

[worktree]
mains_dir = "` + filepath.Join(home, "mains") + `"
worktrees_dir = "` + worktreesDir + `"
`
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o644))
	return worktreesDir
}

func setupStackRepoWithWorktree(t *testing.T) (string, string) {
	t.Helper()
	mainDir := setupCleanRepo(t)

	require.NoError(t, exec.Command("git", "-C", mainDir, "checkout", "-b", "parent").Run())
	addCleanCommit(t, mainDir, "parent")
	require.NoError(t, exec.Command("git", "-C", mainDir, "checkout", "main").Run())

	wtDir := filepath.Join(t.TempDir(), "wt-parent")
	cmd := exec.Command("git", "worktree", "add", wtDir, "parent")
	cmd.Dir = mainDir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "failed to add worktree: %s", out)

	return mainDir, wtDir
}

func TestCurrentCheckoutUsesCanonicalRepoPath(t *testing.T) {
	mainDir, wtDir := setupStackRepoWithWorktree(t)
	resolvedMainDir, _ := filepath.EvalSymlinks(mainDir)
	resolvedWtDir, _ := filepath.EvalSymlinks(wtDir)

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(resolvedWtDir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	co, err := currentCheckout()
	require.NoError(t, err)
	assert.Equal(t, resolvedMainDir, co.RepoPath)
	assert.Equal(t, resolvedWtDir, co.WorktreePath)
	assert.Equal(t, "parent", co.Branch)
}

func TestRecordStackParentUsesCanonicalRepoPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	mainDir, wtDir := setupStackRepoWithWorktree(t)
	resolvedMainDir, _ := filepath.EvalSymlinks(mainDir)
	resolvedWtDir, _ := filepath.EvalSymlinks(wtDir)

	s, err := store.New()
	require.NoError(t, err)
	require.NoError(t, s.EnsureDir())

	state := store.NewState()
	state.AddAnnotation(resolvedMainDir, "parent", "")
	state.SetStackID(resolvedMainDir, "parent", "stack-1")
	require.NoError(t, s.SaveState(state))

	recordStackParent(resolvedWtDir, "child", "parent")

	loaded, err := s.LoadState()
	require.NoError(t, err)

	child := loaded.GetAnnotation(resolvedMainDir, "child")
	require.NotNil(t, child)
	assert.Equal(t, []string{store.AnnotationKey(resolvedMainDir, "parent")}, child.Parents)
	assert.Equal(t, "stack-1", child.StackID)
	assert.Nil(t, loaded.GetAnnotation(resolvedWtDir, "child"))
}

func TestWorktreePathForUsesConfiguredLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	worktreesDir := writeCmdTestConfig(t, home)
	viper.Reset()
	t.Cleanup(viper.Reset)

	got, err := worktreePathFor("/tmp/mains/virtru/wgo", "feat/test")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(worktreesDir, gh.SanitizeBranch("feat/test"), "wgo"), got)
}

func TestEnsureBranchWorktreeCreatesConfiguredPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	worktreesDir := writeCmdTestConfig(t, home)
	viper.Reset()
	t.Cleanup(viper.Reset)

	mainDir := setupCleanRepo(t)
	require.NoError(t, exec.Command("git", "-C", mainDir, "checkout", "-b", "feat/test").Run())
	addCleanCommit(t, mainDir, "feature")
	require.NoError(t, exec.Command("git", "-C", mainDir, "checkout", "main").Run())

	g := git.New("")
	require.NoError(t, ensureBranchWorktree(g, mainDir, "feat/test"))

	expected := filepath.Join(worktreesDir, gh.SanitizeBranch("feat/test"), filepath.Base(mainDir))
	resolvedExpected, _ := filepath.EvalSymlinks(expected)
	info, err := os.Stat(expected)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	worktrees, err := g.ListWorktrees(mainDir)
	require.NoError(t, err)

	found := false
	for _, wt := range worktrees {
		if wt.Path == resolvedExpected && wt.Branch == "feat/test" {
			found = true
		}
	}
	assert.True(t, found, "expected worktree list to include %s", resolvedExpected)
}
