package dash

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/github"
)

// tlBase is hour 0 of the timeline tests.
var tlBase = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func hr(n int) time.Time { return tlBase.Add(time.Duration(n) * time.Hour) }

func evReady(h int) github.TimelineEvent {
	return github.TimelineEvent{Type: github.TimelineReady, At: hr(h)}
}
func evDraft(h int) github.TimelineEvent {
	return github.TimelineEvent{Type: github.TimelineDraft, At: hr(h)}
}
func evMerged(h int) github.TimelineEvent {
	return github.TimelineEvent{Type: github.TimelineMerged, At: hr(h)}
}
func evClosed(h int) github.TimelineEvent {
	return github.TimelineEvent{Type: github.TimelineClosed, At: hr(h)}
}
func evReopened(h int) github.TimelineEvent {
	return github.TimelineEvent{Type: github.TimelineReopened, At: hr(h)}
}
func evRequest(h int, who string) github.TimelineEvent {
	return github.TimelineEvent{Type: github.TimelineReviewRequested, At: hr(h), Reviewer: who}
}
func evUnrequest(h int, who string) github.TimelineEvent {
	return github.TimelineEvent{Type: github.TimelineReviewRequestRemoved, At: hr(h), Reviewer: who}
}
func evReview(h int, who, state string, teams ...string) github.TimelineEvent {
	return github.TimelineEvent{Type: github.TimelineReview, At: hr(h), Reviewer: who, State: state, Teams: teams}
}

// hourOf is t as an hour offset from tlBase, or "" for zero (until now).
func hourOf(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return fmt.Sprint(int(t.Sub(tlBase) / time.Hour))
}

// segText renders segments compactly: "waiting[bob]0-5", each ending where
// the next starts, the last "...5-".
func segText(segs []TimelineSegment) string {
	var out []string
	for i, s := range segs {
		r := ""
		if len(s.Reviewers) > 0 {
			r = "[" + strings.Join(s.Reviewers, ",") + "]"
		}
		end := ""
		if i+1 < len(segs) {
			end = hourOf(segs[i+1].Start)
		}
		out = append(out, s.State+r+hourOf(s.Start)+"-"+end)
	}
	return strings.Join(out, " ")
}

func laneText(lanes []ReviewerLane) string {
	var out []string
	for _, l := range lanes {
		var w []string
		for _, iv := range l.Waits {
			w = append(w, hourOf(iv.Start)+"-"+hourOf(iv.End))
		}
		out = append(out, l.Reviewer+":"+strings.Join(w, ","))
	}
	return strings.Join(out, " ")
}

func reviewText(rs []TimelineReview) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Reviewer+" "+strings.ToLower(r.State)+"@"+hourOf(r.At))
	}
	return strings.Join(out, " ")
}

