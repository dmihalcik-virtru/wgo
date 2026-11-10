package dash

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// This file is the sprint view's Markdown summary (spec/gh-105.md,
// requirement 7): `wgo dash --md --view sprint [--sprint ID] [--query EXPR]`
// and GET /api/extract?view=sprint&sprint=ID&q=EXPR. The page's sprint view
// (internal/review/web/live-sprint.js) lists the same active sprints, picks
// the same default, counts points and days, and explains a missing board by
// the same rules; keep the two in step.

// sprintCategories are the status categories in the order the sprint view
// shows them: done first, so the done share of the points bar lines up with
// the elapsed-time bar above it, then what is under way and what is left.
var sprintCategories = []struct{ key, label string }{
	{StatusDone, "Done"}, {StatusInProgress, "In Progress"}, {StatusToDo, "To Do"},
}

// sprintSlice is one active sprint and its tickets.
type sprintSlice struct {
	node *Node
	// tickets have an in_sprint edge to a sprint node with this ID, in
	// snapshot order, each once.
	tickets []*Node
	// people counts the tickets assigned to a followed person, and
	// unknownPeople those of which that cannot be told (forPeople).
	people, unknownPeople int
}

func (a *sprintSlice) id() int { return a.node.Sprint.ID }

// sprintName is a sprint's name, or "Sprint <id>" when Jira gave none.
func sprintName(sp *SprintInfo) string { return cmpOr(sp.Name, "Sprint "+strconv.Itoa(sp.ID)) }

// activeSprints lists s's active sprints by start, then ID; a sprint ID that
// several nodes carry is listed once, with every ticket of each.
func activeSprints(s *Snapshot) []*sprintSlice {
	byID := map[int]*sprintSlice{}
	sprintOf := map[string]int{} // sprint node ID -> sprint ID, active ones
	var out []*sprintSlice
	for i := range s.Nodes {
		n := &s.Nodes[i]
		if n.Kind != KindSprint || n.Sprint == nil || n.Sprint.State != SprintActive {
			continue
		}
		sprintOf[n.ID] = n.Sprint.ID
		if byID[n.Sprint.ID] == nil {
			a := &sprintSlice{node: n}
			byID[n.Sprint.ID] = a
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil
	}
	members := map[int]map[string]bool{}
	for _, e := range s.Edges {
		if sid, ok := sprintOf[e.Target]; ok && e.Kind == EdgeInSprint {
			if members[sid] == nil {
				members[sid] = map[string]bool{}
			}
			members[sid][e.Source] = true
		}
	}
	for i := range s.Nodes {
		n := &s.Nodes[i]
		if n.Ticket == nil {
			continue
		}
		for _, a := range out {
			if members[a.id()][n.ID] {
				a.tickets = append(a.tickets, n)
				switch forPeople(n.Ticket, a.node.Sprint, s.People) {
				case triTrue:
					a.people++
				case triUnknown:
					a.unknownPeople++
				}
			}
		}
	}
	slices.SortStableFunc(out, func(a, b *sprintSlice) int {
		return cmp.Or(a.node.Sprint.Start.Compare(b.node.Sprint.Start), cmp.Compare(a.id(), b.id()))
	})
	return out
}

// forPeople tells whether ticket t of active sprint sp is assigned to a
// followed person: its assignee's account ID is a followed person's Jira
// account ID, or sp's ticket search, which asks for the followed people's
// tickets, found it. It is not when the ticket is unassigned, when its
// assignee's account ID was compared with every followed person's, or when
// sp's complete search, covering everyone followed, did not find it.
// Otherwise it is unknown: a Jira identity such as currentUser() or an
// email cannot be compared with an account ID, and display names are not
// trusted to match. live-sprint.js's S.forPeople is the same.
func forPeople(t *TicketInfo, sp *SprintInfo, people []Person) tri {
	compared, covered := len(people) > 0, len(people) > 0
	for _, p := range people {
		if isAccountID(p.Jira) {
			if t.AssigneeID != "" && strings.EqualFold(t.AssigneeID, p.Jira) {
				return triTrue
			}
		} else {
			compared = false
		}
		covered = covered && p.Jira != ""
	}
	if slices.Contains(t.SprintSearch, sp.ID) {
		return triTrue
	}
	switch {
	case t.Assignee == "" && t.AssigneeID == "" && t.Freshness.hasData():
		return triFalse
	case t.AssigneeID != "" && compared:
		return triFalse
	case sp.Searched && !sp.Truncated && covered:
		return triFalse
	}
	return triUnknown
}

// isAccountID reports whether a person's Jira identity can be an account
// ID: not empty, an email or a JQL function such as currentUser().
func isAccountID(j string) bool { return j != "" && !strings.ContainsAny(j, "@(") }

// defaultSprint is the sprint the view shows when none is chosen: the one
// with the most tickets for the followed people, then the most tickets,
// then the first by start and ID. When whose tickets they are is unknown
// for some sprint's tickets, it is the one with the most tickets.
func defaultSprint(active []*sprintSlice) *sprintSlice {
	known := true
	for _, a := range active {
		known = known && a.unknownPeople == 0
	}
	var best *sprintSlice
	for _, a := range active {
		switch {
		case best == nil:
			best = a
		case known && a.people != best.people:
			if a.people > best.people {
				best = a
			}
		case len(a.tickets) > len(best.tickets):
			best = a
		}
	}
	return best
}

// parseSprintArg reads a sprint ID as the page and --sprint give it: the
// number Jira shows, or the sprint node ID "sprint:<n>".
func parseSprintArg(arg string) (int, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(arg), "sprint:"))
	if err != nil || n <= 0 {
		return 0, extractArgsError("sprint %q is not a sprint ID; give the number Jira shows for it, such as 1234", arg)
	}
	return n, nil
}

