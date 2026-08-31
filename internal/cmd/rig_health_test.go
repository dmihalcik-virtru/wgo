package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/rig"
	"github.com/virtru/wgo/models"
)

// pinnedAt answers `@ | @-` with a working copy sitting on commit.
type pinnedAt struct {
	at  map[string]string
	err error
}

func (p pinnedAt) Log(repo, _ string) ([]jj.LogEntry, error) {
	if p.err != nil {
		return nil, p.err
	}
	commit, ok := p.at[repo]
	if !ok {
		return nil, errors.New("no such workspace")
	}
	return []jj.LogEntry{
		{CommitID: "ffffffffffffffffffffffffffffffffffffffff", Empty: true, CurrentWorkingCopy: true},
		{CommitID: commit},
	}, nil
}

// rigOnDisk materialises testManifest's directories under a fresh rig.dir and
// returns the config doctor would read plus a fake reporting every checkout on
// its pin.
func rigOnDisk(t *testing.T) (*config.Config, pinnedAt, string) {
	t.Helper()
	rigDir := t.TempDir()
	root := filepath.Join(rigDir, "dsp")
	m := testManifest()
	require.NoError(t, rig.Save(root, m))

	at := map[string]string{}
	for _, c := range m.Checkouts {
		dest := filepath.Join(root, rig.SrcDir, c.Dir)
		require.NoError(t, os.MkdirAll(dest, 0o755))
		at[dest] = c.Commit
	}
	return &config.Config{Rig: config.RigConfig{Dir: rigDir}}, pinnedAt{at: at}, root
}

func TestCheckRigsIsQuietWhenEveryCheckoutIsOnItsPin(t *testing.T) {
	cfg, p, _ := rigOnDisk(t)
	assert.Empty(t, checkRigs(p, cfg))
}

// An unconfigured rig.dir must not be walked: filepath.Clean("") is ".", and
// enumerating the process's working directory as a rig store is nonsense.
func TestCheckRigsSkipsAnUnconfiguredRigDir(t *testing.T) {
	_, p, _ := rigOnDisk(t)
	assert.Nil(t, checkRigs(p, &config.Config{}))
	assert.Nil(t, checkRigs(p, &config.Config{Rig: config.RigConfig{Dir: "  "}}))
}

func TestCheckRigsReportsAMovedCheckout(t *testing.T) {
	cfg, p, root := rigOnDisk(t)
	moved := filepath.Join(root, rig.SrcDir, "platform-v0.9.0")
	p.at[moved] = "cccccccc55556666"

	got := checkRigs(p, cfg)

	require.Len(t, got, 1, "only the moved checkout is reported")
	assert.Equal(t, root, got[0].Repo)
	assert.Equal(t, "platform-v0.9.0", got[0].Workspace)
	// The pin reads as its tag, where the drifted commit has none to read as.
	assert.Contains(t, got[0].Issue, "pinned to service/v0.9.0")
	assert.Contains(t, got[0].Issue, "sits on cccccccc5555")
	// Both ways out: putting the working copy back keeps whatever else is in
	// the checkout, where a sync re-materialises it.
	assert.Contains(t, got[0].Issue, "jj -R "+moved+" edit aaaaaaaa1111")
	assert.Contains(t, got[0].Issue, "wgo rig sync dsp")
}

func TestCheckRigsReportsAMissingCheckout(t *testing.T) {
	cfg, p, root := rigOnDisk(t)
	require.NoError(t, os.RemoveAll(filepath.Join(root, rig.SrcDir, "otdfctl-v0.3.0")))

	got := checkRigs(p, cfg)

	require.Len(t, got, 1)
	assert.Equal(t, "otdfctl-v0.3.0", got[0].Workspace)
	assert.Contains(t, got[0].Issue, "missing")
	assert.Contains(t, got[0].Issue, "wgo rig sync dsp")
}

