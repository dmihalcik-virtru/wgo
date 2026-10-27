package cleanup

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
)

// Workspace classification answers, for each non-default jj workspace, the
// question `wgo clean` has to get right: does this workspace hold anything
// that exists nowhere else? Every rule is deterministic — it reads the jj DAG
// and, when available, GitHub's record of a PR — so the same repository state
// always gets the same answer.
//
// A workspace's "work" is its non-empty mutable changes beyond trunk. The
// rules, in order:
//
//   - no work: idle once untouched for StaleAfter, unless a bookmark of its
//     own marks work not started yet (planned) or it checks out someone
//     else's open PR (active)
//   - all work on a bookmark (local or remote): active while that bookmark
//     has an open PR or none, removable once every PR merged
//   - landed: every uncovered change is a commit of a merged PR, or trunk
//     already contains the work's content (see contain.go)
//   - deletions only: the work only deletes files, so nothing in it is unique
//
// Those are proven and --yes removes them. What remains needs a person, and
// is shown with the evidence found: an older revision of something merged or
// bookmarked (superseded), a closed PR, rebase conflicts, scratch work,
// subjects that match trunk without proof (probably landed), and plain
// unpushed work.

// Tier says what `wgo clean` may do with a classified workspace.
type Tier int

const (
	// TierKeep workspaces are not offered for removal.
	TierKeep Tier = iota
	// TierRemove workspaces are proven to hold nothing that exists only
	// there; --yes removes them.
	TierRemove
	// TierDecide workspaces are shown with their evidence and removed only
	// when the user confirms each one; --yes never removes them.
	TierDecide
)

// WorkspaceType names the rule that classified a workspace.
type WorkspaceType string

// Workspace types, grouped by tier.
const (
	TypeIdle          WorkspaceType = "idle"           // remove: no work beyond trunk, untouched
	TypeLanded        WorkspaceType = "landed"         // remove: work proven merged or on trunk
	TypeDeletionsOnly WorkspaceType = "deletions-only" // remove: work only deletes files

	TypeSuperseded     WorkspaceType = "superseded"      // decide: an older revision of merged or bookmarked work
	TypeClosedPR       WorkspaceType = "closed-pr"       // decide: work on a bookmark whose PR closed unmerged
	TypeConflicted     WorkspaceType = "conflicted"      // decide: work is rebase conflicts
	TypeScratch        WorkspaceType = "scratch"         // decide: marked scratch, or only lockfiles differ
	TypeProbablyLanded WorkspaceType = "probably-landed" // decide: subjects match trunk, content not proven
	TypeUnpushed       WorkspaceType = "unpushed"        // decide: work that exists only here

	TypeFresh      WorkspaceType = "fresh"       // keep: no work yet, used recently
	TypeInProgress WorkspaceType = "in-progress" // keep: unproven work, edited recently
	TypePlanned    WorkspaceType = "planned"     // keep: no work yet, but a bookmark of its own
	TypeActive     WorkspaceType = "active"      // keep: work on a bookmark with an open PR or none
	TypeUnreadable WorkspaceType = "unreadable"  // keep: jj could not read it
)

// WorkspaceRef identifies a workspace to classify.
type WorkspaceRef struct {
	Name          string // jj workspace name
	Path          string // workspace root
	RepoPath      string // main clone, for GitHub lookups
	DefaultBranch string // trunk bookmark name, never counted as the workspace's own
}

// WorkSummary describes a workspace's work for display.
type WorkSummary struct {
	Changes        int       // non-empty mutable changes beyond trunk
	Newest         time.Time // author time of the newest change
	Subject        string    // first line of the newest described change
	Files          int       // files changed beyond trunk
	Added, Deleted int       // lines changed beyond trunk
}

// Classification is the verdict on one workspace.
type Classification struct {
	Type WorkspaceType
	Tier Tier
	// Evidence lists the facts behind the verdict, most decisive first.
	Evidence []string
	// Hint is a command that resolves a TierDecide case, if there is one.
	Hint string
	Work WorkSummary
	// PR is the pull request the evidence refers to, if any.
	PR *github.PRInfo
	// AtCommit is @'s commit id when classified; a workspace whose @ has
	// moved since must be classified again before it is removed.
	AtCommit string
	// AtChange is @'s change id when classified.
	AtChange string
	// Bookmarks are the bookmarks on @.
	Bookmarks []string
	// Ref is the workspace that was classified.
	Ref WorkspaceRef
	// LastUsed is when the workspace was created or jj last saw its files
	// change, whichever is later.
	LastUsed time.Time
}

// Reason is the one-line verdict: the type and its most decisive evidence.
func (c Classification) Reason() string {
	if len(c.Evidence) == 0 {
		return string(c.Type)
	}
	return string(c.Type) + ": " + c.Evidence[0]
}

