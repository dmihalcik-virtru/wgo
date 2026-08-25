package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/config"
	gh "github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/jjtest"
)

const (
	testOwner = "some-org"
	testRepo  = "repo1"
)

// mainsFixture is the on-disk shape `wgo to` is meant to produce: a clone at
// <mains_dir>/<owner>/<repo> with its own trunk, and an empty worktrees_dir
// alongside it.
type mainsFixture struct {
	Cfg          *config.Config
	JJ           *jj.CLIClient
	RepoPath     string
	WorktreesDir string
}

// newMainsFixture builds that layout against a real jj binary.
//
// The bare remote lives at <tmp>/remotes/<owner>/<repo>.git so matchesRemote's
// HasSuffix("<owner>/<repo>") check passes without needing network access, and
// so trunk() has an origin bookmark to resolve to.
func newMainsFixture(t *testing.T) *mainsFixture {
	t.Helper()
	jjtest.RequireJJ(t)
	jjtest.SetIdentity(t)

	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remotes", testOwner, testRepo+".git")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	if out, err := exec.Command("git", "init", "--bare", "-q", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}

	mainsDir := filepath.Join(tmp, "mains")
	worktreesDir := filepath.Join(tmp, "worktrees")
	repoPath := filepath.Join(mainsDir, testOwner, testRepo)
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	mustJJ(t, repoPath, "git", "init", "--colocate")
	mustJJ(t, repoPath, "config", "set", "--repo", "user.name", "wgo-test")
	mustJJ(t, repoPath, "config", "set", "--repo", "user.email", "wgo-test@example.com")
	mustJJ(t, repoPath, "describe", "-m", "initial")
	mustJJ(t, repoPath, "bookmark", "create", "main", "-r", "@")
	mustJJ(t, repoPath, "git", "remote", "add", "origin", remote)
	mustJJ(t, repoPath, "git", "push", "--bookmark", "main")

	return &mainsFixture{
		Cfg: &config.Config{
			Discovery: config.DiscoveryConfig{BaseDirs: []string{mainsDir, worktreesDir}, ScanDepth: 5},
			Worktree:  config.WorktreeConfig{MainsDir: mainsDir, WorktreesDir: worktreesDir},
		},
		JJ:           jj.NewCLI(),
		RepoPath:     repoPath,
		WorktreesDir: worktreesDir,
	}
}

func branchURL(identifier string) *gh.ParsedURL {
	return &gh.ParsedURL{
		Type:       gh.URLTypeBranch,
		Owner:      testOwner,
		Repo:       testRepo,
		Identifier: identifier,
	}
}

// worktreeEntries lists the slug directories created under worktrees_dir, which
// is what the reported bug polluted.
func worktreeEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read worktrees dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestRunToBranchTrunkReturnsMainsClone is the reported bug: an explicit trunk
// URL must resolve to the clone that already is the trunk checkout, and must
// not manufacture a <worktrees_dir>/main/<repo> duplicate of it.
func TestRunToBranchTrunkReturnsMainsClone(t *testing.T) {
	f := newMainsFixture(t)

	out := captureStdout(t, func() {
		if err := runToBranch(f.JJ, f.Cfg, branchURL("main")); err != nil {
			t.Fatalf("runToBranch: %v", err)
		}
	})

	if got := strings.TrimSpace(out); got != f.RepoPath {
		t.Errorf("stdout = %q, want the mains clone %q", got, f.RepoPath)
	}
	if names := worktreeEntries(t, f.WorktreesDir); len(names) > 0 {
		t.Errorf("a trunk checkout created %v under worktrees_dir; it should create nothing", names)
	}
}

