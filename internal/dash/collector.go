package dash

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/effort"
	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/plan"
	"github.com/virtru/wgo/internal/store"
)

// Config wires a Collector to its data sources.
type Config struct {
	// JJ runs jj read queries. The collector always uses JJ.ReadOnly(), so
	// every call carries --ignore-working-copy and never snapshots a
	// workspace or writes the operation log.
	JJ *jj.CLIClient
	// Discover lists candidate workspaces (discovery.FromConfig(cfg).DiscoverAll).
	Discover func() ([]discovery.DiscoveredRepo, error)
	// Store reads the plan and state. It is never written.
	Store effort.Source
	// AgentPolicy decides agent-session liveness. Sessions are only observed:
	// never created, heartbeated or pruned.
	AgentPolicy store.AgentPolicy
	// Days is the activity window (DefaultDays when zero).
	Days int
	// Workers bounds concurrent per-workspace jj queries (4 when zero).
	Workers int
	// Cache TTLs for remote data.
	PRTTL, JiraTTL, IssueTTL time.Duration
	// JiraSite is the Jira host used for browse links, when configured.
	JiraSite string
	// Now is the clock (time.Now when nil).
	Now func() time.Time
}

// Collector builds snapshots. It runs outside HTTP request handling.
type Collector struct {
	cfg Config
	jj  *jj.CLIClient
}

// NewCollector returns a Collector for cfg.
func NewCollector(cfg Config) *Collector {
	if cfg.Days <= 0 {
		cfg.Days = DefaultDays
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.PRTTL <= 0 {
		cfg.PRTTL = 2 * time.Minute
	}
	if cfg.JiraTTL <= 0 {
		cfg.JiraTTL = 10 * time.Minute
	}
	if cfg.IssueTTL <= 0 {
		cfg.IssueTTL = 10 * time.Minute
	}
	c := &Collector{cfg: cfg}
	if cfg.JJ != nil {
		c.jj = cfg.JJ.ReadOnly()
	}
	return c
}

// localState is everything read from the local machine: jj, plan, state and
// agent sessions. It is turned into a Snapshot by assemble, which adds the
// cached remote data.
type localState struct {
	at         time.Time
	days       int
	workspaces []*wsData
	clones     []effort.MainCloneInfo
	origins    map[string]string // main clone path -> origin URL
	// ghSlugs maps a main clone to the owner/repo of its GitHub origin. A
	// clone without one is in noGitHub with the reason, for ticket lookups.
	ghSlugs     map[string]string
	noGitHub    map[string]string
	attribution *effort.AttributionResult
	sessions    []store.ObservedSession
	conflicts   map[string][]string
	annotations map[string]string // AnnotationKey -> purpose
	sources     map[string]SourceStatus
	diags       []string
}

// wsData is one discovered workspace and what jj said about it.
type wsData struct {
	id        string
	root      string // cleaned discovery path, used for attribution
	canonical string // symlink-resolved root, used for IDs and dedupe
	mainClone string
	isMain    bool
	bookmark  string
	current   jj.Change
	changes   []jj.Change // newest first, within ChangeWindow
	truncated int
	stack     []string // bottom-up
	err       error
}

// workspaceSet is the deduplicated set of discovered workspaces.
type workspaceSet struct {
	list  []*wsData
	diags []string
}

// discoverWorkspaces runs discovery, expands every main clone through
// `jj workspace list` (read-only) and deduplicates by main clone plus
// workspace root. Filesystem entries win over jj-listed ones.
func (c *Collector) discoverWorkspaces() (workspaceSet, error) {
	var set workspaceSet
	if c.cfg.Discover == nil {
		return set, nil
	}
	repos, err := c.cfg.Discover()
	if err != nil {
		return set, err
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Path < repos[j].Path })

	seen := map[string]bool{}
	add := func(root, main string) {
		root, main = filepath.Clean(root), filepath.Clean(main)
		key := canonicalPath(main) + "\x00" + canonicalPath(root)
		if seen[key] {
			return
		}
		seen[key] = true
		set.list = append(set.list, &wsData{
			id:        WorkspaceID(main, root),
			root:      root,
			canonical: canonicalPath(root),
			mainClone: main,
			isMain:    canonicalPath(main) == canonicalPath(root),
		})
	}
	var mains []string
	mainSeen := map[string]bool{}
	for _, r := range repos {
		main := filepath.Clean(effort.ResolveMainClone(r))
		add(r.Path, main)
		if !mainSeen[canonicalPath(main)] {
			mainSeen[canonicalPath(main)] = true
			mains = append(mains, main)
		}
	}
	if c.jj != nil {
		for _, main := range mains {
			wss, err := c.jj.ListWorkspaces(main)
			if err != nil {
				set.diags = append(set.diags, fmt.Sprintf("repo %s: could not list workspaces: %s", main, jj.BriefError(err)))
				continue
			}
			for _, w := range wss {
				if w.Path == "" {
					// jj reports no root once the workspace directory is gone.
					set.diags = append(set.diags, fmt.Sprintf("repo %s: workspace %q is listed by jj but its directory is gone; if it was deleted, run: jj -R %s workspace forget %s",
						main, w.Name, main, w.Name))
					continue
				}
				if fi, err := os.Stat(filepath.Join(w.Path, ".jj")); err != nil || !fi.IsDir() {
					set.diags = append(set.diags, fmt.Sprintf("repo %s: workspace %q is listed by jj but %s has no .jj directory; if it was deleted, run: jj -R %s workspace forget %s",
						main, w.Name, w.Path, main, w.Name))
					continue
				}
				add(w.Path, main)
			}
		}
	}
	sort.Slice(set.list, func(i, j int) bool { return set.list[i].root < set.list[j].root })
	return set, nil
}

