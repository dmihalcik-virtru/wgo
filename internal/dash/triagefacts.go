package dash

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// This file derives the PR facts the triage buckets of gh-133 read: human
// review activity with bots left out, approvals a later push dismissed,
// stack links between open PRs, and the linked tickets' status category and
// sprint state as PR fields, since a query field belongs to one node kind.
// Like the rest of the facts, each is unknown (omitted) when its own inputs
// are missing. An empty review decision counts as no review required, and
// stack children are as complete as the other PRs' refs.

// DefaultBotLogins are the default [pr.triage] bot_logins: logins whose
// reviews are not human activity, as case-insensitive regexes.
var DefaultBotLogins = []string{`\[bot\]$`, `^github-actions`, `^sonarqubecloud`, `^codecov`,
	`gemini`, `copilot`, `coderabbit`, `^dependabot`, `^renovate`}

// CompileBotLogins compiles [pr.triage] bot_logins, case-insensitively. nil
// means DefaultBotLogins. A bad pattern is an error naming it.
func CompileBotLogins(patterns []string) ([]*regexp.Regexp, error) {
	if patterns == nil {
		patterns = DefaultBotLogins
	}
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			return nil, fmt.Errorf("[pr.triage] bot_logins has %q, which is not a regular expression: %w", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func isBot(login string, bots []*regexp.Regexp) bool {
	for _, re := range bots {
		if re.MatchString(login) {
			return true
		}
	}
	return false
}

// humanFacts sets f's human activity and approval_dismissed from p's
// reviews. Reviews are the latest per reviewer, so the latest of them is the
// latest human review.
func humanFacts(f *PRFacts, p *PRInfo, bots []*regexp.Regexp) {
	if p.Reviews == nil {
		(*names)(&f.Uncertain).add("reviews")
		return
	}
	f.HumanReviews = []Review{}
	for _, r := range p.Reviews {
		if r.Author == "" || strings.EqualFold(r.Author, p.Author) || isBot(r.Author, bots) {
			continue
		}
		f.HumanReviews = append(f.HumanReviews, r)
		if r.SubmittedAt.After(f.HumanActivity) {
			f.HumanActivity = r.SubmittedAt
		}
	}
	if p.State != "open" {
		return
	}
	// After a push, GitHub leaves a stale approval as APPROVED, or marks it
	// DISMISSED where the repository dismisses stale reviews; either way the
	// decision falls back to review required. DISMISSED is also the state
	// of a dismissed change request, so this can over-report.
	dismissed := false
	if p.ReviewDecision == reviewRequired {
		for _, r := range f.HumanReviews {
			if r.State == ReviewApproved || r.State == ReviewDismissed {
				dismissed = true
			}
		}
	}
	f.ApprovalDismissed = &dismissed
}

// stackFacts links each open PR to the open PRs of the same repository
// whose head is its base (StackedOn) and whose base is its head
// (StackChildren). Refs match by name within the repository, so a head in
// a fork or on the default branch (a fork's main, a main → release PR) is
// never a stack's base. A PR's links are unknown without its refs.
func stackFacts(s *Snapshot, out map[string]*PRFacts) {
	type key struct{ repo, ref string }
	defaults := map[string]string{}
	for i := range s.Nodes {
		if p := s.Nodes[i].PR; p != nil && p.DefaultBranch != "" {
			defaults[strings.ToLower(p.Repo)] = p.DefaultBranch
		}
	}
	// stackable is whether p's head can be the base of another PR.
	stackable := func(p *PRInfo) bool {
		return p.State == "open" && p.HeadRef != "" && !p.CrossRepository && p.HeadRef != defaults[strings.ToLower(p.Repo)]
	}
	heads := map[key][]string{}
	for i := range s.Nodes {
		n := &s.Nodes[i]
		if n.PR != nil && stackable(n.PR) {
			k := key{strings.ToLower(n.PR.Repo), n.PR.HeadRef}
			heads[k] = append(heads[k], n.ID)
		}
	}
	for i := range s.Nodes {
		n := &s.Nodes[i]
		f := out[n.ID]
		if n.PR == nil || f == nil || n.PR.State != "open" {
			continue
		}
		if n.PR.HeadRef == "" || n.PR.BaseRef == "" {
			(*names)(&f.Uncertain).add("base_ref")
			continue
		}
		repo := strings.ToLower(n.PR.Repo)
		f.StackedOn = slices.DeleteFunc(slices.Clone(heads[key{repo, n.PR.BaseRef}]), func(id string) bool { return id == n.ID })
		f.StackChildren = []string{}
		if stackable(n.PR) {
			for j := range s.Nodes {
				c := s.Nodes[j].PR
				if c != nil && c.State == "open" && s.Nodes[j].ID != n.ID && strings.EqualFold(c.Repo, n.PR.Repo) && c.BaseRef == n.PR.HeadRef {
					f.StackChildren = append(f.StackChildren, s.Nodes[j].ID)
				}
			}
		}
		if f.StackedOn == nil {
			f.StackedOn = []string{}
		}
	}
}

// ticketCategory is a ticket's status category: a Jira ticket's own, and
// for a GitHub issue todo while open and done once closed. It is "" when
// unknown.
func ticketCategory(t *TicketInfo) string {
	if t == nil || !t.Freshness.hasData() || t.NotFound {
		return ""
	}
	if t.System == "github" {
		switch strings.ToLower(t.Status) {
		case "open":
			return StatusToDo
		case "closed":
			return StatusDone
		}
		return ""
	}
	return t.StatusCategory
}

// linkedTicketFacts sets the PR's ticket_category and ticket_sprint_state
// from the tickets it links to: empty with no ticket, and unknown while a
// linked ticket's value is.
func linkedTicketFacts(f *PRFacts, p *PRInfo, ticketIDs []string, g *factGraph) {
	if p.TicketKeys == nil && len(ticketIDs) == 0 {
		(*names)(&f.Uncertain).add("ticket_keys")
		return
	}
	cats, states := []string{}, []string{}
	catKnown, stateKnown := true, true
	for _, id := range ticketIDs {
		var t *TicketInfo
		if n := g.byID[id]; n != nil {
			t = n.Ticket
		}
		c := ticketCategory(t)
		if c == "" {
			catKnown = false
			name := id
			if t != nil {
				name = t.Key
			}
			(*names)(&f.Uncertain).add("ticket:" + name)
		} else if !slices.Contains(cats, c) {
			cats = append(cats, c)
		}
		switch {
		case t != nil && t.System == "github" && c != "":
			// GitHub issues have no sprints.
		case t == nil || t.Sprints == nil || !t.Freshness.hasData():
			stateKnown = false
		default:
			for _, sp := range t.Sprints {
				if sp.State != "" && !slices.Contains(states, sp.State) {
					states = append(states, sp.State)
				}
			}
		}
	}
	if catKnown {
		f.TicketCategory = cats
	}
	if stateKnown {
		f.TicketSprintState = states
	}
}

// triageFacts adds the gh-133 facts to the PR facts out holds.
func triageFacts(s *Snapshot, g *factGraph, out map[string]*PRFacts, bots []*regexp.Regexp) {
	for i := range s.Nodes {
		n := &s.Nodes[i]
		f := out[n.ID]
		if n.PR == nil || f == nil {
			continue
		}
		humanFacts(f, n.PR, bots)
		linkedTicketFacts(f, n.PR, g.prTickets[n.ID], g)
	}
	stackFacts(s, out)
}

var defaultBots = mustCompileBotLogins(DefaultBotLogins)

// mustCompileBotLogins panics on a bad pattern, so an edit to
// DefaultBotLogins fails loudly rather than leaving no bots.
func mustCompileBotLogins(patterns []string) []*regexp.Regexp {
	re, err := CompileBotLogins(patterns)
	if err != nil {
		panic(err)
	}
	return re
}
