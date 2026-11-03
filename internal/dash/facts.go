package dash

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// This file derives the attention facts of gh-99 from an assembled snapshot:
// whose move each PR is, why, since when and on whom; whether its CI failures
// are its own or fail across its repository's open PRs; how big and how
// stale it is; and where a Jira ticket and its PRs disagree. Each reason and
// mismatch carries a one-line evidence string built from its inputs, and
// there is deliberately no composite score (gh-96 principle 3). A fact whose
// inputs are unknown is unknown, with the missing input named in Uncertain:
// never false or zero. Durations are not stored; the query registry derives
// them from the timestamps here and generated_at.

// Default [dash.facts] thresholds, and the bounds wgo dash accepts.
const (
	DefaultStaleDays      = 30
	DefaultSystemicRatio  = 0.6
	DefaultSystemicMinPRs = 3

	MinStaleDays      = 1
	MaxStaleDays      = 3650
	MinSystemicMinPRs = 2
	MaxSystemicMinPRs = 1000
)

// FactThresholds are [dash.facts]. A zero field means its default.
type FactThresholds struct {
	// StaleDays is how long an open PR, or the code linked to an in-progress
	// ticket, may go without an update before it is stale.
	StaleDays int
	// SystemicRatio and SystemicMinPRs decide when a check fails everywhere
	// in a repository: it failed on at least SystemicRatio of the open PRs
	// that ran it, and it ran on at least SystemicMinPRs open PRs.
	SystemicRatio  float64
	SystemicMinPRs int
}

// DefaultFactThresholds returns the default [dash.facts].
func DefaultFactThresholds() FactThresholds {
	return FactThresholds{StaleDays: DefaultStaleDays, SystemicRatio: DefaultSystemicRatio, SystemicMinPRs: DefaultSystemicMinPRs}
}

func (t FactThresholds) withDefaults() FactThresholds {
	d := DefaultFactThresholds()
	if t.StaleDays <= 0 {
		t.StaleDays = d.StaleDays
	}
	if t.SystemicRatio <= 0 {
		t.SystemicRatio = d.SystemicRatio
	}
	if t.SystemicMinPRs <= 0 {
		t.SystemicMinPRs = d.SystemicMinPRs
	}
	return t
}

// Whose move a PR is (PRFacts.Move).
const (
	MoveAuthor   = "author"
	MoveReviewer = "reviewer"
	MoveNobody   = "nobody"
	MoveNone     = "none"
	MoveUnknown  = "unknown"
)

// CI summaries (PRFacts.CI).
const (
	CIGreen        = "green"
	CIFailing      = "failing"
	CISystemicOnly = "systemic_only"
	CIPending      = "pending"
	CINone         = "none"
	CIUnknown      = "unknown"
)

// Reason codes (Reason.Code on a PR). The first eight are the move rules in
// precedence order; the rest are informational and never decide the move.
const (
	ReasonClosed           = "closed"
	ReasonDraft            = "draft"
	ReasonChangesRequested = "changes_requested"
	ReasonConflicts        = "conflicts"
	ReasonCIFailing        = "ci_failing"
	ReasonApprovedUnmerged = "approved_unmerged"
	ReasonAwaitingReview   = "awaiting_review"
	ReasonNoReviewer       = "no_reviewer"
	ReasonCISystemicOnly   = "ci_systemic_only"
	ReasonCIPending        = "ci_pending"
	ReasonNoTicket         = "no_ticket"
	ReasonStale            = "stale"
	ReasonTeamOnly         = "team_only"
)

// Mismatch codes (Reason.Code on a ticket), in the order they are listed.
const (
	MismatchCodeMergedTicketOpen = "code_merged_ticket_open"
	MismatchTicketDoneCodeOpen   = "ticket_done_code_open"
	MismatchInProgressNoCode     = "in_progress_no_code"
	MismatchInProgressIdle       = "in_progress_idle"
	MismatchUnassignedInSprint   = "unassigned_in_sprint"
)

// Vocabularies of the facts, in display order, for the registry and
// validation.
var (
	moveValues     = []string{MoveAuthor, MoveReviewer, MoveNobody, MoveNone, MoveUnknown}
	ciValues       = []string{CIGreen, CIFailing, CISystemicOnly, CIPending, CINone, CIUnknown}
	sizeBuckets    = []string{"xs", "s", "m", "l", "xl"}
	linkedPRStates = []string{"draft", "open", "approved", "merged", "closed"}
	reasonCodes    = []string{ReasonClosed, ReasonDraft, ReasonChangesRequested, ReasonConflicts, ReasonCIFailing,
		ReasonApprovedUnmerged, ReasonAwaitingReview, ReasonNoReviewer,
		ReasonCISystemicOnly, ReasonCIPending, ReasonNoTicket, ReasonStale, ReasonTeamOnly}
	mismatchCodes = []string{MismatchCodeMergedTicketOpen, MismatchTicketDoneCodeOpen, MismatchInProgressNoCode,
		MismatchInProgressIdle, MismatchUnassignedInSprint}
)

