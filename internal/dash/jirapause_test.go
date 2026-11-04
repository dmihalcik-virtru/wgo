package dash

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/jira"
	"github.com/virtru/wgo/internal/jiracache"
)

// The messages acli 1.3 prints once its login has expired: auth status,
// searches and sprint lists say so; a work item view does not.
const (
	acliUnauthorized = "exit status 1\n✗ Error: unauthorized: use 'acli jira auth login' to authenticate"
	acliViewFailed   = "exit status 1\n✗ Error: failed to fetch work item details"
)

// fakeAcli fails or answers Jira lookups like acli, counting its calls.
type fakeAcli struct {
	mu sync.Mutex
	// fail is the error a ticket view returns, or "" to succeed.
	fail string
	// sprintFail is the error a sprint list returns, or "" to succeed.
	sprintFail string
	// login is what `acli jira auth status` returns.
	login error
	// delay is how long each call takes.
	delay time.Duration

	views, sprints, logins atomic.Int32
}

func (a *fakeAcli) set(fail string, login error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fail, a.sprintFail, a.login = fail, fail, login
}

func (a *fakeAcli) FetchJira(ticket string) (jiracache.Info, error) {
	a.views.Add(1)
	time.Sleep(a.delay)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail != "" {
		return jiracache.Info{}, fmt.Errorf("get issue %s: %s", ticket, a.fail)
	}
	return jiracache.Info{Status: "In Progress", Summary: ticket}, nil
}

func (a *fakeAcli) FetchSprints(board int, _ []string) (jiracache.BoardSprints, error) {
	a.sprints.Add(1)
	time.Sleep(a.delay)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sprintFail != "" {
		return jiracache.BoardSprints{}, fmt.Errorf("list sprints for board %d: %s", board, a.sprintFail)
	}
	return jiracache.BoardSprints{Board: board, Sprints: []jiracache.SprintEntry{}}, nil
}

func (a *fakeAcli) Login(context.Context) error {
	a.logins.Add(1)
	time.Sleep(a.delay)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.login
}

func jiraJobs(n int) []Job {
	jobs := make([]Job, 0, n+1)
	for i := 1; i <= n; i++ {
		jobs = append(jobs, Job{Kind: JobJira, Ticket: fmt.Sprintf("TEAM-%d", i)})
	}
	return append(jobs, Job{Kind: JobSprints, Board: 42})
}

func jiraRefresher(t *testing.T, a *fakeAcli, workers int, withLogin bool) *Refresher {
	t.Helper()
	f := Fetchers{Jira: a, Sprints: a}
	if withLogin {
		f.JiraLogin = a.Login
	}
	r := NewRefresher(f, RefresherOptions{Workers: workers, IgnoreLeases: true})
	t.Cleanup(r.Close)
	return r
}

// TestJiraPauseOnUnauthorized: acli's "unauthorized" pauses the rest of the
// Jira lookups, sprint lists included, after at most one per worker; every
// one is skipped, none failed, and the summary says why once.
func TestJiraPauseOnUnauthorized(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := &fakeAcli{fail: acliUnauthorized, sprintFail: acliUnauthorized, delay: 5 * time.Millisecond}
	r := jiraRefresher(t, a, 4, true)
	res := <-r.Submit(jiraJobs(200))
	if len(res.Failed) != 0 || res.Err() != nil {
		t.Fatalf("a lapsed login failed lookups: %v", res.Failed)
	}
	if res.JiraSkipped != 201 || res.Jobs != 201 {
		t.Fatalf("skipped %d of %d, want 201 of 201", res.JiraSkipped, res.Jobs)
	}
	if calls := a.views.Load() + a.sprints.Load(); calls > 4 {
		t.Fatalf("%d acli calls, want at most one per worker", calls)
	}
	if a.logins.Load() != 0 {
		t.Fatalf("acli said why; its auth status need not be asked (%d times)", a.logins.Load())
	}
	want := "acli is not authenticated: run `acli jira auth login`; 201 Jira lookups skipped"
	if got := r.skipSummary(res); got != want {
		t.Fatalf("summary %q, want %q", got, want)
	}
	if r.jira.paused() != jiraNotLoggedIn {
		t.Fatalf("pause %q", r.jira.paused())
	}
}

