package dash

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/jiracache"
)

// This file adds the sprints of [jira] board, and their tickets, to the
// snapshot (gh-98).

// sprintAssignees returns the Jira identities a sprint's ticket search
// covers, sorted: every followed person's Jira identity, plus currentUser()
// when no one is known to be the authenticated user, whom the dash always
// follows.
func (c *Collector) sprintAssignees() []string {
	var login, jira string
	if c.cfg.Identity != nil {
		login, jira = c.cfg.Identity()
	}
	var out []string
	me := false
	for _, p := range ResolvePeople(c.cfg.People, c.cfg.Pair, login, jira) {
		if p.Jira != "" {
			out = append(out, p.Jira)
		}
		me = me || p.Me
	}
	if !me {
		out = append(out, JiraCurrentUser)
	}
	return jiracache.SortedAssignees(out)
}

// addSprints adds [jira] board's cached sprints as sprint nodes, and the
// tickets each active sprint's search found, then links tickets to sprints:
// in_sprint from every ticket whose sprints include a sprint node or whose
// sprint search found it, and tracks from a sprint's group effort to each
// ticket nothing else leads to. It returns the jira_sprints source: one
// part for the board's sprint list and one per active sprint's ticket
// search. It only reads caches; a board that is not fresh is queued.
func (c *Collector) addSprints(b *builder, effortNode func(string) string, jira *remoteTally, addJob func(Job)) SourceStatus {
	board := c.cfg.JiraBoard
	if board <= 0 {
		return SourceStatus{State: Unknown, Detail: "no [jira] board is configured; set board in the [jira] section of ~/.wgo/config.toml to show its sprints"}
	}
	assignees := c.sprintAssignees()
	res := jiracache.ReadSprints(board, jiracache.SprintTTL)
	boardName := "board " + strconv.Itoa(board)
	boardPart := SourcePart{Name: boardName, FetchedAt: res.FetchedAt}
	detail := ""
	switch {
	case res.State == jiracache.Miss:
		boardPart.State = Unknown
		detail = boardName + "'s sprints are not cached yet; wgo dash fetches them in the background with acli (wgo dash --json --refresh fetches them now)"
	case res.Failed:
		boardPart.State, boardPart.Error, boardPart.FetchedAt = Error, res.Err, time.Time{}
		detail = boardName + ": the sprint lookup failed; check acli is installed and authenticated (acli jira auth status)"
	case res.State == jiracache.Fresh:
		boardPart.State = Fresh
	default:
		boardPart.State = Stale
	}
	if boardPart.State == Fresh && !slices.Equal(res.Data.Assignees, assignees) {
		// Searched for other people: show it, but fetch it again.
		boardPart.State = Stale
	}
	if boardPart.State != Fresh {
		addJob(Job{Kind: JobSprints, Board: board, Assignees: assignees})
	}

	parts := []SourcePart{boardPart}
	if boardPart.State.hasData() {
		b.diags = append(b.diags, res.Data.Notes...)
	}
	members := map[string][]int{} // ticket node ID -> the active sprints whose search found it
	var found []string            // ticket node IDs in the order found
	if boardPart.State.hasData() {
		sprints := slices.Clone(res.Data.Sprints)
		slices.SortStableFunc(sprints, func(x, y jiracache.SprintEntry) int {
			return cmp.Or(
				cmp.Compare(sprintRank(x.State), sprintRank(y.State)),
				x.Start.Compare(y.Start),
				cmp.Compare(x.ID, y.ID),
			)
		})
		for _, sp := range sprints {
			if sp.ID <= 0 {
				continue
			}
			nid := sprintNodeID(sp.ID)
			label := sp.Name
			if label == "" {
				label = "Sprint " + strconv.Itoa(sp.ID)
			}
			b.node(Node{ID: nid, Kind: KindSprint, Label: label, Sprint: &SprintInfo{
				ID: sp.ID, Name: sp.Name, State: sprintState(sp.State), Start: sp.Start, End: sp.End, Goal: sp.Goal,
				Board: board, Freshness: boardPart.State, FetchedAt: res.FetchedAt, Truncated: sp.Truncated,
			}})
			if sp.State != SprintActive {
				continue
			}
			part := SourcePart{Name: "sprint " + strconv.Itoa(sp.ID), State: boardPart.State, FetchedAt: res.FetchedAt, Truncated: sp.Truncated}
			switch {
			case sp.Error != "" && sp.Tickets != nil:
				// The search failed; its tickets are an earlier search's.
				part.State, part.Error, part.FetchedAt = Stale, sp.Error, sp.TicketsFetchedAt
			case sp.Error != "":
				part.State, part.Error, part.FetchedAt = Error, sp.Error, time.Time{}
			}
			parts = append(parts, part)
			for _, t := range sp.Tickets {
				if t.Key == "" {
					continue
				}
				tid := c.sprintTicketNode(b, t, boardPart.State, res, jira, addJob)
				if members[tid] == nil {
					found = append(found, tid)
				}
				members[tid] = append(members[tid], sp.ID)
				b.edge(tid, nid, EdgeInSprint)
			}
		}
	}

	// in_sprint for every ticket whose sprints include a sprint node. A
	// carried-over ticket lists closed sprints too, which have no node.
	for id, n := range b.nodes {
		if n.Ticket == nil {
			continue
		}
		for _, ref := range n.Ticket.Sprints {
			if sn := b.nodes[sprintNodeID(ref.ID)]; sn != nil && sn.Kind == KindSprint {
				b.edge(id, sn.ID, EdgeInSprint)
			}
		}
	}

	// A sprint ticket that no PR, bookmark or effort leads to joins its
	// (first) active sprint's group effort, labelled with the sprint.
	led := map[string]bool{}
	for e := range b.edges {
		if e.Kind == EdgeTicket || e.Kind == EdgeTracks {
			led[e.Target] = true
		}
	}
	for _, tid := range found {
		if led[tid] {
			continue
		}
		sp := members[tid][0]
		eid := effortNode(sprintEffortKey(sp))
		if sn, en := b.nodes[sprintNodeID(sp)], b.nodes[eid]; sn != nil && en != nil {
			en.Label, en.Effort.Description = sn.Label, sn.Sprint.Goal
		}
		b.edge(eid, tid, EdgeTracks)
	}

	var tally remoteTally
	for _, p := range parts {
		tally.add(p.State, p.FetchedAt)
	}
	st := tally.status()
	st.Detail, st.Parts = detail, parts
	return st
}

