package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/claudemd"
)

func twoRepos() []claudemd.RepoEntry {
	return []claudemd.RepoEntry{
		{Dir: "platform", Label: "virtru/platform"},
		{Dir: "cli", Label: "virtru/cli"},
	}
}

// writeSpec creates <root>/<dir>/spec/<name> with the exact case given.
func writeSpec(t *testing.T, root, dir, name string) {
	t.Helper()
	specDir := filepath.Join(root, dir, "spec")
	require.NoError(t, os.MkdirAll(specDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(specDir, name), []byte("# spec"), 0o644))
}

func readClaudeMD(t *testing.T, root string) string {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	require.NoError(t, err)
	return string(got)
}

func TestWriteSharedClaudeMDSkipsSingleRepo(t *testing.T) {
	dir := t.TempDir()
	err := writeSharedClaudeMD(sharedClaudeMD{
		Root:        dir,
		Ticket:      "DSPX-1",
		Description: "desc",
		Repos:       []claudemd.RepoEntry{{Dir: "platform", Label: "virtru/platform"}},
	})
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(dir, "CLAUDE.md"))
	assert.True(t, os.IsNotExist(statErr), "CLAUDE.md should not be written for a single repo")
}

func TestWriteSharedClaudeMDLinksExistingSpec(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "platform", "DSPX-1.md")

	require.NoError(t, writeSharedClaudeMD(sharedClaudeMD{
		Root: dir, Ticket: "DSPX-1", Description: "desc", Repos: twoRepos(),
	}))

	content := readClaudeMD(t, dir)
	assert.Contains(t, content, "platform/spec/DSPX-1.md")
	assert.Contains(t, content, "virtru/platform")
	assert.Contains(t, content, "virtru/cli")
}

// Regression: tickets are uppercased on the way in (ParseTicketFromBranch),
// but specs for GitHub issues live on disk lowercase (spec/gh-9.md). Building
// the link from the ticket produced spec/GH-21.md — a link that resolves only
// on a case-insensitive filesystem and 404s on github.com and Linux CI.
//
// Asserting on the exact rendered string rather than os.Stat is the point:
// a stat-based assertion passes vacuously on macOS.
func TestWriteSharedClaudeMDUsesOnDiskSpecCase(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "platform", "gh-21.md")

	require.NoError(t, writeSharedClaudeMD(sharedClaudeMD{
		Root: dir, Ticket: "GH-21", Description: "desc", Repos: twoRepos(),
	}))

	content := readClaudeMD(t, dir)
	assert.Contains(t, content, "platform/spec/gh-21.md")
	assert.NotContains(t, content, "GH-21.md", "link must use the real on-disk filename")
}

func TestWriteSharedClaudeMDNoSpecFound(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeSharedClaudeMD(sharedClaudeMD{
		Root: dir, Ticket: "DSPX-1", Description: "desc text", Repos: twoRepos(),
	}))
	assert.Contains(t, readClaudeMD(t, dir), "desc text")
}

// Reachable via `wgo add DSPX-1 --no-spec -r a -r b` with no description: the
// Goal section must say something rather than render empty.
func TestWriteSharedClaudeMDNoSpecNoDescription(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeSharedClaudeMD(sharedClaudeMD{
		Root: dir, Ticket: "DSPX-1", Repos: twoRepos(),
	}))
	assert.Contains(t, readClaudeMD(t, dir), "No goal recorded yet")
}