func TestCheckRigsReportsAnUnreadableCheckout(t *testing.T) {
	cfg, _, _ := rigOnDisk(t)

	got := checkRigs(pinnedAt{err: errors.New("workspace is stale")}, cfg)

	require.Len(t, got, 2, "neither checkout could be read, so neither is vouched for")
	assert.Contains(t, got[0].Issue, "workspace is stale")
}

// doctor is read-only, so an unreachable rig.dir is a finding rather than an
// abort that hides every other check's result.
func TestCheckRigsReportsAnUnreadableRigDir(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "rigs")
	require.NoError(t, os.WriteFile(blocked, []byte("not a directory"), 0o644))

	got := checkRigs(pinnedAt{}, &config.Config{Rig: config.RigConfig{Dir: blocked}})

	require.Len(t, got, 1)
	assert.Equal(t, blocked, got[0].Repo)
	assert.Contains(t, got[0].Issue, "listing rigs failed")
}

func TestRigRefIn(t *testing.T) {
	rigDir := t.TempDir()
	root := filepath.Join(rigDir, "dsp")
	require.NoError(t, rig.Save(root, testManifest()))
	checkout := filepath.Join(root, rig.SrcDir, "platform-v0.9.0")
	require.NoError(t, os.MkdirAll(filepath.Join(checkout, "service"), 0o755))

	got := rigRefIn(rigDir, checkout)
	require.NotNil(t, got)
	assert.Equal(t, "dsp", got.Name)
	assert.Equal(t, root, got.Path)
	assert.Equal(t, "service/v0.9.0", got.Pin)

	// From a subdirectory of a checkout the pin is still the checkout's.
	assert.Equal(t, got, rigRefIn(rigDir, filepath.Join(checkout, "service")))

	// A pseudo-version pin has no tag, so the pin reads as a short commit.
	assert.Equal(t, "v0.3.0", rigRefIn(rigDir, filepath.Join(root, rig.SrcDir, "otdfctl-v0.3.0")).Pin)
}

// At the rig root there is no one checkout, but the user is still in a rig and
// the segment must say so.
func TestRigRefInAtTheRigRoot(t *testing.T) {
	rigDir := t.TempDir()
	root := filepath.Join(rigDir, "dsp")
	require.NoError(t, rig.Save(root, testManifest()))

	got := rigRefIn(rigDir, root)
	require.NotNil(t, got)
	assert.Equal(t, "dsp", got.Name)
	assert.Empty(t, got.Pin)
}

func TestRigRefInOutsideARig(t *testing.T) {
	rigDir := t.TempDir()
	require.NoError(t, rig.Save(filepath.Join(rigDir, "dsp"), testManifest()))

	assert.Nil(t, rigRefIn(rigDir, filepath.Join(t.TempDir(), "worktrees", "WGO-136", "platform")))
	// An unconfigured rig.dir must not turn every relative path into a rig.
	assert.Nil(t, rigRefIn("", filepath.Join(rigDir, "dsp")))
}

// A checkout directory the manifest does not know about — a stray directory
// under src/ — is not a rig checkout, but it is still inside the rig.
func TestRigRefInWithAnUnknownCheckout(t *testing.T) {
	rigDir := t.TempDir()
	root := filepath.Join(rigDir, "dsp")
	require.NoError(t, rig.Save(root, testManifest()))
	stray := filepath.Join(root, rig.SrcDir, "scratch")
	require.NoError(t, os.MkdirAll(stray, 0o755))

	got := rigRefIn(rigDir, stray)
	require.NotNil(t, got)
	assert.Equal(t, "dsp", got.Name)
	assert.Empty(t, got.Pin)
}

func TestRigRefLabel(t *testing.T) {
	assert.Equal(t, "dsp@v2.7.1", rigRefLabel(models.RigRef{Name: "dsp", Pin: "v2.7.1"}))
	assert.Equal(t, "dsp", rigRefLabel(models.RigRef{Name: "dsp"}))
}