// PRFacts are a PR's derived attention facts (PRInfo.Facts).
type PRFacts struct {
	// Move is MoveAuthor, MoveReviewer, MoveNobody, MoveNone or
	// MoveUnknown: the move of the first rule in Reasons, unless an earlier
	// rule is undecidable and would give another move.
	Move string `json:"move"`
	// Reasons lists every rule that holds: the move rules in precedence
	// order, then the informational ones.
	Reasons []Reason `json:"reasons"`
	// MovePeople holds the move: the author for MoveAuthor; for MoveReviewer
	// the outstanding reviewers, then each configured person asked through
	// a team. Empty for MoveNobody and MoveNone; omitted when unknown.
	MovePeople []string `json:"move_people,omitzero"`
	// WaitingSince is when the current wait began: the earliest active
	// review request for MoveReviewer, ready_at for MoveNobody, and the
	// blocking review for MoveAuthor by changes_requested. Omitted
	// otherwise, or when unknown.
	WaitingSince time.Time `json:"waiting_since,omitzero"`
	// WaitingLowerBound is set when a request of unknown time could have
	// started the wait earlier, so the wait is at least this long.
	WaitingLowerBound bool `json:"waiting_lower_bound,omitempty"`
	// WaitingOn lists the outstanding reviewers for MoveReviewer.
	WaitingOn []string `json:"waiting_on,omitzero"`
	// CI is CIGreen, CIFailing, CISystemicOnly, CIPending, CINone or
	// CIUnknown.
	CI string `json:"ci"`
	// Size is additions plus deletions and SizeBucket its bucket; both are
	// omitted when either count is unknown.
	Size       *int   `json:"size,omitempty"`
	SizeBucket string `json:"size_bucket,omitempty"`
	// Stale is set for an open PR not updated for [dash.facts] stale_days;
	// omitted when unknown.
	Stale *bool `json:"stale,omitempty"`
	// Uncertain names the unknown inputs some fact could not be decided
	// without, such as "mergeable" or "requested_at:bob".
	Uncertain []string `json:"uncertain,omitempty"`
}

// Reason is a rule that holds, with the evidence it was decided from.
type Reason struct {
	Code     string `json:"code"`
	Evidence string `json:"evidence"`
}

// TicketFacts are a ticket's derived facts (TicketInfo.Facts).
type TicketFacts struct {
	// Mismatches lists every way the ticket and its code disagree. It is
	// omitted when none holds but some could not be decided (Uncertain says
	// why), and for GitHub issues, which have no Jira status to compare.
	Mismatches []Reason `json:"mismatches,omitzero"`
	// LinkedPRs are the PRs with a ticket edge to the ticket.
	LinkedPRs []LinkedPR `json:"linked_prs"`
	Uncertain []string   `json:"uncertain,omitempty"`
}

// LinkedPR is a PR linked to a ticket.
type LinkedPR struct {
	ID  string `json:"id"`
	Ref string `json:"ref"`
	// State is draft, open, approved, merged or closed; omitted when the
	// PR's state is unknown.
	State string `json:"state,omitempty"`
}

// CheckStat is how one check fared on a repository's open PRs
// (Snapshot.CheckStats).
type CheckStat struct {
	Name string `json:"name"`
	// Runs counts the open PRs on which the check finished (success,
	// failure or neutral) and Failures those on which it failed; pending
	// and skipped runs count in neither.
	Runs     int  `json:"runs"`
	Failures int  `json:"failures"`
	Systemic bool `json:"systemic"`
	// Unlisted counts open PRs whose check list was truncated without
	// showing this check: it may have run there, so they count in neither.
	Unlisted int    `json:"unlisted,omitempty"`
	Evidence string `json:"evidence"`
}

// Facts are the derived facts of one snapshot, by node ID.
type Facts struct {
	PRs     map[string]*PRFacts
	Tickets map[string]*TicketFacts
	// CheckStats is keyed by lowercased owner/repo.
	CheckStats map[string][]CheckStat
}

// ComputeFacts derives the attention facts of snapshot s: CheckStats first,
// since a PR's CI facts depend on which checks fail everywhere, then PR and
// ticket facts. people are the followed people (a team request reaches
// their involvement), th the thresholds and now the snapshot's
// generated_at. It reads only s: it does no I/O, never reads the facts s
// may already carry, and gives the same facts for the same input.
func ComputeFacts(s *Snapshot, people []Person, th FactThresholds, now time.Time) Facts {
	th = th.withDefaults()
	g := newFactGraph(s)
	out := Facts{PRs: map[string]*PRFacts{}, Tickets: map[string]*TicketFacts{}, CheckStats: checkStats(s, th)}
	stats := map[string]map[string]CheckStat{}
	for repo, list := range out.CheckStats {
		stats[repo] = map[string]CheckStat{}
		for _, st := range list {
			stats[repo][st.Name] = st
		}
	}
	for i := range s.Nodes {
		n := &s.Nodes[i]
		switch {
		case n.PR != nil:
			out.PRs[n.ID] = prFacts(n.PR, len(g.prTickets[n.ID]) > 0, stats[strings.ToLower(n.PR.Repo)], people, th, now)
		case n.Ticket != nil:
			out.Tickets[n.ID] = ticketFacts(n.ID, n.Ticket, g, th, now)
		}
	}
	if len(out.CheckStats) == 0 {
		out.CheckStats = nil
	}
	return out
}

// apply stores f on s's nodes, replacing any facts they carried.
func (f Facts) apply(s *Snapshot) {
	for i := range s.Nodes {
		n := &s.Nodes[i]
		if n.PR != nil {
			n.PR.Facts = f.PRs[n.ID]
		}
		if n.Ticket != nil {
			n.Ticket.Facts = f.Tickets[n.ID]
		}
	}
	s.CheckStats = f.CheckStats
}

// The facts use query.go's three-valued tri: a rule holds (triTrue), does
// not (triFalse), or cannot be decided (triUnknown).

func triOf(b bool) tri {
	if b {
		return triTrue
	}
	return triFalse
}

