package dash

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/jiracache"
)

// stubSprintFetcher serves canned board sprints and counts calls.
type stubSprintFetcher struct {
	data      jiracache.BoardSprints
	err       error
	calls     atomic.Int32
	assignees []string
}

func (f *stubSprintFetcher) FetchSprints(board int, assignees []string) (jiracache.BoardSprints, error) {
	f.calls.Add(1)
	f.assignees = assignees
	return f.data, f.err
}

// stubJiraFacts serves a full facts entry for any ticket.
type stubJiraFacts struct{ calls atomic.Int32 }

func (f *stubJiraFacts) FetchJira(ticket string) (jiracache.Info, error) {
	f.calls.Add(1)
	three := 3.0
	return jiracache.Info{Status: "In Progress", Summary: "full facts of " + ticket, StatusCategory: StatusInProgress,
		Sprints: []jiracache.SprintRef{{ID: 1234, Name: "Sprint 12", State: SprintActive}}, StoryPoints: &three, StoryPointsKnown: true}, nil
}

var (
	sprintStart = time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	sprintEnd   = time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
)

// boardFixture is board 42: Sprint 12 (active, with a goal), a parallel
// active sprint Web 7, and Sprint 13 (future).
func boardFixture() jiracache.BoardSprints {
	brief := func(status, category, summary string) jiracache.Info {
		return jiracache.Info{Status: status, StatusCategory: category, Summary: summary, Type: "Story", Labels: []string{}}
	}
	return jiracache.BoardSprints{Site: "acme.example", Sprints: []jiracache.SprintEntry{
		{ID: 1235, Name: "Sprint 13", State: SprintFuture, Start: sprintEnd},
		{ID: 1240, Name: "Web 7", State: SprintActive, Start: sprintStart.Add(time.Hour), End: sprintEnd,
			Tickets: []jiracache.SprintTicket{{Key: "TEAM-4", Info: brief("To Do", StatusToDo, "Translate the settings page")}}},
		{ID: 1234, Name: "Sprint 12", State: SprintActive, Goal: "Ship token refresh", Start: sprintStart, End: sprintEnd,
			Tickets: []jiracache.SprintTicket{
				{Key: "TEAM-1", Info: brief("In Progress", StatusInProgress, "Refresh tokens")},
				{Key: "TEAM-2", Info: brief("To Do", StatusToDo, "Document the token API")},
				{Key: "TEAM-3", Info: brief("In Progress", StatusInProgress, "Rate-limit tokens")},
			}},
	}}
}

// cacheBoard writes data as board 42's cached sprints, searched for
// assignees.
func cacheBoard(t *testing.T, data jiracache.BoardSprints, assignees ...string) {
	t.Helper()
	if err := jiracache.RefreshSprints(&stubSprintFetcher{data: data}, 42, assignees); err != nil {
		t.Fatal(err)
	}
}

func edgesFrom(s *Snapshot, src string, kind EdgeKind) []string {
	var out []string
	for _, e := range s.Edges {
		if e.Source == src && e.Kind == kind {
			out = append(out, e.Target)
		}
	}
	return out
}

