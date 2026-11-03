package dash

import (
	"slices"
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
	if !slices.Contains(s.Diagnostics, "Jira team are unknown: set team in [jira.fields]") {
		t.Fatalf("diagnostics %v", s.Diagnostics)
	}
}
