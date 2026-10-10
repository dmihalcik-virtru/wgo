package dash

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/issuecache"
	"github.com/virtru/wgo/internal/jiracache"
	"github.com/virtru/wgo/internal/jjtest"
	"github.com/virtru/wgo/models"
)

// failingFetchers fail every lookup.
type failingFetchers struct{ calls atomic.Int32 }

func (f *failingFetchers) FetchPRs(string, string) ([]models.PRRef, error) {
	f.calls.Add(1)
	return nil, errors.New("gh: HTTP 502")
}

func (f *failingFetchers) FetchJira(string) (jiracache.Info, error) {
	f.calls.Add(1)
	return jiracache.Info{}, errors.New("acli: not authenticated")
}

func (f *failingFetchers) FetchIssue(issuecache.Key) (issuecache.Info, error) {
	f.calls.Add(1)
	return issuecache.Info{}, errors.New("gh: HTTP 502")
}

func TestRefreshWaitPublishesFetchedData(t *testing.T) {
	f := newFixture(t)
	bf := &blockingFetchers{release: make(chan struct{})}
	// One worker per job, so all four fetches block at once.
	r := NewRefresher(Fetchers{PR: bf, Jira: bf, Issue: bf}, RefresherOptions{Workers: 4})
	defer r.Close()
	d, err := Open(Options{Dir: t.TempDir(), Collector: f.collector(t, time.Now(), memSource{}), Refresher: r})
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		for bf.calls.Load() < 4 {
			time.Sleep(5 * time.Millisecond)
		}
		close(released)
		close(bf.release)
	}()
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-released:
	default:
		t.Fatal("Refresh with Wait returned before the fetches finished")
	}
	v := d.Current()
	if v.Snapshot.Generation != 2 {
		t.Fatalf("generation = %d, want 2 (local, then with remote data)", v.Snapshot.Generation)
	}
	for _, src := range []string{SourceGitHubPRs, SourceGitHubIssues, SourceJira} {
		if v.Snapshot.Sources[src].State != Fresh {
			t.Fatalf("%s: %+v", src, v.Snapshot.Sources[src])
		}
	}
	if len(v.Diagnostics) != 0 {
		t.Fatalf("clean refresh has diagnostics: %v", v.Diagnostics)
	}
}

func TestRefreshWaitReportsFailedLookups(t *testing.T) {
	f := newFixture(t)
	ff := &failingFetchers{}
	r := NewRefresher(Fetchers{PR: ff, Jira: ff, Issue: ff}, RefresherOptions{Workers: 2, IgnoreLeases: true})
	defer r.Close()
	d, err := Open(Options{Dir: t.TempDir(), Collector: f.collector(t, time.Now(), memSource{}), Refresher: r})
	if err != nil {
		t.Fatal(err)
	}
	err = d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true})
	if err == nil || !strings.Contains(err.Error(), "4 of 4 lookups failed") || !strings.Contains(err.Error(), "jira WGO-5: acli: not authenticated") {
		t.Fatalf("Refresh = %v, want the failed lookups", err)
	}
	v := d.Current()
	if !hasDiag(v, "4 of 4 lookups failed") {
		t.Fatalf("failed lookups not in the view: %v", v.Diagnostics)
	}
	// The failed Jira lookup is an error with a hint, not a silent unknown.
	if tk := v.Snapshot.Node(jiraTicketID("WGO-5")).Ticket; tk.Freshness != Error || !strings.Contains(tk.Error, "acli") {
		t.Fatalf("failed jira lookup: %+v", tk)
	}

	// Once every lookup succeeds, the diagnostic clears.
	bf := &blockingFetchers{release: make(chan struct{})}
	close(bf.release)
	r2 := NewRefresher(Fetchers{PR: bf, Jira: bf, Issue: bf}, RefresherOptions{Workers: 2, IgnoreLeases: true})
	defer r2.Close()
	d.opts.Refresher = r2
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true}); err != nil {
		t.Fatal(err)
	}
	if v := d.Current(); len(v.Diagnostics) != 0 {
		t.Fatalf("diagnostics not cleared after a clean refresh: %v", v.Diagnostics)
	}
}