// TestAssembleSprints covers sprint nodes with goals and dates, sprint
// tickets without a PR under the sprint group effort, in_sprint edges
// (carried-over tickets to the active sprint only), parallel active sprints
// and the jira_sprints source.
func TestAssembleSprints(t *testing.T) {
	a := newAsm(t)
	a.c.cfg.JiraBoard = 42
	cacheBoard(t, boardFixture(), JiraCurrentUser)
	// TEAM-1 has a workspace; TEAM-5 too, and is planned for Sprint 13.
	a.ws("/w/acme/widgets-1", "TEAM-1-refresh")
	a.ws("/w/acme/widgets-5", "TEAM-5-translate")
	five := 5.0
	if err := jiracache.Write("TEAM-5", jiracache.Info{Status: "To Do", Sprints: []jiracache.SprintRef{{ID: 1235, Name: "Sprint 13", State: SprintFuture}}}); err != nil {
		t.Fatal(err)
	}
	// TEAM-3 has full facts cached: carried over from Sprint 11.
	if err := jiracache.Write("TEAM-3", jiracache.Info{Status: "In Progress", Summary: "Rate-limit the token endpoints", StatusCategory: StatusInProgress,
		Sprints:     []jiracache.SprintRef{{ID: 1230, Name: "Sprint 11", State: SprintClosed}, {ID: 1234, Name: "Sprint 12", State: SprintActive}},
		StoryPoints: &five, StoryPointsKnown: true}); err != nil {
		t.Fatal(err)
	}
	s, jobs := a.build()

	sp := s.Node(sprintNodeID(1234))
	if sp == nil || sp.Label != "Sprint 12" {
		t.Fatalf("sprint node: %+v", sp)
	}
	want := SprintInfo{ID: 1234, Name: "Sprint 12", State: SprintActive, Start: sprintStart, End: sprintEnd, Goal: "Ship token refresh", Board: 42, Freshness: Fresh,
		TicketScope: TicketScopeConfiguredPeopleAndUnassigned, TicketLookup: Fresh}
	got := *sp.Sprint
	if got.FetchedAt.IsZero() || !got.TicketFetchedAt.Equal(got.FetchedAt) {
		t.Fatalf("sprint fetched_at %v, ticket_fetched_at %v", got.FetchedAt, got.TicketFetchedAt)
	}
	got.FetchedAt, got.TicketFetchedAt = time.Time{}, time.Time{}
	if !got.Start.Equal(want.Start) || !got.End.Equal(want.End) {
		t.Fatalf("sprint dates: %v..%v", got.Start, got.End)
	}
	got.Start, got.End = want.Start, want.End
	if got.TicketCount == nil || *got.TicketCount != 3 {
		t.Fatalf("ticket_count %v, want 3", got.TicketCount)
	}
	got.TicketCount = nil
	if got != want {
		t.Fatalf("sprint info:\n got %+v\nwant %+v", got, want)
	}
	if n := s.Node(sprintNodeID(1240)); n == nil || n.Sprint.State != SprintActive || n.Sprint.TicketCount == nil || *n.Sprint.TicketCount != 1 {
		t.Fatalf("each parallel active sprint is a node with its own count: %+v", n)
	}
	if n := s.Node(sprintNodeID(1235)); n == nil || n.Sprint.State != SprintFuture {
		t.Fatalf("future sprint: %+v", n)
	} else if n.Sprint.TicketScope != "" || n.Sprint.TicketLookup != "" || n.Sprint.TicketCount != nil {
		t.Fatalf("a future sprint is not searched, so has no ticket coverage: %+v", n.Sprint)
	}

	// Tickets nothing else leads to are tracked by their sprint's group.
	eff := s.Node(effortNodeID(sprintEffortKey(1234)))
	if eff == nil || eff.Effort.Group != GroupSprint || eff.Label != "Sprint 12" || eff.Effort.Description != "Ship token refresh" {
		t.Fatalf("sprint group effort: %+v", eff)
	}
	if got := edgesFrom(s, eff.ID, EdgeTracks); !slices.Equal(got, []string{jiraTicketID("TEAM-2"), jiraTicketID("TEAM-3")}) {
		t.Fatalf("Sprint 12 tracks %v", got)
	}
	if got := edgesFrom(s, effortNodeID(sprintEffortKey(1240)), EdgeTracks); !slices.Equal(got, []string{jiraTicketID("TEAM-4")}) {
		t.Fatalf("Web 7 tracks %v", got)
	}
	if s.Node(effortNodeID(sprintEffortKey(1235))) != nil {
		t.Fatal("a sprint with no orphan tickets needs no group effort")
	}
	for _, e := range s.Edges {
		if e.Kind == EdgeTracks && (e.Target == jiraTicketID("TEAM-1") || e.Target == jiraTicketID("TEAM-5")) {
			t.Fatalf("a ticket a bookmark leads to is not tracked: %+v", e)
		}
	}

	// in_sprint: search membership, and each ticket's sprints that are nodes.
	for key, sprint := range map[string]int{"TEAM-1": 1234, "TEAM-2": 1234, "TEAM-3": 1234, "TEAM-4": 1240, "TEAM-5": 1235} {
		if got := edgesFrom(s, jiraTicketID(key), EdgeInSprint); !slices.Equal(got, []string{sprintNodeID(sprint)}) {
			t.Fatalf("%s in_sprint %v, want only sprint %d", key, got, sprint)
		}
	}

	// A ticket known only from the search shows what it reported; the
	// facts it could not report stay unknown.
	t2 := s.Node(jiraTicketID("TEAM-2")).Ticket
	if t2.Title != "Document the token API" || t2.StatusCategory != StatusToDo || t2.Freshness != Fresh || t2.URL != "https://acme.example/browse/TEAM-2" {
		t.Fatalf("brief ticket: %+v", t2)
	}
	if t2.StoryPointsKnown || t2.StoryPoints != nil || t2.Sprints != nil || t2.Components != nil {
		t.Fatalf("a brief ticket's unreported facts must stay unknown: %+v", t2)
	}
	// A ticket with full facts cached uses them.
	t3 := s.Node(jiraTicketID("TEAM-3")).Ticket
	if t3.Title != "Rate-limit the token endpoints" || !t3.StoryPointsKnown || t3.StoryPoints == nil || *t3.StoryPoints != 5 || len(t3.Sprints) != 2 {
		t.Fatalf("full ticket: %+v", t3)
	}

	st := s.Sources[SourceJiraSprints]
	if st.State != Fresh || st.Fresh != 3 || len(st.Parts) != 3 {
		t.Fatalf("jira_sprints: %+v", st)
	}
	names := []string{st.Parts[0].Name, st.Parts[1].Name, st.Parts[2].Name}
	if !slices.Equal(names, []string{"board 42", "sprint 1234", "sprint 1240"}) {
		t.Fatalf("parts %v", names)
	}
	if countJobs(jobs, JobSprints) != 0 {
		t.Fatal("a fresh board is not refetched")
	}
	// Every ticket without fresh facts of its own is queued for them.
	if n := countJobs(jobs, JobJira); n != 3 {
		t.Fatalf("jira jobs %d, want TEAM-1, TEAM-2 and TEAM-4", n)
	}
}