// TestBuildTimeline is gh-104's segment acceptance criterion: re-requests,
// removed requests, several reviewers, draft round-trips, reopen and team
// requests, plus the rules around them.
func TestBuildTimeline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		draft   bool // the PR is a draft at the fetch
		created int  // hour; -1 for unknown
		events  []github.TimelineEvent
		segs    string
		lanes   string
		reviews string
		block   string
	}{
		{name: "requested at creation, approved, merged", events: []github.TimelineEvent{
			evRequest(0, "bob"), evReview(5, "bob", ReviewApproved), evMerged(8), evClosed(8),
		}, segs: "waiting[bob]0-5 approved[bob]5-8 merged8-", lanes: "bob:0-5", reviews: "bob approved@5"},
		{name: "unattended until a request", events: []github.TimelineEvent{evRequest(3, "bob")},
			segs: "unattended0-3 waiting[bob]3-", lanes: "bob:3-"},
		{name: "a re-request ends the change request", events: []github.TimelineEvent{
			evRequest(0, "bob"), evReview(2, "bob", ReviewChangesRequested), evRequest(6, "bob"), evReview(9, "bob", ReviewApproved),
		}, segs: "waiting[bob]0-2 changes_requested[bob]2-6 waiting[bob]6-9 approved[bob]9-", lanes: "bob:0-2,6-9",
			reviews: "bob changes_requested@2 bob approved@9"},
		{name: "a standing change request blocks", events: []github.TimelineEvent{
			evRequest(0, "bob"), evReview(2, "bob", ReviewChangesRequested), evReview(3, "bob", ReviewCommented),
		}, segs: "waiting[bob]0-2 changes_requested[bob]2-", lanes: "bob:0-2", reviews: "bob changes_requested@2", block: "bob changes_requested@2"},
		{name: "a request for someone else does not end the change request", events: []github.TimelineEvent{
			evRequest(0, "bob"), evReview(2, "bob", ReviewChangesRequested), evRequest(4, "carol"),
		}, segs: "waiting[bob]0-2 changes_requested[bob]2-", lanes: "bob:0-2 carol:4-",
			reviews: "bob changes_requested@2", block: "bob changes_requested@2"},
		{name: "a ready event ends the change request's segment, not its block", events: []github.TimelineEvent{
			evRequest(0, "bob"), evReview(2, "bob", ReviewChangesRequested), evDraft(3), evReady(7),
		}, segs: "waiting[bob]0-2 changes_requested[bob]2-3 draft3-7 unattended7-", lanes: "bob:0-2", reviews: "bob changes_requested@2", block: "bob changes_requested@2"},
		{name: "a re-request removed again still leaves the block", events: []github.TimelineEvent{
			evRequest(0, "bob"), evReview(2, "bob", ReviewChangesRequested), evRequest(4, "bob"), evUnrequest(6, "bob"),
		}, segs: "waiting[bob]0-2 changes_requested[bob]2-4 waiting[bob]4-6 unattended6-", lanes: "bob:0-2,4-6", reviews: "bob changes_requested@2", block: "bob changes_requested@2"},
		{name: "a later approval ends the block", events: []github.TimelineEvent{
			evRequest(0, "bob"), evReview(2, "bob", ReviewChangesRequested), evReview(4, "bob", ReviewApproved),
		}, segs: "waiting[bob]0-2 changes_requested[bob]2-4 approved[bob]4-", lanes: "bob:0-2", reviews: "bob changes_requested@2 bob approved@4"},
		{name: "a dismissed approval: its tick, then a wait from the dismissal", events: []github.TimelineEvent{
			evRequest(0, "bob"), evReview(2, "bob", ReviewApproved), {Type: github.TimelineReviewDismissed, At: hr(5), Reviewer: "bob", State: ReviewApproved},
		}, segs: "waiting[bob]0-2 approved[bob]2-5 waiting[bob]5-", lanes: "bob:0-2,5-", reviews: "bob approved@2"},
		{name: "a dismissed change request ends the block", events: []github.TimelineEvent{
			evRequest(0, "bob"), evReview(2, "bob", ReviewChangesRequested), {Type: github.TimelineReviewDismissed, At: hr(5), Reviewer: "bob", State: ReviewChangesRequested},
		}, segs: "waiting[bob]0-2 changes_requested[bob]2-5 waiting[bob]5-", lanes: "bob:0-2,5-", reviews: "bob changes_requested@2"},
		{name: "a request swapped for another in the same second is one wait", events: []github.TimelineEvent{
			evRequest(1, "bob"), evUnrequest(4, "bob"), evRequest(4, "carol"),
		}, segs: "unattended0-1 waiting[bob,carol]1-", lanes: "bob:1-4 carol:4-"},
		{name: "a removed request", events: []github.TimelineEvent{evRequest(1, "bob"), evUnrequest(4, "bob")},
			segs: "unattended0-1 waiting[bob]1-4 unattended4-", lanes: "bob:1-4"},
		{name: "overlapping waits form one segment with every reviewer", events: []github.TimelineEvent{
			evRequest(1, "bob"), evRequest(2, "carol"), evReview(3, "bob", ReviewCommented), evReview(6, "carol", ReviewApproved),
		}, segs: "unattended0-1 waiting[bob,carol]1-6 approved[carol]6-", lanes: "bob:1-3 carol:2-6", reviews: "carol approved@6"},
		{name: "a newer request does not reset an older wait", events: []github.TimelineEvent{
			evRequest(1, "bob"), evRequest(4, "bob"), evReview(6, "bob", ReviewApproved),
		}, segs: "unattended0-1 waiting[bob]1-6 approved[bob]6-", lanes: "bob:1-6", reviews: "bob approved@6"},
		{name: "a waiting reviewer outlasts an approval", events: []github.TimelineEvent{
			evRequest(0, "bob"), evRequest(0, "carol"), evReview(2, "carol", ReviewApproved),
		}, segs: "waiting[bob,carol]0-", lanes: "bob:0- carol:0-2", reviews: "carol approved@2"},
		{name: "opened as a draft: the draft round-trip", draft: false, events: []github.TimelineEvent{
			evReady(2), evRequest(2, "bob"), evDraft(4), evReady(6), evReview(9, "bob", ReviewApproved),
		}, segs: "draft0-2 waiting[bob]2-4 draft4-6 waiting[bob]6-9 approved[bob]9-", lanes: "bob:2-4,6-9", reviews: "bob approved@9"},
		{name: "opened ready, then a draft", events: []github.TimelineEvent{evDraft(1), evReady(3)},
			segs: "unattended0-1 draft1-3 unattended3-"},
		{name: "a draft without draft events is as it is now", draft: true, events: []github.TimelineEvent{evRequest(1, "bob")},
			segs: "draft0-"},
		{name: "reopened", events: []github.TimelineEvent{evClosed(2), evReopened(5), evRequest(5, "bob"), evMerged(7), evClosed(7)},
			segs: "unattended0-2 closed2-5 waiting[bob]5-7 merged7-", lanes: "bob:5-7"},
		{name: "a waiting reviewer does not wait while the PR is closed", events: []github.TimelineEvent{
			evRequest(1, "bob"), evClosed(2), evReopened(5),
		}, segs: "unattended0-1 waiting[bob]1-2 closed2-5 waiting[bob]5-", lanes: "bob:1-2,5-"},
		{name: "a team request answered on the team's behalf", events: []github.TimelineEvent{
			evRequest(0, "team:web"), evReview(3, "carol", ReviewApproved, "team:web"),
		}, segs: "waiting[team:web]0-3 approved[carol]3-", lanes: "team:web:0-3", reviews: "carol approved@3"},
		{name: "a team request stays outstanding after a member's own review", events: []github.TimelineEvent{
			evRequest(0, "team:web"), evReview(3, "carol", ReviewCommented),
		}, segs: "waiting[team:web]0-", lanes: "team:web:0-"},
		{name: "the author's own replies and a dismissed approval decide nothing", events: []github.TimelineEvent{
			evRequest(0, "bob"), evReview(1, "alice", ReviewCommented), evReview(2, "bob", ReviewDismissed),
		}, segs: "waiting[bob]0-2 unattended2-", lanes: "bob:0-2"},
		{name: "events out of order are replayed in time order", events: []github.TimelineEvent{
			evReview(5, "bob", ReviewApproved), evRequest(1, "bob"),
		}, segs: "unattended0-1 waiting[bob]1-5 approved[bob]5-", lanes: "bob:1-5", reviews: "bob approved@5"},
		{name: "an unknown creation starts at the first event", created: -1, events: []github.TimelineEvent{evRequest(2, "bob")},
			segs: "waiting[bob]2-", lanes: "bob:2-"},
		{name: "a request only while a draft never waited", events: []github.TimelineEvent{
			evReady(4), evRequest(1, "bob"), evUnrequest(2, "bob"),
		}, segs: "draft0-4 unattended4-"},
		{name: "no creation and no events", created: -1, segs: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tl := &github.PRTimeline{Repo: "acme/api", Number: 1, Author: "alice", State: "open", IsDraft: tc.draft, UpdatedAt: hr(10), Events: tc.events}
			if tc.created >= 0 {
				tl.CreatedAt = hr(tc.created)
			}
			before := slices.Clone(tl.Events)
			got := BuildTimeline(tl)
			if s := segText(got.Segments); s != tc.segs {
				t.Errorf("segments\n got %s\nwant %s", s, tc.segs)
			}
			if s := laneText(got.Lanes); s != tc.lanes {
				t.Errorf("lanes\n got %s\nwant %s", s, tc.lanes)
			}
			if s := reviewText(got.Reviews); s != tc.reviews {
				t.Errorf("reviews\n got %s\nwant %s", s, tc.reviews)
			}
			if s := reviewText(got.Blocking); s != tc.block {
				t.Errorf("blocking\n got %s\nwant %s", s, tc.block)
			}
			if !reflect.DeepEqual(before, tl.Events) {
				t.Error("BuildTimeline reordered its input")
			}
			if !got.UpdatedAt.Equal(hr(10)) || got.Segments == nil {
				t.Errorf("updated_at %v, segments %v", got.UpdatedAt, got.Segments)
			}
			v := &validator{}
			got.Freshness = Fresh
			v.checkTimeline("pr:acme/api#1", got)
			if err := v.err(); err != nil {
				t.Error(err)
			}
		})
	}
}

