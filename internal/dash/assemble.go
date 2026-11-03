package dash

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/issuecache"
	"github.com/virtru/wgo/internal/jiracache"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/prcache"
	"github.com/virtru/wgo/internal/spec"
	"github.com/virtru/wgo/internal/store"
)

// builder accumulates nodes and edges with dedupe.
type builder struct {
	nodes map[string]*Node
	edges map[Edge]bool
	// diags are problems found while building; they join the snapshot's
	// diagnostics. notes are per-ticket notes, such as Jira facts that
	// could not be read, keyed by cause: each becomes one diagnostic with
	// the tickets it affects.
	diags []string
	notes map[string][]string
}

func (b *builder) node(n Node) *Node {
	if existing, ok := b.nodes[n.ID]; ok {
		return existing
	}
	p := &n
	b.nodes[n.ID] = p
	return p
}

func (b *builder) edge(src, dst string, kind EdgeKind) {
	if src == "" || dst == "" {
		return
	}
	b.edges[Edge{Source: src, Target: dst, Kind: kind}] = true
}

var kindOrder = map[NodeKind]int{
	KindEffort: 0, KindWorkspace: 1, KindBookmark: 2, KindPR: 3, KindTicket: 4, KindAgent: 5, KindSprint: 6,
}

// remoteTally counts item freshness for one remote source.
type remoteTally struct{ SourceStatus }

func (t *remoteTally) add(f Freshness, fetchedAt time.Time) {
	if !fetchedAt.IsZero() && (t.OldestFetch.IsZero() || fetchedAt.Before(t.OldestFetch)) {
		t.OldestFetch = fetchedAt
	}
	switch f {
	case Fresh:
		t.Fresh++
	case Stale:
		t.Stale++
	case Error:
		t.Error++
	default:
		t.Unknown++
	}
}

func (t remoteTally) status() SourceStatus {
	s := t.SourceStatus
	s.State = aggregate(s)
	if s.Fresh+s.Stale+s.Unknown+s.Error == 0 {
		s.Detail = "nothing to look up"
	}
	return s
}