// and3 is three-valued AND: false wins, then unknown.
func and3(ts ...tri) tri {
	out := triTrue
	for _, t := range ts {
		if t == triFalse {
			return triFalse
		}
		if t == triUnknown {
			out = triUnknown
		}
	}
	return out
}

func not3(t tri) tri {
	switch t {
	case triTrue:
		return triFalse
	case triFalse:
		return triTrue
	}
	return triUnknown
}

// names is an ordered set of uncertain input names.
type names []string

func (n *names) add(s ...string) {
	for _, x := range s {
		if !slices.Contains(*n, x) {
			*n = append(*n, x)
		}
	}
}

// factGraph indexes the edges the facts follow.
type factGraph struct {
	byID map[string]*Node
	// prTickets lists the tickets each PR links to; ticketSrc the PRs and
	// bookmarks with a ticket edge to each ticket.
	prTickets map[string][]string
	ticketSrc map[string][]string
	// bmWorkspaces lists the workspaces on each bookmark, prBookmarks the
	// bookmarks each PR was found for, and inSprint the sprints of each
	// ticket.
	bmWorkspaces map[string][]string
	prBookmarks  map[string][]string
	inSprint     map[string][]string
}

func newFactGraph(s *Snapshot) *factGraph {
	g := &factGraph{byID: map[string]*Node{}, prTickets: map[string][]string{}, ticketSrc: map[string][]string{},
		bmWorkspaces: map[string][]string{}, prBookmarks: map[string][]string{}, inSprint: map[string][]string{}}
	for i := range s.Nodes {
		g.byID[s.Nodes[i].ID] = &s.Nodes[i]
	}
	for _, e := range s.Edges {
		switch e.Kind {
		case EdgeTicket:
			g.ticketSrc[e.Target] = append(g.ticketSrc[e.Target], e.Source)
			if n := g.byID[e.Source]; n != nil && n.PR != nil {
				g.prTickets[e.Source] = append(g.prTickets[e.Source], e.Target)
			}
		case EdgeOn:
			g.bmWorkspaces[e.Target] = append(g.bmWorkspaces[e.Target], e.Source)
		case EdgePR:
			g.prBookmarks[e.Target] = append(g.prBookmarks[e.Target], e.Source)
		case EdgeInSprint:
			g.inSprint[e.Source] = append(g.inSprint[e.Source], e.Target)
		}
	}
	return g
}

// ---------- CI ----------

// checkStates reduces a PR's check list to one state per check name, in
// first-seen order: failure if any run of that name failed, else pending if
// any is pending, else success if any succeeded or was neutral, else
// skipped.
func checkStates(details []CheckDetail) ([]string, map[string]string) {
	var order []string
	states := map[string]string{}
	rank := map[string]int{CheckFailure: 4, CheckPending: 3, CheckSuccess: 2, CheckNeutral: 2, CheckSkipped: 1}
	for _, c := range details {
		st := c.State
		if st == CheckNeutral {
			st = CheckSuccess
		}
		prev, seen := states[c.Name]
		if !seen {
			order = append(order, c.Name)
		}
		if !seen || rank[st] > rank[prev] {
			states[c.Name] = st
		}
	}
	return order, states
}

// checkStats counts, per repository, how each check fared on the open PRs
// that report a check list, and decides which fail everywhere (gh-99
// requirement 12). A PR whose list was truncated counts for the checks it
// lists; for any other check it is counted as Unlisted, never as a PR the
// check did not run on.
func checkStats(s *Snapshot, th FactThresholds) map[string][]CheckStat {
	acc := map[string]map[string]*CheckStat{}
	truncated := map[string][]map[string]string{}
	for i := range s.Nodes {
		p := s.Nodes[i].PR
		if p == nil || p.State != "open" || p.CheckDetails == nil {
			continue
		}
		repo := strings.ToLower(p.Repo)
		if acc[repo] == nil {
			acc[repo] = map[string]*CheckStat{}
		}
		order, states := checkStates(p.CheckDetails)
		for _, name := range order {
			st := acc[repo][name]
			if st == nil {
				st = &CheckStat{Name: name}
				acc[repo][name] = st
			}
			switch states[name] {
			case CheckFailure:
				st.Runs++
				st.Failures++
			case CheckSuccess:
				st.Runs++
			}
		}
		if p.ChecksTruncated {
			truncated[repo] = append(truncated[repo], states)
		}
	}
	out := map[string][]CheckStat{}
	for repo, byName := range acc {
		list := make([]CheckStat, 0, len(byName))
		for _, st := range byName {
			for _, states := range truncated[repo] {
				if _, listed := states[st.Name]; !listed {
					st.Unlisted++
				}
			}
			st.Systemic = st.Runs >= th.SystemicMinPRs && float64(st.Failures)/float64(st.Runs) >= th.SystemicRatio
			st.Evidence = checkStatEvidence(*st, repo)
			list = append(list, *st)
		}
		slices.SortFunc(list, func(a, b CheckStat) int { return strings.Compare(a.Name, b.Name) })
		out[repo] = list
	}
	return out
}

func checkStatEvidence(st CheckStat, repo string) string {
	var s string
	if st.Runs == 0 {
		s = "has not finished on any open PR by the configured people in " + repo
	} else {
		s = fmt.Sprintf("fails on %d of %d open %s by the configured people in %s", st.Failures, st.Runs, plural(st.Runs, "PR", "PRs"), repo)
	}
	switch {
	case st.Unlisted == 1:
		s += "; 1 more open PR lists too many checks to show it"
	case st.Unlisted > 1:
		s += fmt.Sprintf("; %d more open PRs list too many checks to show it", st.Unlisted)
	}
	return s
}

