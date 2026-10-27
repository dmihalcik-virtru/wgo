package cleanup

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/jjtest"
)

// trunkRepo is a main clone whose origin holds main, so trunk() resolves to
// main@origin as it does in a real clone.
type trunkRepo struct {
	t    *testing.T
	path string
	jj   *jj.CLIClient
}

func newTrunkRepo(t *testing.T) *trunkRepo {
	t.Helper()
	remote := jjtest.NewBareRemote(t)
	repo, c := jjtest.NewRepo(t)
	require.NoError(t, c.GitRemoteAdd(repo, "origin", remote))
	jjtest.Commit(t, repo, "base", map[string]string{"a.txt": "1\n2\n3\n4\n5\n", "gone.txt": "bye\n"})
	require.NoError(t, c.BookmarkCreate(repo, "main", "@-"))
	_, err := c.GitPush(repo, jj.PushOpts{Bookmarks: []string{"main"}, AllowNew: true})
	require.NoError(t, err)
	return &trunkRepo{t: t, path: repo, jj: c}
}

// land pushes a commit with msg and files on top of main, as a merge would.
func (r *trunkRepo) land(msg string, files map[string]string) {
	r.t.Helper()
	require.NoError(r.t, r.jj.New(r.path, "main", ""))
	jjtest.Commit(r.t, r.path, msg, files)
	require.NoError(r.t, r.jj.BookmarkSet(r.path, "main", "@-", false))
	_, err := r.jj.GitPush(r.path, jj.PushOpts{Bookmarks: []string{"main"}})
	require.NoError(r.t, err)
}

// workspace adds a workspace named name on trunk.
func (r *trunkRepo) workspace(name string) WorkspaceRef {
	r.t.Helper()
	path := jjtest.NewWorkspace(r.t, r.path, name)
	run(r.t, path, "new", "main@origin")
	return WorkspaceRef{Name: name, Path: path, RepoPath: r.path, DefaultBranch: "main"}
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("jj", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), "jj %v: %s", args, stderr.String())
	return strings.TrimSpace(stdout.String())
}

func commitID(t *testing.T, dir, rev string) string {
	t.Helper()
	return run(t, dir, "log", "--no-graph", "-r", rev, "-T", "commit_id")
}

var (
	longAgo = time.Now().Add(-90 * 24 * time.Hour)
	justNow = time.Now()
)

// classifier reads through a read-only client and reports every workspace as
// last used at lastUsed.
func classifier(c *jj.CLIClient, prs PRSource, lastUsed time.Time) *Classifier {
	return NewClassifier(c.ReadOnly(), prs, ClassifyOptions{
		StaleAfter: 14 * 24 * time.Hour,
		LastUsed:   func(string) time.Time { return lastUsed },
	})
}

type fakePRs struct {
	byBranch map[string][]github.PRInfo
	byNumber map[int]*github.PRInfo
	commits  map[int][]github.PRCommit
}

func (f fakePRs) ListPRsForBranch(_, branch string) ([]github.PRInfo, error) {
	return f.byBranch[branch], nil
}

func (f fakePRs) GetPRByNumber(_ string, n int) (*github.PRInfo, error) { return f.byNumber[n], nil }

func (f fakePRs) ListPRCommits(_ string, n int) ([]github.PRCommit, error) { return f.commits[n], nil }

func merged(n int) github.PRInfo {
	at := time.Now().Add(-24 * time.Hour)
	return github.PRInfo{Number: n, State: "merged", MergedAt: &at}
}

func assertVerdict(t *testing.T, cl Classification, typ WorkspaceType, tier Tier, evidence string) {
	t.Helper()
	assert.Equal(t, typ, cl.Type, "evidence: %q", cl.Evidence)
	assert.Equal(t, tier, cl.Tier)
	if evidence != "" {
		assert.Contains(t, strings.Join(cl.Evidence, "\n"), evidence)
	}
}

func TestClassifyIdle(t *testing.T) {
	r := newTrunkRepo(t)
	ws := r.workspace("idle")

	assertVerdict(t, classifier(r.jj, nil, longAgo).Classify(ws), TypeIdle, TierRemove, "no changes of its own")
	assertVerdict(t, classifier(r.jj, nil, justNow).Classify(ws), TypeFresh, TierKeep, "")

	// status.stale_days of 0 must not make a workspace created a moment ago idle.
	unset := NewClassifier(r.jj.ReadOnly(), nil, ClassifyOptions{LastUsed: func(string) time.Time { return justNow }})
	assertVerdict(t, unset.Classify(ws), TypeFresh, TierKeep, "")

	jjtest.Bookmark(t, ws.Path, "WGO-9-plan", "@-")
	assertVerdict(t, classifier(r.jj, nil, longAgo).Classify(ws), TypePlanned, TierKeep, "WGO-9-plan")
}