func TestRefreshWaitCancelled(t *testing.T) {
	f := newFixture(t)
	bf := &blockingFetchers{release: make(chan struct{})}
	r := NewRefresher(Fetchers{PR: bf, Jira: bf, Issue: bf}, RefresherOptions{Workers: 2})
	defer func() {
		close(bf.release)
		r.Close()
	}()
	dir := t.TempDir()
	d, err := Open(Options{Dir: dir, Collector: f.collector(t, time.Now(), memSource{}), Refresher: r})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for bf.calls.Load() == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	if err := d.Refresh(ctx, RefreshOptions{Remote: true, Wait: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh = %v, want context.Canceled", err)
	}
	if v := d.Current(); v == nil || v.Snapshot.Generation != 1 {
		t.Fatalf("the local snapshot should stay published: %+v", v)
	}
	if _, err := os.Stat(filepath.Join(dir, BaselineFile)); !os.IsNotExist(err) {
		t.Fatalf("baseline written without Acknowledge: %v", err)
	}
}

func TestCancelledCollectionPublishesNothing(t *testing.T) {
	f := newFixture(t)
	d, err := Open(Options{Dir: t.TempDir(), Collector: f.collector(t, time.Now(), memSource{})})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Refresh(ctx, RefreshOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh = %v, want context.Canceled", err)
	}
	if d.Current() != nil {
		t.Fatal("a cancelled collection was published")
	}
}

func TestCollectionFailureKeepsLastSnapshot(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	d, err := Open(Options{Dir: dir, Collector: f.collector(t, time.Now(), memSource{})})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Refresh(context.Background(), RefreshOptions{}); err != nil {
		t.Fatal(err)
	}

	var fail atomic.Bool
	fail.Store(true)
	broken := f
	broken.discover = func() ([]discovery.DiscoveredRepo, error) {
		if fail.Load() {
			return nil, errors.New("scan roots unreadable")
		}
		return f.discover()
	}
	d2, err := Open(Options{Dir: dir, Collector: broken.collector(t, time.Now(), memSource{})})
	if err != nil {
		t.Fatal(err)
	}
	if err := d2.Refresh(context.Background(), RefreshOptions{}); err == nil {
		t.Fatal("Refresh with a failing discovery succeeded")
	}
	v := d2.Current()
	if v == nil || !v.Loaded || v.Snapshot.Generation != 1 {
		t.Fatalf("last good snapshot not kept: %+v", v)
	}
	if !hasDiag(v, "collection failed: discover workspaces: scan roots unreadable; showing the snapshot from") ||
		!strings.Contains(viewJSON(t, v), "collection failed") {
		t.Fatalf("collection failure not in the view: %v", v.Diagnostics)
	}
	fail.Store(false)
	if err := d2.Refresh(context.Background(), RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	if v := d2.Current(); len(v.Diagnostics) != 0 {
		t.Fatalf("collection failure not cleared: %v", v.Diagnostics)
	}
}

func TestLoadDiagnosticsClear(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, SnapshotFile), []byte(`{"schema":1,"nodes":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, BaselineFile), []byte(`{"schema":1,`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := openDash(t, dir)
	g := publish(t, d, synth(snapOpts{changesA: []string{"a1"}}))
	v := d.Current()
	if hasDiag(v, "ignoring persisted snapshot") || !hasDiag(v, "ignoring last-seen baseline") {
		t.Fatalf("after a good snapshot write only the baseline problem remains: %v", v.Diagnostics)
	}
	if err := d.Acknowledge(g); err != nil {
		t.Fatal(err)
	}
	if v := d.Current(); len(v.Diagnostics) != 0 {
		t.Fatalf("acknowledging did not clear the baseline problem: %v", v.Diagnostics)
	}
}

func TestJiraTicketFreshness(t *testing.T) {
	f := newFixture(t)
	c := f.collector(t, time.Now(), memSource{})
	jobsFor := func(jobs []Job, kind JobKind) int {
		n := 0
		for _, j := range jobs {
			if j.Kind == kind {
				n++
			}
		}
		return n
	}

	// A real ticket with no mappable status is a fresh hit, not a failure,
	// and is not fetched again.
	if err := jiracache.Write("WGO-5", jiracache.Info{}); err != nil {
		t.Fatal(err)
	}
	ls, err := c.collectLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s, jobs := c.assemble(ls)
	if tk := s.Node(jiraTicketID("WGO-5")).Ticket; tk.Freshness != Fresh || tk.Error != "" {
		t.Fatalf("empty-status ticket: %+v", tk)
	}
	if n := jobsFor(jobs, JobJira); n != 0 {
		t.Fatalf("empty-status ticket queued %d jira jobs", n)
	}

	// A failed lookup leaves a negative entry: an error with a hint, retried.
	t.Setenv("HOME", t.TempDir())
	if _, _, err := jiracache.Resolve(&failingFetchers{}, "WGO-5", jiracache.Opts{Synchronous: true}); err == nil {
		t.Fatal("failing fetcher succeeded")
	}
	s, jobs = c.assemble(ls)
	if tk := s.Node(jiraTicketID("WGO-5")).Ticket; tk.Freshness != Error || !strings.Contains(tk.Error, "acli jira auth status") {
		t.Fatalf("failed ticket: %+v", tk)
	}
	if n := jobsFor(jobs, JobJira); n != 1 {
		t.Fatalf("failed ticket queued %d jira jobs, want 1", n)
	}
}

func TestGitHubTicketNeedsGitHubRemote(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, jjc := jjtest.NewRepo(t)
	if err := jjc.GitRemoteAdd(repo, "origin", "https://gitlab.example.com/acme/widgets.git"); err != nil {
		t.Fatal(err)
	}
	jjtest.Commit(t, repo, "feat: a", map[string]string{"a.txt": "a"})
	jjtest.Bookmark(t, repo, "gh-9-x", "@-")
	f := fixture{repo: repo, discover: func() ([]discovery.DiscoveredRepo, error) {
		return []discovery.DiscoveredRepo{{Path: repo, Name: "widgets"}}, nil
	}}
	c := f.collector(t, time.Now(), memSource{})
	ls, err := c.collectLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s, jobs := c.assemble(ls)
	tk := s.Node(githubTicketID(filepath.Base(repo), 9))
	if tk == nil || tk.Ticket.Freshness != Unknown || tk.Ticket.Error != "no GitHub remote" {
		t.Fatalf("gh-9 on a non-GitHub repo: %+v", tk)
	}
	for _, j := range jobs {
		if j.Kind == JobIssue {
			t.Fatalf("issue lookup queued against a guessed repo: %+v", j.Issue)
		}
		if j.Kind == JobPR {
			t.Fatalf("PR lookup queued for a repo with no GitHub remote: %+v", j)
		}
	}
	bm := s.Node(bookmarkID(repo, "gh-9-x"))
	if bm == nil || bm.Bookmark.PRLookup != Unknown || bm.Bookmark.PRError != "no GitHub remote" {
		t.Fatalf("bookmark on a non-GitHub repo should say why PRs are unknown: %+v", bm)
	}
}

func TestMissingWorkspaceDirIsReported(t *testing.T) {
	f := newFixture(t)
	if err := os.RemoveAll(f.ws); err != nil {
		t.Fatal(err)
	}
	c := f.collector(t, time.Now(), memSource{})
	ls, err := c.collectLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s, _ := c.assemble(ls)
	found := false
	for _, d := range s.Diagnostics {
		if strings.Contains(d, `workspace "two"`) && strings.Contains(d, "workspace forget two") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing workspace not reported: %v", s.Diagnostics)
	}
}

// gatedPRs counts PR fetches and blocks each until release is closed.
type gatedPRs struct {
	release chan struct{}
	calls   atomic.Int32
}

func (g *gatedPRs) FetchPRs(string, string) ([]models.PRRef, error) {
	g.calls.Add(1)
	<-g.release
	return []models.PRRef{}, nil
}

func prJob(branch string) Job {
	return Job{Kind: JobPR, RemoteURL: "https://github.com/acme/widgets.git", RepoPath: "/r", Branch: branch}
}

func waitCalls(t *testing.T, n *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for n.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("fetch calls = %d, want %d", n.Load(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRefresherJoinsPendingJob(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	g := &gatedPRs{release: make(chan struct{})}
	r := NewRefresher(Fetchers{PR: g}, RefresherOptions{Workers: 1, IgnoreLeases: true})
	defer r.Close()
	first := r.Submit([]Job{prJob("a")})
	waitCalls(t, &g.calls, 1)
	// The same job again, while the first is running: not fetched twice,
	// but the second batch is not done until the fetch is.
	second := r.Submit([]Job{prJob("a"), prJob("a")})
	select {
	case <-second:
		t.Fatal("second batch finished before the shared fetch")
	case <-time.After(50 * time.Millisecond):
	}
	close(g.release)
	for _, done := range []<-chan BatchResult{first, second} {
		if res := <-done; res.Err() != nil {
			t.Fatal(res.Err())
		}
	}
	if g.calls.Load() != 1 {
		t.Fatalf("fetch calls = %d, want 1", g.calls.Load())
	}
}

func TestRefresherQueueLimits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	g := &gatedPRs{release: make(chan struct{})}
	r := NewRefresher(Fetchers{PR: g}, RefresherOptions{Workers: 1, Queue: 1, IgnoreLeases: true})
	busy := r.Submit([]Job{prJob("a")})
	waitCalls(t, &g.calls, 1)
	// The worker is busy and the queue holds one: two of three are dropped.
	// A job with no fetcher is not counted at all.
	over := r.Submit([]Job{prJob("b"), prJob("c"), prJob("d"), {Kind: JobJira, Ticket: "WGO-1"}})
	close(g.release)
	<-busy
	res := <-over
	if res.Jobs != 3 || res.Dropped != 2 || len(res.Failed) != 0 {
		t.Fatalf("result = %+v, want 3 jobs, 2 dropped", res)
	}
	if err := res.Err(); err == nil || !strings.Contains(err.Error(), "2 of 3 lookups not run (queue full)") {
		t.Fatalf("Err() = %v", err)
	}
	r.Close()
	if res := <-r.Submit([]Job{prJob("e")}); res.Dropped != 1 {
		t.Fatalf("Submit after Close = %+v, want it dropped", res)
	}
}

func TestBatchResultErrListsAFew(t *testing.T) {
	res := BatchResult{Jobs: 5}
	for i := range 5 {
		res.Failed = append(res.Failed, fmt.Sprintf("jira T-%d: boom", i))
	}
	msg := res.Err().Error()
	if !strings.Contains(msg, "5 of 5 lookups failed") || !strings.Contains(msg, "and 2 more") || strings.Contains(msg, "T-4") {
		t.Fatalf("Err() = %q", msg)
	}
	if (BatchResult{Jobs: 3}).Err() != nil {
		t.Fatal("a clean batch has an error")
	}
}