// ciEval is a PR's CI, read against its repository's check stats.
type ciEval struct {
	ci string
	// failing is rule 5: a check fails that is not systemic.
	failing tri
	// own are the failing checks that are not systemic, systemic those
	// that are, and pending the pending ones.
	own, systemic, pending []string
	// pendingAny is set when some check is pending, named or not.
	pendingAny bool
	// unc names the input that left the CI unknown.
	unc string
}

// evalCI reads p's CI. Without a check list, or with an empty one, it falls
// back on the rollup: success, pending and none say there is no failure,
// but a failure cannot be told systemic or not. A truncated list can hide
// failures beyond it unless the rollup is success or pending, which GitHub
// reports only when nothing failed.
func evalCI(p *PRInfo, stats map[string]CheckStat) ciEval {
	var e ciEval
	if p.CheckDetails == nil {
		switch p.Checks {
		case "success":
			e.ci = CIGreen
		case "pending":
			e.ci, e.pendingAny = CIPending, true
		case "none":
			e.ci = CINone
		case "failure":
			e.ci, e.failing, e.unc = CIUnknown, triUnknown, "check_details"
		default:
			e.ci, e.failing, e.unc = CIUnknown, triUnknown, "checks"
		}
		return e
	}
	order, states := checkStates(p.CheckDetails)
	for _, name := range order {
		switch states[name] {
		case CheckFailure:
			if stats[name].Systemic {
				e.systemic = append(e.systemic, name)
			} else {
				e.own = append(e.own, name)
			}
		case CheckPending:
			e.pending = append(e.pending, name)
		}
	}
	e.pendingAny = len(e.pending) > 0 || (p.ChecksTruncated && p.Checks == "pending")
	hidden := p.ChecksTruncated && p.Checks != "success" && p.Checks != "pending"
	switch {
	case len(e.own) > 0:
		e.ci, e.failing = CIFailing, triTrue
	case hidden:
		e.ci, e.failing, e.unc = CIUnknown, triUnknown, "check_details"
	case len(e.systemic) > 0:
		e.ci = CISystemicOnly
	case e.pendingAny:
		e.ci = CIPending
	case len(order) == 0 && !p.ChecksTruncated:
		switch p.Checks {
		case "failure":
			e.ci, e.failing, e.unc = CIUnknown, triUnknown, "check_details"
		case "pending":
			e.ci, e.pendingAny = CIPending, true
		case "success":
			e.ci = CIGreen
		default:
			e.ci = CINone
		}
	default:
		e.ci = CIGreen
	}
	return e
}

// ---------- PR facts ----------

// reviewRequired is GitHub's review decision for a PR that needs a review.
const reviewRequired = "REVIEW_REQUIRED"

// moveRule is one move rule's outcome.
type moveRule struct {
	code, move string
	holds      tri
	evidence   string
}

// outstanding returns p's outstanding review requests, and whether they are
// known: review_requests when reported, else requested_reviewers with no
// request times.
func outstanding(p *PRInfo) ([]ReviewRequest, bool) {
	if !p.ReviewersKnown {
		return nil, false
	}
	if p.ReviewRequests != nil {
		return p.ReviewRequests, true
	}
	out := []ReviewRequest{}
	for _, r := range p.RequestedReviewers {
		kind := ReviewerUser
		if strings.HasPrefix(r, teamPrefix) {
			kind = ReviewerTeam
		}
		out = append(out, ReviewRequest{Reviewer: r, Kind: kind})
	}
	return out, true
}

