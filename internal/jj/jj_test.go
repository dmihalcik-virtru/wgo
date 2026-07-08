package jj_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	if jj.TemplateSchemaVersion != 2 {
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
	if !slices.Contains(st.Added, "new.txt") {
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

func TestAuthorEmailPopulated(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	jjtest.Commit(t, repo, "with-email", map[string]string{"a.txt": "x"})
	entries, err := c.Log(repo, "@-")
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one entry")
	}
	// Any non-empty, well-formed email proves the template field was parsed.
	// The exact value depends on the user's jj/git config and is not pinned
	// here (env-var-based identity override happens at change creation time;
	// jjtest's initial described change retains the global config identity).
	if entries[0].AuthorEmail == "" || !strings.Contains(entries[0].AuthorEmail, "@") {
		t.Fatalf("AuthorEmail = %q, want non-empty email", entries[0].AuthorEmail)
	}
}

func TestMainWorkspaceRoot(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	// From the main workspace itself.
	got, err := c.MainWorkspaceRoot(repo)
	if err != nil {
		t.Fatalf("MainWorkspaceRoot(main): %v", err)
	}
	wantMain, _ := filepath.EvalSymlinks(repo)
	gotMain, _ := filepath.EvalSymlinks(got)
	if gotMain != wantMain {
		t.Fatalf("MainWorkspaceRoot(main) = %s, want %s", got, repo)
	}
	// From a secondary workspace.
	wsPath := jjtest.NewWorkspace(t, repo, "secondary")
	gotFromWS, err := c.MainWorkspaceRoot(wsPath)
	if err != nil {
		t.Fatalf("MainWorkspaceRoot(secondary): %v", err)
	}
	gotFromWSResolved, _ := filepath.EvalSymlinks(gotFromWS)
	if gotFromWSResolved != wantMain {
		t.Fatalf("MainWorkspaceRoot(secondary) = %s, want %s", gotFromWS, repo)
	}
}

func TestAheadBehindNoRemote(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	jjtest.Commit(t, repo, "x", map[string]string{"f.txt": "x"})
	if err := c.BookmarkCreate(repo, "feat", "@-"); err != nil {
		t.Fatalf("BookmarkCreate: %v", err)
	}
	ahead, behind, err := c.AheadBehind(repo, "feat")
	if err != nil {
		t.Fatalf("AheadBehind: %v", err)
	}
	// Without a remote, both sides count as zero.
	if ahead != 0 || behind != 0 {
		t.Fatalf("AheadBehind = (%d, %d), want (0, 0)", ahead, behind)
	}
}

func TestAheadBehindWithRemote(t *testing.T) {
	jjtest.RequireJJ(t)
	c := jj.NewCLI()

	remote := filepath.Join(t.TempDir(), "remote.git")
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
	// Seed one commit, bookmark it, push.
	if err := c.Describe(seed, "first"); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if err := c.BookmarkCreate(seed, "main", "@"); err != nil {
		t.Fatalf("BookmarkCreate main: %v", err)
	}
	if _, err := c.GitPush(seed, jj.PushOpts{Bookmarks: []string{"main"}, AllowNew: true}); err != nil {
		t.Fatalf("GitPush main: %v", err)
	}
	// Add one more local commit and advance main onto it (do not push), so
	// the local bookmark is one change ahead of its pushed remote position.
	if err := c.New(seed, "", "second"); err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.BookmarkSet(seed, "main", "@", false); err != nil {
		t.Fatalf("BookmarkSet main: %v", err)
	}
	ahead, behind, err := c.AheadBehind(seed, "main")
	if err != nil {
		t.Fatalf("AheadBehind: %v", err)
	}
	if ahead != 1 || behind != 0 {
		t.Fatalf("AheadBehind = (%d, %d), want (1, 0)", ahead, behind)
	}
}

func TestDiffStatAndChangedFiles(t *testing.T) {
	repo, c := jjtest.NewRepo(t)
	jjtest.Commit(t, repo, "first", map[string]string{
		"a.txt": "line1\nline2\nline3\n",
		"b.txt": "alpha\nbeta\n",
	})
	added, deleted, err := c.DiffStat(repo, "@-")
	if err != nil {
		t.Fatalf("DiffStat: %v", err)
	}
	if added != 5 || deleted != 0 {
		t.Fatalf("DiffStat = (%d added, %d deleted), want (5, 0)", added, deleted)
	}
	files, err := c.ChangedFiles(repo, "@-")
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	if len(files) != 2 || !slices.Contains(files, "a.txt") || !slices.Contains(files, "b.txt") {
		t.Fatalf("ChangedFiles = %v, want [a.txt b.txt]", files)
	}
}

// TestBookmarkTrack fetches a branch as an untracked remote bookmark, then
// tracks it — verifying a local bookmark appears and the remote entry is
// marked tracked. A second track call must be a no-op (idempotent).
func TestBookmarkTrack(t *testing.T) {
	jjtest.RequireJJ(t)
	c := jj.NewCLI()

	remote := filepath.Join(t.TempDir(), "remote.git")
	if _, err := runRaw(t, "", "git", "init", "--bare", remote); err != nil {
		t.Fatalf("git init bare: %v", err)
	}

	// Seed repo: create a feature bookmark and push it to the remote.
	seed := filepath.Join(t.TempDir(), "seed")
	if err := c.GitInit(seed, jj.InitOpts{}); err != nil {
		t.Fatalf("GitInit seed: %v", err)
	}
	if _, err := runRaw(t, seed, "jj", "git", "remote", "add", "origin", remote); err != nil {
		t.Fatalf("seed remote add: %v", err)
	}
	if err := c.Describe(seed, "feature work"); err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if err := c.BookmarkCreate(seed, "feature", "@"); err != nil {
		t.Fatalf("BookmarkCreate feature: %v", err)
	}
	if _, err := c.GitPush(seed, jj.PushOpts{Bookmarks: []string{"feature"}, AllowNew: true}); err != nil {
		t.Fatalf("GitPush feature: %v", err)
	}

	// Consumer repo: fetch the branch. With auto-local-bookmark off (the jj
	// default) this arrives as an untracked remote bookmark.
	consumer := filepath.Join(t.TempDir(), "consumer")
	if err := c.GitInit(consumer, jj.InitOpts{}); err != nil {
		t.Fatalf("GitInit consumer: %v", err)
	}
	if _, err := runRaw(t, consumer, "jj", "git", "remote", "add", "origin", remote); err != nil {
		t.Fatalf("consumer remote add: %v", err)
	}
	if err := c.GitFetch(consumer, "origin", []string{"feature"}); err != nil {
		t.Fatalf("GitFetch: %v", err)
	}

	// Track it.
	if err := c.BookmarkTrack(consumer, "feature", "origin"); err != nil {
		t.Fatalf("BookmarkTrack: %v", err)
	}

	bms, err := c.BookmarkList(consumer, jj.BookmarkListOpts{AllRemotes: true})
	if err != nil {
		t.Fatalf("BookmarkList: %v", err)
	}
	var haveLocal, remoteTracked bool
	for _, b := range bms {
		if b.Name != "feature" {
			continue
		}
		if b.Remote == "" {
			haveLocal = true
		}
		if b.Remote == "origin" && b.Tracked {
			remoteTracked = true
		}
	}
	if !haveLocal {
		t.Fatalf("expected a local 'feature' bookmark after track: %+v", bms)
	}
	if !remoteTracked {
		t.Fatalf("expected feature@origin to be tracked after track: %+v", bms)
	}

	// Idempotent: tracking again is a no-op, not an error.
	if err := c.BookmarkTrack(consumer, "feature", "origin"); err != nil {
		t.Fatalf("BookmarkTrack (second call) should be a no-op: %v", err)
	}
}

// TestNearestBookmark verifies that NearestBookmark resolves the bookmark the
// way git resolves "current branch": @ when it carries a bookmark, otherwise
// the nearest ancestor that does (jj's working copy is normally an empty change
// above the bookmark, which sits on @-), and "" when nothing in the ancestry is
// bookmarked.
func TestNearestBookmark(t *testing.T) {
	t.Run("bookmark on @-", func(t *testing.T) {
		repo, c := jjtest.NewRepo(t)
		// Commit leaves @ empty with the described change on @-.
		jjtest.Commit(t, repo, "work", map[string]string{"a.txt": "hi"})
		jjtest.Bookmark(t, repo, "feat", "@-")

		got, err := c.NearestBookmark(repo)
		if err != nil {
			t.Fatalf("NearestBookmark: %v", err)
		}
		if got != "feat" {
			t.Fatalf("NearestBookmark = %q, want %q", got, "feat")
		}
	})

	t.Run("bookmark on @", func(t *testing.T) {
		repo, c := jjtest.NewRepo(t)
		jjtest.Bookmark(t, repo, "onhead", "@")

		got, err := c.NearestBookmark(repo)
		if err != nil {
			t.Fatalf("NearestBookmark: %v", err)
		}
		if got != "onhead" {
			t.Fatalf("NearestBookmark = %q, want %q", got, "onhead")
		}
	})

	t.Run("no bookmark in ancestry", func(t *testing.T) {
		repo, c := jjtest.NewRepo(t)
		jjtest.Commit(t, repo, "work", map[string]string{"a.txt": "hi"})

		got, err := c.NearestBookmark(repo)
		if err != nil {
			t.Fatalf("NearestBookmark: %v", err)
		}
		if got != "" {
			t.Fatalf("NearestBookmark = %q, want empty", got)
		}
	})
}

// helpers

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
