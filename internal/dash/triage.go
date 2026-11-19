package dash

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// This file sorts the operator's open PRs into triage buckets (gh-133):
// ordered rules in the query language, the first match winning. A rule
// whose fields are unknown for a PR does not match it, and the PR's row
// names those fields, so a missing ticket lookup never passes for a
// decision.

// TriageRule is one [[pr.triage.rules]] entry. An empty Match matches
// every PR.
type TriageRule struct {
	Bucket string `json:"bucket"`
	Note   string `json:"note,omitempty"`
	Match  string `json:"match"`
}

// AncientDays is how long a PR with no ticket must go without an update or
// a human review before the default rules suggest closing it.
const AncientDays = 180

// DefaultTriageRules are the rules used when none are configured
// (spec/gh-133.md requirement 7). staleDays is [dash.facts] stale_days.
func DefaultTriageRules(staleDays int) []TriageRule {
	if staleDays <= 0 {
		staleDays = DefaultStaleDays
	}
	s, a := strconv.Itoa(staleDays), strconv.Itoa(AncientDays)
	return []TriageRule{
		{Bucket: "land", Note: "Approved: merge it, or fix what blocks it.", Match: "review:approved;!prstate:draft"},
		{Bucket: "land", Note: "A human approval went stale after a push: re-request review.", Match: "approval_dismissed:true"},
		{Bucket: "keep", Note: "The ticket is in an active sprint.", Match: "ticket_sprint_state:active"},
		{Bucket: "close?", Note: "Every linked ticket is done but the PR never merged: close it if superseded.", Match: "ticket_category:done;!ticket_category:todo,in_progress"},
		{Bucket: "close?", Note: "No ticket, and no update or human review for " + a + " days: close it if abandoned.",
			Match: "!ticket:*;updated_ago:>" + a + ";!human_ago:<=" + a},
		{Bucket: "draft?", Note: "Ready for review but not updated for " + s + " days: ping a reviewer, or convert it to a draft.", Match: "!prstate:draft;stale:true"},
		{Bucket: "fix ticket", Note: "Updated in the last " + s + " days, but the ticket is still to do: move the ticket.", Match: "updated_ago:<" + s + ";ticket_category:todo"},
		{Bucket: "active", Note: "Updated in the last " + s + " days.", Match: "updated_ago:<" + s},
		{Bucket: "parked", Note: "Not updated for " + s + " days and nothing to decide: close it when the work is dropped."},
	}
}

// compiledRule is a rule with its query parsed.
type compiledRule struct {
	TriageRule
	q *Query
}

// TriageRules are checked rules, ready for BuildTriage.
type TriageRules struct{ rules []compiledRule }

// CompileTriageRules checks rules against reg: each needs a bucket whose
// name survives a worklist heading and a match over PR fields, and the last,
// alone, has no match, so it catches every PR the others did not. An error
// names the rule.
func CompileTriageRules(rules []TriageRule, reg *Registry) (*TriageRules, error) {
	if len(rules) == 0 {
		return nil, errors.New("[[pr.triage.rules]] is empty; add a rule, or remove the section to use the defaults")
	}
	out := make([]compiledRule, 0, len(rules))
	for i, r := range rules {
		where := fmt.Sprintf("[[pr.triage.rules]] rule %d", i+1)
		if strings.TrimSpace(r.Bucket) == "" {
			return nil, fmt.Errorf("%s has no bucket; name the bucket it sorts PRs into", where)
		}
		where += fmt.Sprintf(" (bucket %q)", r.Bucket)
		if !headingSafe(r.Bucket) {
			return nil, fmt.Errorf("%s: the name cannot round-trip through a worklist heading; drop surrounding spaces, Markdown punctuation (\\`*_[]<>|), a leading #, \" (\" and a \"New since\" prefix", where)
		}
		last := i == len(rules)-1
		switch {
		case strings.TrimSpace(r.Match) == "" && !last:
			return nil, fmt.Errorf("%s has no match, so it takes every PR and the rules after it never apply; give it a match, or move it last", where)
		case strings.TrimSpace(r.Match) != "" && last:
			return nil, fmt.Errorf("%s is the last rule but has a match, so a PR no rule matches would have no bucket; add a final rule with no match, such as bucket = \"parked\"", where)
		}
		q, err := ParseQuery(r.Match, reg)
		if err != nil {
			return nil, fmt.Errorf("%s: match %q: %w", where, r.Match, err)
		}
		for _, c := range q.Clauses {
			if !slices.Contains(c.field.Kinds, KindPR) {
				return nil, fmt.Errorf("%s: match %q: %s does not apply to PRs; rules match PR fields, such as ticket_category for a linked ticket's status", where, r.Match, c.Name)
			}
		}
		out = append(out, compiledRule{TriageRule: r, q: q})
	}
	return &TriageRules{rules: out}, nil
}

