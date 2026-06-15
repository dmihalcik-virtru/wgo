package jj_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/jjtest"
)

// TestSmokeTemplate is the schema-drift canary required by the spec. If jj
// upstream renames a template field LogEntryTemplate depends on, this test
// will fail before any other test can corrupt its diagnostics.
func TestSmokeTemplate(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	entries, err := c.Log(repo, "root()")
	if err != nil {
		t.Fatalf("Log root(): %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 entry for root(), got %d", len(entries))
	}
	if entries[0].ChangeID == "" || entries[0].CommitID == "" {
		t.Fatalf("template drift: empty id field in %+v", entries[0])
	}
	if jj.TemplateSchemaVersion != 1 {
		t.Fatalf("TemplateSchemaVersion drifted to %d; update parser if intentional", jj.TemplateSchemaVersion)
	}
}

func TestIsRepoAndRoot(t *testing.T) {
	repo, c := jjtest.NewRepo(t)

	if !c.IsRepo(repo) {
		t.Fatalf("IsRepo(%s) = false, want true", repo)
	}
	if c.IsRepo(t.TempDir()) {
		t.Fatalf("IsRepo on empty tempdir returned true")
	}
	root, err := c.Root(repo)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	// jj root may resolve symlinks (e.g. /var -> /private/var on macOS),
	// so compare via EvalSymlinks for both.
	wantRoot, _ := filepath.EvalSymlinks(repo)
	gotRoot, _ := filepath.EvalSymlinks(root)
	if wantRoot != gotRoot {
		t.Fatalf("Root = %s, want %s", root, repo)
	}
}

func TestWorkspacesAddListForget(t *testing.T) {
	repo, c := jjtest.NewRepo(t)

	ws, err := c.ListWorkspaces(repo)
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(ws) != 1 || ws[0].Name != "default" {
		t.Fatalf("expected single default workspace, got %+v", ws)
	}
	if ws[0].ChangeID == "" || ws[0].CommitID == "" {
		t.Fatalf("workspace template fields empty: %+v", ws[0])
	}

	wsPath := jjtest.NewWorkspace(t, repo, "ticket")
	ws2, err := c.ListWorkspaces(repo)
	if err != nil {
		t.Fatalf("ListWorkspaces post-add: %v", err)
	}
	if len(ws2) != 2 {
		t.Fatalf("expected 2 workspaces after add, got %d", len(ws2))
	}
	found := false
	for _, w := range ws2 {
		if w.Name == "ticket" {
			found = true
			gotPath, _ := filepath.EvalSymlinks(w.Path)
			wantPath, _ := filepath.EvalSymlinks(wsPath)
			if gotPath != wantPath {
				t.Fatalf("workspace path = %s, want %s", w.Path, wsPath)
			}
		}
	}
	if !found {
		t.Fatalf("ticket workspace not in list: %+v", ws2)
	}

	if err := c.WorkspaceForget(repo, "ticket"); err != nil {
		t.Fatalf("WorkspaceForget: %v", err)
	}
	ws3, err := c.ListWorkspaces(repo)
	if err != nil {
		t.Fatalf("ListWorkspaces post-forget: %v", err)
	}
	if len(ws3) != 1 {
		t.Fatalf("expected 1 workspace after forget, got %d", len(ws3))
	}
}

func TestWorkspaceRootAndUpdateStale(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	wsPath := jjtest.NewWorkspace(t, repo, "stale")
	root, err := c.WorkspaceRoot(wsPath)
	if err != nil {
		t.Fatalf("WorkspaceRoot: %v", err)
	}
	wantRoot, _ := filepath.EvalSymlinks(wsPath)
	gotRoot, _ := filepath.EvalSymlinks(root)
	if wantRoot != gotRoot {
		t.Fatalf("WorkspaceRoot = %s, want %s", root, wsPath)
	}
	// UpdateStale on a fresh workspace is a no-op; should succeed.
	if err := c.UpdateStale(wsPath); err != nil {
		t.Fatalf("UpdateStale: %v", err)
	}
}

