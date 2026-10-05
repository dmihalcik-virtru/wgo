package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/jjtest"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

// The golden plan has a preamble, a custom section holding a fenced "## "
// line, a duplicate custom section, an Efforts section with a duplicate
// heading, stray text and a fence, and a duplicate Notes section. Every
// command below must change only the line it owns.
func readGoldenPlan(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "plan", "testdata", "roundtrip", "full.md"))
	if err != nil {
		t.Fatalf("read golden plan: %v", err)
	}
	return string(b)
}

// goldenHome points HOME at a temp dir holding the golden plan and an
// optional config.toml, and returns the plan path.
func goldenHome(t *testing.T, configTOML string) (planPath, golden string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	wgoDir := filepath.Join(home, ".wgo")
	if err := os.MkdirAll(wgoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	golden = readGoldenPlan(t)
	planPath = filepath.Join(wgoDir, "plan.md")
	if err := os.WriteFile(planPath, []byte(golden), 0o644); err != nil {
		t.Fatal(err)
	}
	if configTOML == "" {
		configTOML = "author = \"wgo-test\"\n"
	}
	if err := os.WriteFile(filepath.Join(wgoDir, "config.toml"), []byte(configTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	return planPath, golden
}

func withRepoFlag(t *testing.T, dir string) {
	t.Helper()
	old := repoFlag
	repoFlag = dir
	t.Cleanup(func() { repoFlag = old })
}

func mustReplaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if n := strings.Count(s, old); n != 1 {
		t.Fatalf("expected exactly one %q in the golden plan, found %d", old, n)
	}
	return strings.Replace(s, old, new, 1)
}

// assertPlanBytes compares the plan file with want byte for byte.
func assertPlanBytes(t *testing.T, planPath, want string) {
	t.Helper()
	got, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("plan bytes differ\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

const goldenLastBranch = "- **wgo:feat/x** — child work ↳ on gh-71-plan-efforts\n"

func TestAddPreservesPlan(t *testing.T) {
	planPath, golden := goldenHome(t, "")
	if err := addTask("write the tests", false); err != nil {
		t.Fatalf("addTask: %v", err)
	}
	want := mustReplaceOnce(t, golden, "- [ ] a passthrough note line\n", "- [ ] a passthrough note line\n○ write the tests\n")
	assertPlanBytes(t, planPath, want)
}

func TestDonePreservesPlan(t *testing.T) {
	planPath, golden := goldenHome(t, "")
	if err := completeTask("flaky", "", true); err != nil {
		t.Fatalf("completeTask: %v", err)
	}
	want := mustReplaceOnce(t, golden, "! fix the flaky test\n", "")
	assertPlanBytes(t, planPath, want)
}

func TestPlanAddPreservesPlan(t *testing.T) {
	planPath, golden := goldenHome(t, "")
	repo, _ := jjtest.NewRepo(t)
	jjtest.Bookmark(t, repo, "feat-y", "@")
	withRepoFlag(t, repo)

	if err := addAnnotation("why it matters"); err != nil {
		t.Fatalf("addAnnotation: %v", err)
	}
	line := "- **" + filepath.Base(repo) + ":feat-y** — why it matters\n"
	want := mustReplaceOnce(t, golden, goldenLastBranch, goldenLastBranch+line)
	assertPlanBytes(t, planPath, want)
}

func TestJoinPreservesPlan(t *testing.T) {
	f := newMainsFixture(t)
	toml := "[discovery]\nbase_dirs = [\"" + f.Cfg.Worktree.MainsDir + "\"]\nscan_depth = 5\n\n" +
		"[worktree]\nmains_dir = \"" + f.Cfg.Worktree.MainsDir + "\"\nworktrees_dir = \"" + f.WorktreesDir + "\"\n"
	planPath, golden := goldenHome(t, toml)

	// The current workspace: another repo already checked out for feat-x.
	current := filepath.Join(f.WorktreesDir, "feat-x", "other")
	if err := os.MkdirAll(current, 0o755); err != nil {
		t.Fatal(err)
	}
	mustJJ(t, current, "git", "init", "--colocate")
	mustJJ(t, current, "describe", "-m", "work")
	mustJJ(t, current, "bookmark", "create", "feat-x", "-r", "@")
	withRepoFlag(t, current)

	oldNoClaude := joinNoClaudeMD
	joinNoClaudeMD = true
	t.Cleanup(func() { joinNoClaudeMD = oldNoClaude })

	captureStdout(t, func() {
		if err := runJoin(testOwner+"/"+testRepo, true); err != nil {
			t.Fatalf("runJoin: %v", err)
		}
	})
	line := "- **" + testRepo + ":feat-x** — feat-x\n"
	want := mustReplaceOnce(t, golden, goldenLastBranch, goldenLastBranch+line)
	assertPlanBytes(t, planPath, want)
}

func TestSpecNewPreservesPlan(t *testing.T) {
	planPath, golden := goldenHome(t, "")
	repo, _ := jjtest.NewRepo(t)
	jjtest.Bookmark(t, repo, "WGO-999-thing", "@")
	withRepoFlag(t, repo)

	if err := runSpecNew("WGO-999", "a thing"); err != nil {
		t.Fatalf("runSpecNew: %v", err)
	}
	line := "- **" + filepath.Base(repo) + ":WGO-999-thing** — WGO-999: a thing 📄 spec/WGO-999.md\n"
	want := mustReplaceOnce(t, golden, goldenLastBranch, goldenLastBranch+line)
	assertPlanBytes(t, planPath, want)
}

func TestAddWithWorktreePreservesPlan(t *testing.T) {
	f := newMainsFixture(t)
	toml := "[discovery]\nbase_dirs = [\"" + f.Cfg.Worktree.MainsDir + "\"]\nscan_depth = 5\n\n" +
		"[worktree]\nmains_dir = \"" + f.Cfg.Worktree.MainsDir + "\"\nworktrees_dir = \"" + f.WorktreesDir + "\"\n"
	planPath, golden := goldenHome(t, toml)

	oldSpec, oldClaude := addNoSpec, addNoClaudeMD
	addNoSpec, addNoClaudeMD = true, true
	t.Cleanup(func() { addNoSpec, addNoClaudeMD = oldSpec, oldClaude })

	captureStdout(t, func() {
		if err := addWithWorktree("WGO-5", "the thing", []string{testOwner + "/" + testRepo}, false); err != nil {
			t.Fatalf("addWithWorktree: %v", err)
		}
	})
	branch := slugTicketBranch("WGO-5", "the thing")
	want := mustReplaceOnce(t, golden, "- [ ] a passthrough note line\n", "- [ ] a passthrough note line\n○ WGO-5 the thing\n")
	want = mustReplaceOnce(t, want, goldenLastBranch, goldenLastBranch+"- **"+testRepo+":"+branch+"** — WGO-5: the thing\n")
	assertPlanBytes(t, planPath, want)
}

func TestSpecLinkPreservesPlan(t *testing.T) {
	planPath, golden := goldenHome(t, "")
	repo, _ := jjtest.NewRepo(t)
	jjtest.Bookmark(t, repo, "WGO-999-thing", "@")
	withRepoFlag(t, repo)

	// Scaffold the spec, then put the plan back so only the link is under test.
	if err := runSpecNew("WGO-999", "a thing"); err != nil {
		t.Fatalf("runSpecNew: %v", err)
	}
	if err := os.WriteFile(planPath, []byte(golden), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runSpecLink("WGO-999"); err != nil {
		t.Fatalf("runSpecLink: %v", err)
	}
	line := "- **" + filepath.Base(repo) + ":WGO-999-thing** — WGO-999 📄 spec/WGO-999.md\n"
	want := mustReplaceOnce(t, golden, goldenLastBranch, goldenLastBranch+line)
	assertPlanBytes(t, planPath, want)
}

func TestTodayPlanSyncPreservesPlan(t *testing.T) {
	planPath, golden := goldenHome(t, "")
	s := store.NewWithDir(filepath.Dir(planPath))
	p, err := plan.Parse(golden)
	if err != nil {
		t.Fatal(err)
	}
	data := &todayData{
		plan:  p,
		store: s,
		branches: []activeBranch{
			{RepoName: "wgo", Branch: "main", Path: t.TempDir()},
			{RepoName: "wgo", Branch: "feat/x", Path: t.TempDir()}, // already in the plan
			{RepoName: "api", Branch: "fix/new", Path: t.TempDir()},
		},
	}
	captureStdout(t, func() { syncPlanBranches(data) })
	want := mustReplaceOnce(t, golden, goldenLastBranch, goldenLastBranch+"- **api:fix/new** — active branch\n")
	assertPlanBytes(t, planPath, want)
}