// headingSafe reports whether a bucket name reads back the same from a
// worklist's "## name (n)" heading: Markdown escaping leaves it alone, the
// count suffix cannot be confused with it, and it is not "New since".
func headingSafe(name string) bool {
	return name == strings.TrimSpace(name) && mdText(name) == name &&
		!strings.Contains(name, " (") && !strings.HasPrefix(name, "New since")
}

// Triage is the operator's open PRs sorted into buckets.
type Triage struct {
	GeneratedAt time.Time `json:"generated_at"`
	Me          string    `json:"me"`
	// Sources are the freshness of the sources the buckets read.
	Sources []TriageSource `json:"sources"`
	// Incomplete names the lookups that failed or were cut short, so a
	// missing PR or an unknown ticket never reads as none.
	Incomplete []string `json:"incomplete,omitempty"`
	// Dropped are the URLs of PRs no longer in the open PR list whose new
	// state is not known yet; they are in no bucket.
	Dropped []string `json:"dropped,omitempty"`
	// InventoryComplete is set when the operator's open-authored set is
	// whole, so a PR missing from it is not open. Tickets with no status
	// do not clear it.
	InventoryComplete bool           `json:"inventory_complete"`
	Buckets           []TriageBucket `json:"buckets"`
	Total             int            `json:"total"`
	// Warnings are the query warnings of the rules and filter.
	Warnings []string `json:"warnings,omitempty"`
}

// TriageSource is one source's freshness.
type TriageSource struct {
	Name      string    `json:"name"`
	State     Freshness `json:"state"`
	FetchedAt time.Time `json:"fetched_at,omitzero"`
}

// TriageBucket is one bucket and its PRs. Note joins the notes of its rules.
type TriageBucket struct {
	Name  string       `json:"name"`
	Note  string       `json:"note,omitempty"`
	Items []TriageItem `json:"items"`
}

// TriageItem is one PR in a bucket.
type TriageItem struct {
	ID        string         `json:"id"`
	Ref       string         `json:"ref"`
	URL       string         `json:"url"`
	Title     string         `json:"title,omitempty"`
	Draft     bool           `json:"draft"`
	UpdatedAt time.Time      `json:"updated_at,omitzero"`
	Tickets   []TriageTicket `json:"tickets"`
	// HumanReviews are the latest human reviews, nil when unknown.
	HumanReviews  []Review    `json:"human_reviews,omitzero"`
	StackedOn     []TriageRef `json:"stacked_on,omitempty"`
	StackChildren []TriageRef `json:"stack_children,omitempty"`
	// Rule is the note of the rule that matched, "" for the fallback.
	Rule string `json:"rule,omitempty"`
	// Unknown names the fields that kept earlier rules from deciding.
	Unknown []string `json:"unknown,omitempty"`
}

// TriageTicket is a linked ticket. Status is "" when unknown.
type TriageTicket struct {
	Key    string `json:"key"`
	URL    string `json:"url,omitempty"`
	Status string `json:"status,omitempty"`
}

// TriageRef names another PR.
type TriageRef struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	URL    string `json:"url,omitempty"`
}