// collectLocal reads jj, the plan, state and agent sessions.
func (c *Collector) collectLocal(ctx context.Context) (*localState, error) {
	ls := &localState{
		at:          c.cfg.Now(),
		days:        c.cfg.Days,
		origins:     map[string]string{},
		ghSlugs:     map[string]string{},
		noGitHub:    map[string]string{},
		annotations: map[string]string{},
		sources:     map[string]SourceStatus{},
	}
	set, err := c.discoverWorkspaces()
	if err != nil {
		return nil, fmt.Errorf("discover workspaces: %w", err)
	}
	ls.workspaces = set.list
	ls.diags = append(ls.diags, set.diags...)

	c.readWorkspaces(ctx, ls.workspaces)
	jjStatus := SourceStatus{}
	for _, w := range ls.workspaces {
		if w.err != nil {
			jjStatus.Error++
		} else {
			jjStatus.Fresh++
		}
	}
	if jjStatus.Error > 0 {
		jjStatus.Detail = fmt.Sprintf("%d of %d workspaces could not be read", jjStatus.Error, len(ls.workspaces))
	}
	jjStatus.State = aggregate(jjStatus)
	ls.sources[SourceJJ] = jjStatus

	// Main clones, with owner/repo from the origin remote when it is GitHub.
	cloneSeen := map[string]bool{}
	for _, w := range ls.workspaces {
		if cloneSeen[w.mainClone] {
			continue
		}
		cloneSeen[w.mainClone] = true
		clone := effort.MainCloneInfo{
			Path:  w.mainClone,
			Name:  filepath.Base(w.mainClone),
			Owner: filepath.Base(filepath.Dir(w.mainClone)),
			Repo:  filepath.Base(w.mainClone),
		}
		ls.noGitHub[w.mainClone] = "no GitHub remote"
		if c.jj != nil {
			remotes, err := c.jj.RemoteURLs(w.mainClone)
			if err != nil {
				// Not "no remote": jj failed, so say so rather than let every
				// gh-N ticket claim the repo has no GitHub remote.
				ls.diags = append(ls.diags, fmt.Sprintf("repo %s: could not read remotes: %s", w.mainClone, jj.BriefError(err)))
				ls.noGitHub[w.mainClone] = "could not read remotes (jj git remote list failed)"
			} else {
				ls.origins[w.mainClone] = remotes["origin"]
				slug := github.SlugFromRemoteURL(remotes["origin"])
				if owner, repo, ok := strings.Cut(slug, "/"); ok {
					clone.Owner, clone.Repo = owner, repo
					ls.ghSlugs[w.mainClone] = slug
					delete(ls.noGitHub, w.mainClone)
				}
			}
		}
		ls.clones = append(ls.clones, clone)
	}
	sort.Slice(ls.clones, func(i, j int) bool { return ls.clones[i].Path < ls.clones[j].Path })

	// Plan and state: read only.
	var state *store.State
	var p *plan.Plan
	planStatus := SourceStatus{State: Fresh}
	agentStatus := SourceStatus{State: Fresh}
	if c.cfg.Store != nil {
		if state, err = c.cfg.Store.LoadState(); err != nil {
			state = nil
			agentStatus = SourceStatus{State: Error, Detail: "load state: " + err.Error()}
			ls.diags = append(ls.diags, "load state: "+err.Error())
		}
		content, err := c.cfg.Store.LoadPlan()
		if err == nil {
			p, err = plan.Parse(content)
		}
		if err != nil {
			p = nil
			planStatus = SourceStatus{State: Error, Detail: "plan: " + err.Error()}
			ls.diags = append(ls.diags, "plan: "+err.Error())
		}
	}
	if state == nil {
		state = store.NewState()
	}
	if p != nil {
		ls.diags = append(ls.diags, p.Diagnostics...)
	}
	for key, a := range state.Annotations {
		if a.Purpose != "" {
			ls.annotations[key] = a.Purpose
		}
	}

	ls.sessions = state.ObserveAgentSessions(ls.at, c.cfg.AgentPolicy)
	ls.conflicts = store.AgentConflicts(ls.sessions)
	agentStatus.Fresh = len(ls.sessions)
	ls.sources[SourceAgents] = agentStatus

	var planEfforts map[string]plan.EffortEntry
	if p != nil {
		planEfforts = p.Efforts
	}
	merged, mergeDiags := effort.MergeEfforts(state.Efforts, planEfforts)
	ls.diags = append(ls.diags, mergeDiags...)
	if len(merged) > 0 {
		planStatus.Fresh = len(merged)
	}
	ls.sources[SourcePlan] = planStatus

	// gh-72 ThemeIDs: a workspace whose visible sessions name exactly one
	// theme is attributed to it, resolved to an effort when it names one.
	themes := map[string]map[string]bool{}
	for _, o := range ls.sessions {
		if o.ThemeID == "" {
			continue
		}
		k := canonicalPath(o.WorktreePath)
		if themes[k] == nil {
			themes[k] = map[string]bool{}
		}
		themes[k][resolveTheme(o.ThemeID, merged)] = true
	}
	override := func(wsPath string) string {
		t := themes[canonicalPath(wsPath)]
		if len(t) != 1 {
			return ""
		}
		for k := range t {
			return k
		}
		return ""
	}

	infos := make([]effort.WorkspaceInfo, 0, len(ls.workspaces))
	for _, w := range ls.workspaces {
		infos = append(infos, effort.WorkspaceInfo{Path: w.root, MainRepoPath: w.mainClone, Bookmark: w.bookmark})
	}
	ls.attribution = effort.AttributeWorkspaces(infos, ls.clones, merged, override)
	ls.diags = append(ls.diags, ls.attribution.Diagnostics...)
	return ls, nil
}