// TestAssembleSprintsTruncatedAndFailedSearch marks a sprint whose search
// hit the bound, and reports a sprint whose search failed.
func TestAssembleSprintsTruncatedAndFailedSearch(t *testing.T) {
	a := newAsm(t)
	a.c.cfg.JiraBoard = 42
	data := boardFixture()
	data.Sprints[2].Truncated = true
	data.Sprints[1].Tickets, data.Sprints[1].Error = nil, "search issues: exit status 1"
	if err := jiracache.RefreshSprints(&stubSprintFetcher{data: data}, 42, []string{JiraCurrentUser}); err == nil {
		t.Fatal("a failed sprint search is reported")
	}
	s, _ := a.build()
	if !s.Node(sprintNodeID(1234)).Sprint.Truncated {
		t.Fatal("truncated sprint not marked")
	}
	if sp := s.Node(sprintNodeID(1240)).Sprint; sp.TicketLookup != Error || sp.TicketCount != nil || !sp.TicketFetchedAt.IsZero() {
		t.Fatalf("a failed search with no earlier result has an unknown count, never zero: %+v", sp)
	}
	st := s.Sources[SourceJiraSprints]
	if st.State != Error || st.Error != 1 {
		t.Fatalf("jira_sprints: %+v", st)
	}
	for _, p := range st.Parts {
		switch p.Name {
		case "sprint 1234":
			if !p.Truncated || p.State != Fresh {
				t.Fatalf("truncated part: %+v", p)
			}
		case "sprint 1240":
			if p.State != Error || p.Error == "" || !p.FetchedAt.IsZero() {
				t.Fatalf("failed part: %+v", p)
			}
		}
	}
}

