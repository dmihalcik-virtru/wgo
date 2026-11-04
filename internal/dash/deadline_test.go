package dash

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/inventory"
	"github.com/virtru/wgo/internal/issuecache"
	"github.com/virtru/wgo/internal/jiracache"
	"github.com/virtru/wgo/models"
)

// orderLog records the order in which a Refresher's fetches run.
type orderLog struct {
	mu  sync.Mutex
	ran []string
}

func (o *orderLog) add(s string) {
	o.mu.Lock()
	o.ran = append(o.ran, s)
	o.mu.Unlock()
}

func (o *orderLog) SearchPullRequests(_ context.Context, q string) (*github.PRSearchResult, error) {
	switch {
	case strings.Contains(q, "review-requested:"):
		o.add("review-requested")
	case strings.Contains(q, "is:closed"):
		o.add("closed-recent")
	default:
		o.add("open-authored")
	}
	return &github.PRSearchResult{PRs: []github.PRFacts{}}, nil
}

func (o *orderLog) Viewer(context.Context) (string, error) { o.add("identity"); return "alice", nil }

func (o *orderLog) FetchJira(ticket string) (jiracache.Info, error) {
	o.add("jira " + ticket)
	return jiracache.Info{Status: "Done"}, nil
}

func (o *orderLog) FetchIssue(k issuecache.Key) (issuecache.Info, error) {
	o.add("issue " + strconv.Itoa(k.Number))
	return issuecache.Info{Number: k.Number, State: "open"}, nil
}

func (o *orderLog) FetchPRs(string, string) ([]models.PRRef, error) { o.add("pr"); return nil, nil }

func (o *orderLog) FetchSprints(board int, _ []string) (jiracache.BoardSprints, error) {
	o.add("sprints")
	return jiracache.BoardSprints{Board: board, Sprints: []jiracache.SprintEntry{}}, nil
}

// TestRefresherRunsTheInventoryFirst: queued jobs run by priority: the
// login, then open-authored, review-requested and closed-recent, the
// board's sprints, the bookmarks' PRs, and the ticket lookups last, in the
// order submitted.
func TestRefresherRunsTheInventoryFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	o := &orderLog{}
	r := NewRefresher(Fetchers{Inventory: o, Jira: o, Issue: o, PR: o, Sprints: o}, RefresherOptions{Workers: 1, IgnoreLeases: true})
	defer r.Close()
	inv := func(set inventory.Set) Job {
		return Job{Kind: JobInventory, Inventory: inventory.Key{Login: "alice", Set: set}}
	}
	<-r.Submit([]Job{
		{Kind: JobJira, Ticket: "TEAM-1"},
		{Kind: JobIssue, Issue: issuecache.Key{Owner: "acme", Repo: "api", Number: 2}},
		inv(inventory.ClosedRecent),
		{Kind: JobPR, RemoteURL: "u", RepoPath: "/w/acme/api", Branch: "b"},
		inv(inventory.ReviewRequested),
		{Kind: JobSprints, Board: 42},
		{Kind: JobJira, Ticket: "TEAM-3"},
		inv(inventory.OpenAuthored),
		{Kind: JobIdentity},
	})
	want := []string{"identity", "open-authored", "review-requested", "closed-recent", "sprints", "pr", "jira TEAM-1", "issue 2", "jira TEAM-3"}
	if !slices.Equal(o.ran, want) {
		t.Fatalf("ran %v, want %v", o.ran, want)
	}
}

// slowSearch is a GraphQL search whose sets have a given number of PRs,
// served 20 a page, each page taking delay.
type slowSearch struct {
	t     *testing.T
	sizes map[string]int // a query substring -> how many PRs
	delay map[string]time.Duration
}

func (g *slowSearch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Variables map[string]any `json:"variables"`
	}
	_ = json.Unmarshal(body, &req)
	q, _ := req.Variables["q"].(string)
	page := 1
	if after, _ := req.Variables["after"].(string); after != "" {
		page, _ = strconv.Atoi(after)
		page++
	}
	n, delay, set := 0, time.Duration(0), ""
	for sub, size := range g.sizes {
		if strings.Contains(q, sub) {
			n, delay, set = size, g.delay[sub], sub
		}
	}
	select {
	case <-time.After(delay):
	case <-r.Context().Done():
		return
	}
	lo, hi := (page-1)*github.PRSearchPageSize, min(page*github.PRSearchPageSize, n)
	nodes := []any{}
	for i := lo + 1; i <= hi; i++ {
		num := i
		if set == "review-requested:" {
			num += 10000 // other people's PRs
		}
		nodes = append(nodes, map[string]any{
			"number": num, "title": fmt.Sprintf("PR %d", num), "url": fmt.Sprintf("https://github.com/acme/api/pull/%d", num), "state": "OPEN",
			"repository": map[string]any{"nameWithOwner": "acme/api", "isArchived": false},
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"search": map[string]any{
		"issueCount": n, "pageInfo": map[string]any{"hasNextPage": hi < n, "endCursor": strconv.Itoa(page)}, "nodes": nodes,
	}}})
}

