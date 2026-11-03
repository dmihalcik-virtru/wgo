package dash

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/inventory"
)

// This file turns each followed PR's cached review history (gh-104) into
// segments on a time line, after Plaisant and Shneiderman's LifeLines: where
// its time went, in draft, waiting on whom, or with changes requested. The
// segments ride on the snapshot's PRs for the timeline view, and make gh-99's
// waiting_since and rule 3 exact. Histories are fetched by the background
// Refresher only, at most MaxTimelinesPerCycle PRs per collection, and only
// for PRs whose updated_at changed since their last fetch.

// MaxTimelinesPerCycle bounds the PR timelines one collection queues.
const MaxTimelinesPerCycle = 50

// timelineUnknownTTL is how long the timeline of a PR whose updated_at is
// unknown is trusted before it is fetched again: without updated_at there
// is no telling whether it changed.
const timelineUnknownTTL = 30 * time.Minute

// Segment states (TimelineSegment.State).
const (
	SegmentDraft            = "draft"
	SegmentWaiting          = "waiting"
	SegmentChangesRequested = "changes_requested"
	SegmentApproved         = "approved"
	SegmentUnattended       = "unattended"
	SegmentMerged           = "merged"
	SegmentClosed           = "closed"
)

var segmentStates = []string{SegmentDraft, SegmentWaiting, SegmentChangesRequested, SegmentApproved, SegmentUnattended, SegmentMerged, SegmentClosed}

// PRTimeline is a PR's review history as segments (PRInfo.Timeline).
type PRTimeline struct {
	// Freshness is Fresh when the history was fetched for the PR's current
	// updated_at, Stale when the PR changed since (or its updated_at is
	// unknown). Only a fresh, untruncated timeline refines the facts.
	Freshness Freshness `json:"freshness"`
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	// UpdatedAt is the PR's updated_at the history was fetched for.
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	// Truncated is set when GitHub had more history than was read; the
	// newest events are then missing.
	Truncated bool `json:"truncated,omitempty"`
	// Segments cover the PR's life from its creation, in order: each lasts
	// until the next one starts, the last until now.
	Segments []TimelineSegment `json:"segments"`
	// Lanes are one per reviewer ever requested: when a request of theirs
	// was outstanding while the PR was open and ready, for an expanded row.
	Lanes []ReviewerLane `json:"lanes,omitempty"`
	// Reviews are the approvals and change requests, in order: the view's
	// ticks.
	Reviews []TimelineReview `json:"reviews,omitempty"`
	// Blocking are the change requests that still block the PR at the end,
	// as GitHub's review decision counts them (gh-99 rule 3): reviewers
	// whose latest opinionated review is CHANGES_REQUESTED and who are not
	// asked again now. A ready-for-review event, or a request removed
	// again, ends a changes_requested segment but not this.
	Blocking []TimelineReview `json:"blocking,omitempty"`
}

// TimelineSegment is one stretch of a PR's life in one state. It lasts until
// the next segment starts, so it carries no end of its own.
type TimelineSegment struct {
	// State is one of the Segment* constants.
	State string    `json:"state"`
	Start time.Time `json:"start"`
	// Reviewers are, for waiting, everyone a request was outstanding for
	// during the segment (overlapping waits form one segment); for
	// changes_requested, who requested changes; for approved, who approved.
	Reviewers []string `json:"reviewers,omitempty"`
}

// ReviewerLane is one reviewer's waits on a PR.
type ReviewerLane struct {
	// Reviewer is a login, or "team:<slug>".
	Reviewer string     `json:"reviewer"`
	Waits    []Interval `json:"waits"`
}

// Interval is a span of time; End is omitted while it lasts.
type Interval struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end,omitzero"`
}

// TimelineReview is a review that approved or requested changes.
type TimelineReview struct {
	Reviewer string `json:"reviewer"`
	// State is ReviewApproved or ReviewChangesRequested.
	State string    `json:"state"`
	At    time.Time `json:"at"`
}

// last is the current segment, or nil.
func (t *PRTimeline) last() *TimelineSegment {
	if t == nil || len(t.Segments) == 0 {
		return nil
	}
	return &t.Segments[len(t.Segments)-1]
}