func TestAssembleSprintsWithoutBoard(t *testing.T) {
	a := newAsm(t)
	s, jobs := a.build()
	st := s.Sources[SourceJiraSprints]
	if st.State != Unknown || !strings.Contains(st.Detail, "[jira] board") {
		t.Fatalf("jira_sprints: %+v", st)
	}
	if countJobs(jobs, JobSprints) != 0 {
		t.Fatal("no board, nothing to fetch")
	}
}

// TestAssembleSprintsColdAndFailed: a board with nothing cached is unknown
// and queued; one whose fetch failed is an error saying how to fix it.
func TestAssembleSprintsColdAndFailed(t *testing.T) {
	a := newAsm(t)
	a.c.cfg.JiraBoard = 42
	s, jobs := a.build()
	st := s.Sources[SourceJiraSprints]
	if st.State != Unknown || !strings.Contains(st.Detail, "not cached yet") || len(st.Parts) != 1 {
		t.Fatalf("cold jira_sprints: %+v", st)
	}
	var job *Job
	for i := range jobs {
		if jobs[i].Kind == JobSprints {
			job = &jobs[i]
		}
	}
	if job == nil || job.Board != 42 || !slices.Equal(job.Assignees, []string{JiraCurrentUser}) {
		t.Fatalf("sprints job: %+v", job)
	}

	_ = jiracache.RefreshSprints(&stubSprintFetcher{err: errors.New("unauthorized: use 'acli jira auth login'")}, 42, nil)
	s, jobs = a.build()
	st = s.Sources[SourceJiraSprints]
	if st.State != Error || !strings.Contains(st.Detail, "acli jira auth status") || !strings.Contains(st.Parts[0].Error, "unauthorized") {
		t.Fatalf("failed jira_sprints: %+v", st)
	}
	if countJobs(jobs, JobSprints) != 1 {
		t.Fatal("a failed board is retried")
	}
	for _, n := range s.Nodes {
		if n.Kind == KindSprint {
			t.Fatalf("no sprint nodes without data: %+v", n)
		}
	}
}

// TestAssembleSprintsFollowsPeople searches for every followed person with
// a Jira identity, and refetches a board searched for other people.
func TestAssembleSprintsFollowsPeople(t *testing.T) {
	a := newAsm(t)
	a.c.cfg.JiraBoard = 42
	a.c.cfg.People = []Person{{Login: "alice", Jira: "acct-alice"}, {Login: "bob", Jira: "acct-bob"}, {Login: "carol"}}
	if got := a.c.sprintAssignees(); !slices.Equal(got, []string{"acct-alice", "acct-bob", JiraCurrentUser}) {
		t.Fatalf("assignees %v: the authenticated user is always followed", got)
	}
	a.c.cfg.Identity = func() (string, string) { return "alice", "" }
	if got := a.c.sprintAssignees(); !slices.Equal(got, []string{"acct-alice", "acct-bob"}) {
		t.Fatalf("assignees %v: alice is me", got)
	}

	cacheBoard(t, boardFixture(), JiraCurrentUser)
	s, jobs := a.build()
	if st := s.Sources[SourceJiraSprints]; st.State != Stale {
		t.Fatalf("a board searched for other people is stale: %+v", st)
	}
	if s.Node(sprintNodeID(1234)) == nil {
		t.Fatal("its sprints are still shown")
	}
	if countJobs(jobs, JobSprints) != 1 {
		t.Fatal("and refetched")
	}
}