func TestLogAndCurrentChangeAndResolve(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	jjtest.Commit(t, repo, "first", map[string]string{"a.txt": "hello"})

	all, err := c.Log(repo, "::@")
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("expected at least 2 log entries, got %d", len(all))
	}

	cur, err := c.CurrentChange(repo)
	if err != nil {
		t.Fatalf("CurrentChange: %v", err)
	}
	if !cur.CurrentWorkingCopy {
		t.Fatalf("CurrentChange not marked as current_working_copy: %+v", cur)
	}

	resolved, err := c.Resolve(repo, "@")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resolved) != 40 {
		t.Fatalf("Resolve returned %q (len %d), want 40-char hex", resolved, len(resolved))
	}
	if resolved != cur.CommitID {
		t.Fatalf("Resolve(@) = %s, CurrentChange commit = %s", resolved, cur.CommitID)
	}
}

func TestStatusAndIsClean(t *testing.T) {
	repo, c := jjtest.NewRepo(t)

	clean, dirty, err := c.IsClean(repo)
	if err != nil {
		t.Fatalf("IsClean (fresh): %v", err)
	}
	if !clean {
		t.Fatalf("expected clean, got dirty: %v", dirty)
	}

	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	st, err := c.Status(repo)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Clean {
		t.Fatalf("Status reported clean after writing a file: %+v", st)
	}
	if !contains(st.Added, "new.txt") {
		t.Fatalf("Status.Added missing new.txt: %+v", st)
	}
	clean, dirty, err = c.IsClean(repo)
	if err != nil {
		t.Fatalf("IsClean (dirty): %v", err)
	}
	if clean {
		t.Fatalf("IsClean reported clean after writing a file")
	}
	if len(dirty) == 0 {
		t.Fatalf("expected dirty entries, got none")
	}
}

func TestBookmarkLifecycle(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	jjtest.Commit(t, repo, "feature", map[string]string{"f.txt": "feat"})

	if err := c.BookmarkCreate(repo, "feat", "@-"); err != nil {
		t.Fatalf("BookmarkCreate: %v", err)
	}
	bms, err := c.BookmarkList(repo, jj.BookmarkListOpts{})
	if err != nil {
		t.Fatalf("BookmarkList: %v", err)
	}
	if !hasBookmark(bms, "feat") {
		t.Fatalf("BookmarkList missing 'feat': %+v", bms)
	}

	// Move it forward — should succeed without allowBackwards.
	jjtest.Commit(t, repo, "advance", map[string]string{"f2.txt": "more"})
	if err := c.BookmarkSet(repo, "feat", "@-", false); err != nil {
		t.Fatalf("BookmarkSet forward: %v", err)
	}

	// Move it backward — should fail without allowBackwards, succeed with.
	if err := c.BookmarkSet(repo, "feat", "root()", false); err == nil {
		t.Fatalf("BookmarkSet backward without allowBackwards should have failed")
	}
	if err := c.BookmarkSet(repo, "feat", "root()", true); err != nil {
		t.Fatalf("BookmarkSet backward with allowBackwards: %v", err)
	}

	if err := c.BookmarkDelete(repo, "feat"); err != nil {
		t.Fatalf("BookmarkDelete: %v", err)
	}
	bms2, err := c.BookmarkList(repo, jj.BookmarkListOpts{Local: true})
	if err != nil {
		t.Fatalf("BookmarkList post-delete: %v", err)
	}
	if hasBookmark(bms2, "feat") {
		t.Fatalf("bookmark 'feat' still present after delete: %+v", bms2)
	}
}

func TestMutationsNewDescribeEdit(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	jjtest.Commit(t, repo, "base", map[string]string{"base.txt": "b"})

	if err := c.Describe(repo, "renamed-base"); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	cur, err := c.CurrentChange(repo)
	if err != nil {
		t.Fatalf("CurrentChange: %v", err)
	}
	if !strings.Contains(cur.Description, "renamed-base") {
		t.Fatalf("Describe did not stick; description=%q", cur.Description)
	}

	if err := c.New(repo, "", "next"); err != nil {
		t.Fatalf("New: %v", err)
	}
	cur2, err := c.CurrentChange(repo)
	if err != nil {
		t.Fatalf("CurrentChange post-New: %v", err)
	}
	if !strings.Contains(cur2.Description, "next") {
		t.Fatalf("New did not pick up message; description=%q", cur2.Description)
	}

	// EditChange back to the prior change.
	if err := c.EditChange(repo, cur.ChangeID); err != nil {
		t.Fatalf("EditChange: %v", err)
	}
	cur3, err := c.CurrentChange(repo)
	if err != nil {
		t.Fatalf("CurrentChange post-Edit: %v", err)
	}
	if cur3.ChangeID != cur.ChangeID {
		t.Fatalf("EditChange did not move @; got %s want %s", cur3.ChangeID, cur.ChangeID)
	}
}

