package dash

import (
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/github"
)

// factsNow is generated_at in the facts tests.
var factsNow = time.Date(2026, 10, 30, 16, 0, 0, 0, time.UTC)

func day(d int, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, time.UTC) }

func intp(n int) *int { return &n }

// readyPR is a ready, mergeable PR by alice with green checks, a ticket and no
// reviews or requests, every fact known: the base the rule tests modify.
func readyPR() *PRInfo {
	return &PRInfo{
		Number: 41, Repo: "acme/api", State: "open", ReviewDecision: reviewRequired, Checks: "success",
		ReviewersKnown: true, UpdatedAt: day(29, 10), Author: "alice", CreatedAt: day(20, 9), ReadyAt: day(21, 9),
		Additions: intp(120), Deletions: intp(30), Mergeable: MergeMergeable, BaseRef: "main",
		ReviewRequests: []ReviewRequest{}, Reviews: []Review{},
		CheckDetails: []CheckDetail{{Name: "build", State: CheckSuccess}, {Name: "vendor-hsm-tests", State: CheckSuccess}},
		TicketKeys:   []string{"TEAM-41"}, Involvement: []Involvement{{Person: "alice", Role: RoleAuthor}},
	}
}

var factPeople = []Person{{Login: "alice", Me: true}, {Login: "bob"}}

// systemicStats marks vendor-hsm-tests as failing everywhere in acme/api.
var systemicStats = map[string]CheckStat{
	"vendor-hsm-tests": {Name: "vendor-hsm-tests", Runs: 6, Failures: 5, Systemic: true, Evidence: "fails on 5 of 6 open PRs by the configured people in acme/api"},
	"build":            {Name: "build", Runs: 6, Evidence: "fails on 0 of 6 open PRs by the configured people in acme/api"},
}

func requestUser(login string, at time.Time) ReviewRequest {
	return ReviewRequest{Reviewer: login, Kind: ReviewerUser, RequestedAt: at}
}

func requestTeam(slug string, at time.Time) ReviewRequest {
	return ReviewRequest{Reviewer: teamPrefix + slug, Kind: ReviewerTeam, RequestedAt: at}
}

// withRequests sets p's outstanding requests and the matching
// requested_reviewers.
func withRequests(p *PRInfo, rs ...ReviewRequest) {
	p.ReviewRequests = rs
	p.RequestedReviewers = nil
	for _, r := range rs {
		p.RequestedReviewers = append(p.RequestedReviewers, r.Reviewer)
	}
}

func failCheck(p *PRInfo, name string) {
	for i := range p.CheckDetails {
		if p.CheckDetails[i].Name == name {
			p.CheckDetails[i].State = CheckFailure
			p.Checks = "failure"
			return
		}
	}
	p.CheckDetails = append(p.CheckDetails, CheckDetail{Name: name, State: CheckFailure})
	p.Checks = "failure"
}

func codes(rs []Reason) []string {
	out := []string{}
	for _, r := range rs {
		out = append(out, r.Code)
	}
	return out
}

