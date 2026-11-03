package dash

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/inventory"
	"github.com/virtru/wgo/internal/spec"
)

// This file joins the PR inventory (gh-97) into a snapshot: every PR the
// configured people author or are asked to review, whether or not a local
// bookmark leads to it.

// DefaultInventoryTTL is how long the open inventory sets stay fresh when
// [dash] inventory_ttl_seconds is unset.
const DefaultInventoryTTL = 5 * time.Minute

// closedInventoryFactor is how many times longer closed-recent stays fresh
// than the open sets: it changes only when a PR merges or closes. One
// setting, [dash] inventory_ttl_seconds, thus gives 300 s and 1800 s by
// default.
const closedInventoryFactor = 6

// ClosedInventoryTTL is closed-recent's TTL for open-set TTL ttl.
func ClosedInventoryTTL(ttl time.Duration) time.Duration { return closedInventoryFactor * ttl }

// noPeopleDetail is github_inventory's detail while nobody is known to
// follow: no [[dash.people]], no [pair] teammate, and no cached login yet.
const noPeopleDetail = "nobody to follow yet: the authenticated GitHub user is looked up in the background; or list people in [[dash.people]]"

// invPR is one PR the inventory found, merged across sets and people.
type invPR struct {
	pr inventory.PR
	// state and fetchedAt are those of the set the facts were read from:
	// the most recently fetched one holding the PR.
	state     Freshness
	fetchedAt time.Time
	// requested holds the lowercased logins whose review-requested set
	// holds the PR.
	requested map[string]bool
}

// wsTicket is a workspace whose bookmark names a ticket, for attachment.
type wsTicket struct {
	effort string // effort node ID
	repo   string // lowercased owner/repo of its GitHub origin
}

// addInventory reads every configured person's inventory sets from the cache
// and adds their PRs to b: facts merge into the node of a PR a bookmark
// already found, and any other PR is attached per gh-97 requirement 14. Each
// PR's ticket keys become ticket nodes, looked up like bookmarks' tickets. A
// set that is not fresh is queued for refresh, unless GitHub cannot be asked
// at all. It returns the github_inventory source and any diagnostics.
func (c *Collector) addInventory(b *builder, ls *localState, wsEffort map[string]string, effortNode func(string) string,
	issues, jira *remoteTally, addJob func(Job)) (SourceStatus, []string) {
	var login, jiraID string
	if c.cfg.Identity != nil {
		login, jiraID = c.cfg.Identity()
	}
	people := ResolvePeople(c.cfg.People, c.cfg.Pair, login, jiraID)
	canAsk := c.cfg.GitHubUnavailable == ""
	if canAsk && login == "" && c.cfg.Identity != nil {
		addJob(Job{Kind: JobIdentity})
	}

	var tally remoteTally
	var diags []string
	since := inventory.ClosedSince(ls.at, ls.days)
	found := map[string]*invPR{}
	for _, person := range people {
		for _, set := range inventory.Sets {
			k := inventory.Key{Login: person.Login, Set: set}
			ttl := c.cfg.InventoryTTL
			if set == inventory.ClosedRecent {
				ttl = c.cfg.InventoryClosedTTL
			}
			res := inventory.Read(k, ttl, since, ls.at)
			f := inventoryFreshness(res)
			part := SourcePart{Name: k.String(), State: f, FetchedAt: res.FetchedAt, Truncated: res.Truncated}
			if res.Err != nil {
				part.Error = res.Err.Error()
			}
			tally.add(f, res.FetchedAt)
			tally.Parts = append(tally.Parts, part)
			if res.Truncated {
				diags = append(diags, fmt.Sprintf("github_inventory: %s matched %d PRs, but GitHub search returns at most %d, so the rest are missing",
					k, res.IssueCount, github.SearchLimit))
			}
			if f != Fresh && canAsk {
				addJob(Job{Kind: JobInventory, Inventory: k, Since: since})
			}
			if !f.hasData() {
				continue
			}
			for _, pr := range res.PRs {
				id := prNodeID(pr.Repo, pr.Number)
				ip := found[id]
				switch {
				case ip == nil:
					ip = &invPR{pr: pr, state: f, fetchedAt: res.FetchedAt, requested: map[string]bool{}}
					found[id] = ip
				case res.FetchedAt.After(ip.fetchedAt):
					ip.pr, ip.state, ip.fetchedAt = pr, f, res.FetchedAt
				}
				if set == inventory.ReviewRequested {
					ip.requested[strings.ToLower(person.Login)] = true
				}
			}
		}
	}
	st := tally.status()
	switch {
	case !canAsk:
		st.Detail = c.cfg.GitHubUnavailable
		if st.Fresh+st.Stale == 0 {
			st.State = Unknown
		}
	case len(people) == 0:
		st.State, st.Detail = Unknown, noPeopleDetail
	}
	if len(found) == 0 {
		return st, diags
	}

	// Everything the attachment rules and ticket filtering look up.
	isPR := map[string]bool{}
	for id, n := range b.nodes {
		if n.PR != nil {
			isPR[id] = true
		}
	}
	ids := make([]string, 0, len(found))
	for id := range found {
		ids = append(ids, id)
		isPR[id] = true
	}
	sort.Strings(ids)
	known := c.knownTicketPrefixes(ls)
	att := newAttachIndex(ls, wsEffort)

	for _, id := range ids {
		ip := found[id]
		f := ip.pr.PRFacts
		repo := f.Repo
		keys := ticketKeys(f.HeadRef, ip.pr.Refs, known, func(n int) bool { return isPR[prNodeID(repo, n)] })
		inv := involvement(people, f, ip.requested)
		if n := b.nodes[id]; n != nil && n.PR != nil {
			mergeInventory(n.PR, ip, keys, inv)
		} else {
			p := &PRInfo{Number: f.Number, Repo: f.Repo, Freshness: ip.state, FetchedAt: ip.fetchedAt, Sources: []string{PRSourceInventory}}
			setPRBase(p, f)
			setPRFacts(p, f, keys, inv)
			b.node(Node{ID: id, Kind: KindPR, Label: f.Repo + "#" + strconv.Itoa(f.Number), PR: p})
			att.attach(b, id, p, keys, effortNode)
		}
		for _, k := range keys {
			b.edge(id, c.ticketNodeFor(b, branchTicket(k), f.Repo, f.Repo, "", issues, jira, addJob), EdgeTicket)
		}
	}
	return st, diags
}

