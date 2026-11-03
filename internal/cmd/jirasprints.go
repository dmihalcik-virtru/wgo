package cmd

import (
	"errors"
	"strings"

	"github.com/virtru/wgo/internal/jira"
	"github.com/virtru/wgo/internal/jiracache"
)

// searchIssuesJSON and listSprints are the acli seams of the sprint
// fetcher; tests replace them.
var (
	searchIssuesJSON = jira.SearchIssuesJSON
	listSprints      = jira.ListSprints
)

// sprintFetcher adapts the acli-backed jira client to
// jiracache.SprintFetcher.
type sprintFetcher struct{}

// sprintFetcherFn is the seam used to obtain the SprintFetcher for wgo
// dash; tests override it.
var sprintFetcherFn = func() jiracache.SprintFetcher { return sprintFetcher{} }

// FetchSprints implements jiracache.SprintFetcher: the board's active and
// future sprints, and for each active sprint its tickets assigned to any of
// assignees, at most jiracache.MaxSprintTickets of them (one more is asked
// for, to tell a full sprint from a truncated one). A failed ticket search
// is recorded on its sprint rather than failing the board.
func (sprintFetcher) FetchSprints(board int, assignees []string) (jiracache.BoardSprints, error) {
	sprints, err := listSprints(board, "active,future", true)
	if err != nil {
		return jiracache.BoardSprints{}, errors.New(acliErrorLine(err.Error()))
	}
	out := jiracache.BoardSprints{Board: board, Sprints: []jiracache.SprintEntry{}}
	for _, s := range sprints {
		e := jiracache.SprintEntry{ID: s.ID, Name: s.Name, State: strings.ToLower(s.State), Goal: s.Goal, Start: s.StartDate, End: s.EndDate}
		if e.State == "active" && len(assignees) > 0 {
			e.Tickets, e.Truncated, e.Error = sprintTickets(s.ID, assignees)
		}
		out.Sprints = append(out.Sprints, e)
	}
	if site, err := jira.SiteHost(); err == nil {
		out.Site = site
	}
	return out, nil
}

// sprintTickets searches one sprint's tickets assigned to assignees, with
// the fields acli's search allows. errMsg is set, and tickets nil, when
// the search failed.
func sprintTickets(sprint int, assignees []string) (tickets []jiracache.SprintTicket, truncated bool, errMsg string) {
	raws, err := searchIssuesJSON(jira.SprintTicketsJQL(sprint, assignees), jira.SearchFactFields, jiracache.MaxSprintTickets+1)
	if err != nil {
		return nil, false, acliErrorLine(err.Error())
	}
	if len(raws) > jiracache.MaxSprintTickets {
		raws, truncated = raws[:jiracache.MaxSprintTickets], true
	}
	tickets = []jiracache.SprintTicket{}
	for _, raw := range raws {
		f, _, err := jira.ParseFacts(raw, jira.FieldIDs{})
		if err != nil {
			debugf("jira sprint %d: skipping an unreadable search result: %v", sprint, err)
			continue
		}
		tickets = append(tickets, jiracache.SprintTicket{Key: f.Key, Info: infoFromFacts(f)})
	}
	return tickets, truncated, ""
}
