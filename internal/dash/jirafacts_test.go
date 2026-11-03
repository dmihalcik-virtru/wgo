package dash

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/jiracache"
)

// TestJiraTicketFacts maps every cached fact onto a ticket a bookmark leads
// to, and surfaces the entry's notes as diagnostics.
func TestJiraTicketFacts(t *testing.T) {
	a := newAsm(t)
	a.ws("/w/acme/widgets-9", "TEAM-9-facts")
	five := 5.0
	at := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	info := jiracache.Info{Status: "Done", Assignee: "Alice", Summary: "Rate-limit tokens", StatusCategory: StatusDone, Type: "Story", Priority: "High",
		Sprints: []jiracache.SprintRef{{ID: 1234, Name: "Sprint 12", State: "weird"}}, StoryPoints: &five, StoryPointsKnown: true,
		Parent: &jiracache.Parent{Key: "TEAM-100", Summary: "Token lifecycle", Type: "Epic"}, Team: "Platform",
		Components: []string{}, Labels: []string{"backend"}, Resolution: "Done", ResolvedAt: at, CreatedAt: at, UpdatedAt: at, AssigneeID: "acct-alice",
		Notes: []string{"Jira team are unknown: set team in [jira.fields]"}}
	if err := jiracache.Write("TEAM-9", info); err != nil {
		t.Fatal(err)
	}
	s, _ := a.build()
	ti := s.Node(jiraTicketID("TEAM-9")).Ticket
	if ti.Title != "Rate-limit tokens" || ti.StatusCategory != StatusDone || ti.Type != "Story" || ti.Priority != "High" ||
		!ti.StoryPointsKnown || *ti.StoryPoints != 5 || ti.Parent == nil || ti.Parent.Key != "TEAM-100" || ti.Team != "Platform" ||
		ti.Components == nil || len(ti.Labels) != 1 || ti.Resolution != "Done" || !ti.ResolvedAt.Equal(at) || ti.AssigneeID != "acct-alice" {
		t.Fatalf("facts: %+v", ti)
	}
	if len(ti.Sprints) != 1 || ti.Sprints[0].State != "" {
		t.Fatalf("an unknown sprint state is dropped, not published: %+v", ti.Sprints)
	}
	if !slices.Contains(s.Diagnostics, "Jira team are unknown: set team in [jira.fields] (1 ticket: TEAM-9)") {
		t.Fatalf("diagnostics %v", s.Diagnostics)
	}
}

// TestJiraNotesAreCountedByCause: the same note on many tickets is one
// diagnostic with a count and the first few keys, not one per ticket.
func TestJiraNotesAreCountedByCause(t *testing.T) {
	a := newAsm(t)
	const cause = "Jira's story points field customfield_10016 ([jira.fields] story_points) holds an unrecognised object value, so those tickets' story points are unknown"
	for _, key := range []string{"TEAM-1", "TEAM-2", "TEAM-3", "TEAM-4", "TEAM-5"} {
		a.ws("/w/acme/widgets-"+key, key+"-work")
		if err := jiracache.Write(key, jiracache.Info{Status: "To Do", Notes: []string{cause}}); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := a.build()
	var got []string
	for _, d := range s.Diagnostics {
		if strings.HasPrefix(d, "Jira's story points field") {
			got = append(got, d)
		}
	}
	want := []string{cause + " (5 tickets: TEAM-1, TEAM-2, TEAM-3, …)"}
	if !slices.Equal(got, want) {
		t.Fatalf("diagnostics:\n got %q\nwant %q", got, want)
	}
}
