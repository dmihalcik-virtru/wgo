package dash

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/virtru/wgo/internal/jiracache"
)

// This file maps cached Jira ticket facts onto ticket nodes (gh-98).

// applyJiraFacts copies a cached Jira entry's status and facts onto ti. A
// value outside the snapshot's vocabularies (say, a hand-edited cache) is
// dropped as unknown rather than published.
func applyJiraFacts(ti *TicketInfo, info jiracache.Info) {
	ti.Status, ti.Assignee = info.Status, info.Assignee
	ti.Title = info.Summary
	switch info.StatusCategory {
	case StatusToDo, StatusInProgress, StatusDone:
		ti.StatusCategory = info.StatusCategory
	}
	ti.Type, ti.Priority = info.Type, info.Priority
	if info.Sprints != nil {
		ti.Sprints = make([]SprintRef, 0, len(info.Sprints))
		for _, sp := range info.Sprints {
			if sp.ID > 0 {
				ti.Sprints = append(ti.Sprints, SprintRef{ID: sp.ID, Name: sp.Name, State: sprintState(sp.State)})
			}
		}
	}
	ti.StoryPoints, ti.StoryPointsKnown = info.StoryPoints, info.StoryPointsKnown
	if p := info.Parent; p != nil && p.Key != "" {
		ti.Parent = &TicketParent{Key: p.Key, Summary: p.Summary, Type: p.Type}
	}
	ti.Team = info.Team
	ti.Components = slices.Clone(info.Components)
	ti.Labels = slices.Clone(info.Labels)
	ti.Resolution = info.Resolution
	ti.ResolvedAt, ti.CreatedAt, ti.UpdatedAt = info.ResolvedAt, info.CreatedAt, info.UpdatedAt
	ti.AssigneeID = info.AssigneeID
}

// sprintState returns s when it is a sprint state, else "" (unknown).
func sprintState(s string) string {
	switch s {
	case SprintActive, SprintFuture, SprintClosed:
		return s
	}
	return ""
}

// note records that ticket carries note.
func (b *builder) note(note, ticket string) {
	if b.notes == nil {
		b.notes = map[string][]string{}
	}
	b.notes[note] = append(b.notes[note], ticket)
}

// maxNoteTickets bounds how many tickets a note's diagnostic names.
const maxNoteTickets = 3

// noteDiagnostics turns the per-ticket notes into one diagnostic per cause,
// saying how many tickets it affects and naming the first few, so 200
// tickets with one unreadable field are one line, not 200.
func (b *builder) noteDiagnostics() []string {
	var out []string
	for _, note := range slices.Sorted(maps.Keys(b.notes)) {
		keys := slices.Compact(slices.Sorted(slices.Values(b.notes[note])))
		named, more := keys, ""
		if len(named) > maxNoteTickets {
			named, more = named[:maxNoteTickets], ", …"
		}
		word := "tickets"
		if len(keys) == 1 {
			word = "ticket"
		}
		out = append(out, fmt.Sprintf("%s (%d %s: %s%s)", note, len(keys), word, strings.Join(named, ", "), more))
	}
	return out
}
