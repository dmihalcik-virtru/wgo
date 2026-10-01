package review

import (
	"sort"
	"strconv"
	"strings"
)

// Node kinds.
const (
	KindPerson = "person"
	KindPR     = "pr"
	KindTicket = "ticket"
	KindRepo   = "repo"
)

// Edge kinds.
const (
	EdgeAuthored  = "authored"   // me -> my PR
	EdgeWrote     = "wrote"      // someone else -> a PR I reviewed
	EdgeReviewed  = "reviewed"   // person -> my PR
	EdgeBuiltOn   = "built_on"   // person -> my PR they built on
	EdgeIReviewed = "i_reviewed" // me -> their PR
	EdgeDiscussed = "discussed"  // person -> me (aggregate, from people.json)
	EdgeRefs      = "refs"       // my PR -> my PR that references it
	EdgeTicket    = "ticket"     // PR -> ticket
	EdgeParent    = "parent"     // ticket -> parent ticket/epic
	EdgeInRepo    = "in_repo"    // PR -> repo
)

// Node is a vertex in the influence graph. IDs are stable and namespaced by
// kind ("person:<login>", "pr:<url>", "ticket:<KEY>", "repo:<owner/repo>").
type Node struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
	URL   string `json:"url,omitempty"`

	// person
	Me          bool         `json:"me,omitempty"`
	Bot         bool         `json:"bot,omitempty"`
	Outside     bool         `json:"outside,omitempty"` // shares no team with the user
	Counterpart *Counterpart `json:"counterpart,omitempty"`

	// pr / ticket
	Repo       string   `json:"repo,omitempty"`
	State      string   `json:"state,omitempty"`
	Month      string   `json:"month,omitempty"` // YYYY-MM created
	Mine       bool     `json:"mine,omitempty"`  // pr: authored by the user
	Outcome    string   `json:"outcome,omitempty"`
	Complexity string   `json:"complexity,omitempty"`
	Churn      int      `json:"churn,omitempty"`
	CycleHours *float64 `json:"cycle_hours,omitempty"`
	Ticketless bool     `json:"ticketless,omitempty"` // authored PR citing no Jira ticket
	Points     *float64 `json:"points,omitempty"`
}

// Edge is a directed, weighted relationship between two node IDs.
type Edge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
	Weight int    `json:"weight"`
}

// Graph is the explorer's data model. Facets are precomputed value counts so a
// UI can build its filters without scanning the nodes.
type Graph struct {
	Label  string                    `json:"label"`
	Me     string                    `json:"me"`
	Nodes  []Node                    `json:"nodes"`
	Edges  []Edge                    `json:"edges"`
	Facets map[string]map[string]int `json:"facets"`
}

// knownBots are logins that are automation but lack a "[bot]" suffix.
var knownBots = map[string]bool{
	"gemini-code-assist":        true,
	"copilot-swe-agent":         true,
	"github-advanced-security":  true,
	"virtru-internal":           true,
	"virtru-contents-and-pr-rw": true,
}

// IsBot reports whether login looks like automation rather than a person.
func IsBot(login string) bool {
	l := strings.ToLower(login)
	return knownBots[l] ||
		strings.HasSuffix(l, "[bot]") ||
		strings.Contains(l, "copilot") ||
		strings.HasSuffix(l, "-automation") ||
		strings.HasSuffix(l, "-bot")
}

type builder struct {
	run   *Run
	nodes map[string]*Node
	edges map[[3]string]*Edge

	// jiraBrowse is the "https://host/browse/" prefix learned from fetched
	// Jira rows, used to link tickets the ledger only cites.
	jiraBrowse string
}