// sprintRank orders sprints active, future, then the rest.
func sprintRank(state string) int {
	switch state {
	case SprintActive:
		return 0
	case SprintFuture:
		return 1
	}
	return 2
}

// sprintTicketNode adds a ticket a sprint search found and returns its ID.
// Its facts come from the ticket's own cache entry when that has data,
// since it holds every field; until then, from what the search reported,
// with the sprint list's freshness f. Either way the ticket's own entry is
// queued for refresh when it is not fresh.
func (c *Collector) sprintTicketNode(b *builder, t jiracache.SprintTicket, f Freshness, res jiracache.SprintsResult, jira *remoteTally, addJob func(Job)) string {
	id := jiraTicketID(t.Key)
	if _, done := b.nodes[id]; done {
		return id
	}
	id = c.jiraTicketNode(b, t.Key, jira, addJob)
	ti := b.nodes[id].Ticket
	if !ti.Freshness.hasData() {
		applyJiraFacts(ti, t.Info)
		ti.Freshness, ti.FetchedAt, ti.Error = f, res.FetchedAt, ""
	}
	if ti.URL == "" && res.Data.Site != "" {
		ti.URL = "https://" + strings.TrimPrefix(strings.TrimPrefix(res.Data.Site, "https://"), "http://") + "/browse/" + t.Key
	}
	return id
}