func TestClassifyUnpushed(t *testing.T) {
	r := newTrunkRepo(t)
	ws := r.workspace("feature")
	jjtest.Commit(t, ws.Path, "feat: thing", map[string]string{"b.txt": "mine\n"})

	cl := classifier(r.jj, nil, longAgo).Classify(ws)
	assertVerdict(t, cl, TypeUnpushed, TierDecide, "1 change is on no bookmark or remote")
	assert.Contains(t, cl.Evidence, "no trunk commit since the fork touches these files")
	assert.Contains(t, cl.Hint, "bookmark create feature")
	assert.Equal(t, 1, cl.Work.Changes)
	assert.Equal(t, "feat: thing", cl.Work.Subject)

	assertVerdict(t, classifier(r.jj, nil, justNow).Classify(ws), TypeInProgress, TierKeep, "unpushed")
}

// The content proof finds the squash commit even though trunk changed the
// same line again afterwards, which comparing with today's trunk would miss.
func TestClassifyLandedByContent(t *testing.T) {
	r := newTrunkRepo(t)
	ws := r.workspace("fix")
	jjtest.Commit(t, ws.Path, "fix: two", map[string]string{"a.txt": "1\ntwo\n3\n4\n5\n"})
	r.land("fix: two (#12)", map[string]string{"a.txt": "1\ntwo\n3\n4\n5\n"})
	r.land("later: rework", map[string]string{"a.txt": "1\nTWO\n3\n4\nfive\n"})

	assertVerdict(t, classifier(r.jj, nil, longAgo).Classify(ws), TypeLanded, TierRemove, `"fix: two (#12)"`)
}

func TestClassifyProbablyLanded(t *testing.T) {
	r := newTrunkRepo(t)
	ws := r.workspace("draft")
	jjtest.Commit(t, ws.Path, "feat: x", map[string]string{"c.txt": "mine\n"})
	r.land("feat: x (#7)", map[string]string{"c.txt": "theirs\n"})

	cl := classifier(r.jj, nil, longAgo).Classify(ws)
	assertVerdict(t, cl, TypeProbablyLanded, TierDecide, "(PR #7)")
	assert.Contains(t, strings.Join(cl.Evidence, "\n"), "0/1 files on trunk")
	assert.Contains(t, strings.Join(cl.Evidence, "\n"), "c.txt (differs from trunk)")
}

func TestClassifyDeletionsOnly(t *testing.T) {
	r := newTrunkRepo(t)
	ws := r.workspace("oops")
	require.NoError(t, os.Remove(filepath.Join(ws.Path, "gone.txt")))
	run(t, ws.Path, "commit", "-m", "remove a file")

	assertVerdict(t, classifier(r.jj, nil, longAgo).Classify(ws), TypeDeletionsOnly, TierRemove, "only deletes 1 file")
}

func TestClassifyCovered(t *testing.T) {
	r := newTrunkRepo(t)
	ws := r.workspace("covered")
	jjtest.Commit(t, ws.Path, "feat: on a bookmark", map[string]string{"d.txt": "d\n"})
	jjtest.Bookmark(t, ws.Path, "feat-a", "@-")

	assertVerdict(t, classifier(r.jj, nil, longAgo).Classify(ws), TypeActive, TierKeep, "work is on bookmark feat-a")

	head := commitID(t, ws.Path, "@-")
	mergedAtHead := merged(3)
	mergedAtHead.HeadSHA = head
	mergedEarlier := merged(3)
	mergedEarlier.HeadSHA = commitID(t, ws.Path, "main@origin")
	mergedUnfetched := merged(4)
	mergedUnfetched.HeadSHA = "0123456789abcdef0123456789abcdef01234567"
	closed := github.PRInfo{Number: 3, State: "closed"}
	tests := []struct {
		name string
		prs  []github.PRInfo
		typ  WorkspaceType
		tier Tier
		want string
	}{
		{"no PR", nil, TypeActive, TierKeep, "which has no PR"},
		{"open PR", []github.PRInfo{{Number: 3, State: "open"}}, TypeActive, TierKeep, "PR #3 is open"},
		{"merged PR", []github.PRInfo{mergedAtHead}, TypeLanded, TierRemove, "PR #3 merged"},
		{"merged PR whose head the work goes beyond", []github.PRInfo{mergedEarlier}, TypeActive, TierKeep, "has 1 change beyond it"},
		{"merged PR whose head was never fetched but lists the commit", []github.PRInfo{mergedUnfetched}, TypeLanded, TierRemove, "PR #4 merged"},
		{"merged PR with no head commit", []github.PRInfo{merged(3)}, TypeActive, TierKeep, "head commit is unknown"},
		{"closed PR", []github.PRInfo{closed}, TypeClosedPR, TierDecide, "closed without merging"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prs := fakePRs{
				byBranch: map[string][]github.PRInfo{"feat-a": tt.prs},
				commits:  map[int][]github.PRCommit{4: {{SHA: head}}},
			}
			assertVerdict(t, classifier(r.jj, prs, longAgo).Classify(ws), tt.typ, tt.tier, tt.want)
		})
	}
}

