package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/jjtest"
)

func TestParkSlug(t *testing.T) {
	head := jj.Change{ChangeID: "qpvuntsmabcdef"}

	tests := []struct {
		name     string
		work     []jj.Change
		existing string
		override string
		want     string
	}{
		{
			name:     "explicit name wins",
			work:     []jj.Change{{Description: "wip: something"}},
			existing: "feat-x",
			override: "my/override",
			want:     "my-override",
		},
		{
			name:     "existing bookmark reused",
			work:     []jj.Change{{Description: "wip: something"}},
			existing: "feat-x",
			want:     "feat-x",
		},
		{
			name: "derived from newest description",
			work: []jj.Change{{Description: "feat(loader): rework widget cache\n\nbody"}},
			want: "feat-loader-rework-widget-cache",
		},
		{
			name: "skips blank descriptions",
			work: []jj.Change{{Description: "  \n"}, {Description: "fix the thing"}},
			want: "fix-the-thing",
		},
		{
			name: "long description truncated at a dash",
			work: []jj.Change{{Description: "this is a really quite long commit description that keeps going"}},
			want: "this-is-a-really-quite-long-commit",
		},
		{
			name: "undescribed work falls back to change id",
			work: []jj.Change{{Description: ""}},
			want: "wip-qpvuntsm",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parkSlug(tt.work, head, tt.existing, tt.override)
			if got != tt.want {
				t.Errorf("parkSlug = %q, want %q", got, tt.want)
			}
			if len(got) > 40 && tt.override == "" {
				t.Errorf("parkSlug = %q, exceeds the 40-char budget", got)
			}
		})
	}
}

// mustJJ runs jj inside dir, failing the test on non-zero exit.
func mustJJ(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("jj", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("jj %v (in %s): %v\nstderr: %s", args, dir, err, stderr.String())
	}
	return stdout.String()
}

// newTrunkRepo builds a colocated jj repo whose trunk() resolves to a real
// commit: jj's trunk() alias only matches main/master/trunk on a *remote*, so a
// bare jjtest.NewRepo has no trunk and none of the park paths would engage.
//
// After the push, @ is a fresh empty undescribed change on top of trunk — the
// resting state a main clone is supposed to be in.
func newTrunkRepo(t *testing.T) (string, *jj.CLIClient, *config.Config) {
	t.Helper()
	remote := jjtest.NewBareRemote(t)
	repo, jjc := jjtest.NewRepo(t)
	jjtest.Bookmark(t, repo, "main", "@")
	mustJJ(t, repo, "git", "remote", "add", "origin", remote)
	mustJJ(t, repo, "git", "push", "--bookmark", "main")

	cfg := &config.Config{Worktree: config.WorktreeConfig{WorktreesDir: t.TempDir()}}
	return repo, jjc, cfg
}

// opHead returns the current operation id, used to assert that a --dry-run or a
// rejected park left the repo untouched.
func opHead(t *testing.T, repo string) string {
	t.Helper()
	return strings.TrimSpace(mustJJ(t, repo, "op", "log", "--no-graph", "-n", "1", "-T", "id.short()"))
}

func TestPlanParkNoWorkOnCleanClone(t *testing.T) {
	jjtest.RequireJJ(t)
	repo, jjc, cfg := newTrunkRepo(t)

	p, ok, err := planPark(jjc, cfg, repo, parkOpts{})
	if err != nil {
		t.Fatalf("planPark: %v", err)
	}
	if ok {
		t.Fatalf("planPark reported work on a clean clone: %+v", p)
	}
}

// TestPlanParkDetectsDescribedEmptyChange pins the half of the emptiness rule
// that is easy to get wrong: a commit message written but no edits yet is still
// work the user would be upset to have silently left behind.
func TestPlanParkDetectsDescribedEmptyChange(t *testing.T) {
	jjtest.RequireJJ(t)
	repo, jjc, cfg := newTrunkRepo(t)
	mustJJ(t, repo, "describe", "-m", "wip: about to start")

	p, ok, err := planPark(jjc, cfg, repo, parkOpts{})
	if err != nil {
		t.Fatalf("planPark: %v", err)
	}
	if !ok {
		t.Fatal("planPark missed a described-but-empty @")
	}
	if p.Slug != "wip-about-to-start" {
		t.Errorf("Slug = %q, want %q", p.Slug, "wip-about-to-start")
	}
}