// assemble turns local state into a snapshot, joining cached remote data.
// It only reads caches: a miss is reported as Unknown, and every entry that
// is not Fresh becomes a job.
func (c *Collector) assemble(ls *localState) (*Snapshot, []Job) {
	b := &builder{nodes: map[string]*Node{}, edges: map[Edge]bool{}}
	var jobs []Job
	jobSeen := map[string]bool{}
	addJob := func(j Job) {
		if !jobSeen[j.key()] {
			jobSeen[j.key()] = true
			jobs = append(jobs, j)
		}
	}
	var prTally, issueTally, jiraTally remoteTally

	att := ls.attribution
	efforts := att.Efforts

	effortNode := func(key string) string {
		if key == "" || key == GroupUngrouped {
			b.node(Node{ID: UngroupedID, Kind: KindEffort, Label: "Ungrouped",
				Effort: &EffortInfo{Key: GroupUngrouped, Group: GroupUngrouped}})
			return UngroupedID
		}
		id := effortNodeID(key)
		info := &EffortInfo{Key: key}
		label := key
		switch e, ok := efforts[key]; {
		case ok:
			info.Group, info.Description, info.Source = GroupEffort, e.Description, string(e.Source)
			if e.Name != "" {
				label = e.Name
			}
		case strings.HasPrefix(key, "ticket-"):
			info.Group = GroupTicket
			label = strings.ToUpper(strings.TrimPrefix(key, "ticket-"))
		case strings.HasPrefix(key, repoEffortPrefix):
			info.Group = GroupRepo
			label = strings.TrimPrefix(key, repoEffortPrefix)
		case strings.HasPrefix(key, sprintEffortPrefix):
			// The sprint's name is not known here; whoever adds the
			// sprint node may relabel the effort.
			info.Group = GroupSprint
			label = "Sprint " + strings.TrimPrefix(key, sprintEffortPrefix)
		default:
			info.Group = GroupTheme
		}
		b.node(Node{ID: id, Kind: KindEffort, Label: label, Effort: info})
		return id
	}
	for id := range efforts {
		effortNode(id)
	}

	// Workspace -> effort node ID.
	wsEffort := map[string]string{}
	for key, wss := range att.Grouped {
		for _, w := range wss {
			wsEffort[filepath.Clean(w.Path)] = effortNode(key)
		}
	}
	for _, w := range att.Ungrouped {
		wsEffort[filepath.Clean(w.Path)] = effortNode(GroupUngrouped)
	}

	clones := map[string]int{}
	for i, cl := range ls.clones {
		clones[cl.Path] = i
	}

	wsByCanonical := map[string]string{}
	effortBookmarks := map[string]map[string]bool{}
	for _, w := range ls.workspaces {
		cl := ls.clones[clones[w.mainClone]]
		repoSlug := cl.Slug()
		eff := wsEffort[w.root]
		if eff == "" {
			eff = effortNode(GroupUngrouped)
		}
		wi := workspaceInfo(w, cl.Name, repoSlug, eff, ls.annotations)
		wsByCanonical[w.canonical] = w.id
		label := wi.Slug
		if !strings.EqualFold(wi.Slug, cl.Name) {
			label = cl.Name + "/" + wi.Slug
		}
		b.node(Node{ID: w.id, Kind: KindWorkspace, Label: label, Workspace: wi})
		b.edge(eff, w.id, EdgeContains)

		if w.err != nil || w.bookmark == "" {
			continue
		}

		// Bookmark and its cached PRs.
		bmID := bookmarkID(w.mainClone, w.bookmark)
		b.edge(w.id, bmID, EdgeOn)
		if effortBookmarks[eff] == nil {
			effortBookmarks[eff] = map[string]bool{}
		}
		effortBookmarks[eff][bmID] = true
		if _, done := b.nodes[bmID]; done {
			continue
		}
		origin := ls.origins[w.mainClone]
		res := prcache.Read(origin, w.mainClone, w.bookmark, c.cfg.PRTTL)
		bi := &BookmarkInfo{Name: w.bookmark, Repo: cl.Name, PRFetchedAt: res.FetchedAt, PRCount: len(res.PRs), RepoSlug: repoSlug}
		bi.PRLookup = prFreshness(res)
		if res.Err != nil {
			bi.PRError = res.Err.Error()
		}
		prTally.add(bi.PRLookup, res.FetchedAt)
		if ls.ghSlugs[w.mainClone] == "" {
			// No GitHub remote (or jj could not say): a fetch could only
			// fail and record that failure, so say why and queue nothing.
			if bi.PRError == "" {
				bi.PRError = ls.noGitHub[w.mainClone]
			}
		} else if bi.PRLookup != Fresh {
			addJob(Job{Kind: JobPR, RemoteURL: origin, RepoPath: w.mainClone, Branch: w.bookmark})
		}
		b.node(Node{ID: bmID, Kind: KindBookmark, Label: w.bookmark, Bookmark: bi})

		repoKey := repoSlug
		if repoKey == "" {
			repoKey = cl.Name
		}
		var prIDs []string
		if bi.PRLookup.hasData() {
			for _, pr := range res.PRs {
				id := prNodeID(repoKey, pr.Number)
				prIDs = append(prIDs, id)
				label := "#" + strconv.Itoa(pr.Number)
				if repoSlug != "" {
					label = repoSlug + label
				}
				var reviewers []string
				if res.ReviewersKnown {
					reviewers = append([]string{}, pr.RequestedReviewers...)
				}
				b.node(Node{ID: id, Kind: KindPR, Label: label, PR: &PRInfo{
					Number:             pr.Number,
					Repo:               repoKey,
					URL:                pr.URL,
					Title:              pr.Title,
					State:              strings.ToLower(pr.State),
					IsDraft:            pr.IsDraft,
					ReviewDecision:     pr.ReviewDecision,
					Checks:             pr.Checks.State,
					RequestedReviewers: reviewers,
					ReviewersKnown:     res.ReviewersKnown,
					UpdatedAt:          pr.UpdatedAt,
					Freshness:          bi.PRLookup,
					BookmarkID:         bmID,
					Sources:            []string{PRSourceBookmark},
					FetchedAt:          res.FetchedAt,
				}})
				b.edge(bmID, id, EdgePR)
			}
		}

		// Ticket parsed from the bookmark.
		tid := c.ticketNode(b, w.bookmark, cl.Name, ls.ghSlugs[w.mainClone], ls.noGitHub[w.mainClone], &issueTally, &jiraTally, addJob)
		if tid == "" {
			continue
		}
		if len(prIDs) == 0 {
			b.edge(bmID, tid, EdgeTicket)
		}
		for _, id := range prIDs {
			b.edge(id, tid, EdgeTicket)
		}
	}

	// Conflicts recorded on the Ungrouped effort.
	if len(att.Conflicts) > 0 {
		u := b.nodes[effortNode(GroupUngrouped)]
		for _, cf := range att.Conflicts {
			id := wsByCanonical[canonicalPath(cf.Workspace.Path)]
			if id == "" {
				// A conflicted workspace we did not discover cannot be
				// referenced by ID; say so instead of emitting a dangling one.
				ls.diags = append(ls.diags, fmt.Sprintf("workspace %s is claimed by multiple efforts (%s) but is not a discovered workspace",
					cf.Workspace.Path, strings.Join(cf.ClaimedBy, ", ")))
				continue
			}
			u.Effort.Conflicts = append(u.Effort.Conflicts, EffortConflict{WorkspaceID: id, ClaimedBy: append([]string(nil), cf.ClaimedBy...)})
		}
		sort.Slice(u.Effort.Conflicts, func(i, j int) bool { return u.Effort.Conflicts[i].WorkspaceID < u.Effort.Conflicts[j].WorkspaceID })
	}

	// Agent sessions.
	for _, o := range ls.sessions {
		ai := &AgentInfo{
			SessionID:     o.ID,
			Tool:          o.Tool,
			Status:        string(o.Status),
			Liveness:      Liveness(o.Liveness),
			Source:        string(o.Source),
			Branch:        o.Branch,
			ThemeID:       o.ThemeID,
			Path:          o.WorktreePath,
			WorkspaceID:   wsByCanonical[canonicalPath(o.WorktreePath)],
			StartTime:     o.StartTime,
			LastActivity:  o.LastActivity,
			ConflictsWith: append([]string(nil), ls.conflicts[o.ID]...),
		}
		sort.Strings(ai.ConflictsWith)
		ai.Conflict = len(ai.ConflictsWith) > 0
		switch {
		case o.ThemeID != "":
			ai.EffortID = effortNode(resolveTheme(o.ThemeID, efforts))
		case ai.WorkspaceID != "":
			ai.EffortID = b.nodes[ai.WorkspaceID].Workspace.EffortID
		default:
			ai.EffortID = effortNode(GroupUngrouped)
		}
		id := agentNodeID(o.ID)
		b.node(Node{ID: id, Kind: KindAgent, Label: o.Tool, Agent: ai})
		b.edge(ai.EffortID, id, EdgeRuns)
		b.edge(id, ai.WorkspaceID, EdgeWorksIn)
	}

	// The PR inventory (gh-97), after the bookmarks so that it can merge
	// into their PRs and attach to them.
	invStatus, invDiags, invMergedKnown := c.addInventory(b, ls, wsEffort, effortNode, &issueTally, &jiraTally, addJob)

	// Sprints of [jira] board and their tickets (gh-98). Keep this after
	// every other source of ticket nodes: it attaches a sprint ticket to the
	// sprint's group effort only when nothing else leads to it.
	sprintStatus := c.addSprints(b, effortNode, &jiraTally, addJob)
	s := &Snapshot{
		Schema:      SchemaVersion,
		GeneratedAt: ls.at,
		Days:        ls.days,
		Sources:     map[string]SourceStatus{},
		Diagnostics: dedupeStrings(slices.Concat(ls.diags, b.diags, b.noteDiagnostics())),
		Timezone:    zoneName(ls.at.Location()),
	}
	maps.Copy(s.Sources, ls.sources)
	var login, jira string
	if c.cfg.Identity != nil {
		login, jira = c.cfg.Identity()
	}
	s.People = ResolvePeople(c.cfg.People, c.cfg.Pair, login, jira)
	s.Sources[SourceGitHubPRs] = prTally.status()
	s.Sources[SourceGitHubIssues] = issueTally.status()
	s.Sources[SourceJira] = jiraTally.status()
	s.Sources[SourceGitHubInventory] = invStatus
	s.Diagnostics = dedupeStrings(append(s.Diagnostics, invDiags...))
	s.Sources[SourceJiraSprints] = sprintStatus

	for _, n := range b.nodes {
		s.Nodes = append(s.Nodes, *n)
	}
	sort.Slice(s.Nodes, func(i, j int) bool {
		a, z := s.Nodes[i], s.Nodes[j]
		if kindOrder[a.Kind] != kindOrder[z.Kind] {
			return kindOrder[a.Kind] < kindOrder[z.Kind]
		}
		return a.ID < z.ID
	})
	for e := range b.edges {
		s.Edges = append(s.Edges, e)
	}
	sort.Slice(s.Edges, func(i, j int) bool {
		a, z := s.Edges[i], s.Edges[j]
		if a.Source != z.Source {
			return a.Source < z.Source
		}
		if a.Target != z.Target {
			return a.Target < z.Target
		}
		return a.Kind < z.Kind
	})
	s.Counts = computeCounts(s, ls, effortBookmarks)
	if !invMergedKnown {
		s.Counts.Merged = nil
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].key() < jobs[j].key() })
	return s, jobs
}

