package dash

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestResyncWorklist(t *testing.T) {
	v := triageFixture(t, nil)
	tr, err := BuildTriage(v, defaultRules(t), "")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(RenderTriage(tr, true, time.UTC), "\n")
	// The person adds a note, moves the parked section to the top, and
	// ticks #8 by hand while it is still open.
	for i, l := range lines {
		if strings.HasPrefix(l, "- [ ] [api#1]") {
			lines = slices.Insert(lines, i+1, "  - note: waiting on bob")
			break
		}
	}
	for i, l := range lines {
		if strings.HasPrefix(l, "- [ ] [api#8]") {
			lines[i] = strings.Replace(l, "- [ ]", "- [x]", 1)
		}
	}
	// They also add a teammate's PR to watch: it is open but not theirs.
	for i, l := range lines {
		if strings.HasPrefix(l, "- [ ] [api#1]") {
			lines = slices.Insert(lines, i+2, "- [ ] [api#30](https://github.com/acme/api/pull/30) bob's fix")
			break
		}
	}
	p := slices.Index(lines, "## parked (2)")
	parked := slices.Clone(lines[p:])
	lines = append(slices.Insert(slices.Clone(lines[:p]), 2, parked...), "")

	// Since the worklist was written: #1 merged, #5 closed, #6 left
	// GitHub's closed search, #7 became ready and stale, and #20 opened.
	v2 := triageFixture(t, func(ts *triageSnap) {
		for i := range ts.s.Nodes {
			pr := ts.s.Nodes[i].PR
			if pr == nil {
				continue
			}
			switch pr.Number {
			case 1:
				pr.State, pr.MergedAt = "merged", day(30, 9)
			case 5:
				pr.State, pr.ClosedAt = "closed", day(29, 9)
			}
		}
		ts.s.Nodes = slices.DeleteFunc(ts.s.Nodes, func(n Node) bool { return n.PR != nil && n.PR.Number == 6 })
		for i := range ts.s.Nodes {
			if pr := ts.s.Nodes[i].PR; pr != nil && pr.Number == 7 {
				pr.IsDraft, pr.UpdatedAt = false, day(1, 9).AddDate(0, 0, -10)
			}
		}
		n := readyPR()
		n.Number, n.URL, n.HeadRef, n.TicketKeys = 20, "https://github.com/acme/api/pull/20", "b20", []string{}
		ts.pr(n)
		bob := readyPR()
		bob.Number, bob.URL, bob.HeadRef, bob.Author, bob.TicketKeys = 30, "https://github.com/acme/api/pull/30", "b30", "bob", []string{}
		ts.pr(bob)
	})
	tr2, err := BuildTriage(v2, defaultRules(t), "")
	if err != nil {
		t.Fatal(err)
	}
	out, r := ResyncWorklist(lines, v2, tr2, time.UTC)
	text := strings.Join(out, "\n")
	var done []string
	for _, d := range r.Done {
		done = append(done, d.URL[strings.LastIndexByte(d.URL, '/')+1:]+":"+d.State)
	}
	if !slices.Equal(done, []string{"1:merged", "5:closed", "6:gone"}) {
		t.Errorf("done %v", done)
	}
	for _, s := range []string{
		"- [x] [api#1](https://github.com/acme/api/pull/1) ready", "· merged 2026-10-30\n  - note: waiting on bob",
		"· closed 2026-10-29", "· no longer open",
		"- [x] [api#8](https://github.com/acme/api/pull/8) draft · updated 2026-10-29 · no ticket · PR 8\n",
		"## New since 2026-10-30 16:00\n\n### active (1)\n\n- [ ] [api#20]",
		"\n- [ ] [api#30](https://github.com/acme/api/pull/30) bob's fix\n",
	} {
		if !strings.Contains(text, s) {
			t.Errorf("resynced worklist lacks %q:\n%s", s, text)
		}
	}
	if !strings.HasPrefix(text, "# Open PR triage, 2026-10-30 16:00\n\n## parked (2)") {
		t.Errorf("the hand-moved section moved back:\n%s", text)
	}
	for _, n := range r.New {
		if strings.HasSuffix(n.Item.URL, "/8") {
			t.Errorf("the hand-ticked open PR #8 was added again: %+v", n)
		}
	}
	if len(r.New) != 1 || r.New[0].Bucket != "active" {
		t.Errorf("new %+v", r.New)
	}
	if len(r.Moved) != 1 || r.Moved[0].Bucket != "draft?" || r.Moved[0].Section != "fix ticket (1)" {
		t.Errorf("moved %+v", r.Moved)
	}

	// Again with nothing changed: nothing new, nothing done.
	out2, r2 := ResyncWorklist(out, v2, tr2, time.UTC)
	if len(r2.Done) != 0 || len(r2.New) != 0 || !slices.Equal(out2, out) {
		t.Errorf("second resync: done %v new %v, changed %v", r2.Done, r2.New, !slices.Equal(out2, out))
	}
}

