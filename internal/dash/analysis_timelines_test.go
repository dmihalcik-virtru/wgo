package dash

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/github"
)

// analysisTimelinesPath holds the review histories of the analysis fixture's
// PRs (gh-104), as GitHub's timeline reports them. The fixture is a stored
// snapshot, so it carries the timelines built from them; this file keeps the
// two in step and rewrites the fixture's with
//
//	go test ./internal/dash -run 'TestAnalysisFixture(Timelines|Facts)' -update
//
// Timelines first: the facts read them. The test files run in name order, so
// this one runs before facts_fixture_test.go's.
const analysisTimelinesPath = "testdata/analysis_timelines.json"

type analysisTimelines struct {
	FetchedAt time.Time                     `json:"fetched_at"`
	About     string                        `json:"about"`
	Timelines map[string]*github.PRTimeline `json:"timelines"`
}

// fixtureTimelines builds each fixture PR's timeline from its history, as
// assemble would at the fixture's fetch time: by node ID, nil for a PR
// with no history.
func fixtureTimelines(t *testing.T, s *Snapshot) map[string]*PRTimeline {
	t.Helper()
	data, err := os.ReadFile(analysisTimelinesPath)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var in analysisTimelines
	if err := dec.Decode(&in); err != nil {
		t.Fatalf("%s: %v", analysisTimelinesPath, err)
	}
	out := map[string]*PRTimeline{}
	for id, h := range in.Timelines {
		n := s.Node(id)
		if n == nil || n.PR == nil {
			t.Fatalf("%s: %q is not a PR of the fixture", analysisTimelinesPath, id)
		}
		if h.Repo != n.PR.Repo || h.Number != n.PR.Number {
			t.Fatalf("%s: %q holds the history of %s#%d", analysisTimelinesPath, id, h.Repo, h.Number)
		}
		tl := BuildTimeline(h)
		tl.FetchedAt, tl.Freshness = in.FetchedAt, Stale
		if !h.UpdatedAt.Before(n.PR.UpdatedAt) {
			tl.Freshness = Fresh
		}
		out[id] = tl
	}
	return out
}

func TestAnalysisFixtureTimelines(t *testing.T) {
	data, err := os.ReadFile(analysisFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	s := loadAnalysisFixture(t)
	want := fixtureTimelines(t, s)
	if *updateFields {
		root, err := readOrdered(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range root.get("nodes").vals {
			id, _ := n.get("id").str()
			info := n.get("pr")
			if info == nil {
				continue
			}
			b, err := json.Marshal(want[id])
			if err != nil {
				t.Fatal(err)
			}
			v, err := readOrdered(b)
			if err != nil {
				t.Fatal(err)
			}
			info.set("timeline", v)
		}
		var buf bytes.Buffer
		writeOrdered(&buf, root, "")
		buf.WriteByte('\n')
		if err := os.WriteFile(analysisFixturePath, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	with, without := 0, 0
	for _, n := range s.Nodes {
		if n.PR == nil {
			continue
		}
		a, _ := json.Marshal(n.PR.Timeline)
		b, _ := json.Marshal(want[n.ID])
		if !bytes.Equal(a, b) {
			t.Errorf("%s: the fixture holds\n  %s\nits history builds\n  %s\nrun go test ./internal/dash -run 'TestAnalysisFixture(Timelines|Facts)' -update", n.ID, a, b)
		}
		if n.PR.Timeline != nil {
			with++
		} else {
			without++
		}
	}
	// Both kinds of row: the view's fallback needs PRs without a history.
	if with < 8 || without < 4 {
		t.Errorf("%d fixture PRs with a timeline and %d without; want both", with, without)
	}
}

// TestAnalysisFixtureTimelineCases: the fixture's histories cover what the
// timeline view draws: each segment state, a removed request, a re-request,
// a draft round-trip, a reopen, a team request, overlapping waits, a long
// wait on the operator, and a waiting_since made exact by the timeline.
func TestAnalysisFixtureTimelineCases(t *testing.T) {
	s := loadAnalysisFixture(t)
	tl := func(id string) *PRTimeline {
		t.Helper()
		n := s.Node(id)
		if n == nil || n.PR == nil || n.PR.Timeline == nil {
			t.Fatalf("%s has no timeline", id)
		}
		return n.PR.Timeline
	}
	states := map[string]bool{}
	for _, n := range s.Nodes {
		if n.PR != nil && n.PR.Timeline != nil {
			for _, sg := range n.PR.Timeline.Segments {
				states[sg.State] = true
			}
		}
	}
	for _, st := range segmentStates {
		if !states[st] {
			t.Errorf("no fixture timeline has a %s segment", st)
		}
	}
	for id, want := range map[string]string{
		"pr:acme/api#101": "draft waiting unattended waiting",         // a removed request
		"pr:acme/api#102": "waiting changes_requested waiting",        // a re-request
		"pr:acme/api#104": "unattended waiting",                       // a team request, the operator's
		"pr:acme/api#108": "unattended draft waiting approved merged", // a draft round-trip
		"pr:acme/api#109": "unattended closed unattended closed",      // a reopen
	} {
		var got []string
		for _, sg := range tl(id).Segments {
			got = append(got, sg.State)
		}
		if g := strings.Join(got, " "); g != want {
			t.Errorf("%s segments %s, want %s", id, g, want)
		}
	}
	if r := tl("pr:acme/api#107").Segments[0].Reviewers; len(r) != 2 {
		t.Errorf("acme/api#107's first wait lists %v, want both reviewers", r)
	}
	// acme/web#202 changed since its history was fetched: the history is
	// stale, shown but not trusted, so its facts can only bound the wait.
	p := s.Node("pr:acme/web#202").PR
	if f := p.Facts; p.Timeline.Freshness != Stale || f.WaitingSource != WaitingEstimated || !f.WaitingLowerBound {
		t.Errorf("acme/web#202's wait %+v", f)
	}
	f := s.Node("pr:acme/api#101").PR.Facts
	if f.WaitingSource != WaitingFromTimeline || !f.WaitingSince.Equal(time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("acme/api#101's wait %+v", f)
	}
	if f := s.Node("pr:acme/api#103").PR.Facts; f.WaitingSource != "" || s.Node("pr:acme/web#201").PR.Facts.WaitingSource != WaitingFromTimeline {
		t.Errorf("waiting sources: a draft has none, a PR with a history its timeline")
	}
	if f := s.Node("pr:acme/infra#301").PR.Facts; f.WaitingSource != "" {
		t.Errorf("acme/infra#301 is the author's move by CI: %+v", f)
	}
	if f := s.Node("pr:acme/web#207").PR.Facts; f.WaitingSource != WaitingEstimated {
		t.Errorf("acme/web#207 has no history: %+v", f)
	}
}