// slowJira answers each ticket after delay.
type slowJira struct {
	delay time.Duration
	mu    sync.Mutex
	calls int
}

func (j *slowJira) FetchJira(ticket string) (jiracache.Info, error) {
	j.mu.Lock()
	j.calls++
	j.mu.Unlock()
	time.Sleep(j.delay)
	return jiracache.Info{Status: "In Progress", Summary: ticket}, nil
}

// deadlineDash follows alice, whose closed-recent set is cached and names
// 200 Jira tickets, while her open sets come from search; four workers, as
// wgo dash --json --refresh has.
func deadlineDash(t *testing.T, search *slowSearch, jira jiracache.Fetcher) *Dash {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	since := inventory.ClosedSince(time.Now(), DefaultDays)
	var closed []github.PRFacts
	for i := 1; i <= 200; i++ {
		p := openPR(i, "alice", fmt.Sprintf("change-%d", i))
		p.Repo, p.URL, p.Title = "acme/api", fmt.Sprintf("https://github.com/acme/api/pull/%d", 5000+i), fmt.Sprintf("TEAM-%d: change", i)
		p.Number, p.State, p.MergedAt = 5000+i, "merged", time.Now().Add(-time.Hour)
		closed = append(closed, p)
	}
	seedInventory(t, since, map[string][]github.PRFacts{"alice/closed-recent": closed})
	search.t = t
	srv := httptest.NewServer(search)
	t.Cleanup(srv.Close)
	gc := github.NewClient(github.WithBaseURL(srv.URL), github.WithToken("test-token"), github.WithRetryBackoff(0, 0, 0))
	c := NewCollector(Config{
		People:         []Person{{Login: "alice"}},
		Identity:       func() (string, string) { return "alice", "" },
		TicketPrefixes: []string{"TEAM"},
	})
	r := NewRefresher(Fetchers{Inventory: gc, Jira: jira}, RefresherOptions{Workers: 4, IgnoreLeases: true})
	t.Cleanup(r.Close)
	d, err := Open(Options{Dir: t.TempDir(), Collector: c, Refresher: r})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestRefreshDeadlineFinishesTheInventory is the real trial's starvation: a
// large review-requested set (24 pages) takes longer than the deadline,
// beside 200 ticket lookups. The inventory runs first and the set running
// at the deadline is finished, so it is cached; the ticket lookups are cut,
// and the output says both.
func TestRefreshDeadlineFinishesTheInventory(t *testing.T) {
	search := &slowSearch{
		sizes: map[string]int{"author:alice": 212, "review-requested:": 462},
		delay: map[string]time.Duration{"author:alice": 15 * time.Millisecond, "review-requested:": 25 * time.Millisecond},
	}
	jira := &slowJira{delay: 40 * time.Millisecond}
	d := deadlineDash(t, search, jira)
	const deadline = 300 * time.Millisecond
	start := time.Now()
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true, Timeout: deadline}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	took := time.Since(start)
	t.Logf("took %v with a %v deadline; %d of 200 ticket lookups ran", took, deadline, jira.calls)
	if took > (1+deadlineGrace)*deadline+2*time.Second {
		t.Fatalf("took %v, past the hard cap", took)
	}
	v := d.Current()
	st := v.Snapshot.Sources[SourceGitHubInventory]
	if st.State != Fresh || st.Fresh != 3 || st.Detail != "" {
		t.Fatalf("github_inventory %+v: every set should be fetched and cached", st)
	}
	res := inventory.Read(inventory.Key{Login: "alice", Set: inventory.ReviewRequested}, time.Hour, time.Time{}, time.Now())
	if res.State != inventory.Fresh || len(res.PRs) != 462 {
		t.Fatalf("review-requested cache: %v, %d PRs", res.State, len(res.PRs))
	}
	if jira.calls >= 200 {
		t.Fatalf("all %d ticket lookups ran; the deadline should cut them", jira.calls)
	}
	var notRun, finished bool
	for _, diag := range v.Diagnostics {
		notRun = notRun || strings.HasPrefix(diag, "not run: refresh deadline (300ms) reached; ")
		finished = finished || strings.HasPrefix(diag, "finished 1 PR inventory fetch started before the refresh deadline (300ms), ")
		if strings.Contains(diag, "failed") {
			t.Errorf("diagnostic %q: nothing failed", diag)
		}
	}
	if !notRun || !finished {
		t.Fatalf("diagnostics %q", v.Diagnostics)
	}
	if st := v.Snapshot.Sources[SourceJira]; st.Unknown == 0 {
		t.Fatalf("jira %+v: the cut lookups stay unknown", st)
	}
}