// TestJiraPauseConfirmsTheLogin: a work item view that fails without saying
// why is checked against acli's auth status, once; a lapsed login then
// pauses the rest.
func TestJiraPauseConfirmsTheLogin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// The sprint list, which runs first, fails without saying why too.
	a := &fakeAcli{fail: acliViewFailed, sprintFail: acliViewFailed, login: jira.ErrNotLoggedIn, delay: 5 * time.Millisecond}
	r := jiraRefresher(t, a, 4, true)
	res := <-r.Submit(jiraJobs(100))
	if len(res.Failed) != 0 || res.JiraSkipped != 101 {
		t.Fatalf("failed %v, skipped %d; want none failed, 101 skipped", res.Failed, res.JiraSkipped)
	}
	if a.logins.Load() != 1 {
		t.Fatalf("auth status asked %d times, want once", a.logins.Load())
	}
	if calls := a.views.Load() + a.sprints.Load(); calls > 4 {
		t.Fatalf("%d acli calls, want at most one per worker", calls)
	}
}

// TestJiraPauseLoggedInTicketsFailAlone: when acli is logged in, tickets that
// fail (deleted ones, say) fail on their own and never pause the others.
func TestJiraPauseLoggedInTicketsFailAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := &fakeAcli{fail: acliViewFailed}
	r := jiraRefresher(t, a, 1, true)
	jobs := jiraJobs(10)[:10]
	res := <-r.Submit(jobs)
	if len(res.Failed) != 10 || res.JiraSkipped != 0 || a.views.Load() != 10 {
		t.Fatalf("failed %d, skipped %d, views %d; want 10, 0, 10", len(res.Failed), res.JiraSkipped, a.views.Load())
	}
	if a.logins.Load() != 1 {
		t.Fatalf("auth status asked %d times, want once (reused for %v)", a.logins.Load(), jiraLoginRecheck)
	}
	if r.jira.paused() != "" {
		t.Fatalf("paused: %q", r.jira.paused())
	}
}

// TestJiraPauseAfterAStreak: when acli's auth status cannot tell (or cannot
// be asked), three failures in a row with the same error, whatever the
// ticket, pause the rest. Those three are real failures; the rest are
// skipped. Different errors make no streak.
func TestJiraPauseAfterAStreak(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for name, withLogin := range map[string]bool{"login unknown": true, "no login check": false} {
		t.Run(name, func(t *testing.T) {
			a := &fakeAcli{fail: "exit status 1\n✗ Error: dial tcp: lookup jira.example: no such host", login: errors.New("acli jira auth status: exit status 1: dial tcp: no such host")}
			r := jiraRefresher(t, a, 1, withLogin)
			res := <-r.Submit(jiraJobs(20)[:20])
			if len(res.Failed) != jiraFailureStreak || res.JiraSkipped != 20-jiraFailureStreak {
				t.Fatalf("failed %d, skipped %d; want %d and %d", len(res.Failed), res.JiraSkipped, jiraFailureStreak, 20-jiraFailureStreak)
			}
			if res.JiraSkipReason != "Jira lookups paused after acli failed 3 times in a row (get issue TEAM-3: exit status 1 ✗ Error: dial tcp: lookup jira.example: no such host)" {
				t.Fatalf("reason %q", res.JiraSkipReason)
			}
			if res.failedErr() == nil {
				t.Fatal("an unexplained streak is still a failure")
			}
		})
	}

	a := &fakeAcli{}
	r := jiraRefresher(t, a, 1, false)
	for i := range 6 {
		a.set(fmt.Sprintf("exit status 1\n✗ Error: cause %c", 'a'+i%2), nil)
		<-r.Submit([]Job{{Kind: JobJira, Ticket: fmt.Sprintf("TEAM-%d", i)}})
	}
	if r.jira.paused() != "" {
		t.Fatalf("alternating errors paused lookups: %q", r.jira.paused())
	}
}