// resolveTheme maps an agent ThemeID onto an effort ID when it names one by
// ID or by a unique name; otherwise it is returned unchanged.
func resolveTheme(theme string, efforts map[string]effort.MergedEffort) string {
	if _, ok := efforts[theme]; ok {
		return theme
	}
	match := ""
	for id, e := range efforts {
		if strings.EqualFold(e.Name, theme) {
			if match != "" {
				return theme
			}
			match = id
		}
	}
	if match != "" {
		return match
	}
	return theme
}

// readWorkspaces runs the per-workspace jj queries with bounded concurrency.
// A failure is recorded on that workspace and never fails the collection.
func (c *Collector) readWorkspaces(ctx context.Context, wss []*wsData) {
	if c.jj == nil {
		for _, w := range wss {
			w.err = fmt.Errorf("jj unavailable")
		}
		return
	}
	jjc := c.jj.WithContext(ctx)
	sem := make(chan struct{}, c.cfg.Workers)
	var wg sync.WaitGroup
	for _, w := range wss {
		wg.Add(1)
		sem <- struct{}{}
		go func(w *wsData) {
			defer wg.Done()
			defer func() { <-sem }()
			w.err = readWorkspace(jjc, w)
		}(w)
	}
	wg.Wait()
}

// mutableRange is the revset of a workspace's unmerged history: ancestors of
// @ not yet in trunk, pushed or not. It is not jj's mutable() set.
const mutableRange = "trunk()..@"