func TestPRMoveRules(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    func(p *PRInfo)
		move    string
		reasons []string
		people  []string
		ci      string
	}{
		{"merged", func(p *PRInfo) { p.State, p.MergedAt = "merged", day(28, 13) }, MoveNone, []string{ReasonClosed}, []string{}, CIGreen},
		{"closed unmerged", func(p *PRInfo) { p.State, p.ClosedAt = "closed", day(28, 13) }, MoveNone, []string{ReasonClosed}, []string{}, CIGreen},
		{"draft", func(p *PRInfo) { p.IsDraft, p.ReadyAt = true, time.Time{} }, MoveAuthor, []string{ReasonDraft, ReasonNoReviewer}, []string{"alice"}, CIGreen},
		{"changes requested, not re-requested", func(p *PRInfo) {
			p.ReviewDecision = ReviewChangesRequested
			p.Reviews = []Review{{Author: "bob", State: ReviewChangesRequested, SubmittedAt: day(24, 16)}}
		}, MoveAuthor, []string{ReasonChangesRequested, ReasonNoReviewer}, []string{"alice"}, CIGreen},
		{"changes requested, then re-requested", func(p *PRInfo) {
			p.ReviewDecision = ReviewChangesRequested
			p.Reviews = []Review{{Author: "bob", State: ReviewChangesRequested, SubmittedAt: day(24, 16)}}
			withRequests(p, requestUser("bob", day(27, 9)))
		}, MoveReviewer, []string{ReasonAwaitingReview}, []string{"bob"}, CIGreen},
		{"a request older than the change request is no re-request", func(p *PRInfo) {
			p.ReviewDecision = ReviewChangesRequested
			p.Reviews = []Review{{Author: "bob", State: ReviewChangesRequested, SubmittedAt: day(24, 16)}}
			withRequests(p, requestUser("bob", day(23, 9)))
		}, MoveAuthor, []string{ReasonChangesRequested, ReasonAwaitingReview}, []string{"alice"}, CIGreen},
		{"a later comment does not hide a change request", func(p *PRInfo) {
			// gh-97 keeps each reviewer's latest opinionated review.
			p.ReviewDecision = ReviewChangesRequested
			p.Reviews = []Review{{Author: "bob", State: ReviewChangesRequested, SubmittedAt: day(24, 16)}, {Author: "carol", State: ReviewCommented, SubmittedAt: day(25, 9)}}
		}, MoveAuthor, []string{ReasonChangesRequested, ReasonNoReviewer}, []string{"alice"}, CIGreen},
		{"conflicts", func(p *PRInfo) {
			p.Mergeable = MergeConflicting
			withRequests(p, requestUser("bob", day(26, 10)))
		}, MoveAuthor, []string{ReasonConflicts, ReasonAwaitingReview}, []string{"alice"}, CIGreen},
		{"own failing check", func(p *PRInfo) {
			failCheck(p, "build")
			withRequests(p, requestUser("bob", day(26, 10)))
		}, MoveAuthor, []string{ReasonCIFailing, ReasonAwaitingReview}, []string{"alice"}, CIFailing},
		{"approved", func(p *PRInfo) {
			p.ReviewDecision = ReviewApproved
			p.Reviews = []Review{{Author: "bob", State: ReviewApproved, SubmittedAt: day(28, 11)}}
		}, MoveAuthor, []string{ReasonApprovedUnmerged}, []string{"alice"}, CIGreen},
		{"approved, and another review requested", func(p *PRInfo) {
			p.ReviewDecision = ReviewApproved
			p.Reviews = []Review{{Author: "bob", State: ReviewApproved, SubmittedAt: day(28, 11)}}
			withRequests(p, requestUser("carol", day(28, 12)))
		}, MoveAuthor, []string{ReasonApprovedUnmerged, ReasonAwaitingReview}, []string{"alice"}, CIGreen},
		{"awaiting a reviewer", func(p *PRInfo) { withRequests(p, requestUser("bob", day(26, 10))) },
			MoveReviewer, []string{ReasonAwaitingReview}, []string{"bob"}, CIGreen},
		{"awaiting a team only", func(p *PRInfo) { withRequests(p, requestTeam("web", day(26, 10))) },
			MoveReviewer, []string{ReasonAwaitingReview, ReasonTeamOnly}, []string{"team:web"}, CIGreen},
		{"no reviewer", func(p *PRInfo) {}, MoveNobody, []string{ReasonNoReviewer}, []string{}, CIGreen},
		{"precedence: draft before changes requested, conflicts, CI and approval", func(p *PRInfo) {
			p.IsDraft, p.ReadyAt = true, time.Time{}
			p.Reviews = []Review{{Author: "bob", State: ReviewChangesRequested, SubmittedAt: day(24, 16)}}
			p.Mergeable = MergeConflicting
			failCheck(p, "build")
			p.ReviewDecision = ReviewApproved
		}, MoveAuthor, []string{ReasonDraft, ReasonChangesRequested, ReasonConflicts, ReasonCIFailing, ReasonApprovedUnmerged}, []string{"alice"}, CIFailing},
		{"a draft with conflicts and a failing check lists all three", func(p *PRInfo) {
			p.IsDraft, p.ReadyAt = true, time.Time{}
			p.Mergeable = MergeConflicting
			failCheck(p, "build")
		}, MoveAuthor, []string{ReasonDraft, ReasonConflicts, ReasonCIFailing, ReasonNoReviewer}, []string{"alice"}, CIFailing},
		{"a systemic failure does not decide the move", func(p *PRInfo) {
			failCheck(p, "vendor-hsm-tests")
			withRequests(p, requestUser("bob", day(26, 10)))
		}, MoveReviewer, []string{ReasonAwaitingReview, ReasonCISystemicOnly}, []string{"bob"}, CISystemicOnly},
		{"an own failure besides a systemic one does", func(p *PRInfo) {
			failCheck(p, "vendor-hsm-tests")
			failCheck(p, "build")
			withRequests(p, requestUser("bob", day(26, 10)))
		}, MoveAuthor, []string{ReasonCIFailing, ReasonAwaitingReview}, []string{"alice"}, CIFailing},
		{"pending checks", func(p *PRInfo) {
			p.CheckDetails[0].State, p.Checks = CheckPending, "pending"
			withRequests(p, requestUser("bob", day(26, 10)))
		}, MoveReviewer, []string{ReasonAwaitingReview, ReasonCIPending}, []string{"bob"}, CIPending},
		{"no ticket", func(p *PRInfo) { p.TicketKeys = []string{} }, MoveNobody, []string{ReasonNoReviewer, ReasonNoTicket}, []string{}, CIGreen},
		{"stale", func(p *PRInfo) { p.UpdatedAt = factsNow.AddDate(0, 0, -30) }, MoveNobody, []string{ReasonNoReviewer, ReasonStale}, []string{}, CIGreen},
		{"not yet stale", func(p *PRInfo) { p.UpdatedAt = factsNow.AddDate(0, 0, -30).Add(time.Minute) }, MoveNobody, []string{ReasonNoReviewer}, []string{}, CIGreen},
		{"no checks", func(p *PRInfo) { p.CheckDetails, p.Checks = []CheckDetail{}, "none" }, MoveNobody, []string{ReasonNoReviewer}, []string{}, CINone},
		{"every informational reason at once", func(p *PRInfo) {
			failCheck(p, "vendor-hsm-tests")
			p.CheckDetails = append(p.CheckDetails, CheckDetail{Name: "e2e", State: CheckPending})
			p.TicketKeys = []string{}
			p.UpdatedAt = factsNow.AddDate(0, 0, -40)
			withRequests(p, requestTeam("web", day(26, 10)))
		}, MoveReviewer, []string{ReasonAwaitingReview, ReasonCISystemicOnly, ReasonCIPending, ReasonNoTicket, ReasonStale, ReasonTeamOnly}, []string{"team:web"}, CISystemicOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := readyPR()
			tc.edit(p)
			f := prFacts(p, false, systemicStats, factPeople, DefaultFactThresholds(), factsNow)
			if f.Move != tc.move || !slices.Equal(codes(f.Reasons), tc.reasons) || !reflect.DeepEqual(f.MovePeople, tc.people) || f.CI != tc.ci {
				t.Errorf("move %s, reasons %v, move_people %#v, ci %s\nwant %s, %v, %#v, %s", f.Move, codes(f.Reasons), f.MovePeople, f.CI, tc.move, tc.reasons, tc.people, tc.ci)
			}
			for _, r := range f.Reasons {
				if strings.TrimSpace(r.Evidence) == "" || strings.Contains(r.Evidence, "  ") {
					t.Errorf("reason %s has evidence %q", r.Code, r.Evidence)
				}
			}
			if len(f.Uncertain) > 0 {
				t.Errorf("every input is known, yet uncertain = %v", f.Uncertain)
			}
		})
	}
}