// BuildTriage sorts the open PRs the operator authored in v's snapshot into
// rules' buckets. filter, when set, is a query that further narrows the PRs
// (the dash's and the extract's q).
func BuildTriage(v *View, rs *TriageRules, filter string) (*Triage, error) {
	if rs == nil || len(rs.rules) == 0 {
		return nil, errors.New("no triage rules; build them with CompileTriageRules")
	}
	rules := rs.rules
	reg := Fields()
	ix, err := v.queryIndex(reg)
	if err != nil {
		return nil, err
	}
	if ix.env.Me == "" {
		return nil, errors.New("the snapshot names no authenticated GitHub user, so wgo cannot tell which PRs are yours; check gh auth status")
	}
	base := "kind:pr;author:@me;prstate:open,draft"
	if strings.TrimSpace(filter) != "" {
		// Parsed alone first, so an error's column is in the user's text.
		if _, err := ParseQuery(filter, reg); err != nil {
			return nil, err
		}
		base += ";" + filter
	}
	q, err := ParseQuery(base, reg)
	if err != nil {
		return nil, err
	}
	res := ix.evaluate(q)
	t := &Triage{GeneratedAt: v.Snapshot.GeneratedAt, Me: ix.env.Me, Warnings: res.Warnings}
	// Only the inventory is in Incomplete yet; tickets are added last.
	t.Sources, t.Incomplete = triageSources(v.Snapshot, ix.env.Me)
	t.InventoryComplete = len(t.Incomplete) == 0
	var unfetched []string

	type boundRule struct {
		compiledRule
		clauses []boundClause
	}
	bound := make([]boundRule, len(rules))
	for i, r := range rules {
		bound[i].compiledRule = r
		for _, c := range r.q.Clauses {
			b, w := ix.bind(c)
			bound[i].clauses = append(bound[i].clauses, b)
			for _, s := range w {
				if !slices.Contains(t.Warnings, s) {
					t.Warnings = append(t.Warnings, s)
				}
			}
		}
	}
	index := map[string]int{}
	notes := map[string][]string{}
	for _, r := range rules {
		if _, ok := index[r.Bucket]; !ok {
			index[r.Bucket] = len(t.Buckets)
			t.Buckets = append(t.Buckets, TriageBucket{Name: r.Bucket, Items: []TriageItem{}})
		}
		if r.Note != "" && !slices.Contains(notes[r.Bucket], r.Note) {
			notes[r.Bucket] = append(notes[r.Bucket], r.Note)
			t.Buckets[index[r.Bucket]].Note = strings.Join(notes[r.Bucket], " ")
		}
	}
	// CompileTriageRules made the last rule match every PR.
	fallback := index[rules[len(rules)-1].Bucket]
	nodes := map[string]*Node{}
	for i := range v.Snapshot.Nodes {
		nodes[v.Snapshot.Nodes[i].ID] = &v.Snapshot.Nodes[i]
	}
	tickets := map[string][]string{}
	for _, e := range v.Snapshot.Edges {
		if e.Kind == EdgeTicket {
			tickets[e.Source] = append(tickets[e.Source], e.Target)
		}
	}
	// A PR absent from the latest complete open-authored set has most likely
	// merged or closed, even if an older bookmark lookup still calls it open.
	// Older snapshots lack the explicit marker; their inventory-only PRs can
	// still be recognized by comparing the fetch times.
	// With the open-authored part found, it is the first of two sources.
	var listedAt time.Time
	if len(t.Sources) > 1 && t.Sources[0].State.hasData() {
		listedAt = t.Sources[0].FetchedAt
	}
	for _, i := range res.rowIdx {
		n := nodes[ix.ids[i]]
		if n == nil || n.PR == nil {
			continue
		}
		dropped := false
		if t.InventoryComplete {
			switch {
			case n.PR.OpenAuthoredListed != nil:
				dropped = !*n.PR.OpenAuthoredListed
			case slices.Equal(n.PR.Sources, []string{PRSourceInventory}):
				dropped = n.PR.FetchedAt.Before(listedAt)
			}
		}
		if dropped {
			t.Dropped = append(t.Dropped, n.PR.URL)
			continue
		}
		item := triageItem(n, nodes, tickets[n.ID])
		for _, tt := range item.Tickets {
			if tt.Status == "" && !slices.Contains(unfetched, tt.Key) {
				unfetched = append(unfetched, tt.Key)
			}
		}
		dest := fallback
		for _, r := range bound {
			holds, unknown := ruleHolds(ix, r.clauses, i)
			if holds == triTrue {
				dest, item.Rule = index[r.Bucket], r.Note
				break
			}
			if holds == triUnknown {
				for _, u := range unknown {
					if !slices.Contains(item.Unknown, u) {
						item.Unknown = append(item.Unknown, u)
					}
				}
			}
		}
		t.Buckets[dest].Items = append(t.Buckets[dest].Items, item)
		t.Total++
	}
	for i := range t.Buckets {
		slices.SortFunc(t.Buckets[i].Items, compareTriageItems)
	}
	if len(unfetched) > 0 {
		slices.Sort(unfetched)
		t.Incomplete = append(t.Incomplete, "no status for "+joinNames(unfetched, 10))
	}
	return t, nil
}