// TestRefreshDeadlineInterruptsAtTheCap: a set that outlasts three times
// the deadline is cancelled then; the cache records the attempt and why, and
// github_inventory says so instead of a blank.
func TestRefreshDeadlineInterruptsAtTheCap(t *testing.T) {
	search := &slowSearch{
		sizes: map[string]int{"author:alice": 100, "review-requested:": 462},
		delay: map[string]time.Duration{"author:alice": 60 * time.Millisecond, "review-requested:": 60 * time.Millisecond},
	}
	d := deadlineDash(t, search, &slowJira{delay: 40 * time.Millisecond})
	const deadline = 200 * time.Millisecond
	start := time.Now()
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true, Timeout: deadline}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	took := time.Since(start)
	t.Logf("took %v with a %v deadline", took, deadline)
	if took > (1+deadlineGrace)*deadline+time.Second {
		t.Fatalf("took %v, past the hard cap", took)
	}
	v := d.Current()
	k := inventory.Key{Login: "alice", Set: inventory.ReviewRequested}
	res := inventory.Read(k, time.Hour, time.Time{}, time.Now())
	if !res.Interrupted || res.LastAttemptAt.IsZero() || res.Failures != 0 || res.Err == nil ||
		!strings.HasPrefix(res.Err.Error(), "cancelled at the refresh deadline on page ") {
		t.Fatalf("review-requested cache: %+v", res)
	}
	st := v.Snapshot.Sources[SourceGitHubInventory]
	var part SourcePart
	for _, p := range st.Parts {
		if p.Name == k.String() {
			part = p
		}
	}
	if part.State != Unknown || part.Error != res.Err.Error() {
		t.Fatalf("part %+v: an interrupted set is unknown, with why", part)
	}
	if !strings.HasPrefix(st.Detail, "alice/review-requested: cancelled at the refresh deadline on page ") ||
		!strings.HasSuffix(st.Detail, "(fetched again on the next refresh); the refresh deadline (200ms) was reached") {
		t.Fatalf("detail %q", st.Detail)
	}
	if !hasDiag(v, "finished 1 of 2 PR inventory fetches started before the refresh deadline (200ms); cancelled 1 at 600ms, 3 times the deadline (fetched again on the next refresh)") {
		t.Fatalf("diagnostics %q", v.Diagnostics)
	}
	if open := inventory.Read(inventory.Key{Login: "alice", Set: inventory.OpenAuthored}, time.Hour, time.Time{}, time.Now()); open.State != inventory.Fresh {
		t.Fatalf("open-authored, finished in the grace period: %+v", open.State)
	}
}

func TestReasonsDetail(t *testing.T) {
	got := reasonsDetail([]partReason{{"alice/open-authored", "not fetched yet"}, {"bob/closed-recent", "due for a refresh"}, {"alice/review-requested", "not fetched yet"}})
	if want := "alice/open-authored, alice/review-requested: not fetched yet; bob/closed-recent: due for a refresh"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if reasonsDetail(nil) != "" {
		t.Fatal("all fresh: no detail")
	}
}

// floodTickets answers each ticket after a while.
type floodTickets struct{}

func (floodTickets) FetchJira(string) (jiracache.Info, error) {
	time.Sleep(5 * time.Millisecond)
	return jiracache.Info{Status: "Done"}, nil
}

// quickTimeline answers a PR's history at once, counting.
type quickTimeline struct{ calls atomic.Int32 }

func (q *quickTimeline) PRTimeline(_ context.Context, repo string, number int) (*github.PRTimeline, error) {
	q.calls.Add(1)
	return &github.PRTimeline{Repo: repo, Number: number, State: "open", Events: []github.TimelineEvent{}}, nil
}

// TestTimelinesRunUnderATicketFlood: the served dashboard resubmits its
// ticket lookups every refresh; when they come faster than the workers
// drain them, the PR timelines and their pruning still run.
func TestTimelinesRunUnderATicketFlood(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	q := &quickTimeline{}
	r := NewRefresher(Fetchers{Jira: floodTickets{}, Timelines: q}, RefresherOptions{Workers: 2, Queue: 100000, IgnoreLeases: true})
	defer r.Close()
	stop := make(chan struct{})
	var flood sync.WaitGroup
	flood.Add(1)
	go func() {
		defer flood.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			jobs := make([]Job, 50)
			for k := range jobs {
				jobs[k] = Job{Kind: JobJira, Ticket: fmt.Sprintf("TEAM-%d", i*50+k)}
			}
			r.Submit(jobs) // 50 lookups every 10ms: 2 workers drain 400 a second
			time.Sleep(10 * time.Millisecond)
		}
	}()
	defer func() { close(stop); flood.Wait() }()
	time.Sleep(50 * time.Millisecond) // a backlog builds up
	key := inventory.TimelineKey{Repo: "acme/api", Number: 7}
	done := r.Submit([]Job{
		{Kind: JobTimelines, Timelines: []TimelineRequest{{Key: key, UpdatedAt: time.Now()}}},
		{Kind: JobTimelinePrune},
	})
	select {
	case res := <-done:
		if res.Err() != nil || q.calls.Load() != 1 {
			t.Fatalf("timelines: %+v, %d fetched", res, q.calls.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the timelines never ran behind the ticket flood")
	}
}