func TestPRWaiting(t *testing.T) {
	t.Run("waiting on reviewers starts at the later of ready and requested", func(t *testing.T) {
		p := readyPR()
		p.ReadyAt = day(22, 9)
		withRequests(p, requestUser("bob", day(20, 9)))
		f := prFacts(p, false, nil, factPeople, DefaultFactThresholds(), factsNow)
		if !f.WaitingSince.Equal(day(22, 9)) || f.WaitingLowerBound || !slices.Equal(f.WaitingOn, []string{"bob"}) {
			t.Errorf("waiting since %v (lower bound %v) on %v", f.WaitingSince, f.WaitingLowerBound, f.WaitingOn)
		}
	})
	t.Run("a second reviewer added later does not reset the older wait", func(t *testing.T) {
		p := readyPR()
		withRequests(p, requestUser("bob", day(22, 9)))
		before := prFacts(p, false, nil, factPeople, DefaultFactThresholds(), factsNow)
		withRequests(p, requestUser("carol", day(28, 15)), requestUser("bob", day(22, 9)))
		after := prFacts(p, false, nil, factPeople, DefaultFactThresholds(), factsNow)
		if !before.WaitingSince.Equal(day(22, 9)) || !after.WaitingSince.Equal(before.WaitingSince) {
			t.Errorf("waiting since %v, then %v after carol was added; want %v both times", before.WaitingSince, after.WaitingSince, day(22, 9))
		}
		if !slices.Equal(after.WaitingOn, []string{"carol", "bob"}) {
			t.Errorf("waiting on %v", after.WaitingOn)
		}
	})
	t.Run("a request of unknown time makes the wait a lower bound", func(t *testing.T) {
		p := readyPR()
		withRequests(p, requestUser("bob", time.Time{}), requestUser("carol", day(25, 9)))
		f := prFacts(p, false, nil, factPeople, DefaultFactThresholds(), factsNow)
		if !f.WaitingSince.Equal(day(25, 9)) || !f.WaitingLowerBound || !slices.Contains(f.Uncertain, "requested_at:bob") {
			t.Errorf("waiting since %v, lower bound %v, uncertain %v", f.WaitingSince, f.WaitingLowerBound, f.Uncertain)
		}
	})
	t.Run("a wait that starts at ready_at is exact", func(t *testing.T) {
		p := readyPR()
		withRequests(p, requestUser("bob", time.Time{}), requestUser("carol", day(20, 9)))
		f := prFacts(p, false, nil, factPeople, DefaultFactThresholds(), factsNow)
		if !f.WaitingSince.Equal(p.ReadyAt) || f.WaitingLowerBound {
			t.Errorf("waiting since %v, lower bound %v", f.WaitingSince, f.WaitingLowerBound)
		}
	})
	t.Run("no known request time leaves waiting_since out", func(t *testing.T) {
		p := readyPR()
		withRequests(p, requestUser("bob", time.Time{}))
		f := prFacts(p, false, nil, factPeople, DefaultFactThresholds(), factsNow)
		if !f.WaitingSince.IsZero() || f.WaitingLowerBound || !slices.Contains(f.Uncertain, "requested_at:bob") {
			t.Errorf("waiting since %v, lower bound %v, uncertain %v", f.WaitingSince, f.WaitingLowerBound, f.Uncertain)
		}
	})
	t.Run("nobody's move waits since ready", func(t *testing.T) {
		f := prFacts(readyPR(), false, nil, factPeople, DefaultFactThresholds(), factsNow)
		if !f.WaitingSince.Equal(day(21, 9)) || f.WaitingOn != nil {
			t.Errorf("waiting since %v on %v", f.WaitingSince, f.WaitingOn)
		}
	})
	t.Run("a change request waits since the review", func(t *testing.T) {
		p := readyPR()
		p.ReviewDecision = ReviewChangesRequested
		p.Reviews = []Review{{Author: "bob", State: ReviewChangesRequested, SubmittedAt: day(24, 16)}, {Author: "carol", State: ReviewChangesRequested, SubmittedAt: day(23, 10)}}
		f := prFacts(p, false, nil, factPeople, DefaultFactThresholds(), factsNow)
		if f.Move != MoveAuthor || !f.WaitingSince.Equal(day(23, 10)) {
			t.Errorf("move %s waiting since %v", f.Move, f.WaitingSince)
		}
	})
	t.Run("other author moves have no waiting_since", func(t *testing.T) {
		p := readyPR()
		p.Mergeable = MergeConflicting
		if f := prFacts(p, false, nil, factPeople, DefaultFactThresholds(), factsNow); !f.WaitingSince.IsZero() {
			t.Errorf("waiting since %v", f.WaitingSince)
		}
	})
}

// TestTeamRequestReachesMovePeople: a team-only request found in alice's
// review-requested inventory set makes the PR her move, while waiting_on
// names only the team GitHub reports.
func TestTeamRequestReachesMovePeople(t *testing.T) {
	p := readyPR()
	p.Author = "carol"
	withRequests(p, requestTeam("web", day(25, 12)))
	p.Involvement = []Involvement{{Person: "alice", Role: RoleReviewRequested}}
	f := prFacts(p, false, nil, factPeople, DefaultFactThresholds(), factsNow)
	if f.Move != MoveReviewer || !slices.Equal(f.MovePeople, []string{"team:web", "alice"}) || !slices.Equal(f.WaitingOn, []string{"team:web"}) {
		t.Fatalf("move %s, move_people %v, waiting_on %v", f.Move, f.MovePeople, f.WaitingOn)
	}
	if ev := f.Reasons[0].Evidence; !strings.Contains(ev, "team:web") || !strings.Contains(ev, "only") || !strings.Contains(ev, "alice through a team") {
		t.Errorf("evidence %q does not tell the team request apart", ev)
	}
	// A direct request is named once, and not as through a team.
	withRequests(p, requestTeam("web", day(25, 12)), requestUser("alice", day(25, 12)))
	f = prFacts(p, false, nil, factPeople, DefaultFactThresholds(), factsNow)
	if !slices.Equal(f.MovePeople, []string{"team:web", "alice"}) || strings.Contains(f.Reasons[0].Evidence, "through a team") {
		t.Errorf("direct request: move_people %v, evidence %q", f.MovePeople, f.Reasons[0].Evidence)
	}
}