// BuildTimeline turns a fetched history into segments, lanes, review ticks
// and the standing change requests. It is pure: the same history gives the
// same timeline. Freshness and FetchedAt are left for the caller.
//
// At each moment the PR is, in this order: merged; closed; draft;
// changes_requested while a change request stands (until a request for that
// reviewer, their later approval, or a ready-for-review event); waiting
// while a review request is outstanding (until that reviewer reviews,
// directly or for a team they reviewed on behalf of, or the request is
// removed); approved while someone's latest opinion is an approval;
// otherwise unattended: ready, nobody requested, no approval. Whether the PR
// was opened as a draft is read from its first draft event: a
// ready-for-review event says it was, a convert-to-draft event that it was
// not; with neither, it is as it is now. Reviews by the PR's author and
// pending reviews are ignored. Events at the same time apply together, and
// a stretch of no length is dropped.
func BuildTimeline(t *github.PRTimeline) *PRTimeline {
	out := &PRTimeline{UpdatedAt: t.UpdatedAt, Truncated: t.Truncated, Segments: []TimelineSegment{}}
	evs := slices.Clone(t.Events)
	slices.SortStableFunc(evs, func(a, b github.TimelineEvent) int { return a.At.Compare(b.At) })
	start := t.CreatedAt
	if len(evs) > 0 && (start.IsZero() || evs[0].At.Before(start)) {
		start = evs[0].At
	}
	if start.IsZero() {
		return out
	}
	st := &tlState{open: true, draft: openedAsDraft(t, evs)}
	b := &segmentBuilder{out: out}
	b.at(start, st)
	for i := 0; i < len(evs); {
		at := evs[i].At
		for ; i < len(evs) && evs[i].At.Equal(at); i++ {
			st.apply(evs[i], t.Author, out)
		}
		b.at(at, st)
	}
	for _, r := range st.opinions {
		if r.State == ReviewChangesRequested && indexOf(st.outstanding, r.Reviewer) < 0 {
			out.Blocking = append(out.Blocking, r)
		}
	}
	// A reviewer requested only while the PR was a draft never waited.
	out.Lanes = slices.DeleteFunc(out.Lanes, func(l ReviewerLane) bool { return len(l.Waits) == 0 })
	return out
}

// openedAsDraft reads whether the PR was opened as a draft (BuildTimeline).
func openedAsDraft(t *github.PRTimeline, evs []github.TimelineEvent) bool {
	for _, e := range evs {
		switch e.Type {
		case github.TimelineReady:
			return true
		case github.TimelineDraft:
			return false
		}
	}
	return t.IsDraft
}

// tlState is a PR's review state while BuildTimeline replays its events.
type tlState struct {
	open, merged, draft bool
	// outstanding holds the open review requests in request order, each
	// with the time it was made; blocking the change requests that hold up
	// the display segment (until a request for that reviewer or a ready
	// event) and approvals each reviewer's standing approval, in order.
	// opinions is each reviewer's latest opinionated review, approval or
	// change request, as GitHub's review decision reads them.
	outstanding []TimelineReview
	blocking    []TimelineReview
	approvals   []TimelineReview
	opinions    []TimelineReview
}

func indexOf(list []TimelineReview, who string) int {
	return slices.IndexFunc(list, func(r TimelineReview) bool { return strings.EqualFold(r.Reviewer, who) })
}

func without(list []TimelineReview, who string) []TimelineReview {
	if i := indexOf(list, who); i >= 0 {
		return slices.Delete(list, i, i+1)
	}
	return list
}

func (st *tlState) apply(e github.TimelineEvent, author string, out *PRTimeline) {
	switch e.Type {
	case github.TimelineReady:
		// The author says it is ready again: change requests stop holding
		// it up (gh-104 requirement 4).
		st.draft, st.blocking = false, nil
	case github.TimelineDraft:
		st.draft = true
	case github.TimelineReviewRequested:
		// A request already outstanding keeps its older start: a newer
		// request must not reset a wait (gh-99 requirement 7).
		if e.Reviewer != "" && indexOf(st.outstanding, e.Reviewer) < 0 {
			st.outstanding = append(st.outstanding, TimelineReview{Reviewer: e.Reviewer, At: e.At})
		}
		st.blocking = without(st.blocking, e.Reviewer)
	case github.TimelineReviewRequestRemoved:
		st.outstanding = without(st.outstanding, e.Reviewer)
	case github.TimelineReview:
		if e.Reviewer == "" || strings.EqualFold(e.Reviewer, author) {
			return
		}
		// GitHub drops a reviewer's request once they review, and a team's
		// when someone reviews on its behalf.
		st.outstanding = without(st.outstanding, e.Reviewer)
		for _, team := range e.Teams {
			st.outstanding = without(st.outstanding, team)
		}
		r := TimelineReview{Reviewer: e.Reviewer, State: e.State, At: e.At}
		switch e.State {
		case ReviewChangesRequested:
			st.approvals = without(st.approvals, e.Reviewer)
			st.blocking = append(without(st.blocking, e.Reviewer), r)
			st.opinions = append(without(st.opinions, e.Reviewer), r)
			out.Reviews = append(out.Reviews, r)
		case ReviewApproved:
			st.blocking = without(st.blocking, e.Reviewer)
			st.approvals = append(without(st.approvals, e.Reviewer), r)
			st.opinions = append(without(st.opinions, e.Reviewer), r)
			out.Reviews = append(out.Reviews, r)
		case ReviewDismissed:
			// A dismissal GitHub did not report as its own event: the
			// review reads DISMISSED at the time it was submitted, and
			// whatever it said no longer stands.
			st.blocking = without(st.blocking, e.Reviewer)
			st.approvals = without(st.approvals, e.Reviewer)
			st.opinions = without(st.opinions, e.Reviewer)
		}
	case github.TimelineReviewDismissed:
		// The review (an approval or a change request, e.State) stood until
		// now. GitHub asks the reviewer again, so they are waited on from
		// the dismissal.
		if e.Reviewer == "" || strings.EqualFold(e.Reviewer, author) {
			return
		}
		if i := indexOf(st.opinions, e.Reviewer); i >= 0 && (e.State == "" || st.opinions[i].State == e.State) {
			st.opinions = without(st.opinions, e.Reviewer)
			st.approvals = without(st.approvals, e.Reviewer)
			st.blocking = without(st.blocking, e.Reviewer)
		}
		if indexOf(st.outstanding, e.Reviewer) < 0 {
			st.outstanding = append(st.outstanding, TimelineReview{Reviewer: e.Reviewer, At: e.At})
		}
	case github.TimelineMerged:
		st.open, st.merged = false, true
	case github.TimelineClosed:
		st.open = false // a merge closes too; merged still wins
	case github.TimelineReopened:
		if !st.merged {
			st.open = true
		}
	}
}