func TestWriteSharedClaudeMDSpecRepoDirWinsTieBreak(t *testing.T) {
	dir := t.TempDir()
	// Both repos carry the spec; "cli" sorts first, so without the hint the
	// link would disagree with the repo add actually wrote the spec into.
	writeSpec(t, dir, "cli", "DSPX-1.md")
	writeSpec(t, dir, "platform", "DSPX-1.md")

	require.NoError(t, writeSharedClaudeMD(sharedClaudeMD{
		Root: dir, Ticket: "DSPX-1", SpecRepoDir: "platform", Repos: twoRepos(),
	}))
	assert.Contains(t, readClaudeMD(t, dir), "platform/spec/DSPX-1.md")

	require.NoError(t, os.Remove(filepath.Join(dir, "CLAUDE.md")))
	require.NoError(t, writeSharedClaudeMD(sharedClaudeMD{
		Root: dir, Ticket: "DSPX-1", Repos: twoRepos(),
	}))
	assert.Contains(t, readClaudeMD(t, dir), "cli/spec/DSPX-1.md", "no hint: alphabetical order decides")
}

// A broken spec/ in one repo must not hide a sibling's spec.
func TestWriteSharedClaudeMDKeepsScanningPastBrokenSpecDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cli"), 0o755))
	// spec is a regular file, so ReadDir fails with ENOTDIR, not ENOENT.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cli", "spec"), []byte("x"), 0o644))
	writeSpec(t, dir, "platform", "DSPX-1.md")

	require.NoError(t, writeSharedClaudeMD(sharedClaudeMD{
		Root: dir, Ticket: "DSPX-1", Repos: twoRepos(),
	}))
	assert.Contains(t, readClaudeMD(t, dir), "platform/spec/DSPX-1.md")
}

func TestWriteSharedClaudeMDSortsReposDeterministically(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeSharedClaudeMD(sharedClaudeMD{
		Root: dir, Ticket: "DSPX-1", Description: "d", Repos: twoRepos(),
	}))
	content := readClaudeMD(t, dir)
	assert.Less(t, strings.Index(content, "`cli/`"), strings.Index(content, "`platform/`"),
		"repos must render in sorted order so regeneration doesn't churn the file")
}

// The file is regenerated on every run; a second call must fully replace the
// repo list, not leave stale entries behind.
func TestWriteSharedClaudeMDRegenerationReplacesStaleRepoList(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeSharedClaudeMD(sharedClaudeMD{
		Root: dir, Ticket: "DSPX-1", Description: "d", Repos: twoRepos(),
	}))

	require.NoError(t, writeSharedClaudeMD(sharedClaudeMD{
		Root: dir, Ticket: "DSPX-1", Description: "d",
		Repos: []claudemd.RepoEntry{{Dir: "sdk", Label: "virtru/sdk"}, {Dir: "web", Label: "virtru/web"}},
	}))

	content := readClaudeMD(t, dir)
	assert.Contains(t, content, "virtru/sdk")
	assert.NotContains(t, content, "virtru/platform", "stale repo list must be replaced, not appended to")
	assert.Equal(t, 1, strings.Count(content, "## Repos in this workspace"))
}

// A spec that lands between runs must upgrade the Goal section.
func TestWriteSharedClaudeMDRegenerationPicksUpNewSpec(t *testing.T) {
	dir := t.TempDir()
	req := sharedClaudeMD{Root: dir, Ticket: "DSPX-1", Description: "desc text", Repos: twoRepos()}
	require.NoError(t, writeSharedClaudeMD(req))
	assert.Contains(t, readClaudeMD(t, dir), "desc text")

	writeSpec(t, dir, "platform", "DSPX-1.md")
	require.NoError(t, writeSharedClaudeMD(req))

	content := readClaudeMD(t, dir)
	assert.Contains(t, content, "platform/spec/DSPX-1.md")
	assert.NotContains(t, content, "desc text")
}

// The destructive path: a CLAUDE.md we didn't generate is someone's own work.
func TestWriteSharedClaudeMDDoesNotClobberHandWrittenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	handWritten := "# My own notes\n\nDo not delete me.\n"
	require.NoError(t, os.WriteFile(path, []byte(handWritten), 0o644))

	require.NoError(t, writeSharedClaudeMD(sharedClaudeMD{
		Root: dir, Ticket: "DSPX-1", Description: "d", Repos: twoRepos(),
	}), "an unrecognized file is skipped, not an error")

	assert.Equal(t, handWritten, readClaudeMD(t, dir))
}

func TestWriteSharedClaudeMDOverwritesItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	// Descriptions are distinctive on purpose: the template's boilerplate
	// contains ordinary words like "first", so a NotContains on one would fail
	// against the prose rather than against a stale description.
	req := sharedClaudeMD{Root: dir, Ticket: "DSPX-1", Description: "goal-before-regen", Repos: twoRepos()}
	require.NoError(t, writeSharedClaudeMD(req))

	req.Description = "goal-after-regen"
	require.NoError(t, writeSharedClaudeMD(req))

	content := readClaudeMD(t, dir)
	assert.Contains(t, content, "goal-after-regen")
	assert.NotContains(t, content, "goal-before-regen")
}

// --- discoverSharedRepos ---------------------------------------------------

// fakeRepoDiscoveryClient returns the configured remotes (or error) per path.
// Repo-root detection is real (a .jj directory on disk), not faked.
type fakeRepoDiscoveryClient struct {
	remotes    map[string]map[string]string
	remoteErrs map[string]error
}

func (f *fakeRepoDiscoveryClient) RemoteURLs(path string) (map[string]string, error) {
	if err := f.remoteErrs[path]; err != nil {
		return nil, err
	}
	return f.remotes[path], nil
}

// mkRepo creates <root>/<name> with a .jj directory, marking it a repo root.
func mkRepo(t *testing.T, root, name string) string {
	t.Helper()
	p := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Join(p, ".jj"), 0o755))
	return p
}

func TestDiscoverSharedRepos(t *testing.T) {
	dir := t.TempDir()
	platformDir := mkRepo(t, dir, "platform")
	cliDir := mkRepo(t, dir, "cli")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0o644))

	fake := &fakeRepoDiscoveryClient{
		remotes: map[string]map[string]string{
			platformDir: {"origin": "https://github.com/virtru/platform.git"},
			cliDir:      {},
		},
	}

	repos, err := discoverSharedRepos(fake, dir)
	require.NoError(t, err)
	require.Len(t, repos, 2)

	byDir := map[string]claudemd.RepoEntry{}
	for _, r := range repos {
		byDir[r.Dir] = r
	}
	assert.Equal(t, "virtru/platform", byDir["platform"].Label)
	assert.Equal(t, "cli", byDir["cli"].Label, "falls back to dir name when no remote resolves")
}

// Regression: jj.Client.IsRepo walks ancestors, so using it here reported
// every subdirectory as a repo whenever sharedRoot was itself inside a repo.
// Detection must be root-exact.
func TestDiscoverSharedReposIgnoresNonRepoDirs(t *testing.T) {
	dir := t.TempDir()
	mkRepo(t, dir, "platform")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "logs"), 0o755))
	// A repo nested deeper is not an immediate child and must not count.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "scratch", "inner", ".jj"), 0o755))

	repos, err := discoverSharedRepos(&fakeRepoDiscoveryClient{}, dir)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "platform", repos[0].Dir)
}

// A parent directory being a repo must not make its children look like repos.
func TestDiscoverSharedReposIgnoresAncestorRepo(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".jj"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "internal"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cmd"), 0o755))

	repos, err := discoverSharedRepos(&fakeRepoDiscoveryClient{}, dir)
	require.NoError(t, err)
	assert.Empty(t, repos)
}

func TestDiscoverSharedReposFollowsSymlinkedRepo(t *testing.T) {
	dir := t.TempDir()
	target := mkRepo(t, t.TempDir(), "platform")
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "platform")))

	repos, err := discoverSharedRepos(&fakeRepoDiscoveryClient{}, dir)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "platform", repos[0].Dir)
}