func prFacts(p *PRInfo, ticketEdge bool, stats map[string]CheckStat, people []Person, th FactThresholds, now time.Time) *PRFacts {
	f := &PRFacts{Reasons: []Reason{}}
	var unc names

	// Size.
	if p.Additions != nil && p.Deletions != nil {
		size := *p.Additions + *p.Deletions
		f.Size, f.SizeBucket = &size, sizeBucket(size)
	} else {
		if p.Additions == nil {
			unc.add("additions")
		}
		if p.Deletions == nil {
			unc.add("deletions")
		}
	}

	ci := evalCI(p, stats)
	f.CI = ci.ci
	if ci.unc != "" {
		unc.add(ci.unc)
	}

	// no_ticket: a ticket key or a ticket edge says there is one.
	noTicket := triFalse
	switch {
	case len(p.TicketKeys) > 0 || ticketEdge:
	case p.TicketKeys != nil:
		noTicket = triTrue
	default:
		noTicket = triUnknown
		unc.add("ticket_keys")
	}

	var info []Reason
	addInfo := func(holds tri, code, evidence string) {
		if holds == triTrue {
			info = append(info, Reason{Code: code, Evidence: evidence})
		}
	}

	switch p.State {
	case "merged", "closed":
		f.Move, f.MovePeople = MoveNone, []string{}
		f.Reasons = append(f.Reasons, Reason{Code: ReasonClosed, Evidence: closedEvidence(p, now)})
		stale := false
		f.Stale = &stale
		addInfo(noTicket, ReasonNoTicket, noTicketEvidence)
		f.Reasons = append(f.Reasons, info...)
		f.Uncertain = unc
		return f
	case "open":
	default:
		f.Move = MoveUnknown
		unc.add("state")
		addInfo(noTicket, ReasonNoTicket, noTicketEvidence)
		f.Reasons = append(f.Reasons, info...)
		f.Uncertain = unc
		return f
	}

	out, outKnown := outstanding(p)
	if !outKnown {
		unc.add("requested_reviewers")
	}
	var users, teams []ReviewRequest
	for _, r := range out {
		if r.Kind == ReviewerTeam {
			teams = append(teams, r)
		} else {
			users = append(users, r)
		}
	}
	// Configured people asked through a team: their review-requested set
	// holds the PR while no direct request names them.
	var viaTeam []string
	for _, person := range people {
		if !slices.Contains(p.Involvement, Involvement{Person: person.Login, Role: RoleReviewRequested}) {
			continue
		}
		if !slices.ContainsFunc(users, func(r ReviewRequest) bool { return strings.EqualFold(r.Reviewer, person.Login) }) {
			viaTeam = append(viaTeam, person.Login)
		}
	}

	blocking, r3 := changesRequested(p, users, outKnown, &unc)

	r4 := triFalse
	switch p.Mergeable {
	case MergeConflicting:
		r4 = triTrue
	case MergeMergeable:
	default:
		// GitHub computes mergeability lazily: unknown does not stop the
		// rules (gh-99 requirement 5), it is only recorded.
		unc.add("mergeable")
	}

	r6 := triFalse
	switch {
	case p.ReviewDecision == ReviewApproved:
		r6 = triTrue
	case p.ReviewDecision == "" && p.Reviews == nil:
		// A bookmark's PR lookup leaves the decision empty both when there
		// is none and when it could not be read.
		r6 = triUnknown
		unc.add("review_decision")
	}
	r7, r8 := triUnknown, triUnknown
	if outKnown {
		r7 = triOf(len(out) > 0)
		r8 = and3(not3(r7), not3(r6))
	}

	rules := []moveRule{
		{ReasonDraft, MoveAuthor, triOf(p.IsDraft), draftEvidence(p, now)},
		{ReasonChangesRequested, MoveAuthor, r3, changesRequestedEvidence(blocking, now)},
		{ReasonConflicts, MoveAuthor, r4, conflictsEvidence(p)},
		{ReasonCIFailing, MoveAuthor, ci.failing, ciFailingEvidence(ci, stats, p.Repo)},
		{ReasonApprovedUnmerged, MoveAuthor, r6, approvedEvidence(p, now)},
		{ReasonAwaitingReview, MoveReviewer, r7, awaitingEvidence(p, out, viaTeam, len(users) == 0, now)},
		{ReasonNoReviewer, MoveNobody, r8, noReviewerEvidence(p, now)},
	}
	f.Move = MoveUnknown
	decided := ""
	var undecided []string
	for _, r := range rules {
		if r.holds == triUnknown {
			undecided = append(undecided, r.move)
		}
		if r.holds != triTrue {
			continue
		}
		if decided == "" {
			decided = r.code
			f.Move = r.move
			// An earlier rule that could not be decided might have held
			// and given another move.
			for _, m := range undecided {
				if m != r.move {
					f.Move = MoveUnknown
				}
			}
		}
		f.Reasons = append(f.Reasons, Reason{Code: r.code, Evidence: r.evidence})
	}

	switch f.Move {
	case MoveAuthor:
		if p.Author != "" {
			f.MovePeople = []string{p.Author}
		} else {
			unc.add("author")
		}
	case MoveReviewer:
		f.MovePeople, f.WaitingOn = []string{}, []string{}
		for _, r := range out {
			f.MovePeople = append(f.MovePeople, r.Reviewer)
			f.WaitingOn = append(f.WaitingOn, r.Reviewer)
		}
		for _, login := range viaTeam {
			if !slices.ContainsFunc(f.MovePeople, func(m string) bool { return strings.EqualFold(m, login) }) {
				f.MovePeople = append(f.MovePeople, login)
			}
		}
		f.WaitingSince, f.WaitingLowerBound = reviewerWait(p, out, &unc)
	case MoveNobody:
		f.MovePeople = []string{}
		if p.ReadyAt.IsZero() {
			unc.add("ready_at")
		} else {
			f.WaitingSince = p.ReadyAt
		}
	}
	if f.Move == MoveAuthor && decided == ReasonChangesRequested {
		for _, r := range blocking {
			switch {
			case r.SubmittedAt.IsZero():
				unc.add("submitted_at:" + r.Author)
			case f.WaitingSince.IsZero() || r.SubmittedAt.Before(f.WaitingSince):
				f.WaitingSince = r.SubmittedAt
			}
		}
	}

	stale := triUnknown
	if p.UpdatedAt.IsZero() {
		unc.add("updated_at")
	} else {
		s := !now.Before(p.UpdatedAt.Add(time.Duration(th.StaleDays) * 24 * time.Hour))
		f.Stale = &s
		stale = triOf(s)
	}

	addInfo(triOf(len(ci.systemic) > 0 && ci.ci == CISystemicOnly), ReasonCISystemicOnly, systemicEvidence(ci, stats, p.Repo))
	addInfo(triOf(ci.pendingAny), ReasonCIPending, pendingEvidence(ci))
	addInfo(noTicket, ReasonNoTicket, noTicketEvidence)
	addInfo(stale, ReasonStale, staleEvidence(p, th, now))
	addInfo(triOf(r7 == triTrue && len(users) == 0), ReasonTeamOnly, "every outstanding request is a team: "+joinNames(reviewerNames(teams), 0))
	f.Reasons = append(f.Reasons, info...)
	f.Uncertain = unc
	return f
}