// timelineOf builds a fresh timeline for p from events, as assemble does.
func timelineOf(p *PRInfo, created time.Time, events ...github.TimelineEvent) *PRTimeline {
	tl := BuildTimeline(&github.PRTimeline{Repo: p.Repo, Number: p.Number, Author: p.Author, State: p.State, IsDraft: p.IsDraft,
		CreatedAt: created, UpdatedAt: p.UpdatedAt, Events: events})
	tl.Freshness = Fresh
	return tl
}

func reqEvent(at time.Time, who string) github.TimelineEvent {
	return github.TimelineEvent{Type: github.TimelineReviewRequested, At: at, Reviewer: who}
}

func reviewEvent(at time.Time, who, state string) github.TimelineEvent {
	return github.TimelineEvent{Type: github.TimelineReview, At: at, Reviewer: who, State: state}
}

// TestWaitingSinceFromTheTimeline is gh-104 requirement 5's first half: with
// a fresh timeline, waiting_since is the start of the current waiting or
// unattended segment, exact rather than a lower bound; without one, or with
// one the PR has changed since, gh-99's approximation stands. The evidence
// and waiting_source say which applied.
func TestWaitingSinceFromTheTimeline(t *testing.T) {
	// Bob was asked on Oct 21, then team:web on Oct 26, whose request time
	// the PR's facts do not know: the approximation is a lower bound from
	// bob's request.
	awaiting := func() *PRInfo {
		p := readyPR()
		withRequests(p, requestUser("bob", day(26, 10)), ReviewRequest{Reviewer: "team:web", Kind: ReviewerTeam})
		return p
	}
	events := []github.TimelineEvent{reqEvent(day(21, 9), "team:web"), reqEvent(day(22, 9), "bob"), reviewEvent(day(23, 9), "bob", ReviewCommented), reqEvent(day(26, 10), "bob")}
	for _, tc := range []struct {
		name     string
		edit     func(p *PRInfo)
		since    time.Time
		lower    bool
		source   string
		evidence string
		unc      []string
	}{
		{"approximation without a timeline", func(p *PRInfo) {}, day(26, 10), true, WaitingEstimated,
			"ready since Oct 21; review requested from bob Oct 26 and team:web; wait estimated: no PR timeline yet", []string{"requested_at:team:web"}},
		{"the earliest wait still open on a fresh timeline", func(p *PRInfo) { p.Timeline = timelineOf(p, day(21, 9), events...) },
			day(21, 9), false, WaitingFromTimeline, "ready since Oct 21; review requested from bob Oct 26 and team:web; waiting since Oct 21 by its timeline", nil},
		{"a timeline the PR has changed since", func(p *PRInfo) {
			p.Timeline = timelineOf(p, day(21, 9), events...)
			p.Timeline.Freshness = Stale
		}, day(26, 10), true, WaitingEstimated, "ready since Oct 21; review requested from bob Oct 26 and team:web; wait estimated: the PR's timeline predates its last update", []string{"requested_at:team:web"}},
		{"a truncated timeline", func(p *PRInfo) {
			p.Timeline = timelineOf(p, day(21, 9), events...)
			p.Timeline.Truncated = true
		}, day(26, 10), true, WaitingEstimated, "ready since Oct 21; review requested from bob Oct 26 and team:web; wait estimated: the PR's timeline is incomplete", []string{"requested_at:team:web"}},
		{"a timeline that shows no wait now", func(p *PRInfo) {
			p.Timeline = timelineOf(p, day(21, 9), reqEvent(day(22, 9), "bob"), reviewEvent(day(23, 9), "bob", ReviewApproved))
		}, day(26, 10), true, WaitingEstimated, "ready since Oct 21; review requested from bob Oct 26 and team:web; wait estimated: the PR's timeline shows no open request for bob", []string{"requested_at:team:web"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := awaiting()
			tc.edit(p)
			f := prFacts(p, false, systemicStats, factPeople, DefaultFactThresholds(), factsNow)
			if f.Move != MoveReviewer || !f.WaitingSince.Equal(tc.since) || f.WaitingLowerBound != tc.lower || f.WaitingSource != tc.source {
				t.Errorf("move %s, waiting since %v (lower bound %v, source %q); want reviewer, %v (%v, %q)", f.Move, f.WaitingSince, f.WaitingLowerBound, f.WaitingSource, tc.since, tc.lower, tc.source)
			}
			if f.Reasons[0].Evidence != tc.evidence {
				t.Errorf("evidence\n got %s\nwant %s", f.Reasons[0].Evidence, tc.evidence)
			}
			if !slices.Equal(f.Uncertain, tc.unc) {
				t.Errorf("uncertain %v, want %v", f.Uncertain, tc.unc)
			}
		})
	}

	// A wait that ended does not count: bob was asked on Oct 21 and has
	// commented; alice, asked on Oct 28, is waited on since then, though the
	// waiting segment began with bob.
	p0 := readyPR()
	withRequests(p0, requestUser("alice", day(28, 9)))
	p0.Timeline = timelineOf(p0, day(21, 9), reqEvent(day(21, 9), "bob"), reqEvent(day(28, 9), "alice"), reviewEvent(day(29, 9), "bob", ReviewCommented))
	if f := prFacts(p0, false, systemicStats, factPeople, DefaultFactThresholds(), factsNow); !f.WaitingSince.Equal(day(28, 9)) || f.WaitingSource != WaitingFromTimeline ||
		!slices.Equal(f.WaitingOn, []string{"alice"}) || f.Reasons[0].Evidence != "ready since Oct 21; review requested from alice Oct 28; waiting since Oct 28 by its timeline" {
		t.Errorf("an ended wait counted: %+v", f)
	}
	// Bob's request swapped for carol's in the same second: carol's wait
	// starts then.
	withRequests(p0, requestUser("carol", day(25, 10)))
	p0.Timeline = timelineOf(p0, day(21, 9), reqEvent(day(22, 9), "bob"),
		github.TimelineEvent{Type: github.TimelineReviewRequestRemoved, At: day(25, 10), Reviewer: "bob"}, reqEvent(day(25, 10), "carol"))
	if f := prFacts(p0, false, systemicStats, factPeople, DefaultFactThresholds(), factsNow); !f.WaitingSince.Equal(day(25, 10)) || f.WaitingSource != WaitingFromTimeline {
		t.Errorf("a same-second swap: %+v", f)
	}

	// Nobody requested: the unattended segment's start, not ready_at.
	p := readyPR()
	p.Timeline = timelineOf(p, day(20, 9), github.TimelineEvent{Type: github.TimelineReady, At: day(21, 9)},
		reqEvent(day(22, 9), "bob"), github.TimelineEvent{Type: github.TimelineReviewRequestRemoved, At: day(24, 12), Reviewer: "bob"})
	f := prFacts(p, false, systemicStats, factPeople, DefaultFactThresholds(), factsNow)
	if f.Move != MoveNobody || !f.WaitingSince.Equal(day(24, 12)) || f.WaitingSource != WaitingFromTimeline ||
		f.Reasons[0].Evidence != "ready since Oct 21; no review requested and no approval; waiting since Oct 24 by its timeline" {
		t.Errorf("nobody's move from the timeline: %+v", f)
	}
	p.Timeline = nil
	f = prFacts(p, false, systemicStats, factPeople, DefaultFactThresholds(), factsNow)
	if f.Move != MoveNobody || !f.WaitingSince.Equal(p.ReadyAt) || f.WaitingSource != WaitingEstimated {
		t.Errorf("nobody's move without a timeline: %+v", f)
	}
}