// workspaceInfo describes workspace w of repository repo, attributed to the
// effort node effortID. The jj-derived fields are left empty when w could
// not be read.
func workspaceInfo(w *wsData, repo, repoSlug, effortID string, annotations map[string]string) *WorkspaceInfo {
	wi := &WorkspaceInfo{
		Slug:      filepath.Base(w.root),
		Repo:      repo,
		RepoSlug:  repoSlug,
		Path:      w.root,
		MainClone: w.mainClone,
		IsMain:    w.isMain,
		EffortID:  effortID,
	}
	if w.err != nil {
		wi.Error = jj.BriefError(w.err)
		return wi
	}
	wi.Bookmark = w.bookmark
	wi.BookmarksAhead = append([]string(nil), w.ahead...)
	wi.ChangeID = w.current.ChangeID
	wi.LastActivity = w.current.AuthorTimestamp
	for _, ch := range w.changes {
		wi.Changes = append(wi.Changes, ch.ChangeID)
		if wi.Description == "" && strings.TrimSpace(ch.Description) != "" {
			wi.Description = firstLine(ch.Description)
		}
		if ch.AuthorTimestamp.After(wi.LastActivity) {
			wi.LastActivity = ch.AuthorTimestamp
		}
	}
	wi.ChangesTruncated = w.truncated
	wi.ChangesTruncatedUnknown = w.truncatedUnknown
	if w.bookmark != "" {
		for i, name := range w.stack {
			if name == w.bookmark {
				wi.Stack = &StackPosition{Position: i + 1, Size: len(w.stack), Bookmarks: append([]string(nil), w.stack...)}
				break
			}
		}
		wi.Annotation = annotations[store.AnnotationKey(w.mainClone, w.bookmark)]
	}
	return wi
}