func TestPRUnknownInputs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		edit      func(p *PRInfo)
		move      string
		ci        string
		uncertain []string
		check     func(t *testing.T, f *PRFacts)
	}{
		{"unknown reviewers make the reviewer rules undecidable", func(p *PRInfo) {
			p.ReviewersKnown, p.ReviewRequests, p.RequestedReviewers = false, nil, nil
		}, MoveUnknown, CIGreen, []string{"requested_reviewers"}, func(t *testing.T, f *PRFacts) {
			if f.MovePeople != nil || !f.WaitingSince.IsZero() || f.WaitingOn != nil {
				t.Errorf("move_people %v, waiting %v on %v: want them unknown", f.MovePeople, f.WaitingSince, f.WaitingOn)
			}
		}},
		{"an earlier rule still decides", func(p *PRInfo) {
			p.ReviewersKnown, p.ReviewRequests, p.RequestedReviewers = false, nil, nil
			p.IsDraft, p.ReadyAt = true, time.Time{}
		}, MoveAuthor, CIGreen, []string{"requested_reviewers"}, nil},
		{"unknown mergeability does not stop the rules", func(p *PRInfo) {
			p.Mergeable = MergeUnknown
			withRequests(p, requestUser("bob", day(26, 10)))
		}, MoveReviewer, CIGreen, []string{"mergeable"}, nil},
		{"unreported mergeability is unknown too", func(p *PRInfo) { p.Mergeable = "" }, MoveNobody, CIGreen, []string{"mergeable"}, nil},
		{"a failure without check details cannot be told systemic", func(p *PRInfo) {
			p.CheckDetails, p.Checks = nil, "failure"
			withRequests(p, requestUser("bob", day(26, 10)))
		}, MoveUnknown, CIUnknown, []string{"check_details"}, nil},
		{"an approval decides past an unknown CI that would also be the author's", func(p *PRInfo) {
			p.CheckDetails, p.Checks = nil, "failure"
			p.ReviewDecision = ReviewApproved
		}, MoveAuthor, CIUnknown, []string{"check_details"}, nil},
		{"the rollup alone can say green", func(p *PRInfo) { p.CheckDetails = nil }, MoveNobody, CIGreen, nil, nil},
		{"no rollup and no details", func(p *PRInfo) { p.CheckDetails, p.Checks = nil, "" }, MoveUnknown, CIUnknown, []string{"checks"}, nil},
		{"an empty check list does not hide a failing rollup", func(p *PRInfo) {
			p.CheckDetails, p.Checks = []CheckDetail{}, "failure"
			withRequests(p, requestUser("bob", day(26, 10)))
		}, MoveUnknown, CIUnknown, []string{"check_details"}, nil},
		{"an empty check list with a pending rollup is pending", func(p *PRInfo) { p.CheckDetails, p.Checks = []CheckDetail{}, "pending" }, MoveNobody, CIPending, nil,
			func(t *testing.T, f *PRFacts) {
				if !slices.Contains(codes(f.Reasons), ReasonCIPending) {
					t.Errorf("reasons %v lack ci_pending", codes(f.Reasons))
				}
			}},
		{"a truncated list may hide a failure", func(p *PRInfo) {
			p.ChecksTruncated, p.Checks = true, "failure"
			withRequests(p, requestUser("bob", day(26, 10)))
		}, MoveUnknown, CIUnknown, []string{"check_details"}, nil},
		{"a truncated list hides a failure besides a systemic one", func(p *PRInfo) {
			failCheck(p, "vendor-hsm-tests")
			p.ChecksTruncated = true
			withRequests(p, requestUser("bob", day(26, 10)))
		}, MoveUnknown, CIUnknown, []string{"check_details"}, nil},
		{"a truncated list with a successful rollup hides nothing", func(p *PRInfo) { p.ChecksTruncated = true }, MoveNobody, CIGreen, nil, nil},
		{"a truncated list with an own failure is failing anyway", func(p *PRInfo) {
			failCheck(p, "build")
			p.ChecksTruncated = true
		}, MoveAuthor, CIFailing, nil, nil},
		{"unknown sizes are not zero", func(p *PRInfo) { p.Additions = nil }, MoveNobody, CIGreen, []string{"additions"}, func(t *testing.T, f *PRFacts) {
			if f.Size != nil || f.SizeBucket != "" {
				t.Errorf("size %v bucket %q", f.Size, f.SizeBucket)
			}
		}},
		{"an unknown update time is not fresh", func(p *PRInfo) { p.UpdatedAt = time.Time{} }, MoveNobody, CIGreen, []string{"updated_at"}, func(t *testing.T, f *PRFacts) {
			if f.Stale != nil {
				t.Errorf("stale = %v", *f.Stale)
			}
		}},
		{"unknown ticket keys are no evidence of no ticket", func(p *PRInfo) { p.TicketKeys = nil }, MoveNobody, CIGreen, []string{"ticket_keys"}, func(t *testing.T, f *PRFacts) {
			if slices.Contains(codes(f.Reasons), ReasonNoTicket) {
				t.Error("no_ticket listed")
			}
		}},
		{"a bookmark's PR without reviews or a decision", func(p *PRInfo) { p.Reviews, p.ReviewDecision = nil, "" }, MoveUnknown, CIGreen, []string{"reviews", "review_decision"}, nil},
		{"a review decision short of changes requested rules out rule 3", func(p *PRInfo) { p.Reviews = nil }, MoveNobody, CIGreen, nil, nil},
		{"an unknown ready time", func(p *PRInfo) { p.ReadyAt = time.Time{} }, MoveNobody, CIGreen, []string{"ready_at"}, func(t *testing.T, f *PRFacts) {
			if !f.WaitingSince.IsZero() {
				t.Errorf("waiting since %v", f.WaitingSince)
			}
		}},
		{"an unknown author", func(p *PRInfo) { p.Author, p.Mergeable = "", MergeConflicting }, MoveAuthor, CIGreen, []string{"author"}, func(t *testing.T, f *PRFacts) {
			if f.MovePeople != nil {
				t.Errorf("move_people %v", f.MovePeople)
			}
		}},
		{"a re-request unknown while reviewers are", func(p *PRInfo) {
			p.ReviewersKnown, p.ReviewRequests, p.RequestedReviewers = false, nil, nil
			p.ReviewDecision = ReviewChangesRequested
			p.Reviews = []Review{{Author: "bob", State: ReviewChangesRequested, SubmittedAt: day(24, 16)}}
		}, MoveUnknown, CIGreen, []string{"requested_reviewers"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := readyPR()
			tc.edit(p)
			f := prFacts(p, false, systemicStats, factPeople, DefaultFactThresholds(), factsNow)
			if f.Move != tc.move || f.CI != tc.ci || !slices.Equal(f.Uncertain, tc.uncertain) {
				t.Errorf("move %s, ci %s, uncertain %v; want %s, %s, %v", f.Move, f.CI, f.Uncertain, tc.move, tc.ci, tc.uncertain)
			}
			if tc.check != nil {
				tc.check(t, f)
			}
		})
	}
}

// TestPRWithOnlyABookmarkLookup: a PR known only through a bookmark has no
// inventory facts; what can be decided is, the rest is unknown.
func TestPRWithOnlyABookmarkLookup(t *testing.T) {
	p := &PRInfo{Number: 7, Repo: "acme/web", State: "open", ReviewDecision: reviewRequired, Checks: "success", ReviewersKnown: true, UpdatedAt: day(27, 12)}
	f := prFacts(p, true, nil, factPeople, DefaultFactThresholds(), factsNow)
	b, _ := json.Marshal(f)
	want := `{"move":"nobody","reasons":[{"code":"no_reviewer","evidence":"no review requested and no approval"}],"move_people":[],"ci":"green","stale":false,"uncertain":["additions","deletions","mergeable","ready_at"]}`
	if string(b) != want {
		t.Errorf("facts\n got %s\nwant %s", b, want)
	}
}

// statsSnapshot builds a snapshot of open PRs from (repo, check states).
func statsSnapshot(prs ...*PRInfo) *Snapshot {
	s := &Snapshot{GeneratedAt: factsNow}
	for i, p := range prs {
		if p.Number == 0 {
			p.Number = i + 1
		}
		s.Nodes = append(s.Nodes, Node{ID: prNodeID(p.Repo, p.Number), Kind: KindPR, Label: p.Repo + "#", PR: p})
	}
	return s
}