// chooseSprint finds the active sprint arg names, or the default when arg
// is empty. A sprint that is not active, or not in the snapshot, is an
// error naming the active ones.
func chooseSprint(s *Snapshot, active []*sprintSlice, arg string) (*sprintSlice, error) {
	if arg == "" {
		return defaultSprint(active), nil
	}
	id, err := parseSprintArg(arg)
	if err != nil {
		return nil, err
	}
	for _, a := range active {
		if a.id() == id {
			return a, nil
		}
	}
	var names []string
	for _, a := range active {
		names = append(names, strconv.Itoa(a.id())+" ("+sprintName(a.node.Sprint)+")")
	}
	list := "the active " + plural(len(names), "sprint is ", "sprints are ") + strings.Join(names, ", ")
	for _, n := range s.Nodes {
		if n.Sprint != nil && n.Sprint.ID == id {
			state := cmpOr(n.Sprint.State, "of unknown state")
			return nil, extractArgsError("sprint %d (%s) is %s, and the sprint view summarizes active sprints only; %s", id, sprintName(n.Sprint), state, list)
		}
	}
	return nil, extractArgsError("no sprint %d in this snapshot; %s", id, list)
}

// sprintGap explains why there is no active sprint to show.
type sprintGap struct {
	title  string
	detail []string
	// config is set when [jira] board is not set, so the explanation shows
	// how to set it.
	config bool
}

// boardConfigHint is what the sprint view and its summary print when no
// [jira] board is set.
const boardConfigHint = "Add the board whose sprints to show to ~/.wgo/config.toml, using the number in the board's URL (…/boards/<id>):"