// inventoryFreshness maps an inventory cache result onto a Freshness.
func inventoryFreshness(r inventory.Result) Freshness {
	switch r.State {
	case inventory.Fresh:
		return Fresh
	case inventory.Stale:
		return Stale
	}
	return missFreshness(r.Err)
}

// knownTicketPrefixes are the Jira prefixes whose keys count in PR titles and
// bodies: the configured ones and those of local bookmarks' tickets.
func (c *Collector) knownTicketPrefixes(ls *localState) map[string]bool {
	known := map[string]bool{}
	for _, p := range c.cfg.TicketPrefixes {
		known[strings.ToUpper(p)] = true
	}
	for _, w := range ls.workspaces {
		if t := spec.ParseTicketFromBranch(w.bookmark); t != "" {
			if prefix, _, _ := strings.Cut(t, "-"); prefix != "GH" {
				known[prefix] = true
			}
		}
	}
	return known
}

// ticketKeys are a PR's ticket keys: its head branch's ticket by
// spec.ParseTicketFromBranch's rules, then the title and body references
// whose Jira prefix is known, and same-repository issues that are not
// themselves PRs (isPR). Ordered and deduplicated; GitHub issues are
// "gh-<n>", like TicketInfo.Key. Never nil: an inventory PR with no ticket
// reported none.
func ticketKeys(headRef string, refs []string, known map[string]bool, isPR func(int) bool) []string {
	keys := []string{}
	seen := map[string]bool{}
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	if t := spec.ParseTicketFromBranch(headRef); t != "" {
		add(ticketKey(t))
	}
	for _, r := range refs {
		if n, ok := strings.CutPrefix(r, "gh-"); ok {
			if num, err := strconv.Atoi(n); err == nil && !isPR(num) {
				add(r)
			}
			continue
		}
		if prefix, _, _ := strings.Cut(r, "-"); known[prefix] {
			add(r)
		}
	}
	return keys
}

// ticketKey turns a ticket as spec.ParseTicketFromBranch returns it into a
// TicketInfo key: GH-70 becomes gh-70, Jira keys stay.
func ticketKey(t string) string {
	if n, ok := strings.CutPrefix(t, "GH-"); ok {
		return "gh-" + n
	}
	return t
}

// branchTicket is ticketKey's inverse, the form ticketNodeFor takes.
func branchTicket(key string) string {
	if n, ok := strings.CutPrefix(key, "gh-"); ok {
		return "GH-" + n
	}
	return key
}

