package dash

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/inventory"
	"github.com/virtru/wgo/internal/issuecache"
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
	// requested maps the lowercased logins whose review-requested set holds
	// the PR to when that set was fetched, so an older set than the facts'
	// cannot outvote them.
	requested map[string]time.Time
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
// at all. It returns the github_inventory source, any diagnostics, and
// whether merges in the counts window are known: every followed person's
// closed-recent set has data covering the window and up to date with their
// open-authored set. Otherwise someone's merges could be missing anywhere.
func (c *Collector) addInventory(b *builder, ls *localState, wsEffort map[string]string, effortNode func(string) string,
	issues, jira *remoteTally, addJob func(Job)) (SourceStatus, []string, bool) {
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
	y, m, d := ls.at.Date()
	countsStart := time.Date(y, m, d, 0, 0, 0, 0, ls.at.Location()).AddDate(0, 0, -(ls.days - 1))
	mergedKnown := len(people) > 0
	// covered and coveredSince make github_inventory's CoveredSince: the
	// latest closed-recent window start, while every one has data.
	covered, coveredSince := len(people) > 0, time.Time{}
	found := map[string]*invPR{}
	for _, person := range people {
		// PRs that left the person's open-authored set since closed-recent
		// was last fetched: most merged or closed meanwhile.
		var dropped []inventory.PR
		var droppedAt, droppedFetched time.Time
		for _, set := range inventory.Sets {
			k := inventory.Key{Login: person.Login, Set: set}
			ttl := c.cfg.InventoryTTL
			if set == inventory.ClosedRecent {
				ttl = c.cfg.InventoryClosedTTL
			}
			res := inventory.Read(k, ttl, since, ls.at)
			f := inventoryFreshness(res)
			if set == inventory.OpenAuthored && f.hasData() {
				dropped, droppedAt, droppedFetched = res.Dropped, res.DroppedAt, res.DroppedFetchedAt
			}
			// closed-recent is due at once when it predates PRs leaving
			// open-authored: until refetched it cannot say what they became.
			due := set == inventory.ClosedRecent && len(dropped) > 0 && res.FetchedAt.Before(droppedAt)
			if due && f == Fresh {
				f = Stale
			}
			if set == inventory.ClosedRecent && (!f.hasData() || due || res.Incomplete || res.Since.IsZero() || res.Since.After(countsStart)) {
				mergedKnown = false
			}
			part := SourcePart{Name: k.String(), State: f, FetchedAt: res.FetchedAt, Truncated: res.Truncated, Incomplete: res.Incomplete && f.hasData()}
			if set == inventory.ClosedRecent {
				part.CoverageFrom = res.Since
				if !f.hasData() || due || res.Incomplete || res.Truncated || res.Since.IsZero() {
					covered = false
				} else if res.Since.After(coveredSince) {
					coveredSince = res.Since
				}
			}
			if res.Err != nil {
				part.Error = res.Err.Error()
			}
			tally.add(f, res.FetchedAt)
			tally.Parts = append(tally.Parts, part)
			if res.Truncated {
				diags = append(diags, fmt.Sprintf("github_inventory: %s matched %d PRs, but GitHub search returns at most %d, so the rest are missing",
					k, res.IssueCount, github.SearchLimit))
			}
			if part.Incomplete {
				why := fmt.Sprintf("read %d of the %d PRs it matched", len(res.PRs), res.IssueCount)
				if res.Err != nil {
					why = res.Err.Error()
				}
				diags = append(diags, fmt.Sprintf("github_inventory: %s is incomplete (%s), so PRs missing from it may still exist", k, why))
			}
			if f != Fresh && canAsk {
				addJob(Job{Kind: JobInventory, Inventory: k, Since: since})
			}
			if f.hasData() {
				for _, pr := range res.PRs {
					id := prNodeID(pr.Repo, pr.Number)
					ip := found[id]
					switch {
					case ip == nil:
						ip = &invPR{pr: pr, state: f, fetchedAt: res.FetchedAt, requested: map[string]time.Time{}}
						found[id] = ip
					case res.FetchedAt.After(ip.fetchedAt):
						ip.pr, ip.state, ip.fetchedAt = pr, f, res.FetchedAt
					}
					if set == inventory.ReviewRequested {
						ip.requested[strings.ToLower(person.Login)] = res.FetchedAt
					}
				}
			}
			if due {
				// Keep them, as last seen and stale, rather than let a PR
				// that just merged vanish until closed-recent is refetched,
				// all the more when closed-recent has no data at all.
				for _, pr := range dropped {
					if id := prNodeID(pr.Repo, pr.Number); found[id] == nil {
						found[id] = &invPR{pr: pr, state: Stale, fetchedAt: droppedFetched, requested: map[string]time.Time{}}
					}
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
	case login == "" && c.cfg.Identity != nil:
		// The sets read are fine, but the user's own are not among them.
		why := "it is looked up in the background"
		if c.cfg.IdentityError != nil {
			if e := c.cfg.IdentityError(); e != "" {
				why = "the last lookup failed: " + e
			}
		}
		st.Detail = "your GitHub login is not known yet, so your own PRs are not shown; " + why
		diags = append(diags, "github_inventory: "+st.Detail)
	}
	if covered {
		st.CoveredSince = coveredSince
	}
	if len(found) == 0 {
		return st, diags, mergedKnown
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
		owner, repo, _ := strings.Cut(f.Repo, "/")
		// A same-repository reference is no ticket when it is a PR: one in
		// the snapshot, or one the issue cache has found to be a PR.
		refIsPR := func(n int) bool {
			if isPR[prNodeID(f.Repo, n)] {
				return true
			}
			r := issuecache.Read(issuecache.Key{Owner: owner, Repo: repo, Number: n}, c.cfg.IssueTTL)
			return r.State != issuecache.Miss && r.Info.IsPR
		}
		n := b.nodes[id]
		bookmarked := n != nil && n.PR != nil
		// Someone else's branch name ("patch-1", "revert-45-fix") is held to
		// the known prefixes too; a bookmark's PR keeps its branch ticket.
		// Bare #N in titles and bodies counts only on a bookmark's PR: on
		// the inventory's (often other people's) it mostly names no issue
		// of the repository, and GitHub's closing issues say which do.
		keys := ticketKeys(f.HeadRef, f.Repo, f.ClosingIssues, ip.pr.Refs, known, refIsPR, !bookmarked && len(known) > 0, bookmarked)
		var p *PRInfo
		invNewer := true
		if bookmarked {
			p = n.PR
			invNewer = mergeInventory(p, ip, keys)
		} else {
			p = &PRInfo{Number: f.Number, Repo: f.Repo, Freshness: ip.state, FetchedAt: ip.fetchedAt, Sources: []string{PRSourceInventory}}
			setPRBase(p, f)
			setPRFacts(p, f, keys)
		}
		p.Involvement = involvement(people, p, ip.requested, invNewer, ip.fetchedAt)
		if !bookmarked {
			b.node(Node{ID: id, Kind: KindPR, Label: f.Repo + "#" + strconv.Itoa(f.Number), PR: p})
			att.attach(b, id, p, keys, effortNode)
		}
		for _, k := range keys {
			b.edge(id, c.ticketNodeFor(b, branchTicket(k), f.Repo, f.Repo, "", issues, jira, addJob), EdgeTicket)
		}
	}
	return st, diags, mergedKnown
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
// spec.ParseTicketFromBranch's rules; then the title and body references
// (refs) whose Jira prefix is known, and same-repository issues that are not
// themselves PRs (isPR), bare "#<n>" ones only with keepBare; then the
// issues of its repository (repo) that GitHub says it closes (closing).
// With filterHead the branch's Jira ticket counts only for a known prefix
// too: GitHub names branches "patch-1" and "revert-45-x". Ordered and
// deduplicated; GitHub issues are "gh-<n>", like TicketInfo.Key. Never nil:
// an inventory PR with no ticket reported none.
func ticketKeys(headRef, repo string, closing []github.IssueRef, refs []string, known map[string]bool, isPR func(int) bool, filterHead, keepBare bool) []string {
	keys := []string{}
	seen := map[string]bool{}
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	if t := ticketKey(spec.ParseTicketFromBranch(headRef)); t != "" {
		prefix, _, _ := strings.Cut(t, "-")
		if !filterHead || prefix == "gh" || known[prefix] {
			add(t)
		}
	}
	for _, r := range refs {
		if n, ok := strings.CutPrefix(r, "#"); ok {
			if !keepBare {
				continue
			}
			r = "gh-" + n
		}
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
	for _, is := range closing {
		// An issue elsewhere has no gh-<n> key; GitHub links those too.
		if strings.EqualFold(is.Repo, repo) && is.Number > 0 {
			add("gh-" + strconv.Itoa(is.Number))
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

// involvement lists each person's part in PR p, as merged, in people order:
// author; review_requested while p is open and asks them directly, or their
// review-requested set holds it (requested, by that set's fetch time). The
// set is trusted when it is no older than the facts p carries (the inventory
// facts fetched at factsAt, when invNewer); otherwise only while p's newer
// requested reviewers still name them or a team, since an approval since
// removes the request. reviewed when one of p's latest reviews is theirs
// and they are not its author. Never nil.
func involvement(people []Person, p *PRInfo, requested map[string]time.Time, invNewer bool, factsAt time.Time) []Involvement {
	out := []Involvement{}
	for _, person := range people {
		author := strings.EqualFold(p.Author, person.Login)
		if author {
			out = append(out, Involvement{Person: person.Login, Role: RoleAuthor})
		}
		setAt, held := requested[strings.ToLower(person.Login)]
		inSet := held && ((invNewer && !setAt.Before(factsAt)) || mayRequest(p, person.Login))
		if p.State == "open" && (inSet || asksDirectly(p, person.Login)) {
			out = append(out, Involvement{Person: person.Login, Role: RoleReviewRequested})
		}
		if !author && slices.ContainsFunc(p.Reviews, func(r Review) bool { return strings.EqualFold(r.Author, person.Login) }) {
			out = append(out, Involvement{Person: person.Login, Role: RoleReviewed})
		}
	}
	return out
}

// asksDirectly reports whether p's outstanding requests name login.
func asksDirectly(p *PRInfo, login string) bool {
	if p.ReviewRequests != nil {
		return slices.ContainsFunc(p.ReviewRequests, func(r ReviewRequest) bool {
			return r.Kind == ReviewerUser && strings.EqualFold(r.Reviewer, login)
		})
	}
	return slices.ContainsFunc(p.RequestedReviewers, func(r string) bool { return strings.EqualFold(r, login) })
}

// mayRequest reports whether p's requested reviewers could still include
// login: they are unknown, or name login or a team.
func mayRequest(p *PRInfo, login string) bool {
	return !p.ReviewersKnown || slices.ContainsFunc(p.RequestedReviewers, func(r string) bool {
		return strings.EqualFold(r, login) || strings.HasPrefix(r, teamPrefix)
	})
}

// setPRBase sets the fields a bookmark's PR lookup also reports from f.
func setPRBase(p *PRInfo, f github.PRFacts) {
	p.URL, p.Title, p.State, p.IsDraft = f.URL, f.Title, f.State, f.IsDraft
	p.ReviewDecision, p.Checks, p.UpdatedAt = f.ReviewDecision, f.Checks, f.UpdatedAt
	p.RequestedReviewers, p.ReviewersKnown = nil, f.ReviewRequests != nil
	if p.ReviewersKnown {
		p.RequestedReviewers = make([]string, 0, len(f.ReviewRequests))
		for _, r := range f.ReviewRequests {
			p.RequestedReviewers = append(p.RequestedReviewers, r.Reviewer)
		}
	}
}

// setPRFacts sets the inventory's facts from f. A list GitHub did not report
// stays nil (unknown); a reported empty one becomes [].
func setPRFacts(p *PRInfo, f github.PRFacts, keys []string) {
	setStableFacts(p, f, keys)
	setStateFacts(p, f)
}

// setStableFacts sets the facts that do not change with a PR's state.
func setStableFacts(p *PRInfo, f github.PRFacts, keys []string) {
	p.Author, p.CreatedAt = f.Author, f.CreatedAt
	p.Additions, p.Deletions, p.ChangedFiles = f.Additions, f.Deletions, f.ChangedFiles
	p.BaseRef, p.HeadRef = f.BaseRef, f.HeadRef
	p.TicketKeys = keys
}

// setStateFacts sets the facts that go with a PR's state: its times,
// mergeability, requests, reviews and checks.
func setStateFacts(p *PRInfo, f github.PRFacts) {
	p.MergedAt, p.ClosedAt, p.ReadyAt = f.MergedAt, f.ClosedAt, f.ReadyAt
	p.Mergeable = f.Mergeable
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
}

// mergeInventory merges an inventory PR into the node a bookmark found
// (gh-97 requirement 13) and reports whether the inventory's copy was the
// newer, by GitHub's updated_at (the inventory on a tie). The state and
// everything that goes with it (decision, requests, reviews, checks,
// mergeability, times) come together from the newer copy, so old and new
// never mix: when the bookmark's lookup is newer, those inventory facts stay
// unknown. Facts that do not change with the state are always added.
// Freshness becomes the worse of the two sources' and FetchedAt the older
// fetch.
func mergeInventory(p *PRInfo, ip *invPR, keys []string) bool {
	f := ip.pr.PRFacts
	newer := !f.UpdatedAt.Before(p.UpdatedAt)
	if newer {
		setPRBase(p, f)
		setPRFacts(p, f, keys)
	} else {
		setStableFacts(p, f, keys)
	}
	p.Freshness = worseFreshness(p.Freshness, ip.state)
	if p.FetchedAt.IsZero() || (!ip.fetchedAt.IsZero() && ip.fetchedAt.Before(p.FetchedAt)) {
		p.FetchedAt = ip.fetchedAt
	}
	p.Sources = []string{PRSourceBookmark, PRSourceInventory}
	return newer
}

var freshnessRank = map[Freshness]int{Fresh: 0, Stale: 1, Unknown: 2, Error: 3}

// worseFreshness is the less trustworthy of a and b. A value it does not
// know ranks as Unknown, never as Fresh.
func worseFreshness(a, b Freshness) Freshness {
	rank := func(f Freshness) int {
		if r, ok := freshnessRank[f]; ok {
			return r
		}
		return freshnessRank[Unknown]
	}
	if rank(b) > rank(a) {
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