// changesRequested decides rule 3: some reviewer's latest review requests
// changes and they have not been re-requested since. GitHub drops a request
// once its reviewer reviews, so an outstanding direct request means a
// re-request, unless its time says it came before the review. Without the
// reviews, a review decision other than CHANGES_REQUESTED still rules it
// out: GitHub keeps that decision while any such review stands.
func changesRequested(p *PRInfo, users []ReviewRequest, outKnown bool, unc *names) ([]Review, tri) {
	if p.Reviews == nil {
		if p.ReviewDecision == ReviewApproved || p.ReviewDecision == reviewRequired {
			return nil, triFalse
		}
		unc.add("reviews")
		return nil, triUnknown
	}
	var blocking []Review
	undecided := false
	for _, r := range p.Reviews {
		if r.State != ReviewChangesRequested {
			continue
		}
		if !outKnown {
			undecided = true
			continue
		}
		i := slices.IndexFunc(users, func(q ReviewRequest) bool { return strings.EqualFold(q.Reviewer, r.Author) })
		if i >= 0 && !(!users[i].RequestedAt.IsZero() && !r.SubmittedAt.IsZero() && users[i].RequestedAt.Before(r.SubmittedAt)) {
			continue // re-requested since
		}
		blocking = append(blocking, r)
	}
	switch {
	case len(blocking) > 0:
		return blocking, triTrue
	case undecided:
		return nil, triUnknown
	}
	return nil, triFalse
}

// reviewerWait is the earliest active wait among the outstanding requests,
// each starting at the later of ready_at and its request time (gh-99
// requirement 7). A request of unknown time is named in unc; the result is
// then a lower bound unless the wait already starts at ready_at, before
// which no request can start.
func reviewerWait(p *PRInfo, out []ReviewRequest, unc *names) (time.Time, bool) {
	if p.ReadyAt.IsZero() {
		unc.add("ready_at")
		return time.Time{}, false
	}
	var since time.Time
	unknown := false
	for _, r := range out {
		if r.RequestedAt.IsZero() {
			unc.add("requested_at:" + r.Reviewer)
			unknown = true
			continue
		}
		start := r.RequestedAt
		if p.ReadyAt.After(start) {
			start = p.ReadyAt
		}
		if since.IsZero() || start.Before(since) {
			since = start
		}
	}
	return since, unknown && !since.IsZero() && since.After(p.ReadyAt)
}

// sizeBucket buckets additions plus deletions (gh-99 requirement 10).
func sizeBucket(size int) string {
	switch {
	case size < 10:
		return "xs"
	case size < 100:
		return "s"
	case size < 500:
		return "m"
	case size < 2000:
		return "l"
	}
	return "xl"
}

// ---------- ticket facts ----------

func ticketFacts(id string, t *TicketInfo, g *factGraph, th FactThresholds, now time.Time) *TicketFacts {
	f := &TicketFacts{LinkedPRs: []LinkedPR{}}
	var prs []*Node
	workspaces := map[string]bool{}
	for _, src := range g.ticketSrc[id] {
		n := g.byID[src]
		switch {
		case n == nil:
		case n.PR != nil:
			prs = append(prs, n)
			for _, bm := range g.prBookmarks[n.ID] {
				for _, ws := range g.bmWorkspaces[bm] {
					workspaces[ws] = true
				}
			}
		case n.Bookmark != nil:
			for _, ws := range g.bmWorkspaces[n.ID] {
				workspaces[ws] = true
			}
		}
	}
	slices.SortFunc(prs, func(a, b *Node) int {
		if c := strings.Compare(strings.ToLower(a.PR.Repo), strings.ToLower(b.PR.Repo)); c != 0 {
			return c
		}
		return a.PR.Number - b.PR.Number
	})
	for _, n := range prs {
		f.LinkedPRs = append(f.LinkedPRs, LinkedPR{ID: n.ID, Ref: n.Label, State: linkedState(n.PR)})
	}
	if t.System != "jira" {
		return f
	}

	status := t.Status
	if status == "" {
		status = strings.ReplaceAll(t.StatusCategory, "_", " ")
	}
	category := func(c string) operand {
		if t.StatusCategory == "" {
			return operand{triUnknown, "status_category"}
		}
		return operand{triOf(t.StatusCategory == c), ""}
	}

	// Linked PRs' states.
	allClosed, anyMerged, anyOpen := operand{t: triTrue}, operand{t: triFalse}, operand{t: triFalse}
	var closed, open []string
	for i, n := range prs {
		switch st := f.LinkedPRs[i].State; st {
		case "merged":
			anyMerged.t = triTrue
			closed = append(closed, n.Label+" merged"+onDay(n.PR.MergedAt, now))
		case "closed":
			closed = append(closed, n.Label+" closed"+onDay(n.PR.ClosedAt, now))
		case "draft", "open", "approved":
			allClosed.t, anyOpen.t = triFalse, triTrue
			open = append(open, n.Label+" "+st)
		default:
			// A PR of unknown state may be open, merged or closed.
			unknown := operand{triUnknown, "state:" + n.Label}
			if allClosed.t != triFalse {
				allClosed = unknown
			}
			if anyMerged.t != triTrue {
				anyMerged = unknown
			}
			if anyOpen.t != triTrue {
				anyOpen = unknown
			}
		}
	}

	// An active sprint, from the ticket's sprints or an in_sprint edge.
	active, sprintName := operand{t: triFalse}, ""
	for _, r := range t.Sprints {
		if r.State == SprintActive {
			active.t, sprintName = triTrue, cmpOr(r.Name, "sprint "+strconv.Itoa(r.ID))
			break
		}
	}
	for _, sid := range g.inSprint[id] {
		if n := g.byID[sid]; active.t == triFalse && n != nil && n.Sprint != nil && n.Sprint.State == SprintActive {
			active.t, sprintName = triTrue, cmpOr(n.Sprint.Name, n.Label)
		}
	}
	if active.t == triFalse && t.Sprints == nil {
		active = operand{triUnknown, "sprints"}
	}

	hasCode := operand{t: triOf(len(prs) > 0 || len(workspaces) > 0)}

	// Quiet: no activity on the linked PRs and workspaces for stale_days.
	var latest time.Time
	latestOn, unknownActivity := "", false
	note := func(at time.Time, on string) {
		switch {
		case at.IsZero():
			unknownActivity = true
		case at.After(latest):
			latest, latestOn = at, on
		}
	}
	for _, n := range prs {
		note(n.PR.UpdatedAt, n.Label)
	}
	for _, ws := range slices.Sorted(maps.Keys(workspaces)) {
		if n := g.byID[ws]; n != nil && n.Workspace != nil {
			note(n.Workspace.LastActivity, "workspace "+n.Label)
		}
	}
	quiet := operand{t: triTrue}
	switch {
	case !latest.IsZero() && latest.After(now.Add(-time.Duration(th.StaleDays)*24*time.Hour)):
		quiet.t = triFalse
	case unknownActivity:
		quiet = operand{triUnknown, "activity"}
	}

	// Unassigned: an empty assignee is known once Jira data was read.
	unassigned := operand{t: triFalse}
	if t.Assignee == "" && t.AssigneeID == "" {
		unassigned.t = triTrue
		if !t.Freshness.hasData() {
			unassigned = operand{triUnknown, "assignee"}
		}
	}

	var unc names
	codes := []struct {
		code     string
		holds    tri
		evidence func() string
	}{
		{MismatchCodeMergedTicketOpen, decide(&unc, operand{t: triOf(len(prs) > 0)}, allClosed, anyMerged, category(StatusDone).not()), func() string {
			return plural(len(closed), "PR ", "PRs ") + joinNames(closed, 3) + "; ticket " + status
		}},
		{MismatchTicketDoneCodeOpen, decide(&unc, anyOpen, category(StatusDone)), func() string {
			return "ticket " + status + "; " + plural(len(open), "PR ", "PRs ") + joinNames(open, 3)
		}},
		{MismatchInProgressNoCode, decide(&unc, hasCode.not(), category(StatusInProgress), active), func() string {
			return "ticket " + status + " in " + sprintName + "; no linked PR or workspace in the snapshot"
		}},
		{MismatchInProgressIdle, decide(&unc, hasCode, category(StatusInProgress), quiet), func() string {
			return fmt.Sprintf("ticket %s; last activity on %s %s (idle after %d days)", status, latestOn, evidenceDay(latest, now), th.StaleDays)
		}},
		{MismatchUnassignedInSprint, decide(&unc, active, unassigned), func() string {
			return "in " + sprintName + " with no assignee"
		}},
	}
	f.Mismatches = []Reason{}
	undecided := false
	for _, c := range codes {
		switch c.holds {
		case triTrue:
			f.Mismatches = append(f.Mismatches, Reason{Code: c.code, Evidence: c.evidence()})
		case triUnknown:
			undecided = true
		}
	}
	if len(f.Mismatches) == 0 && undecided {
		f.Mismatches = nil
	}
	f.Uncertain = unc
	return f
}