func TestParkMovesDirtyWorkingCopy(t *testing.T) {
	jjtest.RequireJJ(t)
	repo, jjc, cfg := newTrunkRepo(t)

	mustJJ(t, repo, "describe", "-m", "wip: scratch work")
	if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write scratch: %v", err)
	}
	before, err := jjc.CurrentChange(repo)
	if err != nil {
		t.Fatalf("CurrentChange: %v", err)
	}

	if err := runPark(jjc, cfg, repo, parkOpts{}); err != nil {
		t.Fatalf("runPark: %v", err)
	}

	dest := filepath.Join(cfg.Worktree.WorktreesDir, "wip-scratch-work", filepath.Base(repo))

	// The file moved with the work.
	if _, err := os.Stat(filepath.Join(dest, "scratch.txt")); err != nil {
		t.Errorf("scratch.txt missing from destination: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "scratch.txt")); !os.IsNotExist(err) {
		t.Errorf("scratch.txt still present in the main clone (err=%v)", err)
	}

	// The destination's @ is the very change that was stranded.
	destHead, err := jjc.CurrentChange(dest)
	if err != nil {
		t.Fatalf("CurrentChange(dest): %v", err)
	}
	if destHead.ChangeID != before.ChangeID {
		t.Errorf("dest @ = %s, want the original change %s", destHead.ChangeID, before.ChangeID)
	}

	// The clone is back to a clean empty change on trunk.
	mainHead, err := jjc.CurrentChange(repo)
	if err != nil {
		t.Fatalf("CurrentChange(repo): %v", err)
	}
	if !mainHead.Empty {
		t.Errorf("main clone @ is not empty after park")
	}
	if n, err := jjc.CountRevset(repo, "(trunk())..(@)"); err != nil || n != 1 {
		t.Errorf("main clone is %d change(s) above trunk (err=%v), want 1", n, err)
	}

	// And the work is findable by name.
	bms, err := jjc.BookmarkList(repo, jj.BookmarkListOpts{Local: true, Names: []string{"wip-scratch-work"}})
	if err != nil || len(bms) == 0 {
		t.Errorf("bookmark wip-scratch-work not created (err=%v, got %d)", err, len(bms))
	}
}

func TestParkMovesDescribedStack(t *testing.T) {
	jjtest.RequireJJ(t)
	repo, jjc, cfg := newTrunkRepo(t)

	jjtest.Commit(t, repo, "first step", map[string]string{"one.txt": "1\n"})
	jjtest.Commit(t, repo, "second step", map[string]string{"two.txt": "2\n"})

	if err := runPark(jjc, cfg, repo, parkOpts{Name: "stack"}); err != nil {
		t.Fatalf("runPark: %v", err)
	}

	dest := filepath.Join(cfg.Worktree.WorktreesDir, "stack", filepath.Base(repo))
	for _, f := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Errorf("%s missing from destination: %v", f, err)
		}
	}
	if n, err := jjc.CountRevset(repo, "(trunk())..(@)"); err != nil || n != 1 {
		t.Errorf("main clone is %d change(s) above trunk (err=%v), want 1", n, err)
	}
}