// explainNoSprint explains a snapshot without an active sprint: no
// jira_sprints source, no [jira] board, sprint data that is unknown or
// failed, or a board with no active sprint. ok is false when there is an
// active sprint to show.
func explainNoSprint(s *Snapshot, active []*sprintSlice) (gap sprintGap, ok bool) {
	if len(active) > 0 {
		return sprintGap{}, false
	}
	src, known := s.Sources[SourceJiraSprints]
	if !known {
		return sprintGap{title: "No sprint data", config: true, detail: []string{
			"This snapshot does not report the jira_sprints source, so it holds no sprints: it was made by a wgo dash without sprint support, or before a [jira] board was set.",
		}}, true
	}
	var board *SourcePart
	for i := range src.Parts {
		if strings.HasPrefix(src.Parts[i].Name, "board ") {
			board = &src.Parts[i]
			break
		}
	}
	if board == nil && len(src.Parts) == 0 && src.State == Unknown {
		// addSprints reports no board as an unknown source with no parts.
		return sprintGap{title: "No [jira] board is configured", config: true, detail: []string{
			"wgo dash shows the sprints of the Jira board that [jira] board names, and none is set.",
		}}, true
	}
	name, state, failure := "The board", src.State, ""
	if board != nil {
		name, state, failure = "Board "+strings.TrimPrefix(board.Name, "board "), board.State, board.Error
	}
	var detail []string
	switch state {
	case Unknown:
		if src.Detail != "" {
			detail = append(detail, upperFirst(src.Detail)+".")
		}
		return sprintGap{title: name + "'s sprints are not known yet", detail: detail}, true
	case Error:
		if failure != "" {
			detail = append(detail, "The lookup failed: "+failure+".")
		}
		if src.Detail != "" {
			detail = append(detail, upperFirst(src.Detail)+".")
		}
		return sprintGap{title: name + "'s sprints could not be read", detail: detail}, true
	}
	var future []string
	for _, n := range s.Nodes {
		if n.Sprint != nil && n.Sprint.State == SprintFuture {
			f := sprintName(n.Sprint) + " (" + strconv.Itoa(n.Sprint.ID) + ")"
			if !n.Sprint.Start.IsZero() {
				f += " from " + n.Sprint.Start.In(snapshotLocation(s.Timezone, s.GeneratedAt)).Format("2006-01-02")
			}
			future = append(future, f)
		}
	}
	detail = append(detail, "The sprint view shows active sprints only.")
	if len(future) > 0 {
		detail = append(detail, "Future: "+strings.Join(future, ", ")+".")
	}
	return sprintGap{title: name + " has no active sprint", detail: detail}, true
}

func upperFirst(s string) string {
	r := []rune(s)
	if len(r) > 0 {
		r[0] = unicode.ToUpper(r[0])
	}
	return string(r)
}

// sprintDay places now in a sprint from start to end as "day N of M": M
// counts the calendar days from the start's day to the end's in loc (at
// least 1), so Jira's sprint lengths of whole weeks, which end at the time
// of day they began, are 7, 14, 21 or 28 days; N counts from the start's day
// (day 1) to now's, at most M. before is set when now is before the start
// and after when it is past the end. ok is false when either date is
// unknown.
func sprintDay(start, end, now time.Time, loc *time.Location) (day, length int, before, after, ok bool) {
	if start.IsZero() || end.IsZero() || now.IsZero() {
		return 0, 0, false, false, false
	}
	days := func(a, b time.Time) int {
		ya, ma, da := a.In(loc).Date()
		yb, mb, db := b.In(loc).Date()
		return int(time.Date(yb, mb, db, 0, 0, 0, 0, time.UTC).Sub(time.Date(ya, ma, da, 0, 0, 0, 0, time.UTC)).Hours() / 24)
	}
	length = max(1, days(start, end))
	day = min(length, days(start, now)+1)
	return day, length, now.Before(start), !now.Before(end), true
}

// sprintDayText is the elapsed-time label: "day 6 of 21", "starts in 2
// days", "day 14 of 14, past its end", or "" when the dates are unknown.
func sprintDayText(start, end, now time.Time, loc *time.Location) string {
	day, length, before, after, ok := sprintDay(start, end, now, loc)
	switch {
	case !ok:
		return ""
	case before:
		y1, m1, d1 := now.In(loc).Date()
		y2, m2, d2 := start.In(loc).Date()
		n := int(time.Date(y2, m2, d2, 0, 0, 0, 0, time.UTC).Sub(time.Date(y1, m1, d1, 0, 0, 0, 0, time.UTC)).Hours() / 24)
		if n == 0 {
			return "starts today"
		}
		return "starts in " + strconv.Itoa(n) + plural(n, " day", " days")
	case after:
		return fmt.Sprintf("day %d of %d, past its end", length, length)
	}
	return fmt.Sprintf("day %d of %d", day, length)
}

// sprintPart is one status category's share of a sprint.
type sprintPart struct {
	key, label string
	points     float64
	tickets    int
}

// sprintProgress counts a sprint's story points by status category. Only
// a ticket whose points were read and set adds points; unestimated tickets
// (points read, none set) and tickets whose points are unknown are listed
// apart, so neither counts as zero.
type sprintProgress struct {
	parts                      []sprintPart // in sprintCategories order
	unknownCategory            []*Node
	unestimated, unknownPoints []*Node
	total                      int
}