// TestRefresherSprintsJob runs a sprints job through the Refresher and
// writes the board's cache.
func TestRefresherSprintsJob(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sf := &stubSprintFetcher{data: boardFixture()}
	r := NewRefresher(Fetchers{Sprints: sf}, RefresherOptions{Workers: 1, IgnoreLeases: true})
	defer r.Close()
	job := Job{Kind: JobSprints, Board: 42, Assignees: []string{"acct-bob", JiraCurrentUser}}
	res := <-r.Submit([]Job{job, job})
	if res.Err() != nil || res.Jobs != 2 || sf.calls.Load() != 1 {
		t.Fatalf("batch %+v, %d fetches", res, sf.calls.Load())
	}
	if got := jiracache.ReadSprints(42, jiracache.SprintTTL); got.State != jiracache.Fresh || len(got.Data.Sprints) != 3 {
		t.Fatalf("cache %+v", got)
	}
	if job.key() == (Job{Kind: JobSprints, Board: 42}).key() {
		t.Fatal("a search for other people is another job")
	}

	none := NewRefresher(Fetchers{Missing: map[JobKind]string{JobSprints: "acli not on PATH"}}, RefresherOptions{})
	defer none.Close()
	res = <-none.Submit([]Job{job})
	if got := none.skipSummary(res); got != "1 Jira sprint lookups skipped: acli not on PATH" {
		t.Fatalf("skip summary %q", got)
	}
}

// TestRefreshWithoutACLI: without acli the dash still loads; jira and
// jira_sprints are unknown, and the diagnostics say why.
func TestRefreshWithoutACLI(t *testing.T) {
	f := newFixture(t)
	r := NewRefresher(Fetchers{Missing: map[JobKind]string{
		JobPR: "no GitHub token or gh", JobIssue: "no GitHub token or gh",
		JobJira: "acli not on PATH", JobSprints: "acli not on PATH",
	}}, RefresherOptions{Workers: 2, IgnoreLeases: true})
	defer r.Close()
	c := f.collector(t, time.Now(), memSource{})
	c.cfg.JiraBoard = 42
	d, err := Open(Options{Dir: t.TempDir(), Collector: c, Refresher: r})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true}); err != nil {
		t.Fatalf("a missing acli is not an error: %v", err)
	}
	v := d.Current()
	if err := v.Snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	if st := v.Snapshot.Sources[SourceJira]; st.State != Unknown {
		t.Fatalf("jira: %+v", st)
	}
	if st := v.Snapshot.Sources[SourceJiraSprints]; st.State != Unknown || st.Detail == "" {
		t.Fatalf("jira_sprints: %+v", st)
	}
	if !hasDiag(v, "Jira lookups skipped: acli not on PATH") || !hasDiag(v, "Jira sprint lookups skipped: acli not on PATH") {
		t.Fatalf("diagnostics %v", v.Diagnostics)
	}
	if v.Snapshot.Sources[SourceJJ].State == Unknown {
		t.Fatal("nothing else is affected")
	}
}

// TestRefreshWaitFetchesSprintTicketFacts: a waited-for refresh fetches the
// board, then the facts of the tickets it found, in a second round.
func TestRefreshWaitFetchesSprintTicketFacts(t *testing.T) {
	f := newFixture(t)
	sf := &stubSprintFetcher{data: boardFixture()}
	jf := &stubJiraFacts{}
	r := NewRefresher(Fetchers{Jira: jf, Sprints: sf}, RefresherOptions{Workers: 2, IgnoreLeases: true})
	defer r.Close()
	c := f.collector(t, time.Now(), memSource{})
	c.cfg.JiraBoard = 42
	d, err := Open(Options{Dir: t.TempDir(), Collector: c, Refresher: r})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true}); err != nil {
		t.Fatal(err)
	}
	s := d.Current().Snapshot
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sf.assignees, []string{JiraCurrentUser}) {
		t.Fatalf("searched for %v", sf.assignees)
	}
	t2 := s.Node(jiraTicketID("TEAM-2"))
	if t2 == nil || t2.Ticket.Title != "full facts of TEAM-2" || !t2.Ticket.StoryPointsKnown {
		t.Fatalf("sprint ticket after a waited refresh: %+v", t2)
	}
	if st := s.Sources[SourceJiraSprints]; st.State != Fresh {
		t.Fatalf("jira_sprints: %+v", st)
	}
}