func checksPR(repo, state string, checks ...string) *PRInfo {
	p := &PRInfo{Repo: repo, State: state, CheckDetails: []CheckDetail{}}
	for i := 0; i+1 < len(checks); i += 2 {
		p.CheckDetails = append(p.CheckDetails, CheckDetail{Name: checks[i], State: checks[i+1]})
	}
	return p
}

func TestCheckStats(t *testing.T) {
	f, ok := CheckFailure, CheckSuccess
	truncated := checksPR("acme/api", "open", "vendor-hsm-tests", f)
	truncated.ChecksTruncated = true
	s := statsSnapshot(
		checksPR("Acme/API", "open", "vendor-hsm-tests", f, "build", ok),
		checksPR("acme/api", "open", "vendor-hsm-tests", f, "build", ok),
		checksPR("acme/api", "open", "vendor-hsm-tests", f, "build", ok, "build", f), // a re-run that failed counts once, as a failure
		checksPR("acme/api", "open", "vendor-hsm-tests", ok, "build", CheckNeutral),
		checksPR("acme/api", "open", "vendor-hsm-tests", CheckPending, "build", CheckSkipped),
		truncated,
		checksPR("acme/api", "merged", "vendor-hsm-tests", f), // closed PRs do not count
		&PRInfo{Repo: "acme/api", State: "open"},              // no check list: nothing to count
		checksPR("acme/web", "open", "vendor-hsm-tests", f),
		checksPR("acme/web", "open", "vendor-hsm-tests", ok),
		checksPR("acme/web", "open", "vendor-hsm-tests", ok),
	)
	got := checkStats(s, DefaultFactThresholds())
	want := map[string][]CheckStat{
		"acme/api": {
			{Name: "build", Runs: 4, Failures: 1, Unlisted: 1, Evidence: "fails on 1 of 4 open PRs by the configured people in acme/api; 1 more open PR lists too many checks to show it"},
			{Name: "vendor-hsm-tests", Runs: 5, Failures: 4, Systemic: true, Evidence: "fails on 4 of 5 open PRs by the configured people in acme/api"},
		},
		"acme/web": {
			{Name: "vendor-hsm-tests", Runs: 3, Failures: 1, Evidence: "fails on 1 of 3 open PRs by the configured people in acme/web"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("check stats\n got %+v\nwant %+v", got, want)
	}

	// The thresholds' edges: 3 of 5 is exactly 0.6; fewer runs than
	// systemic_min_prs is never systemic.
	edge := statsSnapshot(
		checksPR("acme/api", "open", "x", f), checksPR("acme/api", "open", "x", f), checksPR("acme/api", "open", "x", f),
		checksPR("acme/api", "open", "x", ok), checksPR("acme/api", "open", "x", ok),
		checksPR("acme/web", "open", "x", f), checksPR("acme/web", "open", "x", f),
	)
	st := checkStats(edge, DefaultFactThresholds())
	if !st["acme/api"][0].Systemic || st["acme/web"][0].Systemic {
		t.Errorf("3 of 5 should be systemic and 2 of 2 not (min 3): %+v", st)
	}
	st = checkStats(edge, FactThresholds{SystemicRatio: 0.8, SystemicMinPRs: 2})
	if st["acme/api"][0].Systemic || !st["acme/web"][0].Systemic {
		t.Errorf("with ratio 0.8 and min 2: %+v", st)
	}
	// systemic_min_prs counts the PRs a check ran on, not its failures
	// (gh-99 requirement 12): with the defaults, 2 failures in 3 runs is
	// systemic.
	two := statsSnapshot(checksPR("acme/api", "open", "x", f), checksPR("acme/api", "open", "x", f), checksPR("acme/api", "open", "x", ok))
	if st := checkStats(two, DefaultFactThresholds())["acme/api"][0]; !st.Systemic || st.Failures != 2 || st.Runs != 3 {
		t.Errorf("2 failures in 3 runs: %+v", st)
	}
}

// TestSystemicEvidenceGivesCounts: the evidence states check_stats' counts,
// never "most", which systemic_ratio at or below 0.5 would make untrue.
func TestSystemicEvidenceGivesCounts(t *testing.T) {
	stats := map[string]CheckStat{
		"vendor-hsm-tests": {Name: "vendor-hsm-tests", Runs: 3, Failures: 1, Systemic: true, Evidence: "fails on 1 of 3 open PRs by the configured people in acme/api"},
		"e2e":              {Name: "e2e", Runs: 6, Failures: 4, Systemic: true},
	}
	p := readyPR()
	failCheck(p, "vendor-hsm-tests")
	failCheck(p, "build")
	f := prFacts(p, false, stats, factPeople, DefaultFactThresholds(), factsNow)
	if ev := f.Reasons[0].Evidence; ev != "build fails; vendor-hsm-tests fails too, as on 1 of 3 open PRs in acme/api" {
		t.Errorf("ci_failing evidence %q", ev)
	}
	p = readyPR()
	failCheck(p, "vendor-hsm-tests")
	failCheck(p, "e2e")
	f = prFacts(p, false, stats, factPeople, DefaultFactThresholds(), factsNow)
	i := slices.IndexFunc(f.Reasons, func(r Reason) bool { return r.Code == ReasonCISystemicOnly })
	if i < 0 || f.Reasons[i].Evidence != "only vendor-hsm-tests and e2e fail; they fail on 1 of 3 and 4 of 6 open PRs by the configured people in acme/api" {
		t.Errorf("ci_systemic_only evidence %+v", f.Reasons)
	}
	for _, r := range f.Reasons {
		if strings.Contains(r.Evidence, "most") {
			t.Errorf("%s evidence says most: %q", r.Code, r.Evidence)
		}
	}
}

// ticketSnapshot builds a snapshot around one Jira ticket.
type ticketCase struct {
	s  *Snapshot
	id string
}

func newTicketCase(t *TicketInfo) *ticketCase {
	if t.System == "" {
		t.System = "jira"
	}
	if t.Freshness == "" {
		t.Freshness = Fresh
	}
	id := jiraTicketID(t.Key)
	return &ticketCase{s: &Snapshot{GeneratedAt: factsNow, Nodes: []Node{
		{ID: id, Kind: KindTicket, Label: t.Key, Ticket: t},
		{ID: "sprint:12", Kind: KindSprint, Label: "Sprint 12", Sprint: &SprintInfo{ID: 12, Name: "Sprint 12", State: SprintActive}},
	}}, id: id}
}

func (c *ticketCase) pr(number int, state string, edit func(p *PRInfo)) *ticketCase {
	p := &PRInfo{Number: number, Repo: "acme/api", State: state, UpdatedAt: day(29, 9)}
	if edit != nil {
		edit(p)
	}
	id := prNodeID(p.Repo, number)
	c.s.Nodes = append(c.s.Nodes, Node{ID: id, Kind: KindPR, Label: "acme/api#" + strconv.Itoa(number), PR: p})
	c.s.Edges = append(c.s.Edges, Edge{Source: id, Target: c.id, Kind: EdgeTicket})
	return c
}

// workspace adds a workspace whose bookmark (without a PR) names the ticket.
func (c *ticketCase) workspace(last time.Time) *ticketCase {
	c.s.Nodes = append(c.s.Nodes,
		Node{ID: "ws-1", Kind: KindWorkspace, Label: "api/TEAM-1", Workspace: &WorkspaceInfo{LastActivity: last}},
		Node{ID: "bm-1", Kind: KindBookmark, Label: "TEAM-1", Bookmark: &BookmarkInfo{Name: "TEAM-1"}})
	c.s.Edges = append(c.s.Edges, Edge{Source: "ws-1", Target: "bm-1", Kind: EdgeOn}, Edge{Source: "bm-1", Target: c.id, Kind: EdgeTicket})
	return c
}

func (c *ticketCase) inSprint() *ticketCase {
	c.s.Edges = append(c.s.Edges, Edge{Source: c.id, Target: "sprint:12", Kind: EdgeInSprint})
	return c
}

func (c *ticketCase) facts() *TicketFacts {
	return ComputeFacts(c.s, factPeople, DefaultFactThresholds(), factsNow).Tickets[c.id]
}

var activeSprint = []SprintRef{{ID: 12, Name: "Sprint 12", State: SprintActive}}

func TestTicketMismatches(t *testing.T) {
	inProgress := func() *TicketInfo {
		return &TicketInfo{Key: "TEAM-1", Status: "In Progress", StatusCategory: StatusInProgress, Assignee: "Alice", Sprints: activeSprint}
	}
	done := func() *TicketInfo {
		return &TicketInfo{Key: "TEAM-1", Status: "Done", StatusCategory: StatusDone, Assignee: "Alice", Sprints: activeSprint}
	}
	todo := func() *TicketInfo {
		return &TicketInfo{Key: "TEAM-1", Status: "To Do", StatusCategory: StatusToDo, Assignee: "Alice", Sprints: []SprintRef{}}
	}
	merged := func(p *PRInfo) { p.MergedAt = day(29, 15) }
	for _, tc := range []struct {
		name      string
		c         *ticketCase
		want      []string // nil: unknown
		uncertain []string
		evidence  string
	}{
		{"code merged, ticket open", newTicketCase(inProgress()).pr(41, "merged", merged), []string{MismatchCodeMergedTicketOpen}, nil,
			"PR acme/api#41 merged Oct 29; ticket In Progress"},
		{"code merged and closed, ticket open", newTicketCase(todo()).pr(41, "merged", merged).pr(42, "closed", nil), []string{MismatchCodeMergedTicketOpen}, nil,
			"PRs acme/api#41 merged Oct 29 and acme/api#42 closed; ticket To Do"},
		{"code merged, ticket done", newTicketCase(done()).pr(41, "merged", merged), []string{}, nil, ""},
		{"code partly merged", newTicketCase(inProgress()).pr(41, "merged", merged).pr(42, "open", nil), []string{}, nil, ""},
		{"code closed unmerged", newTicketCase(todo()).pr(41, "closed", nil), []string{}, nil, ""},
		{"code merged, status unknown", newTicketCase(&TicketInfo{Key: "TEAM-1", Status: "Resolved", Assignee: "Alice", Sprints: []SprintRef{}}).pr(41, "merged", merged),
			nil, []string{"status_category"}, ""},
		{"ticket done, code open", newTicketCase(done()).pr(41, "open", func(p *PRInfo) { p.IsDraft = true }), []string{MismatchTicketDoneCodeOpen}, nil,
			"ticket Done; PR acme/api#41 draft"},
		{"ticket done, code merged", newTicketCase(done()).pr(41, "merged", merged), []string{}, nil, ""},
		{"in progress with no code in a sprint", newTicketCase(inProgress()), []string{MismatchInProgressNoCode}, nil,
			"ticket In Progress in Sprint 12; no linked PR or workspace in the snapshot"},
		{"in progress with no code, sprint from the edge", newTicketCase(&TicketInfo{Key: "TEAM-1", Status: "In Progress", StatusCategory: StatusInProgress, Assignee: "Alice"}).inSprint(),
			[]string{MismatchInProgressNoCode}, nil, ""},
		{"in progress with a workspace", newTicketCase(inProgress()).workspace(day(29, 9)), []string{}, nil, ""},
		{"in progress with no code outside a sprint", newTicketCase(&TicketInfo{Key: "TEAM-1", StatusCategory: StatusInProgress, Assignee: "Alice", Sprints: []SprintRef{}}), []string{}, nil, ""},
		{"in progress with no code, sprints unknown", newTicketCase(&TicketInfo{Key: "TEAM-1", StatusCategory: StatusInProgress, Assignee: "Alice"}), nil, []string{"sprints"}, ""},
		{"in progress and idle", newTicketCase(inProgress()).pr(41, "open", func(p *PRInfo) { p.UpdatedAt = factsNow.AddDate(0, 0, -40) }), []string{MismatchInProgressIdle}, nil,
			"ticket In Progress; last activity on acme/api#41 Sep 20 (idle after 30 days)"},
		{"in progress with recent workspace activity", newTicketCase(inProgress()).pr(41, "open", func(p *PRInfo) { p.UpdatedAt = factsNow.AddDate(0, 0, -40) }).workspace(day(29, 9)), []string{}, nil, ""},
		{"in progress, activity unknown", newTicketCase(inProgress()).workspace(time.Time{}), nil, []string{"activity"}, ""},
		{"unassigned in a sprint", newTicketCase(&TicketInfo{Key: "TEAM-1", Status: "To Do", StatusCategory: StatusToDo, Sprints: activeSprint}), []string{MismatchUnassignedInSprint}, nil,
			"in Sprint 12 with no assignee"},
		{"assigned in a sprint", newTicketCase(&TicketInfo{Key: "TEAM-1", StatusCategory: StatusToDo, Assignee: "Bob", Sprints: activeSprint}), []string{}, nil, ""},
		{"assignee unknown", newTicketCase(&TicketInfo{Key: "TEAM-1", StatusCategory: StatusToDo, Sprints: activeSprint, Freshness: Unknown}), nil, []string{"assignee"}, ""},
		{"several at once", newTicketCase(&TicketInfo{Key: "TEAM-1", Status: "In Progress", StatusCategory: StatusInProgress, Sprints: activeSprint}),
			[]string{MismatchInProgressNoCode, MismatchUnassignedInSprint}, nil, ""},
		{"a known mismatch beside an unknown one", newTicketCase(&TicketInfo{Key: "TEAM-1", StatusCategory: StatusToDo, Sprints: activeSprint, Freshness: Unknown}).pr(41, "merged", merged),
			[]string{MismatchCodeMergedTicketOpen}, []string{"assignee"}, ""},
		{"nothing to say", newTicketCase(todo()), []string{}, nil, ""},
		{"a linked PR of unknown state leaves code merged unknown", newTicketCase(inProgress()).pr(41, "merged", merged).pr(42, "", nil),
			nil, []string{"state:acme/api#42"}, ""},
		{"only a linked PR of unknown state", newTicketCase(done()).pr(42, "", nil), nil, []string{"state:acme/api#42"}, ""},
		{"an open PR decides beside one of unknown state", newTicketCase(done()).pr(41, "open", nil).pr(42, "", nil), []string{MismatchTicketDoneCodeOpen}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.c.facts()
			var got []string
			if f.Mismatches != nil {
				got = codes(f.Mismatches)
			}
			if !reflect.DeepEqual(got, tc.want) || !slices.Equal(f.Uncertain, tc.uncertain) {
				t.Fatalf("mismatches %#v, uncertain %v; want %#v, %v", got, f.Uncertain, tc.want, tc.uncertain)
			}
			for _, m := range f.Mismatches {
				if strings.TrimSpace(m.Evidence) == "" {
					t.Errorf("%s has no evidence", m.Code)
				}
			}
			if tc.evidence != "" && f.Mismatches[0].Evidence != tc.evidence {
				t.Errorf("evidence %q, want %q", f.Mismatches[0].Evidence, tc.evidence)
			}
		})
	}
}

func TestTicketLinkedPRs(t *testing.T) {
	c := newTicketCase(&TicketInfo{Key: "TEAM-1", StatusCategory: StatusInProgress, Assignee: "Alice", Sprints: []SprintRef{}}).
		pr(10, "open", nil).
		pr(9, "open", func(p *PRInfo) { p.IsDraft = true }).
		pr(11, "open", func(p *PRInfo) { p.ReviewDecision = ReviewApproved }).
		pr(12, "merged", nil).
		pr(13, "closed", nil)
	got := c.facts().LinkedPRs
	want := []LinkedPR{
		{ID: "pr:acme/api#9", Ref: "acme/api#9", State: "draft"},
		{ID: "pr:acme/api#10", Ref: "acme/api#10", State: "open"},
		{ID: "pr:acme/api#11", Ref: "acme/api#11", State: "approved"},
		{ID: "pr:acme/api#12", Ref: "acme/api#12", State: "merged"},
		{ID: "pr:acme/api#13", Ref: "acme/api#13", State: "closed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("linked PRs\n got %+v\nwant %+v", got, want)
	}
	// A PR of unknown state is linked without a state, never "".
	unk := newTicketCase(&TicketInfo{Key: "TEAM-3", StatusCategory: StatusToDo, Assignee: "Bob", Sprints: []SprintRef{}}).pr(7, "", nil)
	b, _ := json.Marshal(unk.facts().LinkedPRs)
	if string(b) != `[{"id":"pr:acme/api#7","ref":"acme/api#7"}]` {
		t.Errorf("unknown state: %s", b)
	}
	unk.s.Schema = SchemaVersion
	unk.s.Nodes[0].Ticket.Facts = unk.facts()
	if err := unk.s.Validate(); err != nil && strings.Contains(err.Error(), "linked PR state") {
		t.Errorf("validation: %v", err)
	}
	// A GitHub issue gets its linked PRs, and no Jira comparison.
	gh := newTicketCase(&TicketInfo{Key: "gh-57", System: "github", Status: "open"}).pr(10, "merged", nil)
	f := gh.facts()
	if f.Mismatches != nil || f.Uncertain != nil || len(f.LinkedPRs) != 1 {
		t.Errorf("github issue facts %+v", f)
	}
	// No linked PR is a known empty list.
	b, _ = json.Marshal(newTicketCase(&TicketInfo{Key: "TEAM-2", StatusCategory: StatusToDo, Assignee: "Bob", Sprints: []SprintRef{}}).facts())
	if string(b) != `{"mismatches":[],"linked_prs":[]}` {
		t.Errorf("no linked PRs: %s", b)
	}
}

// TestFactsOnTheFixture pins the facts the later specs' acceptance criteria
// rely on (gh-99 to gh-105), on the stored analysis fixture.
func TestFactsOnTheFixture(t *testing.T) {
	s := loadAnalysisFixture(t)
	pr := func(id string) *PRFacts {
		n := s.Node(id)
		if n == nil || n.PR == nil || n.PR.Facts == nil {
			t.Fatalf("%s has no facts", id)
		}
		return n.PR.Facts
	}
	stat := func(repo, name string) CheckStat {
		for _, st := range s.CheckStats[repo] {
			if st.Name == name {
				return st
			}
		}
		t.Fatalf("no %s in check_stats[%s]", name, repo)
		return CheckStat{}
	}
	// A check failing on 5 of 6 PRs in one repository is systemic there,
	// and not in another where it fails once.
	if st := stat("acme/api", "vendor-hsm-tests"); !st.Systemic || st.Runs != 6 || st.Failures != 5 ||
		st.Evidence != "fails on 5 of 6 open PRs by the configured people in acme/api" {
		t.Errorf("acme/api vendor-hsm-tests: %+v", st)
	}
	if st := stat("acme/web", "vendor-hsm-tests"); st.Systemic || st.Failures != 1 {
		t.Errorf("acme/web vendor-hsm-tests: %+v", st)
	}
	// A PR whose only failure is that check: ci systemic_only, its move not
	// decided by CI.
	if f := pr("pr:acme/api#101"); f.CI != CISystemicOnly || f.Move != MoveReviewer || slices.Contains(codes(f.Reasons), ReasonCIFailing) ||
		!slices.Contains(codes(f.Reasons), ReasonCISystemicOnly) {
		t.Errorf("acme/api#101: %+v", f)
	}
	// The same check failing once in acme/web is that PR's own failure.
	if f := pr("pr:acme/web#203"); f.CI != CIFailing || f.Move != MoveAuthor {
		t.Errorf("acme/web#203: %+v", f)
	}
	// The draft with conflicts and a failing check.
	if f := pr("pr:acme/api#103"); !slices.Equal(codes(f.Reasons)[:3], []string{ReasonDraft, ReasonConflicts, ReasonCIFailing}) {
		t.Errorf("acme/api#103 reasons %v", codes(f.Reasons))
	}
	// A team-only request for alice is her move.
	if f := pr("pr:acme/api#104"); !slices.Contains(f.MovePeople, "alice") || !slices.Contains(codes(f.Reasons), ReasonTeamOnly) {
		t.Errorf("acme/api#104: %+v", f)
	}
	// A truncated check list counts what it shows and is no evidence that a
	// check did not run.
	if st := stat("acme/web", "docs-preview"); st.Unlisted != 1 {
		t.Errorf("docs-preview: %+v", st)
	}
	// Each mismatch code holds on one fixture ticket and fails on another.
	holds := map[string][]string{}
	for _, n := range s.Nodes {
		if n.Ticket != nil && n.Ticket.Facts != nil {
			for _, m := range n.Ticket.Facts.Mismatches {
				holds[m.Code] = append(holds[m.Code], n.ID)
			}
		}
	}
	want := map[string][]string{
		MismatchCodeMergedTicketOpen: {"ticket:jira:TEAM-103"},
		MismatchTicketDoneCodeOpen:   {"ticket:jira:TEAM-102"},
		MismatchInProgressNoCode:     {"ticket:jira:TEAM-105"},
		MismatchInProgressIdle:       {"ticket:jira:TEAM-107"},
		MismatchUnassignedInSprint:   {"ticket:jira:TEAM-106"},
	}
	if !reflect.DeepEqual(holds, want) {
		t.Errorf("mismatches on the fixture: %v, want %v", holds, want)
	}
	// TEAM-101 is in progress in the active sprint, assigned, with open and
	// merged PRs and recent work: no code holds for it.
	if f := s.Node("ticket:jira:TEAM-101").Ticket.Facts; f.Mismatches == nil || len(f.Mismatches) != 0 {
		t.Errorf("TEAM-101: %+v", f)
	}
	// TEAM-108's cache entry predates status_category.
	if f := s.Node("ticket:jira:TEAM-108").Ticket.Facts; f.Mismatches != nil || !slices.Equal(f.Uncertain, []string{"status_category"}) {
		t.Errorf("TEAM-108: %+v", f)
	}
}

// TestEveryReasonAndMismatchHasEvidence: across the fixture, every reason
// and mismatch carries evidence, and the fixture plus the rule tests above
// produce every code.
func TestEveryReasonAndMismatchHasEvidence(t *testing.T) {
	s := loadAnalysisFixture(t)
	seen := map[string]bool{}
	check := func(id string, rs []Reason) {
		for _, r := range rs {
			seen[r.Code] = true
			if strings.TrimSpace(r.Evidence) == "" {
				t.Errorf("%s: %s has no evidence", id, r.Code)
			}
		}
	}
	for _, n := range s.Nodes {
		if n.PR != nil {
			check(n.ID, n.PR.Facts.Reasons)
		}
		if n.Ticket != nil {
			check(n.ID, n.Ticket.Facts.Mismatches)
		}
	}
	for _, list := range s.CheckStats {
		for _, st := range list {
			if st.Evidence == "" {
				t.Errorf("check %s has no evidence", st.Name)
			}
		}
	}
	// The fixture lacks a standing change request; the rule tests cover it.
	p := readyPR()
	p.Reviews = []Review{{Author: "bob", State: ReviewChangesRequested, SubmittedAt: day(24, 16)}}
	p.ReviewDecision = ReviewChangesRequested
	check("changes requested", prFacts(p, false, nil, factPeople, DefaultFactThresholds(), factsNow).Reasons)
	for _, c := range append(slices.Clone(reasonCodes), mismatchCodes...) {
		if !seen[c] {
			t.Errorf("no case produces %s", c)
		}
	}
}

func TestFactsAreDeterministic(t *testing.T) {
	base := loadAnalysisFixture(t)
	for _, s := range []*Snapshot{base, scaleAnalysisFixture(t, base, 2000)} {
		start := time.Now()
		facts := ComputeFacts(s, s.People, DefaultFactThresholds(), s.GeneratedAt)
		t.Logf("facts for %d nodes in %v", len(s.Nodes), time.Since(start))
		first, _ := json.Marshal(facts)
		for range 3 {
			again, _ := json.Marshal(ComputeFacts(s, s.People, DefaultFactThresholds(), s.GeneratedAt))
			if string(again) != string(first) {
				t.Fatal("facts differ between two runs on the same snapshot")
			}
		}
	}
}

// TestComputeFactsIgnoresStoredFacts: facts already on the snapshot are
// never inputs.
func TestComputeFactsIgnoresStoredFacts(t *testing.T) {
	s := loadAnalysisFixture(t)
	want, _ := json.Marshal(ComputeFacts(s, s.People, DefaultFactThresholds(), s.GeneratedAt))
	for i := range s.Nodes {
		if p := s.Nodes[i].PR; p != nil {
			p.Facts = &PRFacts{Move: MoveNobody, CI: CIFailing}
		}
	}
	s.CheckStats = map[string][]CheckStat{"acme/api": {{Name: "build", Runs: 9, Failures: 9, Systemic: true}}}
	if got, _ := json.Marshal(ComputeFacts(s, s.People, DefaultFactThresholds(), s.GeneratedAt)); string(got) != string(want) {
		t.Error("stored facts changed the computed ones")
	}
}

// TestAssembleDerivesFacts: collection ends by deriving the facts, with the
// configured thresholds and generated_at as now; a team-only request found
// in alice's review-requested set is her move.
func TestAssembleDerivesFacts(t *testing.T) {
	a := inventoryAsm(t)
	a.c.cfg.Facts = FactThresholds{StaleDays: 1}
	pr := readyInventoryPR(3)
	pr.ReviewRequests = []github.ReviewRequestFact{{Reviewer: "team:web", Kind: ReviewerTeam, RequestedAt: pr.ReadyAt.Add(time.Hour)}}
	seedInventory(t, a.since(), map[string][]github.PRFacts{"alice/review-requested": {pr}})
	s, _ := a.build()
	f := s.Node(prNodeID(asmSlug, 3)).PR.Facts
	if f == nil || f.Move != MoveReviewer || !slices.Equal(f.MovePeople, []string{"team:web", "alice"}) || f.Stale == nil || !*f.Stale {
		t.Fatalf("facts %+v", f)
	}
	if !f.WaitingSince.Equal(pr.ReadyAt.Add(time.Hour)) {
		t.Errorf("waiting since %v", f.WaitingSince)
	}
	if st := s.CheckStats[strings.ToLower(asmSlug)]; len(st) != 1 || st[0].Name != "build" || st[0].Runs != 1 {
		t.Errorf("check stats %+v", s.CheckStats)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func readyInventoryPR(n int) github.PRFacts { return openPR(n, "carol", "carol-change") }