func countSprint(tickets []*Node) sprintProgress {
	p := sprintProgress{total: len(tickets)}
	at := map[string]int{}
	for i, c := range sprintCategories {
		p.parts = append(p.parts, sprintPart{key: c.key, label: c.label})
		at[c.key] = i
	}
	for _, n := range tickets {
		t := n.Ticket
		switch {
		case !t.StoryPointsKnown:
			p.unknownPoints = append(p.unknownPoints, n)
		case t.StoryPoints == nil:
			p.unestimated = append(p.unestimated, n)
		}
		i, ok := at[t.StatusCategory]
		if !ok {
			p.unknownCategory = append(p.unknownCategory, n)
			continue
		}
		p.parts[i].tickets++
		if t.StoryPointsKnown && t.StoryPoints != nil {
			p.parts[i].points += *t.StoryPoints
		}
	}
	return p
}

// pointsText is "3 points", "1 point" or "2.5 points": rounded to two
// decimals, as the page's S.num rounds, so sums print no float noise.
func pointsText(f float64) string {
	f = math.Round(f*100) / 100
	if f == 1 {
		return "1 point"
	}
	return jsNumber(f) + " points"
}

func ticketsText(n int) string { return strconv.Itoa(n) + plural(n, " ticket", " tickets") }

// keysText lists up to five ticket keys, then how many more.
func keysText(nodes []*Node) string {
	var keys []string
	for _, n := range nodes {
		keys = append(keys, n.Label)
	}
	return joinNames(keys, 5)
}

// compareKeys orders ticket keys naturally: TEAM-9 before TEAM-10.
func compareKeys(a, b string) int {
	pa, na := splitKey(a)
	pb, nb := splitKey(b)
	return cmp.Or(cmp.Compare(strings.ToLower(pa), strings.ToLower(pb)), cmp.Compare(na, nb), cmp.Compare(a, b))
}

func splitKey(k string) (string, int) {
	i := len(k)
	for i > 0 && k[i-1] >= '0' && k[i-1] <= '9' {
		i--
	}
	n, err := strconv.Atoi(k[i:])
	if err != nil {
		return k, -1
	}
	return k[:i], n
}

// codeText is a mismatch or reason code as words, as the page shows it.
func codeText(code string) string { return strings.ReplaceAll(code, "_", " ") }

// renderSprint is the sprint view: the chosen active sprint's dates, goal
// and points by status for the whole sprint (with the share the query
// shows), the Jira mismatches on tickets the query shows, and the
// provenance footer. Without an active sprint it explains why.
func renderSprint(x *extractInput) (string, error) {
	s := x.v.Snapshot
	active := activeSprints(s)
	var b strings.Builder
	if gap, ok := explainNoSprint(s, active); ok {
		b.WriteString("## Sprint summary unavailable: " + mdText(gap.title) + "\n\n")
		for _, d := range gap.detail {
			b.WriteString(mdText(d) + "\n\n")
		}
		if gap.config {
			b.WriteString(mdText(boardConfigHint) + "\n\n```toml\n[jira]\nboard = <id>\n```\n\n")
		}
		x.writeSprintFooter(&b, nil, 0, 0)
		return b.String(), nil
	}
	sp, err := chooseSprint(s, active, x.args.Sprint)
	if err != nil {
		return "", err
	}
	info, loc := sp.node.Sprint, snapshotLocation(s.Timezone, s.GeneratedAt)
	b.WriteString("## " + mdText(sprintName(info)) + "\n\n")
	line := []string{"Sprint " + strconv.Itoa(info.ID)}
	if info.Board > 0 {
		line = append(line, "board "+strconv.Itoa(info.Board))
	}
	if info.Start.IsZero() || info.End.IsZero() {
		line = append(line, "dates unknown")
	} else {
		line = append(line, info.Start.In(loc).Format("2006-01-02")+" to "+info.End.In(loc).Format("2006-01-02"), sprintDayText(info.Start, info.End, s.GeneratedAt, loc))
	}
	b.WriteString(strings.Join(line, " · ") + "\n\n")
	if goal := strings.TrimSpace(info.Goal); goal != "" {
		// Verbatim: one quoted line per line of the goal.
		for _, l := range strings.Split(strings.ReplaceAll(goal, "\r\n", "\n"), "\n") {
			b.WriteString(strings.TrimRight("> "+mdText(l), " ") + "\n")
		}
		b.WriteString("\n")
	} else {
		b.WriteString("No sprint goal.\n\n")
	}
	for _, note := range sprintNotes(s, info) {
		b.WriteString("Note: " + mdText(note) + "\n\n")
	}

	var shown []*Node
	for _, n := range sp.tickets {
		if x.res.Visible(n.ID) {
			shown = append(shown, n)
		}
	}
	filtered := x.res.Filtered()
	p := countSprint(sp.tickets)
	b.WriteString("### Points by status\n\n")
	for _, part := range p.parts {
		b.WriteString("- " + part.label + ": " + pointsText(part.points) + " · " + ticketsText(part.tickets) + "\n")
	}
	if n := len(p.unknownCategory); n > 0 {
		b.WriteString("- Status unknown: " + ticketsText(n) + " (" + mdText(keysText(p.unknownCategory)) + "), not counted above\n")
	}
	b.WriteString("\n")
	var apart []string
	if n := len(p.unestimated); n > 0 {
		apart = append(apart, strconv.Itoa(n)+" unestimated ("+keysText(p.unestimated)+")")
	}
	if n := len(p.unknownPoints); n > 0 {
		apart = append(apart, strconv.Itoa(n)+" with points unknown ("+keysText(p.unknownPoints)+")")
	}
	if len(apart) > 0 {
		b.WriteString("Not in the points: " + mdText(strings.Join(apart, " · ")) + "\n\n")
	}
	if filtered {
		q := countSprint(shown)
		var parts []string
		for _, part := range q.parts {
			parts = append(parts, part.label+" "+pointsText(part.points)+" · "+ticketsText(part.tickets))
		}
		fmt.Fprintf(&b, "The query shows %d of %s: %s.\n\n", len(shown), ticketsText(len(sp.tickets)), strings.Join(parts, "; "))
	}

	x.writeSprintMismatches(&b, sp.tickets, filtered)
	x.writeSprintFooter(&b, sp, len(shown), len(sp.tickets))
	return b.String(), nil
}