// operand is one condition of a mismatch, and the input that left it
// unknown.
type operand struct {
	t       tri
	unknown string
}

func (o operand) not() operand { return operand{not3(o.t), o.unknown} }

// decide is the three-valued AND of ops. Only when the result is unknown
// are the unknown operands' inputs added to unc: an input that cannot
// change the outcome is not uncertain.
func decide(unc *names, ops ...operand) tri {
	r := triTrue
	for _, o := range ops {
		r = and3(r, o.t)
	}
	if r == triUnknown {
		for _, o := range ops {
			if o.t == triUnknown {
				unc.add(o.unknown)
			}
		}
	}
	return r
}

// linkedState is a linked PR's state: draft, open or approved while open,
// else merged or closed; "" (omitted) when unknown.
func linkedState(p *PRInfo) string {
	switch p.State {
	case "merged", "closed":
		return p.State
	case "open":
		switch {
		case p.IsDraft:
			return "draft"
		case p.ReviewDecision == ReviewApproved:
			return "approved"
		}
		return "open"
	}
	return ""
}

// ---------- validation ----------

// checkFacts checks the derived facts' closed vocabularies and references:
// every reason and mismatch has a known code and evidence, linked PRs are
// PR nodes, and check stats are keyed by lowercased repository.
func (v *validator) checkFacts(s *Snapshot, isKind func(string, ...NodeKind) bool) {
	reasons := func(id, what string, rs []Reason, allowed []string) {
		for _, r := range rs {
			v.enum(id, what, r.Code, allowed...)
			if r.Code == "" || strings.TrimSpace(r.Evidence) == "" {
				v.addf("facts: %q has a %s %q without evidence", id, what, r.Code)
			}
		}
	}
	for _, n := range s.Nodes {
		if n.PR != nil && n.PR.Facts != nil {
			f := n.PR.Facts
			v.enum(n.ID, "move", f.Move, moveValues...)
			v.enum(n.ID, "ci", f.CI, ciValues...)
			v.enum(n.ID, "size_bucket", f.SizeBucket, sizeBuckets...)
			reasons(n.ID, "reason", f.Reasons, reasonCodes)
		}
		if n.Ticket != nil && n.Ticket.Facts != nil {
			reasons(n.ID, "mismatch", n.Ticket.Facts.Mismatches, mismatchCodes)
			for _, l := range n.Ticket.Facts.LinkedPRs {
				if !isKind(l.ID, KindPR) {
					v.addf("facts: %q links PR %q, which is not a PR node", n.ID, l.ID)
				}
				v.enum(n.ID, "linked PR state", l.State, linkedPRStates...)
			}
		}
	}
	for _, repo := range sortedKeys(s.CheckStats) {
		if repo != strings.ToLower(repo) {
			v.addf("check_stats: key %q is not lowercase", repo)
		}
		for _, st := range s.CheckStats[repo] {
			if st.Name == "" || st.Failures < 0 || st.Failures > st.Runs || st.Unlisted < 0 {
				v.addf("check_stats: %s has check %q with %d failures in %d runs", repo, st.Name, st.Failures, st.Runs)
			}
		}
	}
}