// ticketNode adds the ticket parsed from a bookmark and returns its ID.
// repoSlug is the owner/repo of the clone's GitHub origin; when it is empty,
// noGitHub says why and gh-N tickets cannot be looked up. The clone's
// directory-derived owner/repo is deliberately not used: a guessed repo would
// fetch some other project's issue.
func (c *Collector) ticketNode(b *builder, bookmark, repoName, repoSlug, noGitHub string, issues, jira *remoteTally, addJob func(Job)) string {
	return c.ticketNodeFor(b, spec.ParseTicketFromBranch(bookmark), repoName, repoSlug, noGitHub, issues, jira, addJob)
}

// ticketNodeFor adds the node of ticket, as spec.ParseTicketFromBranch
// returns it ("WGO-12", "GH-70"), and returns its ID, or "" for no ticket.
// The repository arguments are ticketNode's.
func (c *Collector) ticketNodeFor(b *builder, ticket, repoName, repoSlug, noGitHub string, issues, jira *remoteTally, addJob func(Job)) string {
	if ticket == "" {
		return ""
	}
	if n, ok := strings.CutPrefix(ticket, "GH-"); ok {
		num, err := strconv.Atoi(n)
		if err != nil {
			// The ticket regex guarantees digits, so only overflow reaches
			// here. Show the ticket rather than drop it.
			id := "ticket:gh:invalid#" + n
			b.node(Node{ID: id, Kind: KindTicket, Label: "gh-" + n, Ticket: &TicketInfo{
				Key: "gh-" + n, System: "github", Freshness: Unknown, Error: "issue number out of range"}})
			issues.add(Unknown, time.Time{})
			return id
		}
		if repoSlug == "" {
			// No GitHub remote (or jj could not say): the issue cannot be
			// looked up at all.
			id := githubTicketID(repoName, num)
			b.node(Node{ID: id, Kind: KindTicket, Label: "gh-" + n, Ticket: &TicketInfo{
				Key: "gh-" + n, System: "github", Freshness: Unknown, Error: noGitHub}})
			issues.add(Unknown, time.Time{})
			return id
		}
		owner, repo, _ := strings.Cut(repoSlug, "/")
		id := githubTicketID(repoSlug, num)
		if _, done := b.nodes[id]; done {
			return id
		}
		k := issuecache.Key{Owner: owner, Repo: repo, Number: num}
		res := issuecache.Read(k, c.cfg.IssueTTL)
		ti := &TicketInfo{Key: "gh-" + n, System: "github", FetchedAt: res.FetchedAt, Freshness: issueFreshness(res)}
		if res.Err != nil {
			ti.Error = res.Err.Error()
		}
		if ti.Freshness.hasData() {
			ti.Status, ti.Title, ti.URL = res.Info.State, res.Info.Title, res.Info.URL
		}
		if ti.URL == "" {
			ti.URL = "https://github.com/" + repoSlug + "/issues/" + n
		}
		issues.add(ti.Freshness, res.FetchedAt)
		if ti.Freshness != Fresh {
			addJob(Job{Kind: JobIssue, Issue: k})
		}
		b.node(Node{ID: id, Kind: KindTicket, Label: repoSlug + "#" + n, Ticket: ti})
		return id
	}
	return c.jiraTicketNode(b, ticket, jira, addJob)
}

