package dash

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// maxValidateErrors caps how many problems Validate reports individually.
const maxValidateErrors = 20

// edgeEndpoints lists, per edge kind, the allowed source and target kinds.
var edgeEndpoints = map[EdgeKind]struct{ src, dst []NodeKind }{
	EdgeContains: {[]NodeKind{KindEffort}, []NodeKind{KindWorkspace}},
	EdgeOn:       {[]NodeKind{KindWorkspace}, []NodeKind{KindBookmark}},
	EdgePR:       {[]NodeKind{KindBookmark}, []NodeKind{KindPR}},
	EdgeTicket:   {[]NodeKind{KindPR, KindBookmark}, []NodeKind{KindTicket}},
	EdgeRuns:     {[]NodeKind{KindEffort}, []NodeKind{KindAgent}},
	EdgeWorksIn:  {[]NodeKind{KindAgent}, []NodeKind{KindWorkspace}},
	EdgeTracks:   {[]NodeKind{KindEffort}, []NodeKind{KindPR, KindTicket}},
	EdgeInSprint: {[]NodeKind{KindTicket}, []NodeKind{KindSprint}},
}

// Validate checks the snapshot's structural invariants: schema version,
// unique typed nodes, well-formed edges, consistent counts and resolvable
// cross-references, plus the closed vocabularies and consistency of PR,
// ticket and sprint facts, source states and parts, and the people list.
// It returns every problem found (the first
// maxValidateErrors individually) joined into one error, or nil.
func (s *Snapshot) Validate() error {
	if s == nil {
		return errors.New("snapshot: nil")
	}
	v := &validator{}

	if s.Schema != SchemaVersion {
		v.addf("schema: got %d, want %d", s.Schema, SchemaVersion)
	}

	kinds := make(map[string]NodeKind, len(s.Nodes))
	byID := make(map[string]*Node, len(s.Nodes))
	for i, n := range s.Nodes {
		if n.ID == "" {
			v.addf("node id: node %d has an empty id", i)
			continue
		}
		if _, dup := kinds[n.ID]; dup {
			v.addf("node id: duplicate id %q", n.ID)
			continue
		}
		kinds[n.ID] = n.Kind
		byID[n.ID] = &s.Nodes[i]
		v.checkNodeKind(n)
	}

	// isKind reports whether id names an existing node of one of ks.
	isKind := func(id string, ks ...NodeKind) bool {
		k, ok := kinds[id]
		return ok && slices.Contains(ks, k)
	}

	// incoming records, per target node ID and edge kind, the sources of the
	// edges reaching it.
	incoming := map[string]map[EdgeKind][]string{}
	// inSprint records, per ticket ID, the IDs of the sprints its in_sprint
	// edges reach; sprints the IDs of every sprint node.
	inSprint := map[string]map[int]bool{}
	sprints := map[int]bool{}
	for _, n := range s.Nodes {
		if n.Sprint != nil {
			sprints[n.Sprint.ID] = true
		}
	}
	for _, e := range s.Edges {
		if incoming[e.Target] == nil {
			incoming[e.Target] = map[EdgeKind][]string{}
		}
		incoming[e.Target][e.Kind] = append(incoming[e.Target][e.Kind], e.Source)
		srcKind, srcOK := kinds[e.Source]
		dstKind, dstOK := kinds[e.Target]
		if !srcOK {
			v.addf("edge source: %s edge %q -> %q: source not found", e.Kind, e.Source, e.Target)
		}
		if !dstOK {
			v.addf("edge target: %s edge %q -> %q: target not found", e.Kind, e.Source, e.Target)
		}
		ends, ok := edgeEndpoints[e.Kind]
		if !ok {
			v.addf("edge kind: unknown kind %q on %q -> %q", e.Kind, e.Source, e.Target)
			continue
		}
		if srcOK && dstOK && (!slices.Contains(ends.src, srcKind) || !slices.Contains(ends.dst, dstKind)) {
			v.addf("edge endpoints: %s edge %q (%s) -> %q (%s) joins the wrong kinds", e.Kind, e.Source, srcKind, e.Target, dstKind)
			continue
		}
		// A ticket is in_sprint of exactly the sprint nodes it lists.
		if t, sp := byID[e.Source], byID[e.Target]; e.Kind == EdgeInSprint && t.Ticket != nil && sp.Sprint != nil {
			if !slices.ContainsFunc(t.Ticket.Sprints, func(r SprintRef) bool { return r.ID == sp.Sprint.ID }) {
				v.addf("reference: ticket %q has an in_sprint edge to %q, a sprint it does not list", e.Source, e.Target)
			}
			if inSprint[e.Source] == nil {
				inSprint[e.Source] = map[int]bool{}
			}
			inSprint[e.Source][sp.Sprint.ID] = true
		}
	}

	// Counts.
	if s.Days > 0 && len(s.Counts.Days) != s.Days {
		v.addf("counts days: got %d days, want %d", len(s.Counts.Days), s.Days)
	}
	for _, id := range sortedKeys(s.Counts.Activity) {
		if got := len(s.Counts.Activity[id]); got != len(s.Counts.Days) {
			v.addf("counts activity: %q has %d entries, want %d", id, got, len(s.Counts.Days))
		}
		if !isKind(id, KindEffort) {
			v.addf("counts key: activity key %q is not an effort node", id)
		}
	}
	for _, id := range sortedKeys(s.Counts.PRStates) {
		if !isKind(id, KindEffort) {
			v.addf("counts key: pr_states key %q is not an effort node", id)
		}
	}
	for _, id := range sortedKeys(s.Counts.Agents) {
		if !isKind(id, KindEffort) {
			v.addf("counts key: agents key %q is not an effort node", id)
		}
	}
	for _, id := range sortedKeys(s.Counts.Merged) {
		if got := len(s.Counts.Merged[id]); got != len(s.Counts.Days) {
			v.addf("counts merged: %q has %d entries, want %d", id, got, len(s.Counts.Days))
		}
		if !isKind(id, KindEffort) {
			v.addf("counts key: merged key %q is not an effort node", id)
		}
	}
	if s.Counts.Merged != nil && !mergedKnown(s) {
		v.addf("counts merged: present, but merges cannot be counted unless people include me and every person's closed-recent inventory set is fresh and untruncated")
	}
	v.checkPeople(s.People)
	v.checkSources(s.Sources)

	// Cross-references.
	for _, n := range s.Nodes {
		switch {
		case n.Workspace != nil:
			if !isKind(n.Workspace.EffortID, KindEffort) {
				v.addf("reference: workspace %q effort_id %q is not an effort node", n.ID, n.Workspace.EffortID)
			}
		case n.PR != nil:
			// A PR hangs off the bookmark it was found for, or, found only
			// by the inventory, off an effort that tracks it.
			switch bm := n.PR.BookmarkID; {
			case bm == "" && len(incoming[n.ID][EdgeTracks]) == 0:
				v.addf("reference: pr %q has neither a bookmark_id nor an incoming tracks edge", n.ID)
			case bm != "" && !isKind(bm, KindBookmark):
				v.addf("reference: pr %q bookmark_id %q is not a bookmark node", n.ID, bm)
			case bm != "" && !slices.Contains(incoming[n.ID][EdgePR], bm):
				v.addf("reference: pr %q bookmark_id %q has no pr edge to it", n.ID, bm)
			}
		case n.Ticket != nil:
			// A ticket is reached from a PR or bookmark, or, found only in a
			// sprint, tracked by an effort.
			if len(incoming[n.ID][EdgeTicket]) == 0 && len(incoming[n.ID][EdgeTracks]) == 0 {
				v.addf("reference: ticket %q has neither an incoming ticket edge nor a tracks edge", n.ID)
			}
			for _, sp := range n.Ticket.Sprints {
				if sprints[sp.ID] && !inSprint[n.ID][sp.ID] {
					v.addf("reference: ticket %q lists sprint %d but has no in_sprint edge to its node", n.ID, sp.ID)
				}
			}
		case n.Agent != nil:
			if !isKind(n.Agent.EffortID, KindEffort) {
				v.addf("reference: agent %q effort_id %q is not an effort node", n.ID, n.Agent.EffortID)
			}
			// An empty WorkspaceID is a session outside any discovered workspace.
			if id := n.Agent.WorkspaceID; id != "" && !isKind(id, KindWorkspace) {
				v.addf("reference: agent %q workspace_id %q is not a workspace node", n.ID, id)
			}
		case n.Effort != nil:
			for _, cf := range n.Effort.Conflicts {
				if !isKind(cf.WorkspaceID, KindWorkspace) {
					v.addf("reference: effort %q conflict workspace_id %q is not a workspace node", n.ID, cf.WorkspaceID)
				}
			}
		}
	}
	return v.err()
}