// TestRunToBranchTrunkWithDriftIsNonDestructive covers why the bug looked
// intermittent: once the clone's @ drifts off the trunk bookmark the old
// findExistingCheckout lookup missed and a worktree was manufactured. The
// answer must not depend on that drift, and `wgo to` must not clean it up
// behind the user's back.
func TestRunToBranchTrunkWithDriftIsNonDestructive(t *testing.T) {
	f := newMainsFixture(t)
	mustJJ(t, f.RepoPath, "describe", "-m", "wip: drifted")
	if err := os.WriteFile(filepath.Join(f.RepoPath, "drift.txt"), []byte("d\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	before, err := f.JJ.CurrentChange(f.RepoPath)
	if err != nil {
		t.Fatalf("CurrentChange: %v", err)
	}

	stderr := captureStderr(t, func() {
		out := captureStdout(t, func() {
			if err := runToBranch(f.JJ, f.Cfg, branchURL("main")); err != nil {
				t.Fatalf("runToBranch: %v", err)
			}
		})
		if got := strings.TrimSpace(out); got != f.RepoPath {
			t.Errorf("stdout = %q, want the mains clone %q", got, f.RepoPath)
		}
	})

	if !strings.Contains(stderr, "wgo park") {
		t.Errorf("stderr should point at `wgo park`, got:\n%s", stderr)
	}
	if names := worktreeEntries(t, f.WorktreesDir); len(names) > 0 {
		t.Errorf("drift caused %v to be created under worktrees_dir", names)
	}

	after, err := f.JJ.CurrentChange(f.RepoPath)
	if err != nil {
		t.Fatalf("CurrentChange after: %v", err)
	}
	if after.ChangeID != before.ChangeID {
		t.Errorf("@ moved from %s to %s; a lookup must not mutate the repo", before.ChangeID, after.ChangeID)
	}
	if _, err := os.Stat(filepath.Join(f.RepoPath, "drift.txt")); err != nil {
		t.Errorf("drifted work disturbed: %v", err)
	}
}

// TestRunToBranchReportsRedundantTrunkWorkspace covers the debris left by the
// old behaviour: the duplicate is neither returned nor deleted, only named.
func TestRunToBranchReportsRedundantTrunkWorkspace(t *testing.T) {
	f := newMainsFixture(t)
	junk := filepath.Join(f.WorktreesDir, "main", testRepo)
	if err := os.MkdirAll(filepath.Dir(junk), 0o755); err != nil {
		t.Fatalf("mkdir junk parent: %v", err)
	}
	mustJJ(t, f.RepoPath, "workspace", "add", "--name", "main", junk)

	stderr := captureStderr(t, func() {
		out := captureStdout(t, func() {
			if err := runToBranch(f.JJ, f.Cfg, branchURL("main")); err != nil {
				t.Fatalf("runToBranch: %v", err)
			}
		})
		if got := strings.TrimSpace(out); got != f.RepoPath {
			t.Errorf("stdout = %q, want the mains clone %q (not the redundant workspace)", got, f.RepoPath)
		}
	})

	if !strings.Contains(stderr, junk) {
		t.Errorf("stderr should name the redundant workspace %s, got:\n%s", junk, stderr)
	}
	if _, err := os.Stat(junk); err != nil {
		t.Errorf("the redundant workspace was removed; reporting must be non-destructive: %v", err)
	}
}

// TestRunToBranchNonTrunkStillCreatesWorktree pins the behaviour that must not
// regress: everything that is not trunk keeps getting its own workspace.
func TestRunToBranchNonTrunkStillCreatesWorktree(t *testing.T) {
	f := newMainsFixture(t)
	// Park the bookmark on a side change and leave the clone's @ on trunk, so
	// findExistingCheckout does not (correctly) hand back the clone itself.
	mustJJ(t, f.RepoPath, "new", "main", "-m", "feature work")
	jjtest.Bookmark(t, f.RepoPath, "feature/x", "@")
	mustJJ(t, f.RepoPath, "new", "main")

	out := captureStdout(t, func() {
		if err := runToBranch(f.JJ, f.Cfg, branchURL("feature/x")); err != nil {
			t.Fatalf("runToBranch: %v", err)
		}
	})

	want := filepath.Join(f.WorktreesDir, gh.SanitizeBranch("feature/x"), testRepo)
	if got := strings.TrimSpace(out); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("workspace not created at %s: %v", want, err)
	}
}

// TestRunToBranchColocatesLegacyClone mirrors TestCreateWorktreeColocatesLegacyRepo
// through the new trunk path. createWorktree was the only caller of
// EnsureColocated, so the trunk short-circuit is exactly where that side effect
// could have been dropped.
func TestRunToBranchColocatesLegacyClone(t *testing.T) {
	f := newMainsFixture(t)
	mustJJ(t, f.RepoPath, "git", "colocation", "disable")
	if f.JJ.IsColocated(f.RepoPath) {
		t.Fatalf("expected the clone to be non-colocated after simulating legacy state")
	}

	captureStdout(t, func() {
		if err := runToBranch(f.JJ, f.Cfg, branchURL("main")); err != nil {
			t.Fatalf("runToBranch: %v", err)
		}
	})

	if !f.JJ.IsColocated(f.RepoPath) {
		t.Errorf("the trunk path did not colocate the legacy clone")
	}
}

// TestRedundantTrunkWorkspaceIgnoresUnrelatedWorkspaces guards the path
// comparison from matching a workspace that merely shares the trunk name.
func TestRedundantTrunkWorkspaceIgnoresUnrelatedWorkspaces(t *testing.T) {
	f := newMainsFixture(t)
	elsewhere := filepath.Join(t.TempDir(), "main", testRepo)
	if err := os.MkdirAll(filepath.Dir(elsewhere), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mustJJ(t, f.RepoPath, "workspace", "add", "--name", "main", elsewhere)

	if got := redundantTrunkWorkspace(f.JJ, f.Cfg, f.RepoPath, "main"); got != "" {
		t.Errorf("redundantTrunkWorkspace = %q, want empty for a workspace outside worktrees_dir", got)
	}
}

// TestRunToLocalNoBranchReturnsMainRoot covers the second defect in the same
// bug: `wgo to owner/repo` returned whichever checkout the discovery walk hit
// first, which is routinely a secondary workspace under worktrees_dir. With no
// branch named, "owner/repo" means the repo itself — the main clone.
//
// This is the one test that drives the global config, so it isolates HOME and
// writes the config file `config.Init` reads.
func TestRunToLocalNoBranchReturnsMainRoot(t *testing.T) {
	f := newMainsFixture(t)

	// A secondary workspace that discovery will reach before the clone.
	ws := filepath.Join(f.WorktreesDir, "feature", testRepo)
	if err := os.MkdirAll(filepath.Dir(ws), 0o755); err != nil {
		t.Fatalf("mkdir workspace parent: %v", err)
	}
	mustJJ(t, f.RepoPath, "workspace", "add", "--name", "feature", ws)

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".wgo"), 0o755); err != nil {
		t.Fatalf("mkdir .wgo: %v", err)
	}
	// worktrees_dir is listed first so the walk finds the secondary workspace
	// before the clone; without the IsWorktree promotion that is what wins.
	toml := "[discovery]\nbase_dirs = [\"" + f.WorktreesDir + "\", \"" + f.Cfg.Worktree.MainsDir + "\"]\n" +
		"scan_depth = 5\n\n[worktree]\nmains_dir = \"" + f.Cfg.Worktree.MainsDir + "\"\n" +
		"worktrees_dir = \"" + f.WorktreesDir + "\"\n"
	if err := os.WriteFile(filepath.Join(home, ".wgo", "config.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runToLocal(testOwner + "/" + testRepo); err != nil {
			t.Fatalf("runToLocal: %v", err)
		}
	})

	if got := strings.TrimSpace(out); got != f.RepoPath {
		t.Errorf("stdout = %q, want the main clone %q", got, f.RepoPath)
	}
}

// captureStderr mirrors captureStdout for the advisory output, which is where
// every warning in this change lands so that `cd $(wgo to ...)` keeps working.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()

	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, readErr := r.Read(buf)
			sb.Write(buf[:n])
			if readErr != nil {
				break
			}
		}
		done <- sb.String()
	}()

	fn()
	_ = w.Close()
	return <-done
}