func TestDiscoverSharedReposRemotePreference(t *testing.T) {
	tests := []struct {
		name    string
		remotes map[string]string
		want    string
	}{
		{
			name:    "origin wins over other remotes",
			remotes: map[string]string{"origin": "git@github.com:virtru/platform.git", "upstream": "git@github.com:other/platform.git"},
			want:    "virtru/platform",
		},
		{
			name:    "no origin falls back to first remote by name",
			remotes: map[string]string{"upstream": "git@github.com:virtru/platform.git", "zed": "git@github.com:other/platform.git"},
			want:    "virtru/platform",
		},
		{
			name: "unparseable origin still tries other remotes",
			// The old code stopped at origin and fell all the way to the dir name.
			remotes: map[string]string{"origin": "/local/mirror/path", "upstream": "git@github.com:virtru/platform.git"},
			want:    "virtru/platform",
		},
		{
			name:    "no remote resolves falls back to dir name",
			remotes: map[string]string{"origin": "/local/mirror/path"},
			want:    "platform",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			repoPath := mkRepo(t, dir, "platform")
			fake := &fakeRepoDiscoveryClient{remotes: map[string]map[string]string{repoPath: tt.remotes}}

			repos, err := discoverSharedRepos(fake, dir)
			require.NoError(t, err)
			require.Len(t, repos, 1)
			assert.Equal(t, tt.want, repos[0].Label)
		})
	}
}

func TestDiscoverSharedReposFallsBackToDirNameOnRemoteURLsError(t *testing.T) {
	dir := t.TempDir()
	repoPath := mkRepo(t, dir, "platform")
	fake := &fakeRepoDiscoveryClient{
		remoteErrs: map[string]error{repoPath: errors.New("jj git remote list: boom")},
	}

	repos, err := discoverSharedRepos(fake, dir)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "platform", repos[0].Label, "falls back to dir name when remote lookup errors")
}

func TestDiscoverSharedReposPropagatesReadDirError(t *testing.T) {
	_, err := discoverSharedRepos(&fakeRepoDiscoveryClient{}, filepath.Join(t.TempDir(), "does-not-exist"))
	require.Error(t, err)
}

// --- isManagedWorkspaceRoot ------------------------------------------------

func TestIsManagedWorkspaceRoot(t *testing.T) {
	base := t.TempDir()
	worktrees := filepath.Join(base, "worktrees")
	shared := filepath.Join(worktrees, "DSPX-1-slug")
	require.NoError(t, os.MkdirAll(filepath.Join(shared, "platform"), 0o755))

	assert.True(t, isManagedWorkspaceRoot(worktrees, shared))
	assert.False(t, isManagedWorkspaceRoot(worktrees, filepath.Join(shared, "platform")),
		"a repo dir is one level too deep to be a shared root")
	assert.False(t, isManagedWorkspaceRoot(worktrees, base), "an ancestor is not a shared root")
	assert.False(t, isManagedWorkspaceRoot(worktrees, t.TempDir()), "an unrelated dir is not a shared root")
	assert.False(t, isManagedWorkspaceRoot("", shared), "unconfigured worktrees_dir matches nothing")
}

// --- flag wiring -----------------------------------------------------------

// Both entry points must expose the escape hatch, and it must default to
// generating the file — an opt-out that silently defaulted to "on" would turn
// a documented flag into dead code.
func TestNoClaudeMDFlagRegistered(t *testing.T) {
	for _, tt := range []struct {
		cmdName string
		target  *bool
	}{
		{"add", &addNoClaudeMD},
		{"join", &joinNoClaudeMD},
	} {
		t.Run(tt.cmdName, func(t *testing.T) {
			cmd, _, err := rootCmd.Find([]string{tt.cmdName})
			require.NoError(t, err)

			flag := cmd.Flags().Lookup("no-claude-md")
			require.NotNil(t, flag, "%s must accept --no-claude-md", tt.cmdName)
			assert.Equal(t, "false", flag.DefValue, "generating the file is the default")
			assert.False(t, *tt.target)
		})
	}
}