// sprintNotes are what may make a sprint's tickets incomplete: stale
// sprint data, a search cut at its bound, or a failed search.
func sprintNotes(s *Snapshot, info *SprintInfo) []string {
	var out []string
	if info.Freshness == Stale {
		at := ""
		if !info.FetchedAt.IsZero() {
			at = " (fetched " + info.FetchedAt.In(s.GeneratedAt.Location()).Format("2006-01-02 15:04 -07:00") + ")"
		}
		out = append(out, "The sprint data is stale"+at+".")
	}
	if info.Truncated {
		out = append(out, "Jira found more tickets in this sprint than wgo reads, so some are missing.")
	}
	for _, part := range s.Sources[SourceJiraSprints].Parts {
		if part.Name != "sprint "+strconv.Itoa(info.ID) || part.Error == "" {
			continue
		}
		if part.State == Error {
			out = append(out, "This sprint's ticket search failed ("+part.Error+"), so its tickets are unknown.")
		} else {
			out = append(out, "This sprint's last ticket search failed ("+part.Error+"); its tickets are from an earlier search.")
		}
	}
	return out
}

// writeSprintMismatches lists, by code, the mismatches on the sprint's
// tickets the query shows, with links to each ticket and its linked PRs.
func (x *extractInput) writeSprintMismatches(b *strings.Builder, tickets []*Node, filtered bool) {
	b.WriteString("### Jira and the code disagree\n\n")
	byCode := map[string][]*Node{}
	evidence := map[*Node]map[string]string{}
	var unchecked []*Node
	outside := 0
	for _, n := range tickets {
		f := n.Ticket.Facts
		if !x.res.Visible(n.ID) {
			if f != nil && len(f.Mismatches) > 0 {
				outside++
			}
			continue
		}
		if f == nil || (f.Mismatches == nil && n.Ticket.System == "jira") {
			unchecked = append(unchecked, n)
			continue
		}
		evidence[n] = map[string]string{}
		for _, m := range f.Mismatches {
			byCode[m.Code] = append(byCode[m.Code], n)
			evidence[n][m.Code] = m.Evidence
		}
	}
	// Known codes in their order, then any others by name.
	var other []string
	for c := range byCode {
		if !slices.Contains(mismatchCodes, c) {
			other = append(other, c)
		}
	}
	slices.Sort(other)
	codes := append(slices.Clone(mismatchCodes), other...)
	any := false
	for _, c := range codes {
		list := byCode[c]
		if len(list) == 0 {
			continue
		}
		any = true
		slices.SortStableFunc(list, func(a, b *Node) int { return compareKeys(a.Label, b.Label) })
		b.WriteString("**" + mdText(codeText(c)) + "** (" + strconv.Itoa(len(list)) + ")\n\n")
		for _, n := range list {
			b.WriteString("- " + x.nodeLink(n))
			if t := n.Ticket.Title; t != "" {
				b.WriteString(" — " + mdText(t))
			}
			if e := evidence[n][c]; e != "" {
				b.WriteString(": " + mdText(e))
			}
			if prs := x.linkedPRsText(n); prs != "" {
				b.WriteString(" · PRs: " + prs)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if !any {
		if filtered {
			b.WriteString("None on the tickets the query shows.\n\n")
		} else {
			b.WriteString("None: Jira and the code agree on every ticket checked.\n\n")
		}
	}
	if len(unchecked) > 0 {
		slices.SortStableFunc(unchecked, func(a, b *Node) int { return compareKeys(a.Label, b.Label) })
		var parts []string
		for _, n := range unchecked {
			why := "no derived facts"
			if f := n.Ticket.Facts; f != nil && len(f.Uncertain) > 0 {
				why = strings.Join(f.Uncertain, ", ") + " unknown"
			}
			parts = append(parts, n.Label+" ("+why+")")
		}
		b.WriteString("Not checked: " + mdText(strings.Join(parts, ", ")) + "\n\n")
	}
	if outside > 0 {
		fmt.Fprintf(b, "%d more %s mismatches the query does not show.\n\n", outside, plural(outside, "ticket has", "tickets have"))
	}
}

// nodeLink is a node's label, linked to its URL when it has a safe one.
func (x *extractInput) nodeLink(n *Node) string {
	url := ""
	switch {
	case n.Ticket != nil:
		url = n.Ticket.URL
	case n.PR != nil:
		url = n.PR.URL
	}
	if u := safeMDURL(url); u != "" {
		return "[" + mdText(n.Label) + "](" + u + ")"
	}
	return mdText(n.Label)
}

// linkedPRsText lists a ticket's linked PRs with their states, linked.
func (x *extractInput) linkedPRsText(n *Node) string {
	f := n.Ticket.Facts
	if f == nil {
		return ""
	}
	var out []string
	for _, l := range f.LinkedPRs {
		ref := mdText(cmpOr(l.Ref, l.ID))
		if pr := x.v.Snapshot.Node(l.ID); pr != nil && pr.PR != nil {
			if u := safeMDURL(pr.PR.URL); u != "" {
				ref = "[" + ref + "](" + u + ")"
			}
		}
		out = append(out, ref+" "+cmpOr(l.State, "state unknown"))
	}
	return strings.Join(out, ", ")
}

// writeSprintFooter states the provenance: the snapshot, the sprint and how
// many of its tickets the query shows, the query and each source's
// freshness. sp is nil when there is no sprint to summarize.
func (x *extractInput) writeSprintFooter(b *strings.Builder, sp *sprintSlice, shown, total int) {
	s := x.v.Snapshot
	b.WriteString("---\n\n")
	line := fmt.Sprintf("Snapshot %s · generation %d", s.GeneratedAt.Format("2006-01-02 15:04 -07:00"), s.Generation)
	if sp == nil {
		line += " · no active sprint"
	} else {
		info := sp.node.Sprint
		line += " · sprint " + strconv.Itoa(info.ID)
		if info.Board > 0 {
			line += " on board " + strconv.Itoa(info.Board)
		}
		if !info.FetchedAt.IsZero() {
			line += ", fetched " + info.FetchedAt.In(s.GeneratedAt.Location()).Format("2006-01-02 15:04 -07:00")
		}
		line += fmt.Sprintf(" · the query shows %d of %s", shown, ticketsText(total))
	}
	b.WriteString(line + "\n\n")
	if x.args.Query == "" {
		b.WriteString("Query: none (the whole sprint)\n\n")
	} else {
		q := "Query: " + mdCode(x.args.Query)
		if name := filterNameFor(x.filters, x.args.Query); name != "" {
			q += " (saved filter " + mdText(name) + ")"
		}
		b.WriteString(q + "\n\n")
	}
	x.writeSources(b)
}