// checkNodeKind checks that n has a known kind and exactly the matching
// kind-specific field set.
func (v *validator) checkNodeKind(n Node) {
	set := map[NodeKind]bool{
		KindEffort:    n.Effort != nil,
		KindWorkspace: n.Workspace != nil,
		KindBookmark:  n.Bookmark != nil,
		KindPR:        n.PR != nil,
		KindTicket:    n.Ticket != nil,
		KindAgent:     n.Agent != nil,
		KindSprint:    n.Sprint != nil,
	}
	if _, known := set[n.Kind]; !known {
		v.addf("node kind: %q has unknown kind %q", n.ID, n.Kind)
		return
	}
	var present []NodeKind
	for _, k := range []NodeKind{KindEffort, KindWorkspace, KindBookmark, KindPR, KindTicket, KindAgent, KindSprint} {
		if set[k] {
			present = append(present, k)
		}
	}
	if len(present) != 1 || present[0] != n.Kind {
		v.addf("node kind: %q of kind %s must carry exactly its %s info, has %v", n.ID, n.Kind, n.Kind, present)
		return
	}
	if n.Agent != nil {
		switch n.Agent.Liveness {
		case LivenessActive, LivenessLive, LivenessUncertain:
		default:
			v.addf("node kind: agent %q has unknown liveness %q", n.ID, n.Agent.Liveness)
		}
	}
	switch {
	case n.PR != nil:
		v.checkPR(n.ID, n.PR)
	case n.Ticket != nil:
		v.checkTicket(n.ID, n.Ticket)
	case n.Sprint != nil:
		if n.Sprint.ID <= 0 {
			v.addf("sprint: %q has no sprint id", n.ID)
		}
		v.required(n.ID, "state", n.Sprint.State, SprintActive, SprintFuture, SprintClosed)
	}
}