func TestResyncCountsAndTracksNewPRBuckets(t *testing.T) {
	url := func(n int) string { return "https://github.com/acme/api/pull/" + strconv.Itoa(n) }
	item := func(n int) TriageItem { return TriageItem{Ref: "acme/api#" + strconv.Itoa(n), URL: url(n)} }
	triage := func(bucket2 string) *Triage {
		buckets := []TriageBucket{{Name: "active", Items: []TriageItem{item(1), item(3)}}, {Name: "land", Items: []TriageItem{}}}
		if bucket2 == "active" {
			buckets[0].Items = append(buckets[0].Items, item(2))
		} else {
			buckets[1].Items = append(buckets[1].Items, item(2))
		}
		return &Triage{GeneratedAt: factsNow, InventoryComplete: true, Buckets: buckets, Total: 3}
	}
	lines := []string{"# PRs", "## active (1)", "- [ ] [api#3](" + url(3) + ")"}
	v := &View{Snapshot: &Snapshot{}}
	out, first := ResyncWorklist(lines, v, triage("land"), time.UTC)
	if !slices.Equal(first.Left, []SectionCount{{Section: "active", Left: 2}, {Section: "land", Left: 1}}) || len(first.New) != 2 {
		t.Fatalf("first resync: left %+v, new %+v", first.Left, first.New)
	}
	_, second := ResyncWorklist(out, v, triage("active"), time.UTC)
	if !slices.Equal(second.Left, []SectionCount{{Section: "active", Left: 2}, {Section: "land", Left: 1}}) || len(second.Moved) != 1 || second.Moved[0].URL != url(2) || second.Moved[0].Bucket != "active" {
		t.Errorf("second resync: left %+v, moved %+v", second.Left, second.Moved)
	}
}

func TestResyncDoesNotTickWhenTheListIsIncomplete(t *testing.T) {
	v := triageFixture(t, nil)
	tr, _ := BuildTriage(v, defaultRules(t), "")
	lines := strings.Split(RenderTriage(tr, true, time.UTC), "\n")
	v2 := triageFixture(t, func(ts *triageSnap) {
		ts.s.Nodes = slices.DeleteFunc(ts.s.Nodes, func(n Node) bool { return n.PR != nil && n.PR.Number == 6 })
	})
	v2.Snapshot.Sources[SourceGitHubInventory] = SourceStatus{State: Error, Parts: []SourcePart{{Name: "alice/open-authored", State: Error, Error: "HTTP 502"}}}
	v2, _ = newView(v2.Snapshot, nil, false, nil)
	tr2, _ := BuildTriage(v2, defaultRules(t), "")
	out, r := ResyncWorklist(lines, v2, tr2, time.UTC)
	if len(r.Done) != 0 || !r.TickSkipped || !slices.Equal(out, lines) || len(r.Incomplete) == 0 {
		t.Errorf("incomplete: done %v skipped %v incomplete %v", r.Done, r.TickSkipped, r.Incomplete)
	}
}

// TestDroppedPRsLeaveTheBuckets: a PR the latest open-authored fetch no
// longer lists is in no bucket, and resync ticks it as no longer open.
func TestDroppedPRsLeaveTheBuckets(t *testing.T) {
	v := triageFixture(t, nil)
	tr, _ := BuildTriage(v, defaultRules(t), "")
	lines := strings.Split(RenderTriage(tr, true, time.UTC), "\n")
	v2 := triageFixture(t, func(ts *triageSnap) {
		for i := range ts.s.Nodes {
			if pr := ts.s.Nodes[i].PR; pr != nil {
				pr.Sources, pr.FetchedAt = []string{PRSourceInventory}, factsNow
				if pr.Number == 9 {
					pr.FetchedAt = factsNow.Add(-4 * time.Hour)
				}
			}
		}
	})
	tr2, err := BuildTriage(v2, defaultRules(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if tr2.Total != tr.Total-1 || !slices.Equal(tr2.Dropped, []string{"https://github.com/acme/api/pull/9"}) {
		t.Errorf("total %d, dropped %v", tr2.Total, tr2.Dropped)
	}
	out, r := ResyncWorklist(lines, v2, tr2, time.UTC)
	if len(r.Done) != 1 || r.Done[0].State != "gone" || !strings.Contains(strings.Join(out, "\n"), "PR 9 · no longer open") {
		t.Errorf("done %+v", r.Done)
	}
}

func TestDroppedBookmarkedPRLeavesTheBuckets(t *testing.T) {
	v := triageFixture(t, nil)
	initial, err := BuildTriage(v, defaultRules(t), "")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(RenderTriage(initial, true, time.UTC), "\n")
	v2 := triageFixture(t, func(ts *triageSnap) {
		for i := range ts.s.Nodes {
			p := ts.s.Nodes[i].PR
			if p == nil {
				continue
			}
			listed := p.Number != 9
			p.OpenAuthoredListed = &listed
			p.Sources = []string{PRSourceInventory}
			p.FetchedAt = factsNow
			if p.Number == 8 || p.Number == 9 {
				p.Sources = []string{PRSourceBookmark, PRSourceInventory}
				p.FetchedAt = factsNow.Add(-4 * time.Hour)
			}
		}
	})
	current, err := BuildTriage(v2, defaultRules(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if current.Total != initial.Total-1 || !slices.Equal(current.Dropped, []string{"https://github.com/acme/api/pull/9"}) {
		t.Fatalf("total %d; dropped %v", current.Total, current.Dropped)
	}
	_, resync := ResyncWorklist(lines, v2, current, time.UTC)
	if len(resync.Done) != 1 || resync.Done[0].URL != "https://github.com/acme/api/pull/9" {
		t.Errorf("done %+v", resync.Done)
	}
}