// ---------- evidence ----------

const noTicketEvidence = "no ticket key in the branch, title or body"

// evidenceDay is t's calendar day in now's zone ("Oct 8"), with the year
// when it differs from now's.
func evidenceDay(t, now time.Time) string {
	t = t.In(now.Location())
	if t.Year() == now.Year() {
		return t.Format("Jan 2")
	}
	return t.Format("Jan 2, 2006")
}

// onDay is " Oct 8" for a known time t, else "".
func onDay(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	return " " + evidenceDay(t, now)
}

// joinNames joins items as "a, b and c"; with max > 0, items beyond max
// become "+n more".
func joinNames(items []string, max int) string {
	if max > 0 && len(items) > max {
		return strings.Join(items[:max], ", ") + " and " + strconv.Itoa(len(items)-max) + " more"
	}
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func cmpOr(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

func reviewerNames(rs []ReviewRequest) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Reviewer
	}
	return out
}

func closedEvidence(p *PRInfo, now time.Time) string {
	if p.State == "merged" {
		return "merged" + onDay(p.MergedAt, now)
	}
	return "closed" + onDay(p.ClosedAt, now)
}

func draftEvidence(p *PRInfo, now time.Time) string {
	if p.CreatedAt.IsZero() {
		return "draft"
	}
	return "draft, opened " + evidenceDay(p.CreatedAt, now)
}

func changesRequestedEvidence(blocking []Review, now time.Time) string {
	var who []string
	for _, r := range blocking {
		who = append(who, r.Author+onDay(r.SubmittedAt, now))
	}
	return "changes requested by " + joinNames(who, 3) + "; not re-requested since"
}

func conflictsEvidence(p *PRInfo) string {
	if p.BaseRef != "" {
		return "merge conflicts with " + p.BaseRef
	}
	return "merge conflicts"
}

func ciFailingEvidence(ci ciEval, stats map[string]CheckStat, repo string) string {
	s := checkList(ci.own, "fails", "fail")
	if len(ci.systemic) > 0 {
		s += "; " + joinNames(ci.systemic, 0) + plural(len(ci.systemic), " fails", " fail") + " too, as on " +
			systemicCounts(ci.systemic, stats) + " open PRs in " + strings.ToLower(repo)
	}
	return s
}

// systemicCounts is "5 of 6" for each check, as check_stats counts it,
// joined like the names: "4 of 6 and 5 of 6".
func systemicCounts(checks []string, stats map[string]CheckStat) string {
	var parts []string
	for _, name := range checks {
		st := stats[name]
		parts = append(parts, fmt.Sprintf("%d of %d", st.Failures, st.Runs))
	}
	return joinNames(parts, 0)
}

// checkList is "unit-tests fails" or "2 checks fail: a and b".
func checkList(checks []string, one, many string) string {
	if len(checks) == 1 {
		return checks[0] + " " + one
	}
	return fmt.Sprintf("%d checks %s: %s", len(checks), many, joinNames(checks, 3))
}

func systemicEvidence(ci ciEval, stats map[string]CheckStat, repo string) string {
	if len(ci.systemic) == 1 {
		return "only " + ci.systemic[0] + " fails; it " + stats[ci.systemic[0]].Evidence
	}
	return "only " + joinNames(ci.systemic, 0) + " fail; they fail on " + systemicCounts(ci.systemic, stats) +
		" open PRs by the configured people in " + strings.ToLower(repo)
}

func pendingEvidence(ci ciEval) string {
	if len(ci.pending) == 0 {
		return "CI pending"
	}
	return checkList(ci.pending, "is pending", "are pending")
}

func approvedEvidence(p *PRInfo, now time.Time) string {
	var who []string
	for _, r := range p.Reviews {
		if r.State == ReviewApproved {
			who = append(who, r.Author+onDay(r.SubmittedAt, now))
		}
	}
	if len(who) == 0 {
		return "review decision approved; not merged"
	}
	return "approved by " + joinNames(who, 3) + "; not merged"
}

func readySince(p *PRInfo, now time.Time) string {
	if p.ReadyAt.IsZero() {
		return ""
	}
	return "ready since " + evidenceDay(p.ReadyAt, now) + "; "
}

func awaitingEvidence(p *PRInfo, out []ReviewRequest, viaTeam []string, teamOnly bool, now time.Time) string {
	var who []string
	for _, r := range out {
		who = append(who, r.Reviewer+onDay(r.RequestedAt, now))
	}
	s := readySince(p, now) + "review requested from " + joinNames(who, 0)
	if teamOnly {
		s += " only"
	}
	if len(viaTeam) > 0 {
		s += "; " + joinNames(viaTeam, 0) + " through a team"
	}
	return s
}

func noReviewerEvidence(p *PRInfo, now time.Time) string {
	return readySince(p, now) + "no review requested and no approval"
}

func staleEvidence(p *PRInfo, th FactThresholds, now time.Time) string {
	return fmt.Sprintf("not updated since %s (stale after %d days)", evidenceDay(p.UpdatedAt, now), th.StaleDays)
}