func readWorkspace(jjc *jj.CLIClient, w *wsData) error {
	cur, err := jjc.Log(w.root, "@")
	if err != nil {
		return err
	}
	if len(cur) == 0 {
		return fmt.Errorf("jj log @: no entries")
	}
	w.current = cur[0]
	if w.bookmark, err = jjc.NearestBookmark(w.root); err != nil {
		return err
	}
	entries, err := jjc.Log(w.root, fmt.Sprintf("latest(%s, %d)", mutableRange, ChangeWindow))
	if err != nil {
		return err
	}
	if len(entries) >= ChangeWindow {
		total, err := jjc.CountRevset(w.root, mutableRange)
		if err == nil && total > len(entries) {
			w.truncated = total - len(entries)
		}
	}
	for _, e := range entries {
		if e.Empty && strings.TrimSpace(e.Description) == "" {
			continue // the empty working-copy change, or other placeholders
		}
		w.changes = append(w.changes, e)
	}
	// Log is children-first, so bookmarks read newest first; the stack is
	// listed bottom-up.
	for i := len(entries) - 1; i >= 0; i-- {
		w.stack = append(w.stack, entries[i].Bookmarks...)
	}
	return nil
}

// Collect builds a snapshot from local data and cached remote data only. It
// never makes a network call. The returned jobs name the remote items whose
// cache entries are not fresh; a Refresher can warm them.
func (c *Collector) Collect(ctx context.Context) (*Snapshot, []Job, error) {
	ls, err := c.collectLocal(ctx)
	if err != nil {
		return nil, nil, err
	}
	s, jobs := c.assemble(ls)
	return s, jobs, nil
}

// aggregate folds per-item counts into a source state.
func aggregate(s SourceStatus) Freshness {
	switch {
	case s.Error > 0:
		return Error
	case s.Unknown > 0:
		return Unknown
	case s.Stale > 0:
		return Stale
	default:
		return Fresh
	}
}

// Discovered reports the currently discovered workspaces by ID. It runs
// discovery and read-only `jj workspace list`, but no per-workspace queries.
func (c *Collector) Discovered() (map[string]Target, error) {
	set, err := c.discoverWorkspaces()
	if err != nil {
		return nil, err
	}
	out := make(map[string]Target, len(set.list))
	for _, w := range set.list {
		if fi, err := os.Stat(filepath.Join(w.root, ".jj")); err != nil || !fi.IsDir() {
			continue
		}
		out[w.id] = Target{ID: w.id, Root: w.root, MainClone: w.mainClone}
	}
	return out, nil
}

// Target is a resolved workspace.
type Target struct {
	ID        string
	Root      string
	MainClone string
}