// segment is the state st puts the PR in, with its reviewers.
func (st *tlState) segment() (string, []TimelineReview) {
	switch {
	case st.merged:
		return SegmentMerged, nil
	case !st.open:
		return SegmentClosed, nil
	case st.draft:
		return SegmentDraft, nil
	case len(st.blocking) > 0:
		return SegmentChangesRequested, st.blocking
	case len(st.outstanding) > 0:
		return SegmentWaiting, st.outstanding
	case len(st.approvals) > 0:
		return SegmentApproved, st.approvals
	}
	return SegmentUnattended, nil
}

// segmentBuilder records the segments and lanes as the state changes.
type segmentBuilder struct {
	out *PRTimeline
	// lane is each reviewer's lane index in out.Lanes.
	lane map[string]int
}

// at records st as the PR's state from time at on.
func (b *segmentBuilder) at(at time.Time, st *tlState) {
	state, who := st.segment()
	segs := &b.out.Segments
	if n := len(*segs); n > 0 && (*segs)[n-1].State != state && !at.After((*segs)[n-1].Start) {
		// The current segment has no length: drop it, continuing the one
		// before it if the PR is back in that state.
		*segs = (*segs)[:n-1]
	}
	if n := len(*segs); n == 0 || (*segs)[n-1].State != state {
		*segs = append(*segs, TimelineSegment{State: state, Start: at})
	}
	cur := &(*segs)[len(*segs)-1]
	for _, r := range who {
		if !slices.Contains(cur.Reviewers, r.Reviewer) {
			cur.Reviewers = append(cur.Reviewers, r.Reviewer)
		}
	}

	// Lanes: a reviewer waits while their request is outstanding on an open,
	// ready PR.
	waiting := map[string]bool{}
	if st.open && !st.draft {
		for _, r := range st.outstanding {
			waiting[r.Reviewer] = true
		}
	}
	if b.lane == nil {
		b.lane = map[string]int{}
	}
	for _, r := range st.outstanding {
		if _, ok := b.lane[r.Reviewer]; !ok {
			b.lane[r.Reviewer] = len(b.out.Lanes)
			b.out.Lanes = append(b.out.Lanes, ReviewerLane{Reviewer: r.Reviewer, Waits: []Interval{}})
		}
	}
	for i := range b.out.Lanes {
		l := &b.out.Lanes[i]
		open := len(l.Waits) > 0 && l.Waits[len(l.Waits)-1].End.IsZero()
		switch {
		case waiting[l.Reviewer] && !open:
			l.Waits = append(l.Waits, Interval{Start: at})
		case !waiting[l.Reviewer] && open:
			w := &l.Waits[len(l.Waits)-1]
			if at.After(w.Start) {
				w.End = at
			} else {
				l.Waits = l.Waits[:len(l.Waits)-1]
			}
		}
	}
}

// ---------- collection ----------

// timelineCandidate is a PR whose history the dash follows.
type timelineCandidate struct {
	key  inventory.TimelineKey
	p    *PRInfo
	open bool
}