func TestParkReusesExistingBookmark(t *testing.T) {
	jjtest.RequireJJ(t)
	repo, jjc, cfg := newTrunkRepo(t)

	mustJJ(t, repo, "describe", "-m", "feat: widget")
	if err := os.WriteFile(filepath.Join(repo, "w.txt"), []byte("w\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	jjtest.Bookmark(t, repo, "feat-widget", "@")

	if err := runPark(jjc, cfg, repo, parkOpts{}); err != nil {
		t.Fatalf("runPark: %v", err)
	}

	dest := filepath.Join(cfg.Worktree.WorktreesDir, "feat-widget", filepath.Base(repo))
	if _, err := os.Stat(filepath.Join(dest, "w.txt")); err != nil {
		t.Errorf("work not at the bookmark-derived destination %s: %v", dest, err)
	}
	// The description-derived slug must not have been used instead.
	if _, err := os.Stat(filepath.Join(cfg.Worktree.WorktreesDir, "feat-widget-2")); err == nil {
		t.Errorf("a second bookmark-derived directory was created")
	}
}

func TestParkDryRunChangesNothing(t *testing.T) {
	jjtest.RequireJJ(t)
	repo, jjc, cfg := newTrunkRepo(t)

	mustJJ(t, repo, "describe", "-m", "wip: preview me")
	if err := os.WriteFile(filepath.Join(repo, "p.txt"), []byte("p\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Snapshot the working copy so the op head is stable before we compare.
	if _, err := jjc.CurrentChange(repo); err != nil {
		t.Fatalf("CurrentChange: %v", err)
	}
	before := opHead(t, repo)

	if err := runPark(jjc, cfg, repo, parkOpts{DryRun: true}); err != nil {
		t.Fatalf("runPark --dry-run: %v", err)
	}

	if after := opHead(t, repo); after != before {
		t.Errorf("--dry-run advanced the operation log: %s -> %s", before, after)
	}
	dest := filepath.Join(cfg.Worktree.WorktreesDir, "wip-preview-me", filepath.Base(repo))
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("--dry-run created %s (err=%v)", dest, err)
	}
}

func TestParkNoWorkIsNoOp(t *testing.T) {
	jjtest.RequireJJ(t)
	repo, jjc, cfg := newTrunkRepo(t)

	if err := runPark(jjc, cfg, repo, parkOpts{}); err != nil {
		t.Fatalf("runPark on a clean clone should succeed, got: %v", err)
	}
	entries, err := os.ReadDir(cfg.Worktree.WorktreesDir)
	if err != nil {
		t.Fatalf("read worktrees dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("runPark created %d entry/entries under worktrees_dir on a clean clone", len(entries))
	}
}

func TestParkAbortsOnOccupiedDestination(t *testing.T) {
	jjtest.RequireJJ(t)
	repo, jjc, cfg := newTrunkRepo(t)

	mustJJ(t, repo, "describe", "-m", "wip: collide")
	if err := os.WriteFile(filepath.Join(repo, "c.txt"), []byte("c\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	dest := filepath.Join(cfg.Worktree.WorktreesDir, "wip-collide", filepath.Base(repo))
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "someone-elses.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write squatter: %v", err)
	}
	if _, err := jjc.CurrentChange(repo); err != nil {
		t.Fatalf("CurrentChange: %v", err)
	}
	before := opHead(t, repo)

	err := runPark(jjc, cfg, repo, parkOpts{})
	if err == nil {
		t.Fatal("expected an error for an occupied destination")
	}
	if !strings.Contains(err.Error(), "--name") {
		t.Errorf("error should suggest --name, got: %v", err)
	}
	if after := opHead(t, repo); after != before {
		t.Errorf("a rejected park advanced the operation log: %s -> %s", before, after)
	}
}

// TestParkRollsBackBookmarkWhenWorkspaceAddFails pins the M2 rollback: the
// bookmark created by M1 must not survive a failed workspace add, or a retry
// would abort at the P8 name-collision check on the debris of the first run.
func TestParkRollsBackBookmarkWhenWorkspaceAddFails(t *testing.T) {
	jjtest.RequireJJ(t)
	if os.Geteuid() == 0 {
		t.Skip("running as root; a read-only destination would still be writable")
	}
	repo, jjc, cfg := newTrunkRepo(t)

	mustJJ(t, repo, "describe", "-m", "wip: rollback me")
	if err := os.WriteFile(filepath.Join(repo, "r.txt"), []byte("r\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// An empty but unwritable destination passes preflight (P5 only rejects a
	// non-empty non-workspace directory) and then fails inside `jj workspace add`.
	dest := filepath.Join(cfg.Worktree.WorktreesDir, "wip-rollback-me", filepath.Base(repo))
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}
	if err := os.Chmod(dest, 0o555); err != nil {
		t.Fatalf("chmod dest: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dest, 0o755) })

	err := runPark(jjc, cfg, repo, parkOpts{})
	if err == nil {
		t.Fatal("expected runPark to fail on an unwritable destination")
	}
	if !strings.Contains(err.Error(), "recover with") {
		t.Errorf("error should carry a recovery hint, got: %v", err)
	}

	bms, listErr := jjc.BookmarkList(repo, jj.BookmarkListOpts{Local: true, Names: []string{"wip-rollback-me"}})
	if listErr != nil {
		t.Fatalf("BookmarkList: %v", listErr)
	}
	for _, b := range bms {
		if b.Name == "wip-rollback-me" && b.Remote == "" && b.Present {
			t.Errorf("bookmark wip-rollback-me survived the rollback")
		}
	}

	// The work itself must be untouched — that is what makes the rollback safe.
	if n, cntErr := jjc.CountRevset(repo, "(trunk())..(@)"); cntErr != nil || n != 1 {
		t.Errorf("main clone is %d change(s) above trunk (err=%v), want the work still there (1)", n, cntErr)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "r.txt")); statErr != nil {
		t.Errorf("work file lost from the main clone: %v", statErr)
	}
}

func TestResolveParkTargetRejectsSecondaryWorkspace(t *testing.T) {
	jjtest.RequireJJ(t)
	repo, jjc, _ := newTrunkRepo(t)
	ws := jjtest.NewWorkspace(t, repo, "feature")

	_, err := resolveParkTarget(jjc, ws)
	if err == nil {
		t.Fatal("expected an error when run from a secondary workspace")
	}
	if !strings.Contains(err.Error(), repo) {
		t.Errorf("error should name the main clone %s, got: %v", repo, err)
	}
}

func TestResolveParkTargetAcceptsMainClone(t *testing.T) {
	jjtest.RequireJJ(t)
	repo, jjc, _ := newTrunkRepo(t)

	got, err := resolveParkTarget(jjc, repo)
	if err != nil {
		t.Fatalf("resolveParkTarget: %v", err)
	}
	if absResolved(got) != absResolved(repo) {
		t.Errorf("resolveParkTarget = %q, want %q", got, repo)
	}
}