// ruleHolds is AND over a rule's clauses on node i, and when it is unknown
// the fields that made it so.
func ruleHolds(ix *queryIndex, clauses []boundClause, i int) (tri, []string) {
	out := triTrue
	var unknown []string
	for k := range clauses {
		switch clauses[k].test(ix.values[clauses[k].Name][i]) {
		case triFalse:
			return triFalse, nil
		case triUnknown:
			out = triUnknown
			unknown = append(unknown, clauses[k].Name)
		}
	}
	return out, unknown
}

// compareTriageItems orders a bucket by first ticket (none last), then
// repository and number.
func compareTriageItems(a, b TriageItem) int {
	ka, kb := "~", "~"
	if len(a.Tickets) > 0 {
		ka = a.Tickets[0].Key
	}
	if len(b.Tickets) > 0 {
		kb = b.Tickets[0].Key
	}
	ra, na := splitRef(a.Ref)
	rb, nb := splitRef(b.Ref)
	return cmp.Or(cmp.Compare(ka, kb), cmp.Compare(ra, rb), cmp.Compare(na, nb), cmp.Compare(a.ID, b.ID))
}

// splitRef is the lower-cased repository and number of "owner/repo#N".
func splitRef(ref string) (string, int) {
	i := strings.LastIndexByte(ref, '#')
	if i < 0 {
		return strings.ToLower(ref), 0
	}
	n, _ := strconv.Atoi(ref[i+1:])
	return strings.ToLower(ref[:i]), n
}

func triageItem(n *Node, nodes map[string]*Node, ticketIDs []string) TriageItem {
	p := n.PR
	it := TriageItem{ID: n.ID, Ref: p.Repo + "#" + strconv.Itoa(p.Number), URL: p.URL, Title: p.Title, Draft: p.IsDraft, UpdatedAt: p.UpdatedAt, Tickets: []TriageTicket{}}
	for _, id := range ticketIDs {
		tn := nodes[id]
		if tn == nil || tn.Ticket == nil {
			it.Tickets = append(it.Tickets, TriageTicket{Key: id[strings.LastIndexByte(id, ':')+1:]})
			continue
		}
		tt := TriageTicket{Key: tn.Ticket.Key, URL: tn.Ticket.URL}
		if tn.Ticket.Freshness.hasData() {
			tt.Status = tn.Ticket.Status
		}
		it.Tickets = append(it.Tickets, tt)
	}
	ref := func(id string) TriageRef {
		r := TriageRef{ID: id}
		if o := nodes[id]; o != nil && o.PR != nil {
			r.Number, r.URL = o.PR.Number, o.PR.URL
		}
		return r
	}
	if f := p.Facts; f != nil {
		it.HumanReviews = f.HumanReviews
		for _, id := range f.StackedOn {
			it.StackedOn = append(it.StackedOn, ref(id))
		}
		for _, id := range f.StackChildren {
			it.StackChildren = append(it.StackChildren, ref(id))
		}
	}
	return it
}

// triageSources reports the freshness of me's open-authored inventory set
// and of Jira, and what makes that set incomplete: not fetched, cut short
// by the search limit, or partly read. BuildTriage adds the tickets with no
// status.
func triageSources(s *Snapshot, me string) ([]TriageSource, []string) {
	var srcs []TriageSource
	var incomplete []string
	part := strings.ToLower(inventoryPart(me, InventoryOpenAuthored))
	found := false
	for _, p := range s.Sources[SourceGitHubInventory].Parts {
		if strings.ToLower(p.Name) != part {
			continue
		}
		found = true
		srcs = append(srcs, TriageSource{Name: p.Name, State: p.State, FetchedAt: p.FetchedAt})
		switch {
		case !p.State.hasData():
			incomplete = append(incomplete, p.Name+": "+cmpOr(p.Error, "not fetched"))
		case p.Truncated:
			incomplete = append(incomplete, p.Name+": cut short by GitHub's search limit")
		case p.Incomplete:
			incomplete = append(incomplete, p.Name+": the last fetch missed some PRs"+errSuffix(p.Error))
		}
	}
	if !found {
		incomplete = append(incomplete, inventoryPart(me, InventoryOpenAuthored)+": not fetched")
	}
	jira := s.Sources[SourceJira]
	srcs = append(srcs, TriageSource{Name: SourceJira, State: jira.State, FetchedAt: jira.OldestFetch})
	return srcs, incomplete
}

func errSuffix(e string) string {
	if e == "" {
		return ""
	}
	return " (" + e + ")"
}