// involvement lists each person's part in PR f, in people order: author,
// review_requested (their review-requested set holds the open PR, or it
// asks them directly), reviewed (a latest review is theirs). Never nil.
func involvement(people []Person, f github.PRFacts, requested map[string]bool) []Involvement {
	out := []Involvement{}
	for _, p := range people {
		if strings.EqualFold(f.Author, p.Login) {
			out = append(out, Involvement{Person: p.Login, Role: RoleAuthor})
		}
		if f.State == "open" && (requested[strings.ToLower(p.Login)] || hasReviewer(f.ReviewRequests, p.Login)) {
			out = append(out, Involvement{Person: p.Login, Role: RoleReviewRequested})
		}
		for _, r := range f.Reviews {
			if strings.EqualFold(r.Author, p.Login) {
				out = append(out, Involvement{Person: p.Login, Role: RoleReviewed})
				break
			}
		}
	}
	return out
}

func hasReviewer(reqs []github.ReviewRequestFact, login string) bool {
	for _, r := range reqs {
		if r.Kind == ReviewerUser && strings.EqualFold(r.Reviewer, login) {
			return true
		}
	}
	return false
}

// setPRBase sets the fields a bookmark's PR lookup also reports from f.
func setPRBase(p *PRInfo, f github.PRFacts) {
	p.URL, p.Title, p.State, p.IsDraft = f.URL, f.Title, f.State, f.IsDraft
	p.ReviewDecision, p.Checks, p.UpdatedAt = f.ReviewDecision, f.Checks, f.UpdatedAt
	p.RequestedReviewers, p.ReviewersKnown = nil, f.ReviewRequests != nil
	for _, r := range f.ReviewRequests {
		p.RequestedReviewers = append(p.RequestedReviewers, r.Reviewer)
	}
}

// setPRFacts sets the inventory's facts from f. A list GitHub did not report
// stays nil (unknown); a reported empty one becomes [].
func setPRFacts(p *PRInfo, f github.PRFacts, keys []string, inv []Involvement) {
	p.Author = f.Author
	p.CreatedAt, p.MergedAt, p.ClosedAt, p.ReadyAt = f.CreatedAt, f.MergedAt, f.ClosedAt, f.ReadyAt
	p.Additions, p.Deletions, p.ChangedFiles = f.Additions, f.Deletions, f.ChangedFiles
	p.Mergeable, p.BaseRef, p.HeadRef = f.Mergeable, f.BaseRef, f.HeadRef
	p.ReviewRequests, p.Reviews, p.CheckDetails = nil, nil, nil
	if f.ReviewRequests != nil {
		p.ReviewRequests = make([]ReviewRequest, 0, len(f.ReviewRequests))
		for _, r := range f.ReviewRequests {
			p.ReviewRequests = append(p.ReviewRequests, ReviewRequest{Reviewer: r.Reviewer, Kind: r.Kind, RequestedAt: r.RequestedAt})
		}
	}
	if f.Reviews != nil {
		p.Reviews = make([]Review, 0, len(f.Reviews))
		for _, r := range f.Reviews {
			p.Reviews = append(p.Reviews, Review{Author: r.Author, State: r.State, SubmittedAt: r.SubmittedAt})
		}
	}
	if f.CheckDetails != nil {
		p.CheckDetails = make([]CheckDetail, 0, min(len(f.CheckDetails), MaxCheckDetails))
		for _, cd := range f.CheckDetails[:min(len(f.CheckDetails), MaxCheckDetails)] {
			p.CheckDetails = append(p.CheckDetails, CheckDetail{Name: cd.Name, State: cd.State, URL: cd.URL})
		}
	}
	p.ChecksTruncated = f.ChecksTruncated || len(f.CheckDetails) > MaxCheckDetails
	p.TicketKeys, p.Involvement = keys, inv
}

