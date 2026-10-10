package dash

import (
	"maps"
	"path/filepath"
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
}

func (b *builder) node(n Node) *Node {
	if existing, ok := b.nodes[n.ID]; ok {
		return existing
	}
	p := &n
	b.nodes[n.ID] = p
	return p
}

func (b *builder) edge(src, dst, kind string) {
	if src == "" || dst == "" {
		return
	}
	b.edges[Edge{Source: src, Target: dst, Kind: kind}] = true
}

var kindOrder = map[NodeKind]int{
	KindEffort: 0, KindWorkspace: 1, KindBookmark: 2, KindPR: 3, KindTicket: 4, KindAgent: 5,
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
		bi := &BookmarkInfo{Name: w.bookmark, Repo: cl.Name, PRFetchedAt: res.FetchedAt, PRCount: len(res.PRs)}
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
				b.node(Node{ID: id, Kind: KindPR, Label: label, PR: &PRInfo{
					Number:             pr.Number,
					Repo:               repoKey,
					URL:                pr.URL,
					Title:              pr.Title,
					State:              strings.ToLower(pr.State),
					IsDraft:            pr.IsDraft,
					ReviewDecision:     pr.ReviewDecision,
					Checks:             pr.Checks.State,
					RequestedReviewers: append([]string(nil), pr.RequestedReviewers...),
					ReviewersKnown:     res.ReviewersKnown,
					UpdatedAt:          pr.UpdatedAt,
					Freshness:          bi.PRLookup,
					BookmarkID:         bmID,
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
			Liveness:      string(o.Liveness),
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

	s := &Snapshot{
		Schema:      SchemaVersion,
		GeneratedAt: ls.at,
		Days:        ls.days,
		Sources:     map[string]SourceStatus{},
		Diagnostics: dedupeStrings(ls.diags),
	}
	maps.Copy(s.Sources, ls.sources)
	s.Sources[SourceGitHubPRs] = prTally.status()
	s.Sources[SourceGitHubIssues] = issueTally.status()
	s.Sources[SourceJira] = jiraTally.status()

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
	ticket := spec.ParseTicketFromBranch(bookmark)
	if ticket == "" {
		return ""
	}
	if n, ok := strings.CutPrefix(ticket, "GH-"); ok {
		num, err := strconv.Atoi(n)
		if err != nil {
			return ""
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
		ti.Status, ti.Assignee = info.Status, info.Assignee
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
	}
	for i := range days {
		c.Days = append(c.Days, start.AddDate(0, 0, i).Format("2006-01-02"))
	}
	for _, n := range s.Nodes {
		if n.Kind == KindEffort {
			c.Activity[n.ID] = make([]int, days)
			c.PRStates[n.ID] = map[string]int{}
			c.Agents[n.ID] = map[string]int{}
		}
	}

	// Activity: distinct changes per effort per local day.
	seen := map[string]bool{}
	for _, w := range ls.workspaces {
		if w.err != nil {
			continue
		}
		n := s.Node(w.id)
		eff := n.Workspace.EffortID
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

	// PR states per effort, over its bookmarks.
	prsByBookmark := map[string][]*PRInfo{}
	for i := range s.Nodes {
		if p := s.Nodes[i].PR; p != nil {
			prsByBookmark[p.BookmarkID] = append(prsByBookmark[p.BookmarkID], p)
		}
	}
	for eff, bms := range effortBookmarks {
		for bm := range bms {
			n := s.Node(bm)
			switch {
			case n == nil:
			case n.Bookmark.PRLookup == Unknown || n.Bookmark.PRLookup == Error:
				c.PRStates[eff]["unknown"]++
			case len(prsByBookmark[bm]) == 0:
				c.PRStates[eff]["none"]++
			default:
				for _, p := range prsByBookmark[bm] {
					c.PRStates[eff][prBucket(p)]++
				}
			}
		}
	}

	for _, n := range s.Nodes {
		if n.Agent != nil {
			c.Agents[n.Agent.EffortID][n.Agent.Liveness]++
		}
	}
	return c
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