// jiraTicketNode adds the Jira ticket with the given key, with its cached
// status and facts, and returns its ID. A ticket whose cache entry is not
// fresh is queued for refresh.
func (c *Collector) jiraTicketNode(b *builder, ticket string, jira *remoteTally, addJob func(Job)) string {
	id := jiraTicketID(ticket)
	if _, done := b.nodes[id]; done {
		return id
	}
	info, state, failed := jiracache.ReadFailed(ticket, c.cfg.JiraTTL)
	ti := &TicketInfo{Key: ticket, System: "jira"}
	switch {
	case state == jiracache.Miss:
		ti.Freshness = Unknown
	case failed:
		// A negative entry: the last lookup failed, so there is no status
		// to show. A real ticket with an empty status is a normal hit.
		ti.Freshness = Error
		ti.Error = "Jira lookup failed; check acli is installed and authenticated (acli jira auth status)"
	case state == jiracache.Fresh:
		ti.Freshness = Fresh
	default:
		ti.Freshness = Stale
	}
	if ti.Freshness.hasData() {
		applyJiraFacts(ti, info)
		for _, n := range info.Notes {
			b.note(n, ticket)
		}
	}
	site := c.cfg.JiraSite
	if site == "" {
		site = info.Site
	}
	if site != "" {
		ti.URL = "https://" + strings.TrimPrefix(strings.TrimPrefix(site, "https://"), "http://") + "/browse/" + ticket
	}
	jira.add(ti.Freshness, time.Time{})
	if ti.Freshness != Fresh {
		addJob(Job{Kind: JobJira, Ticket: ticket})
	}
	b.node(Node{ID: id, Kind: KindTicket, Label: ticket, Ticket: ti})
	return id
}