// TestJiraPauseBacksOffAndProbes is the served dashboard's back-off: while
// paused, lookups are skipped without calling acli; once the pause ends one
// probe runs, and a failed probe doubles the pause up to its bound; the
// first lookup that works ends it.
func TestJiraPauseBacksOffAndProbes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := &fakeAcli{fail: acliUnauthorized, sprintFail: acliUnauthorized}
	r := jiraRefresher(t, a, 2, true)
	r.jira.first, r.jira.max = 40*time.Millisecond, 100*time.Millisecond

	<-r.Submit(jiraJobs(5))
	if r.jira.backoff != 40*time.Millisecond {
		t.Fatalf("first pause %v", r.jira.backoff)
	}
	before := a.views.Load() + a.sprints.Load()
	if res := <-r.Submit(jiraJobs(5)); res.JiraSkipped != 6 || a.views.Load()+a.sprints.Load() != before {
		t.Fatalf("a paused round called acli: skipped %d, calls %d → %d", res.JiraSkipped, before, a.views.Load()+a.sprints.Load())
	}

	for _, want := range []time.Duration{80 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond} {
		time.Sleep(r.jira.backoff + 10*time.Millisecond)
		before = a.views.Load() + a.sprints.Load()
		res := <-r.Submit(jiraJobs(5))
		if calls := a.views.Load() + a.sprints.Load() - before; calls != 1 || res.JiraSkipped != 6 {
			t.Fatalf("probe round: %d calls, %d skipped; want 1 probe and 6 skipped", calls, res.JiraSkipped)
		}
		if r.jira.backoff != want {
			t.Fatalf("pause after a failed probe %v, want %v", r.jira.backoff, want)
		}
	}

	a.set("", nil)
	time.Sleep(r.jira.backoff + 10*time.Millisecond)
	<-r.Submit(jiraJobs(1)[:1]) // the probe, which works
	if r.jira.paused() != "" {
		t.Fatalf("still paused after a lookup worked: %q", r.jira.paused())
	}
	if res := <-r.Submit(jiraJobs(5)); res.JiraSkipped != 0 || len(res.Failed) != 0 {
		t.Fatalf("after the pause: %+v", res)
	}
}

// TestRefreshWaitJiraLoggedOut: a waited-for refresh with acli logged out
// succeeds; it says once why Jira lookups were skipped and marks the jira
// source in error with the reason.
func TestRefreshWaitJiraLoggedOut(t *testing.T) {
	f := newFixture(t)
	a := &fakeAcli{fail: acliViewFailed, login: jira.ErrNotLoggedIn}
	bf := &blockingFetchers{release: make(chan struct{})}
	close(bf.release)
	r := NewRefresher(Fetchers{PR: bf, Issue: bf, Jira: a, JiraLogin: a.Login}, RefresherOptions{Workers: 2, IgnoreLeases: true})
	defer r.Close()
	d, err := Open(Options{Dir: t.TempDir(), Collector: f.collector(t, time.Now(), memSource{}), Refresher: r})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true}); err != nil {
		t.Fatalf("a lapsed acli login failed the refresh: %v", err)
	}
	v := d.Current()
	if !hasDiag(v, "acli is not authenticated: run `acli jira auth login`; 1 Jira lookup skipped") {
		t.Fatalf("diagnostics %v", v.Diagnostics)
	}
	for _, diag := range v.Diagnostics {
		if strings.Contains(diag, "failed") {
			t.Fatalf("a skip reported as a failure: %q", diag)
		}
	}
	if st := v.Snapshot.Sources[SourceJira]; st.State != Error || st.Detail != jiraNotLoggedIn {
		t.Fatalf("jira source %+v", st)
	}
}

// deletedTicket is how a work item view of a ticket that no longer exists
// may fail while acli is logged in.
func deletedTicket(key string) error {
	return fmt.Errorf("get issue %s: %s", key, acliViewFailed)
}