// Build derives the graph from a loaded run. Only PRs the user authored or
// reviewed, tickets they touched, people who interacted with them, and the
// repos involved become nodes: cross-referenced PRs the user had no part in
// (release automation, other people's follow-ups) stay as person edges and as
// refs edges only when both ends are in the ledger.
func Build(r *Run) *Graph {
	b := &builder{run: r, nodes: map[string]*Node{}, edges: map[[3]string]*Edge{}}
	me := b.person(r.Me)
	me.Me = true

	// Cards carry the analyst's reconciled judgement; it wins over the ledger.
	judged := map[string]CardItem{}
	for _, it := range r.Cards {
		for _, u := range it.URLs {
			judged[u] = it
		}
	}

	for i := range r.Rows {
		row := &r.Rows[i]
		switch row.Kind {
		case "pr_authored":
			b.authored(row, judged)
		case "pr_reviewed":
			b.reviewed(row)
		case "jira":
			b.jira(row)
		}
	}
	// Second pass: refs edges need every authored PR to exist first.
	for i := range r.Rows {
		row := &r.Rows[i]
		if row.Kind != "pr_authored" {
			continue
		}
		for _, x := range row.CrossRefs {
			if _, ok := b.nodes["pr:"+x.URL]; ok && x.URL != row.URL {
				b.edge("pr:"+x.URL, "pr:"+row.URL, EdgeRefs, 1)
			}
		}
	}
	b.counterparts()
	return b.finish()
}

func (b *builder) person(login string) *Node {
	id := "person:" + login
	if n, ok := b.nodes[id]; ok {
		return n
	}
	n := &Node{ID: id, Kind: KindPerson, Label: login, URL: "https://github.com/" + login, Bot: IsBot(login)}
	b.nodes[id] = n
	return n
}

func (b *builder) repo(name string) string {
	id := "repo:" + name
	if _, ok := b.nodes[id]; !ok {
		b.nodes[id] = &Node{ID: id, Kind: KindRepo, Label: name, URL: "https://github.com/" + name}
	}
	return id
}

func (b *builder) ticket(key string) string {
	id := "ticket:" + key
	if _, ok := b.nodes[id]; !ok {
		b.nodes[id] = &Node{ID: id, Kind: KindTicket, Label: key}
	}
	return id
}

func (b *builder) edge(src, dst, kind string, w int) {
	if w <= 0 {
		return
	}
	k := [3]string{src, dst, kind}
	if e, ok := b.edges[k]; ok {
		e.Weight += w
		return
	}
	b.edges[k] = &Edge{Source: src, Target: dst, Kind: kind, Weight: w}
}

func month(created string) string {
	if len(created) >= 7 {
		return created[:7]
	}
	return ""
}

func (b *builder) pr(row *Row, mine bool) *Node {
	id := "pr:" + row.URL
	n, ok := b.nodes[id]
	if !ok {
		n = &Node{ID: id, Kind: KindPR, Label: row.Repo + "#" + strconv.Itoa(row.Number) + " " + row.Title, URL: row.URL}
		b.nodes[id] = n
	}
	n.Repo, n.State, n.Month = row.Repo, row.State, month(row.Created)
	n.Mine = n.Mine || mine
	b.edge(id, b.repo(row.Repo), EdgeInRepo, 1)
	for _, t := range row.Tickets {
		b.edge(id, b.ticket(t), EdgeTicket, 1)
	}
	return n
}

func (b *builder) authored(row *Row, judged map[string]CardItem) {
	n := b.pr(row, true)
	n.Outcome, n.Complexity = row.Outcome, row.Complexity
	if j, ok := judged[row.URL]; ok {
		n.Outcome, n.Complexity = j.Outcome, j.Complexity
	}
	n.Churn = row.Additions + row.Deletions
	n.CycleHours = row.CycleHours
	n.Ticketless = len(row.Tickets) == 0
	b.edge("person:"+b.run.Me, n.ID, EdgeAuthored, 1)

	for login, states := range row.Reviewers {
		total := 0
		for _, c := range states {
			total += c
		}
		b.person(login)
		b.edge("person:"+login, n.ID, EdgeReviewed, total)
	}
	for _, login := range row.BotReviewers {
		b.person(login).Bot = true
	}
	for _, login := range row.DownstreamOthers {
		b.person(login)
		b.edge("person:"+login, n.ID, EdgeBuiltOn, 1)
	}
}

