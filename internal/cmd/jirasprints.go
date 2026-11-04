package cmd

import (
	"context"
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
	searchIssuesJSON = jira.SearchIssuesJSONContext
	listSprints      = jira.ListSprintsContext
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
// is recorded on its sprint rather than failing the board. Jira rejects a
// search naming an identity it does not know, so a failed search of several
// assignees is retried one by one: a stale identity in config then costs
// only its own tickets, with a note naming its config key, and is left out
// of the later sprints' searches.
func (f sprintFetcher) FetchSprints(board int, assignees []string) (jiracache.BoardSprints, error) {
	return f.FetchSprintsContext(context.Background(), board, assignees)
}

// FetchSprintsContext implements jiracache.SprintContextFetcher:
// FetchSprints, with acli killed when ctx is done.
func (sprintFetcher) FetchSprintsContext(ctx context.Context, board int, assignees []string) (jiracache.BoardSprints, error) {
	sprints, err := listSprints(ctx, board, "active,future", true)
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
		if e.State == "active" && len(who) > 0 {
			e.Tickets, e.Truncated, e.Error = sprintTickets(ctx, s.ID, who, rejected)
		}
		out.Sprints = append(out.Sprints, e)
	}
	if err := ctx.Err(); err != nil {
		// acli was killed part way: the failed searches say nothing.
		return jiracache.BoardSprints{}, err
	}
	for _, a := range slices.Sorted(maps.Keys(rejected)) {
		out.Notes = append(out.Notes, fmt.Sprintf("Jira rejected the sprint search for %q (%s), so that person's sprint tickets are missing: %s; fix it in ~/.wgo/config.toml, or remove it",
			a, jiraIdentitySource(a), rejected[a]))
	}
	out.Site = acliSiteHost()
	return out, nil
}

// sprintTickets searches one sprint's tickets assigned to assignees, with
// the fields acli's search allows. errMsg is set, and tickets nil, when
// the search failed for every assignee. When it fails for several, each is
// searched alone, and the ones Jira rejects are added to rejected; once ctx
// is done a failure says nothing about an identity, and none is added.
func sprintTickets(ctx context.Context, sprint int, assignees []string, rejected map[string]string) (tickets []jiracache.SprintTicket, truncated bool, errMsg string) {
	raws, err := searchSprint(ctx, sprint, assignees)
	if err != nil && len(assignees) > 1 && ctx.Err() == nil {
		var found []json.RawMessage
		var bad []string
		for _, a := range assignees {
			one, aerr := searchSprint(ctx, sprint, []string{a})
			if aerr != nil && ctx.Err() != nil {
				for _, b := range bad {
					delete(rejected, b)
				}
				return nil, false, acliErrorLine(aerr.Error())
			}
			if aerr != nil {
				bad = append(bad, a)
				rejected[a] = acliErrorLine(aerr.Error())
				continue
			}
			found = append(found, one...)
		}
		if len(bad) < len(assignees) {
			raws, err = found, nil
		} else {
			// Every search failed: not an identity, but the search itself.
			for _, a := range bad {
				delete(rejected, a)
			}
		}
	}
	if err != nil {
		return nil, false, acliErrorLine(err.Error())
	}
	seen := map[string]bool{}
	tickets = []jiracache.SprintTicket{}
	for _, raw := range raws {
		f, _, err := jira.ParseFacts(raw, jira.FieldIDs{})
		if err != nil {
			debugf("jira sprint %d: skipping an unreadable search result: %v", sprint, err)
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
	return tickets, truncated, ""
}

// searchSprint runs one sprint's ticket search for assignees.
func searchSprint(ctx context.Context, sprint int, assignees []string) ([]json.RawMessage, error) {
	return searchIssuesJSON(ctx, jira.SprintTicketsJQL(sprint, assignees), jira.SearchFactFields, jiracache.MaxSprintTickets+1)
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