// addTimelines attaches each PR's cached review history to its node and
// returns the github_timelines source. The PRs it follows are the open ones
// and those merged or closed since the closed-recent window began (gh-97);
// up to MaxTimelinesPerCycle of those whose history is missing or older
// than their updated_at, open ones first and the most recently updated
// first, are queued as one job, unless GitHub cannot be asked, a rate limit
// paused the timelines, or a PR is backing off after failures. A PR outside
// that set still shows a history already cached. It only reads caches.
func (c *Collector) addTimelines(b *builder, ls *localState, addJob func(Job)) SourceStatus {
	since := inventory.ClosedSince(ls.at, ls.days)
	canAsk := c.cfg.GitHubUnavailable == ""
	pausedUntil := inventory.TimelinesPausedUntil()
	paused := ls.at.Before(pausedUntil)
	var tally remoteTally
	var need []timelineCandidate
	have := 0
	var ids []string
	for id, n := range b.nodes {
		if n.PR != nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := b.nodes[id].PR
		k := inventory.TimelineKey{Repo: p.Repo, Number: p.Number}
		if p.Repo == "" || p.Number <= 0 {
			continue
		}
		res := inventory.ReadTimeline(k)
		f := Unknown
		switch {
		case res.Current(p.UpdatedAt):
			f = Fresh
		case res.Timeline != nil:
			f = Stale
			if p.UpdatedAt.IsZero() && ls.at.Sub(res.FetchedAt) < timelineUnknownTTL {
				f = Fresh // as current as can be told; not trusted by the facts
			}
		case res.Err != nil:
			f = Error
		}
		if res.Timeline != nil {
			p.Timeline = BuildTimeline(res.Timeline)
			p.Timeline.FetchedAt = res.FetchedAt.UTC().Truncate(time.Second)
			p.Timeline.Freshness = Stale
			if res.Current(p.UpdatedAt) {
				p.Timeline.Freshness = Fresh
			}
		}
		open := p.State == "open"
		if !open && !closedSince(p, since) {
			continue
		}
		if res.Timeline != nil {
			have++
		}
		tally.add(f, res.FetchedAt)
		if f != Fresh && canAsk && !paused && !ls.at.Before(res.NotBefore()) {
			need = append(need, timelineCandidate{key: k, p: p, open: open})
		}
	}
	sort.SliceStable(need, func(i, j int) bool {
		if need[i].open != need[j].open {
			return need[i].open
		}
		return need[i].p.UpdatedAt.After(need[j].p.UpdatedAt)
	})
	if len(need) > MaxTimelinesPerCycle {
		need = need[:MaxTimelinesPerCycle]
	}
	if len(need) > 0 {
		j := Job{Kind: JobTimelines}
		for _, cand := range need {
			j.Timelines = append(j.Timelines, TimelineRequest{Key: cand.key, UpdatedAt: cand.p.UpdatedAt})
		}
		addJob(j)
	}
	st := tally.status()
	total := st.Fresh + st.Stale + st.Unknown + st.Error
	if total > 0 {
		st.Detail = fmt.Sprintf("%d of %d %s have a cached history", have, total, plural(total, "PR", "PRs"))
	}
	switch {
	case !canAsk:
		st.Detail = joinDetail(st.Detail, c.cfg.GitHubUnavailable)
	case paused:
		st.Detail = joinDetail(st.Detail, "paused by a GitHub rate limit until "+pausedUntil.In(ls.at.Location()).Format("15:04"))
	}
	return st
}

func joinDetail(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// closedSince reports whether a merged or closed PR left the open state at or
// after since, by merged_at or closed_at, else by updated_at when neither is
// known (a PR known only through a bookmark).
func closedSince(p *PRInfo, since time.Time) bool {
	at := p.MergedAt
	if at.IsZero() {
		at = p.ClosedAt
	}
	if at.IsZero() {
		at = p.UpdatedAt
	}
	return (p.State == "merged" || p.State == "closed") && !at.IsZero() && !at.Before(since)
}

// ---------- validation ----------

// checkTimeline checks a PR timeline's states, and that each segment starts
// after the one before it in another state.
func (v *validator) checkTimeline(id string, t *PRTimeline) {
	if t == nil {
		return
	}
	v.enum(id, "timeline freshness", string(t.Freshness), string(Fresh), string(Stale))
	for i, sg := range t.Segments {
		v.enum(id, "segment state", sg.State, segmentStates...)
		switch {
		case sg.Start.IsZero():
			v.addf("timeline: %q segment %d has no start", id, i)
		case i > 0 && !sg.Start.After(t.Segments[i-1].Start):
			v.addf("timeline: %q segment %d does not start after segment %d", id, i, i-1)
		case i > 0 && sg.State == t.Segments[i-1].State:
			v.addf("timeline: %q segment %d continues segment %d's state %s", id, i, i-1, sg.State)
		}
	}
	for _, r := range slices.Concat(t.Reviews, t.Blocking) {
		v.enum(id, "timeline review state", r.State, ReviewApproved, ReviewChangesRequested)
	}
}