func (b *builder) reviewed(row *Row) {
	n := b.pr(row, false)
	total := 0
	for _, c := range row.MyReviewStates {
		total += c
	}
	if total == 0 {
		total = 1
	}
	b.edge("person:"+b.run.Me, n.ID, EdgeIReviewed, total)
	if row.Author != "" && row.Author != b.run.Me {
		b.person(row.Author)
		b.edge("person:"+row.Author, n.ID, EdgeWrote, 1)
	}
}

func (b *builder) jira(row *Row) {
	if row.Key == "" {
		return
	}
	id := b.ticket(row.Key)
	n := b.nodes[id]
	n.Label = row.Key + " " + row.Summary
	n.State = row.Status
	n.Month = month(row.Created)
	n.Points = row.StoryPoints
	if row.URL != "" {
		n.URL = row.URL
		if i := strings.LastIndex(row.URL, "/browse/"); i >= 0 {
			b.jiraBrowse = row.URL[:i+len("/browse/")]
		}
	}
	if row.Parent != "" {
		b.edge(id, b.ticket(row.Parent), EdgeParent, 1)
	}
}

// counterparts folds people.json in: it attaches the aggregate numbers to
// person nodes, creates nodes for people with no per-PR evidence, and decides
// who is outside the user's teams.
func (b *builder) counterparts() {
	mine := map[string]bool{}
	for _, t := range b.run.People.MyTeams {
		mine[t] = true
	}
	for login, c := range b.run.People.Counterparts {
		n := b.person(login)
		c := c
		n.Counterpart = &c
		b.edge(n.ID, "person:"+b.run.Me, EdgeDiscussed, c.Discussed)
		if teams, ok := b.run.People.Teams[login]; ok && !n.Bot {
			n.Outside = true
			for _, t := range teams {
				if mine[t] {
					n.Outside = false
					break
				}
			}
		}
	}
}

func (b *builder) finish() *Graph {
	g := &Graph{Label: b.run.Label, Me: b.run.Me, Facets: map[string]map[string]int{}}
	for _, n := range b.nodes {
		// Tickets cited by a PR (or named as a parent) but never fetched have
		// no Jira row, hence no URL; derive it so every ticket is clickable.
		if n.Kind == KindTicket && n.URL == "" && b.jiraBrowse != "" {
			n.URL = b.jiraBrowse + strings.TrimPrefix(n.ID, "ticket:")
		}
		g.Nodes = append(g.Nodes, *n)
	}
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	for _, e := range b.edges {
		g.Edges = append(g.Edges, *e)
	}
	sort.Slice(g.Edges, func(i, j int) bool {
		a, c := g.Edges[i], g.Edges[j]
		if a.Source != c.Source {
			return a.Source < c.Source
		}
		if a.Target != c.Target {
			return a.Target < c.Target
		}
		return a.Kind < c.Kind
	})
	for _, n := range g.Nodes {
		facet(g, "kind", n.Kind)
		if n.Kind == KindPerson {
			switch {
			case n.Me:
			case n.Bot:
				facet(g, "person", "bot")
			case n.Outside:
				facet(g, "person", "outside my teams")
			default:
				facet(g, "person", "my teams")
			}
		}
		if n.Kind == KindPR && n.Mine {
			facet(g, "repo", n.Repo)
			facet(g, "outcome", n.Outcome)
			facet(g, "complexity", n.Complexity)
			facet(g, "month", n.Month)
			if n.Ticketless {
				facet(g, "ticket", "none")
			} else {
				facet(g, "ticket", "has ticket")
			}
		}
	}
	return g
}

func facet(g *Graph, name, value string) {
	if value == "" {
		return
	}
	if g.Facets[name] == nil {
		g.Facets[name] = map[string]int{}
	}
	g.Facets[name][value]++
}