// TestRule3FromTheTimeline is gh-104 requirement 5's second half: with a
// fresh timeline, "changes requested and not re-requested" is decided from
// the events.
func TestRule3FromTheTimeline(t *testing.T) {
	cr := func(p *PRInfo) {
		p.ReviewDecision = ReviewChangesRequested
		p.Reviews = []Review{{Author: "bob", State: ReviewChangesRequested, SubmittedAt: day(24, 16)}}
	}
	run := func(p *PRInfo) *PRFacts {
		return prFacts(p, false, systemicStats, factPeople, DefaultFactThresholds(), factsNow)
	}

	// Bob requested changes; the author drafted and marked it ready again.
	// That ends the changes_requested segment, but bob's latest review still
	// reads CHANGES_REQUESTED and GitHub still blocks the merge: rule 3
	// holds, by the timeline too.
	p := readyPR()
	cr(p)
	if f := run(p); f.Move != MoveAuthor || f.Reasons[0].Code != ReasonChangesRequested || f.WaitingSource != WaitingEstimated ||
		f.Reasons[0].Evidence != "changes requested by bob Oct 24; not re-requested since, by its latest reviews: no PR timeline yet" {
		t.Fatalf("without a timeline: %+v", f)
	}
	p.Timeline = timelineOf(p, day(21, 9), reqEvent(day(21, 9), "bob"), reviewEvent(day(24, 16), "bob", ReviewChangesRequested),
		github.TimelineEvent{Type: github.TimelineDraft, At: day(25, 9)}, github.TimelineEvent{Type: github.TimelineReady, At: day(27, 9)})
	if f := run(p); f.Move != MoveAuthor || f.Reasons[0].Code != ReasonChangesRequested || !f.WaitingSince.Equal(day(24, 16)) || f.WaitingSource != WaitingFromTimeline ||
		f.Reasons[0].Evidence != "changes requested by bob Oct 24; not re-requested since, by its timeline" {
		t.Fatalf("a ready event after the change request: %+v", f)
	}
	// Nor does a re-request that was removed again.
	p.Timeline = timelineOf(p, day(21, 9), reqEvent(day(21, 9), "bob"), reviewEvent(day(24, 16), "bob", ReviewChangesRequested),
		reqEvent(day(26, 9), "bob"), github.TimelineEvent{Type: github.TimelineReviewRequestRemoved, At: day(26, 12), Reviewer: "bob"})
	if f := run(p); f.Move != MoveAuthor || f.Reasons[0].Code != ReasonChangesRequested || !f.WaitingSince.Equal(day(24, 16)) {
		t.Fatalf("a re-request removed again: %+v", f)
	}
	// A later approval does.
	p.Timeline = timelineOf(p, day(21, 9), reqEvent(day(21, 9), "bob"), reviewEvent(day(24, 16), "bob", ReviewChangesRequested), reviewEvent(day(26, 9), "bob", ReviewApproved))
	if f := run(p); slices.Contains(codes(f.Reasons), ReasonChangesRequested) {
		t.Fatalf("an approval after the change request: %+v", f)
	}

	// The reviews are unknown (a bookmark's lookup), so the approximation
	// cannot decide rule 3; the timeline can.
	p = readyPR()
	p.Reviews, p.ReviewDecision = nil, ""
	if f := run(p); f.Move != MoveUnknown || !slices.Contains(f.Uncertain, "reviews") {
		t.Fatalf("unknown reviews without a timeline: %+v", f)
	}
	p.Timeline = timelineOf(p, day(21, 9), reqEvent(day(21, 9), "bob"), reviewEvent(day(24, 16), "bob", ReviewChangesRequested))
	f := run(p)
	if f.Move != MoveAuthor || f.Reasons[0].Code != ReasonChangesRequested || !f.WaitingSince.Equal(day(24, 16)) || f.WaitingSource != WaitingFromTimeline ||
		f.Reasons[0].Evidence != "changes requested by bob Oct 24; not re-requested since, by its timeline" || slices.Contains(f.Uncertain, "reviews") {
		t.Fatalf("rule 3 from the timeline: %+v", f)
	}

	// A re-request on the timeline answers it, though GitHub's outstanding
	// request carries no time.
	p = readyPR()
	cr(p)
	withRequests(p, ReviewRequest{Reviewer: "bob", Kind: ReviewerUser})
	p.Timeline = timelineOf(p, day(21, 9), reqEvent(day(21, 9), "bob"), reviewEvent(day(24, 16), "bob", ReviewChangesRequested), reqEvent(day(27, 9), "bob"))
	if f := run(p); f.Move != MoveReviewer || !f.WaitingSince.Equal(day(27, 9)) || f.WaitingLowerBound || len(f.Uncertain) != 0 {
		t.Fatalf("a re-request on the timeline: %+v", f)
	}
}