// prFreshness maps a PR cache result onto a Freshness.
func prFreshness(r prcache.Result) Freshness {
	switch r.State {
	case prcache.Fresh:
		return Fresh
	case prcache.Stale:
		return Stale
	}
	return missFreshness(r.Err)
}

// issueFreshness maps a GitHub issue cache result onto a Freshness.
func issueFreshness(r issuecache.Result) Freshness {
	switch r.State {
	case issuecache.Fresh:
		return Fresh
	case issuecache.Stale:
		return Stale
	}
	return missFreshness(r.Err)
}

// missFreshness is the Freshness of a cache miss: Error when the last
// lookup failed, otherwise Unknown.
func missFreshness(err error) Freshness {
	if err != nil {
		return Error
	}
	return Unknown
}

// prBucket is the PR-state count bucket for a PR.
func prBucket(p *PRInfo) string {
	if p.State == "open" && p.IsDraft {
		return "draft"
	}
	if p.State == "" {
		return "unknown"
	}
	return p.State
}

// computeCounts precomputes the per-effort small multiples.
func computeCounts(s *Snapshot, ls *localState, effortBookmarks map[string]map[string]bool) Counts {
	days := ls.days
	loc := ls.at.Location()
	y, m, d := ls.at.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, loc)
	start := today.AddDate(0, 0, -(days - 1))
	c := Counts{
		Activity: map[string][]int{},
		PRStates: map[string]map[string]int{},
		Agents:   map[string]map[string]int{},
		Merged:   map[string][]int{},
	}
	for i := range days {
		c.Days = append(c.Days, start.AddDate(0, 0, i).Format("2006-01-02"))
	}
	for _, n := range s.Nodes {
		if n.Kind == KindEffort {
			c.Activity[n.ID] = make([]int, days)
			c.PRStates[n.ID] = map[string]int{}
			c.Agents[n.ID] = map[string]int{}
			c.Merged[n.ID] = make([]int, days)
		}
	}

	// Activity: distinct changes per effort per local day.
	// mergedUnknown holds efforts whose merged count cannot be known; a
	// workspace jj could not read hides its bookmarks, and so their PRs.
	seen := map[string]bool{}
	mergedUnknown := map[string]bool{}
	for _, w := range ls.workspaces {
		n := s.Node(w.id)
		eff := n.Workspace.EffortID
		if w.err != nil {
			mergedUnknown[eff] = true
			continue
		}
		for _, ch := range w.changes {
			t := ch.AuthorTimestamp.In(loc)
			day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
			idx := int(day.Sub(start).Hours()/24 + 0.5)
			if day.Before(start) || idx < 0 || idx >= days {
				continue
			}
			k := eff + "\x00" + ch.ChangeID
			if seen[k] {
				continue
			}
			seen[k] = true
			c.Activity[eff][idx]++
		}
	}

	// PR states per effort, over its bookmarks and the PRs it tracks.
	// prEfforts records which efforts each PR counted under, for Merged.
	prsByBookmark := map[string][]*Node{}
	prByID := map[string]*Node{}
	prEfforts := map[*Node][]string{}
	for i := range s.Nodes {
		if p := s.Nodes[i].PR; p != nil {
			prByID[s.Nodes[i].ID] = &s.Nodes[i]
			if p.BookmarkID != "" {
				prsByBookmark[p.BookmarkID] = append(prsByBookmark[p.BookmarkID], &s.Nodes[i])
			}
		}
	}
	countPR := func(eff string, n *Node) {
		if slices.Contains(prEfforts[n], eff) {
			return
		}
		prEfforts[n] = append(prEfforts[n], eff)
		c.PRStates[eff][prBucket(n.PR)]++
	}
	for eff, bms := range effortBookmarks {
		for bm := range bms {
			n := s.Node(bm)
			if n == nil {
				continue
			}
			for _, p := range prsByBookmark[bm] {
				countPR(eff, p)
			}
			// A PR the inventory attached to a bookmark does not make the
			// bookmark's own lookup known: it may have missed others.
			switch {
			case n.Bookmark.PRLookup == Unknown || n.Bookmark.PRLookup == Error:
				c.PRStates[eff]["unknown"]++
				mergedUnknown[eff] = true
			case len(prsByBookmark[bm]) == 0:
				c.PRStates[eff]["none"]++
			}
		}
	}
	for _, e := range s.Edges {
		if e.Kind != EdgeTracks || c.PRStates[e.Source] == nil {
			continue
		}
		if n := prByID[e.Target]; n != nil {
			countPR(e.Source, n)
		}
	}

	// Merged PRs per effort per local day of merge. An effort with a PR of
	// unknown state, a bookmark whose lookup is unknown or failed, or a
	// workspace jj could not read has no series rather than a misleading
	// zero. So does one with a merged PR whose merge time is unknown, unless
	// its last update (never before the merge) precedes the window.
	for n, effs := range prEfforts {
		if n.PR.State == "" {
			for _, eff := range effs {
				mergedUnknown[eff] = true
			}
		}
	}
	for n, effs := range prEfforts {
		if n.PR.State != "merged" {
			continue
		}
		if n.PR.MergedAt.IsZero() {
			if n.PR.UpdatedAt.IsZero() || !n.PR.UpdatedAt.Before(start) {
				for _, eff := range effs {
					mergedUnknown[eff] = true
				}
			}
			continue
		}
		t := n.PR.MergedAt.In(loc)
		day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		idx := int(day.Sub(start).Hours()/24 + 0.5)
		if day.Before(start) || idx < 0 || idx >= days {
			continue
		}
		for _, eff := range effs {
			if c.Merged[eff] != nil {
				c.Merged[eff][idx]++
			}
		}
	}
	for eff := range mergedUnknown {
		delete(c.Merged, eff)
	}
	if !mergedKnown(s) {
		c.Merged = nil
	}

	for _, n := range s.Nodes {
		if n.Agent != nil {
			c.Agents[n.Agent.EffortID][string(n.Agent.Liveness)]++
		}
	}
	return c
}

// zoneName is loc's IANA name, resolving time.Local through $TZ or the
// /etc/localtime link, or "" when no loadable name is found.
func zoneName(loc *time.Location) string {
	name := loc.String()
	if loc == time.Local {
		name = strings.TrimPrefix(os.Getenv("TZ"), ":")
		if name == "" {
			if target, err := os.Readlink("/etc/localtime"); err == nil {
				if _, after, ok := strings.Cut(target, "zoneinfo/"); ok {
					name = after
				}
			}
		}
	}
	if name == "" || name == "Local" {
		return ""
	}
	if _, err := time.LoadLocation(name); err != nil {
		return ""
	}
	return name
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