// checkPR checks the closed vocabularies of a PR's inventory facts. An empty
// optional fact such as mergeable is unknown and allowed, but each entry of a
// reported list must carry its state, kind or role.
func (v *validator) checkPR(id string, p *PRInfo) {
	v.enum(id, "mergeable", p.Mergeable, MergeMergeable, MergeConflicting, MergeUnknown)
	for _, r := range p.ReviewRequests {
		if r.Reviewer == "" {
			v.addf("pr facts: %q has a review request with no reviewer", id)
		}
		v.required(id, "review request kind", r.Kind, ReviewerUser, ReviewerTeam)
		if r.Kind != "" && (r.Kind == ReviewerTeam) != strings.HasPrefix(r.Reviewer, teamPrefix) {
			v.addf("pr facts: %q has review request %q of kind %s; a team is %q and only a team is", id, r.Reviewer, r.Kind, teamPrefix+"<slug>")
		}
	}
	for _, r := range p.Reviews {
		if r.Author == "" {
			v.addf("pr facts: %q has a review with no author", id)
		}
		v.required(id, "review state", r.State, ReviewApproved, ReviewChangesRequested, ReviewCommented, ReviewDismissed)
	}
	for _, c := range p.CheckDetails {
		if c.Name == "" {
			v.addf("pr facts: %q has a check with no name", id)
		}
		v.required(id, "check state", c.State, CheckSuccess, CheckFailure, CheckPending, CheckSkipped, CheckNeutral)
	}
	if len(p.CheckDetails) > MaxCheckDetails {
		v.addf("pr facts: %q has %d check details, more than %d", id, len(p.CheckDetails), MaxCheckDetails)
	}
	for _, in := range p.Involvement {
		if in.Person == "" {
			v.addf("pr facts: %q has an involvement with no person", id)
		}
		v.required(id, "involvement role", in.Role, RoleAuthor, RoleReviewRequested, RoleReviewed)
	}
	for _, src := range p.Sources {
		v.required(id, "source", src, PRSourceBookmark, PRSourceInventory)
	}
	// Sources is absent only from snapshots older than the inventory.
	switch {
	case p.Sources == nil:
	case !slices.IsSorted(p.Sources) || len(slices.Compact(slices.Clone(p.Sources))) != len(p.Sources):
		v.addf("pr facts: %q has sources %v, want them sorted without repeats", id, p.Sources)
	case slices.Contains(p.Sources, PRSourceBookmark) && p.BookmarkID == "":
		v.addf("pr facts: %q was found through a bookmark but has no bookmark_id", id)
	case !slices.Contains(p.Sources, PRSourceInventory) && p.BookmarkID == "":
		v.addf("pr facts: %q has no bookmark_id and was not found by the inventory", id)
	}
}