// failingJiraFacts fails every ticket lookup and counts them per ticket.
type failingJiraFacts struct {
	mu    sync.Mutex
	calls map[string]int
}

func (f *failingJiraFacts) FetchJira(ticket string) (jiracache.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[ticket]++
	return jiracache.Info{}, errors.New("unauthorized")
}

// TestRefreshWaitRunsAFailedTicketLookupOnce: a sprint ticket whose lookup
// fails is still not fresh after its round, but a waited-for refresh does
// not run it again: a failure costs one acli call, and is reported once.
func TestRefreshWaitRunsAFailedTicketLookupOnce(t *testing.T) {
	f := newFixture(t)
	jf := &failingJiraFacts{calls: map[string]int{}}
	r := NewRefresher(Fetchers{Jira: jf, Sprints: &stubSprintFetcher{data: boardFixture()}}, RefresherOptions{Workers: 2, IgnoreLeases: true})
	defer r.Close()
	c := f.collector(t, time.Now(), memSource{})
	c.cfg.JiraBoard = 42
	d, err := Open(Options{Dir: t.TempDir(), Collector: c, Refresher: r})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true}); err == nil {
		t.Fatal("the failed lookups fail the waited-for refresh")
	}
	for _, ticket := range []string{"TEAM-1", "TEAM-2", "TEAM-3", "TEAM-4"} {
		if jf.calls[ticket] == 0 {
			t.Fatalf("looked up %v, want every sprint ticket", jf.calls)
		}
	}
	for ticket, n := range jf.calls {
		if n != 1 {
			t.Errorf("%s looked up %d times, want once", ticket, n)
		}
	}
}

// TestAssembleSprintsKeepsTicketsOfAFailedSearch: a sprint whose search
// failed still shows the tickets of its last good search, as stale with
// the error, and the board is refetched once negativeTTL has passed.
func TestAssembleSprintsKeepsTicketsOfAFailedSearch(t *testing.T) {
	a := newAsm(t)
	a.c.cfg.JiraBoard = 42
	cacheBoard(t, boardFixture(), JiraCurrentUser)
	failed := boardFixture()
	failed.Sprints[2].Tickets, failed.Sprints[2].Error = nil, "search issues: exit status 1"
	if err := jiracache.RefreshSprints(&stubSprintFetcher{data: failed}, 42, []string{JiraCurrentUser}); err == nil {
		t.Fatal("the failed search is reported")
	}
	s, jobs := a.build()
	if got := edgesFrom(s, effortNodeID(sprintEffortKey(1234)), EdgeTracks); len(got) != 3 {
		t.Fatalf("Sprint 12's tickets are kept: tracks %v", got)
	}
	var part *SourcePart
	st := s.Sources[SourceJiraSprints]
	for i := range st.Parts {
		if st.Parts[i].Name == "sprint 1234" {
			part = &st.Parts[i]
		}
	}
	if part == nil || part.State != Stale || part.Error == "" || part.FetchedAt.IsZero() {
		t.Fatalf("kept part: %+v", part)
	}
	if sp := s.Node(sprintNodeID(1234)).Sprint; sp.TicketLookup != Stale || !sp.TicketFetchedAt.Equal(part.FetchedAt) || sp.TicketCount == nil || *sp.TicketCount != 3 {
		t.Fatalf("the kept tickets' coverage is the earlier search's: %+v", sp)
	}
	if st.State != Stale {
		t.Fatalf("jira_sprints: %+v", st)
	}
	if countJobs(jobs, JobSprints) != 0 {
		t.Fatal("within negativeTTL the board is not refetched yet")
	}
}

