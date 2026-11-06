package cmd

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/dash"
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
// assignees or to no one, at most jiracache.MaxSprintTickets of them (one
// more is asked for, to tell a full sprint from a truncated one). A failed
// ticket search is recorded on its sprint rather than failing the board.
// Jira rejects a search naming an identity it does not know, so a failed
// search is retried in parts: a stale identity in config then costs only
// its own tickets, with a note naming its config key, and is left out of
// the later sprints' searches.
func (sprintFetcher) FetchSprints(board int, assignees []string) (jiracache.BoardSprints, error) {
	sprints, err := listSprints(board, "active,future", true)
	if err != nil {
		return jiracache.BoardSprints{}, errors.New(acliErrorLine(err.Error()))
	}
	out := jiracache.BoardSprints{Board: board, Sprints: []jiracache.SprintEntry{}}
	rejected := map[string]string{} // identity -> Jira's error
	for _, s := range sprints {
		e := jiracache.SprintEntry{ID: s.ID, Name: s.Name, State: strings.ToLower(s.State), Goal: s.Goal, Start: s.StartDate, End: s.EndDate}
		var who []string
		for _, a := range assignees {
			if _, bad := rejected[a]; !bad {
				who = append(who, a)
			}
		}
		if e.State == "active" {
			var skipped int
			e.Tickets, e.Truncated, skipped, e.Error = sprintTickets(s.ID, who, rejected)
			if skipped > 0 {
				out.Notes = append(out.Notes, fmt.Sprintf("%d of %s's search results could not be read, so its ticket list is incomplete (WGO_DEBUG=1 shows why)", skipped, cmp.Or(s.Name, fmt.Sprintf("sprint %d", s.ID))))
			}
		}
		out.Sprints = append(out.Sprints, e)
	}
	for _, a := range slices.Sorted(maps.Keys(rejected)) {
		out.Notes = append(out.Notes, fmt.Sprintf("Jira rejected the sprint search for %q (%s), so that person's sprint tickets are missing: %s; fix it in ~/.wgo/config.toml, or remove it",
			a, jiraIdentitySource(a), rejected[a]))
	}
	out.Site = acliSiteHost()
	return out, nil
}

// sprintTickets searches one sprint's tickets assigned to assignees or to
// no one, with the fields acli's search allows. skipped counts the results
// it could not read. errMsg is set, and tickets nil, when the search
// failed. A failed search is retried in parts: the
// unassigned tickets alone, which name no identity, so their failure is
// the search's own; then each assignee alone. Only an assignee Jira says
// does not exist is added to rejected; any other failure (a rate limit, a
// timeout) fails the sprint's search, so its earlier tickets are kept.
func sprintTickets(sprint int, assignees []string, rejected map[string]string) (tickets []jiracache.SprintTicket, truncated bool, skipped int, errMsg string) {
	raws, err := searchSprint(sprint, assignees, true)
	if err != nil && len(assignees) > 0 {
		raws, err = searchSprint(sprint, nil, true)
		for _, a := range assignees {
			if err != nil {
				break
			}
			one, aerr := searchSprint(sprint, []string{a}, false)
			switch {
			case aerr == nil:
				raws = append(raws, one...)
			case identityRejected(aerr):
				rejected[a] = acliErrorLine(aerr.Error())
			default:
				err = aerr
			}
		}
	}
	if err != nil {
		return nil, false, 0, acliErrorLine(err.Error())
	}
	seen := map[string]bool{}
	tickets = []jiracache.SprintTicket{}
	for _, raw := range raws {
		f, _, err := jira.ParseFacts(raw, jira.FieldIDs{})
		if err != nil {
			debugf("jira sprint %d: skipping an unreadable search result: %v", sprint, err)
			skipped++
			continue
		}
		if seen[f.Key] {
			continue
		}
		seen[f.Key] = true
		if len(tickets) == jiracache.MaxSprintTickets {
			truncated = true
			break
		}
		tickets = append(tickets, jiracache.SprintTicket{Key: f.Key, Info: infoFromFacts(f)})
	}
	return tickets, truncated, skipped, ""
}

// searchSprint runs one sprint's ticket search for assignees and, when
// unassigned is set, its tickets assigned to no one.
func searchSprint(sprint int, assignees []string, unassigned bool) ([]json.RawMessage, error) {
	return searchIssuesJSON(jira.SprintTicketsJQL(sprint, assignees, unassigned), jira.SearchFactFields, jiracache.MaxSprintTickets+1)
}

// identityRejected reports whether a search failed because Jira does not
// know an assignee it names ("The value 'x' does not exist for the field
// 'assignee'"), as opposed to failing for another reason.
func identityRejected(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "does not exist for the field") && strings.Contains(s, "assignee")
}

// jiraIdentitySource names where a Jira identity in a sprint search comes
// from, for a diagnostic.
func jiraIdentitySource(id string) string {
	if id == dash.JiraCurrentUser {
		return "the user acli is logged in as"
	}
	if cfg := config.Get(); cfg != nil {
		for i, p := range cfg.Dash.People {
			if p.Jira == id {
				return fmt.Sprintf("jira of [[dash.people]] entry %d, github = %q", i+1, p.GitHub)
			}
		}
		if cfg.Pair.TeammateJira == id {
			return "[pair] teammate_jira"
		}
	}
	return "a configured Jira identity"
}