// String renders the summary as "2 changes, newest 3d ago, 4 files (+10 -2)".
func (w WorkSummary) String() string {
	if w.Changes == 0 {
		return "no changes"
	}
	s := plural(w.Changes, "change")
	if !w.Newest.IsZero() {
		s += ", newest " + formatAge(w.Newest)
	}
	if w.Files > 0 {
		s += fmt.Sprintf(", %s (+%d -%d)", plural(w.Files, "file"), w.Added, w.Deleted)
	}
	return s
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// Reader is the read-only jj surface the classifier needs. *jj.CLIClient
// satisfies it; pass a ReadOnly() client so classifying never snapshots a
// workspace. Every repo argument is a workspace root, so `@` is that
// workspace's working copy.
type Reader interface {
	Log(repo, revset string) ([]jj.LogEntry, error)
	LogMembership(repo, revset string, sets []string) ([]jj.Membership, error)
	DiffSummaryBetween(repo, from, to string) ([]jj.FileChange, error)
	DiffStatBetween(repo, from, to string) (added, deleted int, err error)
	DiffNamesBetween(repo, from, to string, paths []string) ([]string, error)
	DiffGitBetween(repo, from, to string, paths []string) (string, error)
	RefNames(repo, revset string, remote bool) ([]string, error)
}

// ClassifyOptions bound the classifier's work.
type ClassifyOptions struct {
	// StaleAfter is how long a workspace with no work must sit untouched
	// before it counts as idle; zero or less means two weeks.
	StaleAfter time.Duration
	// MaxCandidates caps the trunk commits the content proof tries.
	MaxCandidates int
	// MaxFiles skips the content proof for work touching more files.
	MaxFiles int
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// LastUsed reports when a workspace was created or last had its files
	// change; nil means WorkingCopyTouched. A zero time means unknown, and @'s
	// author time is used instead.
	LastUsed func(wsPath string) time.Time
}

// defaultStaleAfter is the idle threshold when none is configured, matching
// status.stale_days' default.
const defaultStaleAfter = 14 * 24 * time.Hour

// Classifier classifies workspaces. It is safe for concurrent use when its
// Reader and PRSource are.
type Classifier struct {
	jj   Reader
	prs  PRSource
	opts ClassifyOptions
}

// NewClassifier returns a Classifier reading jj through r and GitHub through
// prs, which may be nil. PR lookups are cached for the classifier's lifetime.
func NewClassifier(r Reader, prs PRSource, opts ClassifyOptions) *Classifier {
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = defaultStaleAfter
	}
	if opts.MaxCandidates <= 0 {
		opts.MaxCandidates = 40
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 300
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.LastUsed == nil {
		opts.LastUsed = WorkingCopyTouched
	}
	c := &Classifier{jj: r, opts: opts}
	if prs != nil {
		c.prs = newMemoPRs(prs)
	}
	return c
}

// Revsets, evaluated inside the workspace.
const (
	revWork       = "trunk()..@ & mutable() ~ empty()"
	revFork       = "heads(::@ & ::trunk())"
	revBookmarked = "bookmarks() | remote_bookmarks()"
	revCovered    = "::(" + revBookmarked + ")"
	// revForeign is the immutable history beyond trunk: a release tag, or
	// someone else's PR checked out.
	revForeign = "(trunk()..@ ~ empty()) & immutable()"
	// revCovering is the bookmarked commits that hold some of the work.
	revCovering = "(" + revBookmarked + ") & descendants(" + revWork + ")"
	// revSurvey is every commit the first pass looks at: @, everything
	// beyond trunk, the fork point, and the bookmarks holding the work.
	revSurvey = "@ | (trunk()..@) | " + revFork + " | " + revCovering
)

// surveySets are the questions the survey asks about each commit, indexed by
// the in* constants.
var surveySets = []string{revCovered, revFork, "conflicts()", "trunk()..@", revCovering}

const (
	inCovered = iota
	inFork
	inConflict
	inBeyond
	inCovering
)

// survey is what one jj invocation tells the classifier about a workspace.
type survey struct {
	at         jj.LogEntry
	work       []jj.LogEntry // non-empty mutable changes beyond trunk
	uncovered  []jj.LogEntry // work no bookmark reaches
	conflicted int           // uncovered changes with conflicts
	foreign    []jj.LogEntry // non-empty immutable commits beyond trunk
	fork       *jj.LogEntry  // where @'s history leaves trunk; nil if not exactly one
	own        []string      // local bookmarks on the fork or beyond trunk
	covering   []string      // local and remote bookmarks holding some of the work
}

// uniqueNames sorts and deduplicates bookmark names, dropping the trunk
// bookmark, which is never a workspace's own.
func uniqueNames(names []string, trunk string) []string {
	slices.Sort(names)
	names = slices.Compact(names)
	return slices.DeleteFunc(names, func(n string) bool { return n == trunk })
}

func (c *Classifier) survey(ws WorkspaceRef) (survey, error) {
	var s survey
	entries, err := c.jj.LogMembership(ws.Path, revSurvey, surveySets)
	if err != nil {
		return s, err
	}
	forks := 0
	foundAt := false
	for i := range entries {
		m := entries[i]
		if m.CurrentWorkingCopy {
			s.at, foundAt = m.LogEntry, true
		}
		if m.In[inFork] {
			forks++
			s.fork = &entries[i].LogEntry
		}
		if m.In[inFork] || m.In[inBeyond] {
			s.own = append(s.own, m.LocalBookmarks...)
		}
		if m.In[inCovering] {
			s.covering = append(append(s.covering, m.LocalBookmarks...), m.RemoteBookmarks...)
		}
		if !m.In[inBeyond] || m.Empty {
			continue
		}
		if m.Immutable {
			s.foreign = append(s.foreign, m.LogEntry)
			continue
		}
		s.work = append(s.work, m.LogEntry)
		if !m.In[inCovered] {
			s.uncovered = append(s.uncovered, m.LogEntry)
			if m.In[inConflict] {
				s.conflicted++
			}
		}
	}
	if !foundAt {
		return s, fmt.Errorf("jj returned no working copy")
	}
	if forks != 1 {
		s.fork = nil
	}
	s.own = uniqueNames(s.own, ws.DefaultBranch)
	s.covering = uniqueNames(s.covering, ws.DefaultBranch)
	return s, nil
}

var (
	prWorkspaceRe = regexp.MustCompile(`^pr-(\d+)-`)
	prSuffixRe    = regexp.MustCompile(`^(.*) \(#(\d+)\)$`)
	scratchRe     = regexp.MustCompile(`(?i)^\s*(scratch|wip|tmp|temp)\b|do not push|don't push`)
)

// lockfiles are generated files whose differences carry no authored content.
var lockfiles = map[string]bool{
	"package-lock.json": true, "npm-shrinkwrap.json": true, "yarn.lock": true,
	"pnpm-lock.yaml": true, "bun.lockb": true, "go.sum": true, "Cargo.lock": true,
	"poetry.lock": true, "uv.lock": true, "Pipfile.lock": true, "Gemfile.lock": true,
	"composer.lock": true, "flake.lock": true,
}

func isLockfile(p string) bool { return lockfiles[path.Base(p)] }

// Classify classifies one live workspace.
func (c *Classifier) Classify(ws WorkspaceRef) Classification {
	cl := c.classify(ws)
	cl.Ref = ws
	return cl
}

func (c *Classifier) classify(ws WorkspaceRef) Classification {
	s, err := c.survey(ws)
	if err != nil {
		return unreadable(err)
	}
	cl := Classification{AtCommit: s.at.CommitID, AtChange: s.at.ChangeID, Bookmarks: s.at.Bookmarks}
	if cl.LastUsed = c.opts.LastUsed(ws.Path); cl.LastUsed.IsZero() {
		cl.LastUsed = s.at.AuthorTimestamp
	}

	if len(s.work) == 0 {
		return c.classifyIdle(ws, s, cl)
	}
	cl.Work = summarize(s.work)
	if len(s.uncovered) == 0 {
		return c.classifyCovered(ws, s, cl)
	}
	verdict := c.classifyWork(ws, s, cl)
	if verdict.Tier == TierDecide && c.recent(cl.LastUsed) {
		return cl.with(TypeInProgress, TierKeep, "edited here "+formatAge(cl.LastUsed)+"; "+verdict.Reason())
	}
	return verdict
}

// recent reports whether t is within StaleAfter of now.
func (c *Classifier) recent(t time.Time) bool {
	return c.opts.Now().Sub(t) < c.opts.StaleAfter
}

// WorkingCopyTouched returns when the workspace at wsPath was created or jj
// last recorded a change to its files, from the mtimes of jj's working-copy
// state: "type" is written once when the workspace is created, "tree_state"
// whenever a snapshot finds changed files. Neither moves when a fetch merely
// rebases @, which rewrites @'s own timestamps. It is only ever used to keep a
// workspace that looks in use, never to prove one safe to remove. A zero time
// means neither file could be read.
func WorkingCopyTouched(wsPath string) time.Time {
	var latest time.Time
	for _, name := range []string{"type", "tree_state"} {
		if fi, err := os.Stat(filepath.Join(wsPath, ".jj", "working_copy", name)); err == nil && fi.ModTime().After(latest) {
			latest = fi.ModTime()
		}
	}
	return latest
}

func unreadable(err error) Classification {
	return Classification{Type: TypeUnreadable, Tier: TierKeep, Evidence: []string{jj.BriefError(err)}}
}

func (c Classification) with(t WorkspaceType, tier Tier, evidence ...string) Classification {
	c.Type, c.Tier = t, tier
	c.Evidence = append(slices.Clone(c.Evidence), evidence...)
	return c
}

// summarize counts the work and finds its newest change.
func summarize(work []jj.LogEntry) WorkSummary {
	s := WorkSummary{Changes: len(work)}
	for _, e := range work {
		if e.AuthorTimestamp.After(s.Newest) {
			s.Newest = e.AuthorTimestamp
			if first := firstLine(e.Description); first != "" {
				s.Subject = first
			}
		}
	}
	if s.Subject == "" {
		for _, e := range work {
			if first := firstLine(e.Description); first != "" {
				s.Subject = first
				break
			}
		}
	}
	return s
}

func firstLine(s string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(first)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// classifyIdle handles a workspace with no work of its own beyond trunk.
func (c *Classifier) classifyIdle(ws WorkspaceRef, s survey, cl Classification) Classification {
	if len(s.own) > 0 {
		return cl.with(TypePlanned, TierKeep, "no changes yet on bookmark "+strings.Join(s.own, ", "))
	}
	foreign := s.foreign
	if len(foreign) > 0 {
		if pr := c.openPRFor(ws, revForeign); pr != nil {
			cl.PR = pr
			return cl.with(TypeActive, TierKeep, fmt.Sprintf("checks out open PR #%d", pr.Number))
		}
	}

	if c.recent(cl.LastUsed) {
		return cl.with(TypeFresh, TierKeep, "no changes yet; used "+formatAge(cl.LastUsed))
	}
	evidence := []string{"no changes of its own beyond trunk"}
	if len(foreign) > 0 {
		evidence = append(evidence, plural(len(foreign), "immutable commit")+" beyond trunk (a tag or someone else's branch)")
	}
	evidence = append(evidence, "last used "+formatAge(cl.LastUsed))
	return cl.with(TypeIdle, TierRemove, evidence...)
}

// openPRFor returns an open PR checked out by the workspace: the PR its name
// says it is for, or one whose head bookmark points into revset.
func (c *Classifier) openPRFor(ws WorkspaceRef, revset string) *github.PRInfo {
	if c.prs == nil {
		return nil
	}
	if m := prWorkspaceRe.FindStringSubmatch(ws.Name); m != nil {
		n, _ := strconv.Atoi(m[1])
		if pr, err := c.prs.GetPRByNumber(ws.RepoPath, n); err == nil && pr != nil && isOpen(pr) {
			return pr
		}
	}
	names, err := c.jj.RefNames(ws.Path, revset, true)
	if err != nil {
		return nil
	}
	for _, name := range names {
		prs, err := c.prs.ListPRsForBranch(ws.RepoPath, name)
		if err != nil {
			continue
		}
		for i := range prs {
			if isOpen(&prs[i]) {
				return &prs[i]
			}
		}
	}
	return nil
}

func isOpen(pr *github.PRInfo) bool { return !pr.IsMerged() && !pr.IsClosed() }

// classifyCovered handles a workspace whose work is all on bookmarks. Removing
// it loses nothing, so the question is only whether it is still in use.
func (c *Classifier) classifyCovered(ws WorkspaceRef, s survey, cl Classification) Classification {
	names := s.covering
	if len(names) == 0 {
		return cl.with(TypeActive, TierKeep, "work is on the local "+ws.DefaultBranch+" bookmark")
	}
	on := "bookmark " + strings.Join(names, ", ")
	if c.prs == nil {
		return cl.with(TypeActive, TierKeep, "work is on "+on)
	}

	var merged, closed []*github.PRInfo
	mergedNames := map[*github.PRInfo]string{}
	for _, name := range names {
		prs, err := c.prs.ListPRsForBranch(ws.RepoPath, name)
		if err != nil {
			return cl.with(TypeActive, TierKeep, "work is on "+on, "PR state unknown: "+err.Error())
		}
		if len(prs) == 0 {
			return cl.with(TypeActive, TierKeep, "work is on bookmark "+name+", which has no PR")
		}
		var nameMerged, nameClosed *github.PRInfo
		for i := range prs {
			pr := &prs[i]
			switch {
			case isOpen(pr):
				cl.PR = pr
				return cl.with(TypeActive, TierKeep, fmt.Sprintf("PR #%d is open on %s", pr.Number, name))
			case pr.IsMerged() && nameMerged == nil:
				nameMerged = pr
			case pr.IsClosed() && nameClosed == nil:
				nameClosed = pr
			}
		}
		if nameMerged != nil {
			merged = append(merged, nameMerged)
			mergedNames[nameMerged] = name
		} else {
			closed = append(closed, nameClosed)
		}
	}
	if len(closed) > 0 {
		cl.PR = closed[0]
		return cl.with(TypeClosedPR, TierDecide,
			fmt.Sprintf("PR #%d was closed without merging", closed[0].Number),
			"work stays on "+on+" if the workspace is removed")
	}
	// A reused branch name can carry newer work than its merged PR did, so
	// the work must lie within what the merged PRs' heads reached.
	var heads []string
	for _, pr := range merged {
		if pr.HeadSHA == "" {
			return cl.with(TypeActive, TierKeep, "work is on "+on, fmt.Sprintf("PR #%d merged, but its head commit is unknown", pr.Number))
		}
		heads = append(heads, "present("+pr.HeadSHA+")")
	}
	beyond, err := c.jj.Log(ws.Path, "("+revWork+") ~ ::("+strings.Join(heads, " | ")+")")
	if err != nil {
		return cl.with(TypeActive, TierKeep, "work is on "+on, "could not compare it with the merged PRs: "+jj.BriefError(err))
	}
	// A head commit that was never fetched reaches nothing locally; the PRs'
	// commit lists can still show the remaining changes were merged.
	if len(beyond) > 0 && !c.inPRCommits(ws, beyond, merged) {
		pr := merged[0]
		cl.PR = pr
		return cl.with(TypeActive, TierKeep, fmt.Sprintf("PR #%d merged, but %s has %s beyond it",
			pr.Number, mergedNames[pr], plural(len(beyond), "change")))
	}
	cl.PR = merged[0]
	return cl.with(TypeLanded, TierRemove, fmt.Sprintf("work is on %s and PR #%d merged", on, merged[0].Number))
}

// inPRCommits reports whether every change is a commit of one of prs.
func (c *Classifier) inPRCommits(ws WorkspaceRef, changes []jj.LogEntry, prs []*github.PRInfo) bool {
	shas := map[string]bool{}
	for _, pr := range prs {
		commits, err := c.prs.ListPRCommits(ws.RepoPath, pr.Number)
		if err != nil {
			return false
		}
		for _, pc := range commits {
			shas[pc.SHA] = true
		}
	}
	for _, e := range changes {
		if !shas[e.CommitID] {
			return false
		}
	}
	return true
}

// subjectHint is a trunk commit whose subject matches a work subject, with the
// PR number from its "(#N)" suffix when it has one.
type subjectHint struct {
	commit jj.LogEntry
	pr     int
}

// classifyWork handles a workspace with work that no bookmark holds.
func (c *Classifier) classifyWork(ws WorkspaceRef, s survey, cl Classification) Classification {
	fork, uncovered := s.fork, s.uncovered
	var changes []jj.FileChange
	if fork != nil {
		changes, _ = c.jj.DiffSummaryBetween(ws.Path, fork.CommitID, "@")
		cl.Work.Files = len(changes)
		cl.Work.Added, cl.Work.Deleted, _ = c.jj.DiffStatBetween(ws.Path, fork.CommitID, "@")
	}

	hints := c.subjectHints(ws, fork, uncovered)

	// Commit identity against merged PRs.
	id := c.provenByPR(ws, uncovered, prCandidates(ws, hints))
	if id.exact != nil {
		cl.PR = id.exact
		return cl.with(TypeLanded, TierRemove, fmt.Sprintf("its %s of merged PR #%d",
			pluralVerb(len(uncovered), "change is a commit", "changes are commits"), id.exact.Number))
	}

	// With one uncovered change the net diff is that change's own; across
	// several, an earlier one could add what a later one deletes.
	if len(uncovered) == 1 && len(changes) > 0 && !slices.ContainsFunc(changes, func(f jj.FileChange) bool { return f.Status != 'D' }) {
		return cl.with(TypeDeletionsOnly, TierRemove, "the work only deletes "+plural(len(changes), "file"))
	}

	// Content on trunk.
	var content contentResult
	if fork != nil {
		var first []string
		for _, h := range hints {
			first = append(first, h.commit.CommitID)
		}
		for _, pr := range id.merged {
			if pr.MergeCommit != nil && pr.MergeCommit.OID != "" {
				first = append(first, pr.MergeCommit.OID)
			}
		}
		content = c.contentProof(ws, *fork, changes, first)
		if content.landedAt != nil {
			return cl.with(TypeLanded, TierRemove, fmt.Sprintf("its content is on trunk as of %s %q",
				shortID(content.landedAt.CommitID), firstLine(content.landedAt.Description)))
		}
	}

	// Nothing proven: classify for a person, with what was found.
	partial := content.evidence()
	if id.revision != nil {
		cl.PR = id.revision
		return cl.with(TypeSuperseded, TierDecide, append([]string{fmt.Sprintf(
			"older revisions of commits in merged PR #%d; the rewrite may have dropped some of it", id.revision.Number)}, partial...)...)
	}
	if names := c.newerRevisions(ws, uncovered); len(names) > 0 {
		cl.Hint = fmt.Sprintf("refresh it: jj -R %s new %s", ws.Path, names[0])
		return cl.with(TypeSuperseded, TierDecide, append([]string{
			"an older revision of changes now on bookmark " + strings.Join(names, ", ")}, partial...)...)
	}
	if s.conflicted == len(uncovered) {
		return cl.with(TypeConflicted, TierDecide, append([]string{
			pluralVerb(s.conflicted, "change has", "changes have") + " unresolved conflicts, typically a rebase leftover"}, partial...)...)
	}
	if why := scratchReason(uncovered, changes, content); why != "" {
		return cl.with(TypeScratch, TierDecide, append([]string{why}, partial...)...)
	}
	if len(hints) > 0 {
		h := hints[0]
		where := shortID(h.commit.CommitID)
		if h.pr > 0 {
			where += fmt.Sprintf(" (PR #%d)", h.pr)
		}
		return cl.with(TypeProbablyLanded, TierDecide, append([]string{"subject matches trunk commit " + where + ", but the content differs"}, partial...)...)
	}
	evidence := []string{pluralVerb(len(uncovered), "change is", "changes are") + " on no bookmark or remote"}
	if id.open != nil {
		cl.PR = id.open
		evidence = append(evidence, fmt.Sprintf("PR #%d is open; these changes are not on its branch", id.open.Number))
	}
	newest := uncovered[0]
	for _, e := range uncovered {
		if e.CommitterTimestamp.After(newest.CommitterTimestamp) {
			newest = e
		}
	}
	cl.Hint = fmt.Sprintf("keep it: jj -R %s bookmark create %s -r %s", ws.Path, bookmarkName(ws.Name), shortID(newest.ChangeID))
	return cl.with(TypeUnpushed, TierDecide, append(evidence, partial...)...)
}

func pluralVerb(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// bookmarkName suggests a bookmark name for a workspace's work.
func bookmarkName(wsName string) string {
	if wsName == "" {
		return "rescued-work"
	}
	return wsName
}

// subjectHints finds trunk commits since the fork whose subject is a work
// subject, optionally followed by a squash merge's " (#N)". They only steer
// the proofs toward the right PR and commit; a matching subject proves nothing.
func (c *Classifier) subjectHints(ws WorkspaceRef, fork *jj.LogEntry, uncovered []jj.LogEntry) []subjectHint {
	if fork == nil {
		return nil
	}
	var subjects []string
	for _, e := range uncovered {
		if s := firstLine(e.Description); s != "" && !slices.Contains(subjects, s) {
			subjects = append(subjects, s)
		}
		if len(subjects) == 10 {
			break
		}
	}
	if len(subjects) == 0 {
		return nil
	}
	quote := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	var terms []string
	for _, s := range subjects {
		terms = append(terms, `description(substring:"`+quote.Replace(s)+`")`)
	}
	found, err := c.jj.Log(ws.Path, "("+fork.CommitID+"..trunk()) & ("+strings.Join(terms, " | ")+")")
	if err != nil {
		return nil
	}
	var hints []subjectHint
	for _, e := range found {
		first := firstLine(e.Description)
		if slices.Contains(subjects, first) {
			hints = append(hints, subjectHint{commit: e})
			continue
		}
		if m := prSuffixRe.FindStringSubmatch(first); m != nil && slices.Contains(subjects, m[1]) {
			n, _ := strconv.Atoi(m[2])
			hints = append(hints, subjectHint{commit: e, pr: n})
		}
	}
	return hints
}

// prCandidates lists the PRs worth checking for commit identity: the one the
// workspace is named for, then those named by subject hints.
func prCandidates(ws WorkspaceRef, hints []subjectHint) []int {
	var out []int
	if m := prWorkspaceRe.FindStringSubmatch(ws.Name); m != nil {
		n, _ := strconv.Atoi(m[1])
		out = append(out, n)
	}
	for _, h := range hints {
		if h.pr > 0 && !slices.Contains(out, h.pr) {
			out = append(out, h.pr)
		}
	}
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

// identity is what commit identity established against the candidate PRs.
type identity struct {
	exact    *github.PRInfo   // every uncovered change is one of its commits
	revision *github.PRInfo   // every uncovered change is one of its commits or an older revision of one
	open     *github.PRInfo   // a candidate PR that is still open
	merged   []*github.PRInfo // every merged candidate, for the content proof
}

// provenByPR checks the uncovered changes against each candidate PR's commit
// list. jj writes a change-id header into the commits it pushes, so a PR
// commit fetched at any point (hidden ones included) carries the change id of
// the local change it was pushed from.
func (c *Classifier) provenByPR(ws WorkspaceRef, uncovered []jj.LogEntry, candidates []int) identity {
	var id identity
	if c.prs == nil {
		return id
	}
	for _, n := range candidates {
		pr, err := c.prs.GetPRByNumber(ws.RepoPath, n)
		if err != nil || pr == nil {
			continue
		}
		if isOpen(pr) && id.open == nil {
			id.open = pr
		}
		if !pr.IsMerged() {
			continue
		}
		id.merged = append(id.merged, pr)
		commits, err := c.prs.ListPRCommits(ws.RepoPath, n)
		if err != nil || len(commits) == 0 {
			continue
		}
		shas := map[string]bool{}
		var terms []string
		for _, pc := range commits {
			shas[pc.SHA] = true
			terms = append(terms, "present("+pc.SHA+")")
		}
		var pending []jj.LogEntry
		for _, e := range uncovered {
			if !shas[e.CommitID] {
				pending = append(pending, e)
			}
		}
		if len(pending) == 0 {
			id.exact = pr
			return id
		}
		if id.revision != nil {
			continue
		}
		known, err := c.jj.Log(ws.Path, strings.Join(terms, " | "))
		if err != nil {
			continue
		}
		latest := map[string]time.Time{}
		for _, k := range known {
			if k.CommitterTimestamp.After(latest[k.ChangeID]) {
				latest[k.ChangeID] = k.CommitterTimestamp
			}
		}
		older := true
		for _, e := range pending {
			t, ok := latest[e.ChangeID]
			if !ok || e.CommitterTimestamp.After(t) {
				older = false
				break
			}
		}
		if older {
			id.revision = pr
		}
	}
	return id
}

// newerRevisions returns the bookmarks holding a newer revision of every
// uncovered change, or nil when some change has none. A divergent change has
// several visible commits; the bookmarked one with the latest committer time
// is the newest.
func (c *Classifier) newerRevisions(ws WorkspaceRef, uncovered []jj.LogEntry) []string {
	revs, err := c.jj.Log(ws.Path, "divergent() & "+revCovered)
	if err != nil || len(revs) == 0 {
		return nil
	}
	newest := map[string]jj.LogEntry{}
	for _, r := range revs {
		if cur, ok := newest[r.ChangeID]; !ok || r.CommitterTimestamp.After(cur.CommitterTimestamp) {
			newest[r.ChangeID] = r
		}
	}
	var heads []string
	for _, e := range uncovered {
		r, ok := newest[e.ChangeID]
		if !ok || r.CommitID == e.CommitID || e.CommitterTimestamp.After(r.CommitterTimestamp) {
			return nil
		}
		heads = append(heads, r.CommitID)
	}
	names, err := c.jj.RefNames(ws.Path, "("+revBookmarked+") & descendants("+strings.Join(heads, " | ")+")", true)
	if err != nil {
		return nil
	}
	return slices.DeleteFunc(names, func(n string) bool { return n == ws.DefaultBranch })
}

// scratchReason says why uncovered work is scratch, or "" when it is not:
// every change is described as scratch, or the work changes nothing but
// lockfiles, or the content proof found everything else on trunk.
func scratchReason(uncovered []jj.LogEntry, changes []jj.FileChange, content contentResult) string {
	marked := true
	for _, e := range uncovered {
		if !scratchRe.MatchString(e.Description) {
			marked = false
			break
		}
	}
	if marked {
		return fmt.Sprintf("described as scratch: %q", firstLine(uncovered[0].Description))
	}
	if len(changes) > 0 && !slices.ContainsFunc(changes, func(f jj.FileChange) bool { return !isLockfile(f.Path) }) {
		return "only lockfiles changed"
	}
	if content.best != nil && len(content.misses) > 0 &&
		!slices.ContainsFunc(content.misses, func(m fileMiss) bool { return !isLockfile(m.path) }) {
		return fmt.Sprintf("on trunk as of %s except lockfiles", shortID(content.best.CommitID))
	}
	return ""
}

// fileMiss is a file the content proof could not find on trunk, and why.
type fileMiss struct {
	path, why string
}

// contentResult is what the content proof found.
type contentResult struct {
	landedAt  *jj.LogEntry // the first trunk commit containing all of the work
	best      *jj.LogEntry // otherwise, the one containing the most files
	contained int          // files best contains
	total     int          // files the work touches
	misses    []fileMiss   // files best does not contain
	skipped   string       // why the proof did not run
}

// evidence renders a failed proof's best attempt.
func (r contentResult) evidence() []string {
	switch {
	case r.skipped != "":
		return []string{"content not compared: " + r.skipped}
	case r.best == nil && r.total > 0:
		return []string{"no trunk commit since the fork touches these files"}
	case r.best == nil:
		return nil
	}
	var not []string
	for i, m := range r.misses {
		if i == 4 {
			not = append(not, fmt.Sprintf("and %d more", len(r.misses)-i))
			break
		}
		not = append(not, m.path+" ("+m.why+")")
	}
	return []string{fmt.Sprintf("%d/%d files on trunk as of %s; not: %s",
		r.contained, r.total, shortID(r.best.CommitID), strings.Join(not, ", "))}
}

// contentProof looks for a trunk commit since the fork that already contains
// every file the work changes. It tries the commits in first (subject matches,
// PR merge commits) and then every trunk commit since the fork that touches
// one of the files, oldest first. Comparing against today's trunk instead
// would fail whenever those files changed again after the work landed.
func (c *Classifier) contentProof(ws WorkspaceRef, fork jj.LogEntry, changes []jj.FileChange, first []string) contentResult {
	var r contentResult
	var files []string
	for _, f := range changes {
		if f.Status != 'A' && f.Status != 'M' && f.Status != 'D' {
			r.skipped = "the work renames or copies files"
			return r
		}
		files = append(files, f.Path)
	}
	r.total = len(files)
	switch {
	case len(files) == 0:
		return r
	case len(files) > c.opts.MaxFiles:
		r.skipped = fmt.Sprintf("the work touches %d files", len(files))
		return r
	}

	out, err := c.jj.DiffGitBetween(ws.Path, fork.CommitID, "@", files)
	if err != nil {
		r.skipped = jj.BriefError(err)
		return r
	}
	toWork, err := parseGitDiff(out)
	if err != nil {
		r.skipped = err.Error()
		return r
	}
	// Every file must be in the work's diff. One that is not means the paths
	// did not round-trip through jj, and a fileset that matches nothing makes
	// jj report "no differences", which would read as contained.
	for _, f := range files {
		if toWork[f] == nil {
			r.skipped = "could not read the work's diff of " + f
			return r
		}
	}

	candidates, err := c.landingCandidates(ws, fork, files, first)
	if err != nil {
		r.skipped = jj.BriefError(err)
		return r
	}
	for _, cand := range candidates {
		differ, err := c.jj.DiffNamesBetween(ws.Path, cand.CommitID, "@", files)
		if err != nil {
			continue
		}
		contained := len(files) - len(differ)
		var misses []fileMiss
		if len(differ) > 0 {
			out, err := c.jj.DiffGitBetween(ws.Path, fork.CommitID, cand.CommitID, differ)
			if err != nil {
				continue
			}
			toTrunk, err := parseGitDiff(out)
			if err != nil {
				continue
			}
			for _, f := range differ {
				if ok, why := fileContained(toTrunk[f], toWork[f]); ok {
					contained++
				} else {
					misses = append(misses, fileMiss{f, why})
				}
			}
		}
		if contained == len(files) {
			r.landedAt = &cand
			return r
		}
		if r.best == nil || contained > r.contained {
			r.best, r.contained, r.misses = &cand, contained, misses
		}
	}
	return r
}

// landingCandidates lists the trunk commits to try, the preferred ones first
// and the rest oldest first, capped at MaxCandidates.
func (c *Classifier) landingCandidates(ws WorkspaceRef, fork jj.LogEntry, files, first []string) ([]jj.LogEntry, error) {
	since := "(" + fork.CommitID + "..trunk())"
	var out []jj.LogEntry
	seen := map[string]bool{}
	if len(first) > 0 {
		var terms []string
		for _, id := range first {
			terms = append(terms, "present("+id+")")
		}
		pref, err := c.jj.Log(ws.Path, since+" & ("+strings.Join(terms, " | ")+")")
		if err != nil {
			return nil, err
		}
		for _, e := range pref {
			seen[e.CommitID] = true
			out = append(out, e)
		}
	}
	var fs []string
	for _, f := range files {
		fs = append(fs, jj.FilesetPath(f))
	}
	touching, err := c.jj.Log(ws.Path, since+" & files("+strings.Join(fs, " | ")+")")
	if err != nil {
		return nil, err
	}
	slices.Reverse(touching) // jj lists children first
	for _, e := range touching {
		if len(out) >= c.opts.MaxCandidates {
			break
		}
		if !seen[e.CommitID] {
			out = append(out, e)
		}
	}
	return out, nil
}