// TestAssembleSprintsShowsSearchNotes: the board's notes, such as a
// rejected Jira identity, are diagnostics.
func TestAssembleSprintsShowsSearchNotes(t *testing.T) {
	a := newAsm(t)
	a.c.cfg.JiraBoard = 42
	data := boardFixture()
	data.Notes = []string{`Jira rejected the sprint search for "acct-old" ([pair] teammate_jira)`}
	cacheBoard(t, data, JiraCurrentUser)
	s, _ := a.build()
	if !slices.Contains(s.Diagnostics, data.Notes[0]) {
		t.Fatalf("diagnostics %v", s.Diagnostics)
	}
}

// TestRefreshWaitQueueOverflowIsNotAFailure: jobs beyond the queue bound
// are not run; a waited refresh says so but does not fail, and their data
// stays unknown.
func TestRefreshWaitQueueOverflowIsNotAFailure(t *testing.T) {
	f := newFixture(t)
	bf := &blockingFetchers{release: make(chan struct{})}
	r := NewRefresher(Fetchers{PR: bf, Jira: bf, Issue: bf}, RefresherOptions{Workers: 1, Queue: 1, IgnoreLeases: true})
	defer r.Close()
	d, err := Open(Options{Dir: t.TempDir(), Collector: f.collector(t, time.Now(), memSource{}), Refresher: r})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for bf.calls.Load() < 1 {
			time.Sleep(time.Millisecond)
		}
		close(bf.release)
	}()
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true}); err != nil {
		t.Fatalf("dropped lookups must not fail the refresh: %v", err)
	}
	if v := d.Current(); !hasDiag(v, "not run (queue full)") {
		t.Fatalf("diagnostics %v", v.Diagnostics)
	}
}

// TestSprintSearchFillsTicketsOthersAdded: a ticket a bookmark (or PR)
// added first, with no Jira data of its own, gets the sprint search's
// facts, freshness and URL; one whose own entry has data keeps it.
func TestSprintSearchFillsTicketsOthersAdded(t *testing.T) {
	a := newAsm(t)
	a.c.cfg.JiraBoard = 42
	cacheBoard(t, boardFixture(), JiraCurrentUser)
	a.ws("/w/acme/widgets-2", "TEAM-2-docs")
	a.ws("/w/acme/widgets-3", "TEAM-3-limits")
	if err := jiracache.Write("TEAM-3", jiracache.Info{Status: "In Review", Summary: "Rate-limit the token endpoints", StatusCategory: StatusInProgress}); err != nil {
		t.Fatal(err)
	}
	s, _ := a.build()

	t2 := s.Node(jiraTicketID("TEAM-2")).Ticket
	if t2.Status != "To Do" || t2.Title != "Document the token API" || t2.StatusCategory != StatusToDo ||
		t2.Freshness != Fresh || t2.FetchedAt.IsZero() || t2.URL != "https://acme.example/browse/TEAM-2" {
		t.Fatalf("a bookmark's ticket without Jira data gets the search's facts: %+v", t2)
	}
	t3 := s.Node(jiraTicketID("TEAM-3")).Ticket
	if t3.Status != "In Review" || t3.Title != "Rate-limit the token endpoints" || t3.Freshness != Fresh {
		t.Fatalf("a ticket's own Jira data is kept over the search's: %+v", t3)
	}
	if got := edgesFrom(s, effortNodeID(sprintEffortKey(1234)), EdgeTracks); slices.Contains(got, jiraTicketID("TEAM-2")) {
		t.Fatal("a ticket a bookmark leads to is not tracked by the sprint group")
	}
	if got := edgesFrom(s, jiraTicketID("TEAM-2"), EdgeInSprint); !slices.Equal(got, []string{sprintNodeID(1234)}) {
		t.Fatalf("TEAM-2 in_sprint %v", got)
	}
}