// TestJiraPauseLoggedInResetsTheStreak: failures acli's auth status shows
// to be the tickets' own leave no streak behind, so a later failure that
// cannot be checked does not pause lookups with an unrelated reason.
func TestJiraPauseLoggedInResetsTheStreak(t *testing.T) {
	var loginErr error
	var mu sync.Mutex
	p := newJiraPause(func(context.Context) error { mu.Lock(); defer mu.Unlock(); return loginErr })
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		ok, probe, _ := p.admit()
		if err := p.done(ctx, deletedTicket(fmt.Sprintf("TEAM-%d", i)), probe); !ok || err == nil {
			t.Fatalf("lookup %d: admitted %v, err %v", i, ok, err)
		}
		if p.streak != 0 {
			t.Fatalf("after lookup %d, logged in: streak %d", i, p.streak)
		}
	}
	// A blip: acli's auth status cannot tell, once the answer has aged.
	mu.Lock()
	loginErr = errors.New("acli jira auth status: exit status 1: dial tcp: i/o timeout")
	mu.Unlock()
	p.mu.Lock()
	p.loginAt = time.Now().Add(-2 * jiraLoginRecheck)
	p.mu.Unlock()
	_, probe, _ := p.admit()
	_ = p.done(ctx, deletedTicket("TEAM-4"), probe)
	if p.paused() != "" || p.streak != 1 {
		t.Fatalf("one failure after a reset paused lookups: %q, streak %d", p.paused(), p.streak)
	}
}

// TestJiraPauseFailedProbeWhileLoggedIn: a probe that fails on its own
// ticket while acli says it is logged in ends the pause; the failure counts.
func TestJiraPauseFailedProbeWhileLoggedIn(t *testing.T) {
	p := newJiraPause(func(context.Context) error { return nil })
	ctx := context.Background()
	_, probe, _ := p.admit()
	if err := p.done(ctx, fmt.Errorf("get issue TEAM-1: %s", acliUnauthorized), probe); err == nil || p.paused() != jiraNotLoggedIn {
		t.Fatalf("not paused by an unauthorized lookup: %v, %q", err, p.paused())
	}
	if ok, _, reason := p.admit(); ok || reason != jiraNotLoggedIn {
		t.Fatalf("admitted while paused (%v), reason %q", ok, reason)
	}
	p.mu.Lock()
	p.until = time.Now().Add(-time.Second)
	p.mu.Unlock()
	ok, probe, _ := p.admit()
	if !ok || !probe {
		t.Fatalf("no probe after the pause: %v, %v", ok, probe)
	}
	err := p.done(ctx, deletedTicket("TEAM-2"), probe)
	var paused *pausedError
	if err == nil || errors.As(err, &paused) {
		t.Fatalf("the probe's own failure: %v, want a plain failure", err)
	}
	if p.paused() != "" || p.probing || p.streak != 0 {
		t.Fatalf("still paused after acli said it is logged in: %q, probing %v, streak %d", p.paused(), p.probing, p.streak)
	}
}

// TestBatchKeepsTheFirstSkipReason: a later skip cannot overwrite the reason
// a batch reports.
func TestBatchKeepsTheFirstSkipReason(t *testing.T) {
	b := &batch{progressed: make(chan struct{}, 1)}
	b.wg.Add(2)
	b.finish(Job{Kind: JobJira, Ticket: "TEAM-1"}, &pausedError{reason: jiraNotLoggedIn})
	b.finish(Job{Kind: JobJira, Ticket: "TEAM-2"}, &pausedError{reason: ""})
	if b.res.JiraSkipped != 2 || b.res.JiraSkipReason != jiraNotLoggedIn {
		t.Fatalf("%+v", b.res)
	}
	var total BatchResult
	total.add(b.res)
	total.add(BatchResult{JiraSkipped: 1, JiraSkipReason: "Jira lookups paused after acli failed 3 times in a row (x)"})
	if total.JiraSkipReason != jiraNotLoggedIn {
		t.Fatalf("reason %q", total.JiraSkipReason)
	}
}