func TestClassifyLandedByIdentity(t *testing.T) {
	r := newTrunkRepo(t)
	ws := r.workspace("pr-5-thing")
	jjtest.Commit(t, ws.Path, "feat: thing", map[string]string{"e.txt": "e\n"})
	pr := merged(5)
	prs := fakePRs{
		byNumber: map[int]*github.PRInfo{5: &pr},
		commits:  map[int][]github.PRCommit{5: {{SHA: commitID(t, ws.Path, "@-")}}},
	}

	assertVerdict(t, classifier(r.jj, prs, longAgo).Classify(ws), TypeLanded, TierRemove, "is a commit of merged PR #5")
}

// A merged PR holding a newer revision of the local change proves only that
// the change was rewritten, not that nothing was dropped, so it is left to a
// person.
func TestClassifySupersededByRevision(t *testing.T) {
	r := newTrunkRepo(t)
	ws := r.workspace("pr-6-rewrite")
	jjtest.Commit(t, ws.Path, "feat: r", map[string]string{"f.txt": "draft\n"})
	run(t, ws.Path, "describe", "-r", "@-", "-m", "feat: r, reworded")
	newer := commitID(t, ws.Path, "@-")
	run(t, ws.Path, "undo")
	require.NotEqual(t, newer, commitID(t, ws.Path, "@-"))

	pr := merged(6)
	prs := fakePRs{
		byNumber: map[int]*github.PRInfo{6: &pr},
		commits:  map[int][]github.PRCommit{6: {{SHA: newer}}},
	}
	assertVerdict(t, classifier(r.jj, prs, longAgo).Classify(ws), TypeSuperseded, TierDecide, "older revisions of commits in merged PR #6")
}

func TestClassifyConflicted(t *testing.T) {
	r := newTrunkRepo(t)
	ws := r.workspace("conflict")
	jjtest.Commit(t, ws.Path, "one", map[string]string{"a.txt": "one\n2\n3\n4\n5\n"})
	jjtest.Bookmark(t, ws.Path, "keep", "@-")
	run(t, ws.Path, "new", "main@origin")
	require.NoError(t, os.WriteFile(filepath.Join(ws.Path, "a.txt"), []byte("two\n2\n3\n4\n5\n"), 0o644))
	run(t, ws.Path, "describe", "-m", "two")
	run(t, ws.Path, "rebase", "-r", "@", "-d", "keep")

	assertVerdict(t, classifier(r.jj, nil, longAgo).Classify(ws), TypeConflicted, TierDecide, "unresolved conflicts")
}

func TestClassifyScratch(t *testing.T) {
	r := newTrunkRepo(t)
	marked := r.workspace("marked")
	jjtest.Commit(t, marked.Path, "scratch: local bench sizes (do not push)", map[string]string{"g.txt": "g\n"})
	assertVerdict(t, classifier(r.jj, nil, longAgo).Classify(marked), TypeScratch, TierDecide, "described as scratch")

	locks := r.workspace("locks")
	jjtest.Commit(t, locks.Path, "", map[string]string{"web/package-lock.json": "{}\n"})
	assertVerdict(t, classifier(r.jj, nil, longAgo).Classify(locks), TypeScratch, TierDecide, "only lockfiles changed")
}

// Classifying reads with --ignore-working-copy, so looking at a workspace with
// unsnapshotted edits records no operation.
func TestClassifyNeverSnapshots(t *testing.T) {
	r := newTrunkRepo(t)
	ws := r.workspace("dirty")
	require.NoError(t, os.WriteFile(filepath.Join(ws.Path, "new.txt"), []byte("unsaved\n"), 0o644))
	before := jjtest.OpCount(t, r.path)

	assertVerdict(t, classifier(r.jj, nil, longAgo).Classify(ws), TypeIdle, TierRemove, "")
	assert.Equal(t, before, jjtest.OpCount(t, r.path), "classifying snapshotted the workspace")
}