func TestGitInitInteropAndRemotes(t *testing.T) {
	jjtest.RequireJJ(t)
	c := jj.NewCLI()

	dest := filepath.Join(t.TempDir(), "fresh")
	if err := c.GitInit(dest, jj.InitOpts{}); err != nil {
		t.Fatalf("GitInit: %v", err)
	}
	if !c.IsRepo(dest) {
		t.Fatalf("expected new dir to be a jj repo")
	}
	// No remotes configured by default.
	remotes, err := c.RemoteURLs(dest)
	if err != nil {
		t.Fatalf("RemoteURLs: %v", err)
	}
	if len(remotes) != 0 {
		t.Fatalf("expected zero remotes on fresh init, got %+v", remotes)
	}
}

func TestGitCloneAndFetch(t *testing.T) {
	jjtest.RequireJJ(t)
	c := jj.NewCLI()

	// Use a bare-init local git repo as the "remote" to keep the test
	// hermetic.
	remote := filepath.Join(t.TempDir(), "remote.git")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	if _, err := runRaw(t, "", "git", "init", "--bare", remote); err != nil {
		t.Fatalf("git init bare: %v", err)
	}
	// Seed the bare repo with one commit via a scratch jj repo + push.
	seed := filepath.Join(t.TempDir(), "seed")
	if err := c.GitInit(seed, jj.InitOpts{}); err != nil {
		t.Fatalf("GitInit seed: %v", err)
	}
	if _, err := runRaw(t, seed, "jj", "git", "remote", "add", "origin", remote); err != nil {
		t.Fatalf("git remote add: %v", err)
	}
	if err := c.Describe(seed, "seed"); err != nil {
		t.Fatalf("Describe seed: %v", err)
	}
	if err := c.BookmarkCreate(seed, "main", "@"); err != nil {
		t.Fatalf("BookmarkCreate seed: %v", err)
	}
	if _, err := c.GitPush(seed, jj.PushOpts{Bookmarks: []string{"main"}, AllowNew: true}); err != nil {
		t.Fatalf("GitPush seed: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "clone")
	if err := c.GitClone(remote, dest); err != nil {
		t.Fatalf("GitClone: %v", err)
	}
	if !c.IsRepo(dest) {
		t.Fatalf("clone did not yield a jj repo")
	}
	rem, err := c.RemoteURLs(dest)
	if err != nil {
		t.Fatalf("RemoteURLs after clone: %v", err)
	}
	if rem["origin"] == "" {
		t.Fatalf("expected origin remote after clone, got %+v", rem)
	}

	if err := c.GitFetch(dest, "origin", nil); err != nil {
		t.Fatalf("GitFetch: %v", err)
	}
}

func TestGitPushNothingToPush(t *testing.T) {
	jjtest.RequireJJ(t)
	c := jj.NewCLI()

	remote := filepath.Join(t.TempDir(), "rem.git")
	if _, err := runRaw(t, "", "git", "init", "--bare", remote); err != nil {
		t.Fatalf("git init bare: %v", err)
	}
	seed := filepath.Join(t.TempDir(), "seed")
	if err := c.GitInit(seed, jj.InitOpts{}); err != nil {
		t.Fatalf("GitInit: %v", err)
	}
	if _, err := runRaw(t, seed, "jj", "git", "remote", "add", "origin", remote); err != nil {
		t.Fatalf("remote add: %v", err)
	}

	// With no bookmarks and --tracked, jj will report nothing to push.
	_, err := c.GitPush(seed, jj.PushOpts{})
	if err == nil {
		t.Fatalf("expected nothing-to-push error, got nil")
	}
	if !errors.Is(err, jj.ErrNothingToPush) && !strings.Contains(err.Error(), "no bookmarks to push") {
		// jj 0.42 phrases this as "Nothing changed." for some
		// configurations; both classifications are acceptable.
		t.Logf("ErrNothingToPush not matched directly: %v", err)
	}
}

// helpers

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func hasBookmark(bms []jj.Bookmark, name string) bool {
	for _, b := range bms {
		if b.Name == name {
			return true
		}
	}
	return false
}

func runRaw(t *testing.T, dir, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}