// checkSources checks each source's state and, when it has parts, that
// their names are unique (case-insensitively, as inventory logins are) and
// their states known.
func (v *validator) checkSources(sources map[string]SourceStatus) {
	states := []string{string(Fresh), string(Stale), string(Unknown), string(Error)}
	for _, name := range sortedKeys(sources) {
		st := sources[name]
		v.required("source "+name, "state", string(st.State), states...)
		seen := map[string]bool{}
		for i, p := range st.Parts {
			key := strings.ToLower(p.Name)
			switch {
			case key == "":
				v.addf("sources: %s part %d has no name", name, i)
			case seen[key]:
				v.addf("sources: %s lists part %q twice", name, p.Name)
			}
			seen[key] = true
			v.required("source "+name+" part "+p.Name, "state", string(p.State), states...)
		}
	}
}

// checkTicket checks the closed vocabularies of a ticket's Jira facts.
func (v *validator) checkTicket(id string, t *TicketInfo) {
	v.enum(id, "status_category", t.StatusCategory, StatusToDo, StatusInProgress, StatusDone)
	for _, sp := range t.Sprints {
		if sp.ID <= 0 {
			v.addf("ticket facts: %q lists a sprint with no id", id)
		}
		v.enum(id, "sprint state", sp.State, SprintActive, SprintFuture, SprintClosed)
	}
}

// enum reports a non-empty value outside allowed. An empty value is
// unknown, which an optional field may be.
func (v *validator) enum(id, field, value string, allowed ...string) {
	if value != "" && !slices.Contains(allowed, value) {
		v.addf("facts: %q has %s %q, want one of %v", id, field, value, allowed)
	}
}

// required is enum for a field that must be set.
func (v *validator) required(id, field, value string, allowed ...string) {
	if value == "" {
		v.addf("facts: %q has no %s, want one of %v", id, field, allowed)
		return
	}
	v.enum(id, field, value, allowed...)
}

// checkPeople checks that each person has a distinct login and at most one
// is the authenticated user.
func (v *validator) checkPeople(people []Person) {
	seen := map[string]bool{}
	me := 0
	for i, p := range people {
		key := strings.ToLower(p.Login)
		switch {
		case key == "":
			v.addf("people: entry %d has no login", i)
		case seen[key]:
			v.addf("people: duplicate login %q", p.Login)
		}
		seen[key] = true
		if p.Me {
			me++
		}
	}
	if me > 1 {
		v.addf("people: %d entries are marked me, want at most one", me)
	}
}

type validator struct {
	errs  []error
	extra int
}

func (v *validator) addf(format string, args ...any) {
	if len(v.errs) >= maxValidateErrors {
		v.extra++
		return
	}
	v.errs = append(v.errs, fmt.Errorf(format, args...))
}

func (v *validator) err() error {
	if v.extra > 0 {
		v.errs = append(v.errs, fmt.Errorf("and %d more", v.extra))
	}
	return errors.Join(v.errs...)
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