// mergeInventory merges an inventory PR into the node a bookmark found
// (gh-97 requirement 13). The fields both sources report come from the
// newer of the two by GitHub's updated_at, the inventory on a tie, except
// that a review decision the inventory lacks never erases the bookmark's;
// the facts only the inventory has are added. Freshness becomes the worse
// of the two sources' and FetchedAt the older fetch.
func mergeInventory(p *PRInfo, ip *invPR, keys []string, inv []Involvement) {
	f := ip.pr.PRFacts
	if !f.UpdatedAt.Before(p.UpdatedAt) {
		decision := p.ReviewDecision
		setPRBase(p, f)
		if p.ReviewDecision == "" {
			p.ReviewDecision = decision
		}
	}
	setPRFacts(p, f, keys, inv)
	p.Freshness = worseFreshness(p.Freshness, ip.state)
	if p.FetchedAt.IsZero() || (!ip.fetchedAt.IsZero() && ip.fetchedAt.Before(p.FetchedAt)) {
		p.FetchedAt = ip.fetchedAt
	}
	p.Sources = []string{PRSourceBookmark, PRSourceInventory}
}

var freshnessRank = map[Freshness]int{Fresh: 0, Stale: 1, Unknown: 2, Error: 3}

// worseFreshness is the less trustworthy of a and b.
func worseFreshness(a, b Freshness) Freshness {
	if freshnessRank[b] > freshnessRank[a] {
		return b
	}
	return a
}

// attachIndex holds what attaching an inventory-only PR looks up.
type attachIndex struct {
	// bookmarks maps lowercased owner/repo, NUL, bookmark name to the
	// bookmark node of the first workspace on it.
	bookmarks map[string]string
	// tickets maps a ticket key to the workspaces whose bookmark names it.
	tickets map[string][]wsTicket
	// effortRepos lists, per effort node ID, the repositories of its
	// workspaces.
	effortRepos map[string]map[string]bool
}

func newAttachIndex(ls *localState, wsEffort map[string]string) *attachIndex {
	ix := &attachIndex{bookmarks: map[string]string{}, tickets: map[string][]wsTicket{}, effortRepos: map[string]map[string]bool{}}
	for _, w := range ls.workspaces {
		repo := strings.ToLower(ls.ghSlugs[w.mainClone])
		eff := wsEffort[w.root]
		if eff != "" && repo != "" {
			if ix.effortRepos[eff] == nil {
				ix.effortRepos[eff] = map[string]bool{}
			}
			ix.effortRepos[eff][repo] = true
		}
		if w.err != nil || w.bookmark == "" || repo == "" {
			continue
		}
		if k := repo + "\x00" + w.bookmark; ix.bookmarks[k] == "" {
			ix.bookmarks[k] = bookmarkID(w.mainClone, w.bookmark)
		}
		if t := spec.ParseTicketFromBranch(w.bookmark); t != "" && eff != "" && eff != UngroupedID {
			key := ticketKey(t)
			ix.tickets[key] = append(ix.tickets[key], wsTicket{effort: eff, repo: repo})
		}
	}
	return ix
}

// attach hangs PR node id, which no bookmark found, off the graph. First
// match wins (gh-97 requirement 14):
//  1. the bookmark of a workspace in the same repository whose name is the
//     PR's head ref, by a pr edge;
//  2. the effort keyed ticket-<id> for one of its ticket keys;
//  3. the effort of a workspace whose bookmark names one of its ticket keys;
//  4. the repository's group effort repo:<owner/repo>.
//
// Cases 2 to 4 use a tracks edge. A GitHub issue key only matches work in
// the PR's own repository, since issue numbers are per repository. A head
// ref equal to the base ref (a fork's main) never names a local bookmark,
// and Ungrouped is not an effort to attach to.
func (ix *attachIndex) attach(b *builder, id string, p *PRInfo, keys []string, effortNode func(string) string) {
	repo := strings.ToLower(p.Repo)
	if p.HeadRef != "" && p.HeadRef != p.BaseRef {
		if bm := ix.bookmarks[repo+"\x00"+p.HeadRef]; bm != "" && b.nodes[bm] != nil {
			p.BookmarkID = bm
			b.edge(bm, id, EdgePR)
			return
		}
	}
	for _, k := range keys {
		eff := effortNodeID("ticket-" + strings.ToLower(k))
		n := b.nodes[eff]
		if n == nil || n.Effort == nil || n.Effort.Group != GroupTicket {
			continue
		}
		if strings.HasPrefix(k, "gh-") && !ix.effortRepos[eff][repo] {
			continue
		}
		b.edge(eff, id, EdgeTracks)
		return
	}
	for _, k := range keys {
		for _, w := range ix.tickets[k] {
			if strings.HasPrefix(k, "gh-") && w.repo != repo {
				continue
			}
			b.edge(w.effort, id, EdgeTracks)
			return
		}
	}
	b.edge(effortNode(repoEffortKey(p.Repo)), id, EdgeTracks)
}