// The rig segment leads the line: inside a rig there is no bookmark, nothing to
// be ahead or behind of, and no PR, so every field after it needs the context.
func TestStatuslineLeadsWithTheRig(t *testing.T) {
	out := captureStdout(t, func() {
		err := renderStatuslineLine(os.Stdout, &models.Context{
			Repo:   "platform",
			Branch: "(no bookmark)",
			Rig:    &models.RigRef{Name: "dsp-2.7.1", Pin: "service/v0.11.6"},
		}, false)
		require.NoError(t, err)
	})
	assert.Equal(t, "⚓ dsp-2.7.1@service/v0.11.6 platform (no bookmark)\n", out)
}

func TestStatuslineOmitsTheRigSegmentOutsideARig(t *testing.T) {
	out := captureStdout(t, func() {
		err := renderStatuslineLine(os.Stdout, &models.Context{Repo: "platform", Branch: "WGO-136"}, false)
		require.NoError(t, err)
	})
	assert.Equal(t, "platform WGO-136\n", out)
}

func TestRenderTextLeadsWithTheRig(t *testing.T) {
	out := captureStdout(t, func() {
		renderText(os.Stdout, &models.Context{
			Repo:   "platform",
			Branch: "(no bookmark)",
			Rig:    &models.RigRef{Name: "dsp-2.7.1", Pin: "service/v0.11.6"},
		}, false)
	})
	assert.Contains(t, out, "rig:    ⚓ dsp-2.7.1@service/v0.11.6 — pinned source, not branch work\n")
	assert.Less(t, indexOfLine(out, "rig:"), indexOfLine(out, "repo:"),
		"the rig leads, because it changes how every line under it reads")
}

// indexOfLine is the line number of the first line starting with prefix, or -1.
func indexOfLine(out, prefix string) int {
	for i, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return i
		}
	}
	return -1
}

// The rig subcommands that act on an existing rig complete its name; `new`
// takes one that does not exist yet, so it completes nothing.
func TestRigNameCompletionsAreRegistered(t *testing.T) {
	for _, name := range []string{"show", "rm", "verify", "sync", "add"} {
		c, _, err := rigCmd.Find([]string{name})
		require.NoError(t, err)
		assert.NotNil(t, c.ValidArgsFunction, "wgo rig %s completes its name argument", name)
	}
	c, _, err := rigCmd.Find([]string{"new"})
	require.NoError(t, err)
	names, directive := c.ValidArgsFunction(c, nil, "")
	assert.Empty(t, names)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
}

// The second argument is not a rig name, and offering one there would splice a
// nonsense token into the user's command line.
func TestRigNameCompletionsStopAfterTheFirstArgument(t *testing.T) {
	names, directive := rigNameCompletions(nil, []string{"dsp"}, "")
	assert.Empty(t, names)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
}

func TestCheckNotInsideRig(t *testing.T) {
	cfg := &config.Config{
		Rig:      config.RigConfig{Dir: filepath.FromSlash("/home/u/rigs")},
		Worktree: config.WorktreeConfig{WorktreesDir: filepath.FromSlash("/home/u/worktrees")},
	}

	err := checkNotInsideRig(cfg, filepath.FromSlash("/home/u/rigs/dsp-2.7.1/src/platform-v0.9.0"))
	require.Error(t, err)
	// The error names the command that does what the user was reaching for.
	assert.Contains(t, err.Error(), "wgo rig add -m <module>@<version>")
	assert.Contains(t, err.Error(), filepath.FromSlash("/home/u/worktrees"))

	assert.NoError(t, checkNotInsideRig(cfg, filepath.FromSlash("/home/u/worktrees/WGO-136/platform")))
	// A sibling of rig.dir sharing its prefix is ordinary branch work.
	assert.NoError(t, checkNotInsideRig(cfg, filepath.FromSlash("/home/u/rigsomething/platform")))
	// An unconfigured rig.dir must not block every join.
	assert.NoError(t, checkNotInsideRig(&config.Config{}, filepath.FromSlash("/home/u/worktrees/x/y")))
}
