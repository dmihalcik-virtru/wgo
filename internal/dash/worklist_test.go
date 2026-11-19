package dash

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWorklistRoundTrip(t *testing.T) {
	v := triageFixture(t, func(ts *triageSnap) {
		child := readyPR()
		child.Number, child.URL, child.HeadRef, child.BaseRef, child.IsDraft = 13, "https://github.com/acme/api/pull/13", "b13", "b3", true
		child.Title, child.TicketKeys = "fix [WIP] *all* the_things", []string{}
		ts.pr(child)
	})
	tr, err := BuildTriage(v, defaultRules(t), "")
	if err != nil {
		t.Fatal(err)
	}
	md := RenderTriage(tr, true, time.UTC)
	items := ParseWorklist(strings.Split(md, "\n"))
	var got, want []string
	for _, it := range items {
		if it.Checked {
			t.Errorf("%s is ticked in a new worklist", it.URL)
		}
		got = append(got, it.URL)
	}
	perBucket := map[string]int{}
	for _, it := range items {
		perBucket[it.Section]++
	}
	for _, b := range tr.Buckets {
		for _, it := range b.Items {
			want = append(want, it.URL)
		}
		if n := perBucket[b.Name+" ("+strconv.Itoa(len(b.Items))+")"]; n != len(b.Items) {
			t.Errorf("section %s has %d items, want %d", b.Name, n, len(b.Items))
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) || len(got) != tr.Total {
		t.Errorf("parsed %v\nwant %v", got, want)
	}
	for _, s := range []string{
		"# Open PR triage, 2026-10-30 16:00",
		"11 open PRs by alice.",
		"- [ ] [api#3](https://github.com/acme/api/pull/3) draft · updated 2025-10-01 · [TEAM-3](https://jira.example/browse/TEAM-3) Done · PR 3 · stack: base of [#13](https://github.com/acme/api/pull/13)\n",
		"- [ ] [api#2](https://github.com/acme/api/pull/2) ready · updated 2026-10-29 · no ticket · bob approved · PR 2\n",
		"fix \\[WIP\\] \\*all\\* the_things",
		"TEAM-10 status unknown · PR 10 · unknown: ticket_sprint_state, ticket_category",
		"> **Incomplete:** no status for TEAM-10.",
	} {
		if !strings.Contains(md, s) {
			t.Errorf("worklist lacks %q:\n%s", s, md)
		}
	}
	if ex := RenderTriage(tr, false, time.UTC); strings.Contains(ex, "- [ ]") || strings.Contains(ex, "--resync") {
		t.Errorf("the extract has checkboxes:\n%s", ex)
	}
}

func TestParseWorklistIgnoresOtherLines(t *testing.T) {
	lines := []string{
		"# notes",
		"- [x] [anything](https://github.com/o/r/pull/7) · closed 2026-11-01",
		"  - note: superseded by #9",
		"- [ ] not a link",
		"- [ ] [issue](https://github.com/o/r/issues/8)",
		"## my own heading",
		"- [X] [r#9](https://github.com/o/r/pull/9)",
	}
	items := ParseWorklist(lines)
	if len(items) != 2 || !items[0].Checked || items[0].URL != "https://github.com/o/r/pull/7" || items[1].Section != "my own heading" || items[1].Line != 6 {
		t.Errorf("items %+v", items)
	}
}

func TestTriageItemLineNamesUnknownHumanReviews(t *testing.T) {
	it := TriageItem{Ref: "acme/api#7", URL: "https://github.com/acme/api/pull/7"}
	unknown := TriageItemLine(it, true, time.UTC)
	it.HumanReviews = []Review{}
	knownEmpty := TriageItemLine(it, true, time.UTC)
	if !strings.Contains(unknown, "human reviews unknown") || strings.Contains(knownEmpty, "human reviews unknown") {
		t.Errorf("unknown reviews %q; known empty reviews %q", unknown, knownEmpty)
	}
}

func TestParseWorklistUsesNewSinceBucketHeadings(t *testing.T) {
	items := ParseWorklist([]string{
		"## active (2)",
		"- [ ] [api#1](https://github.com/acme/api/pull/1)",
		"## New since 2026-10-30 16:00",
		"### land (1)",
		"- [ ] [api#2](https://github.com/acme/api/pull/2)",
		"### active (1)",
		"- [ ] [api#3](https://github.com/acme/api/pull/3)",
	})
	if len(items) != 3 || items[0].Section != "active (2)" || items[1].Section != "land (1)" || items[2].Section != "active (1)" {
		t.Errorf("sections: %+v", items)
	}
}

// TestTriageExtractAgrees: the triage extract, GET /api/extract?view=triage
// and BuildTriage's JSON give the same buckets and counts for the same
// snapshot, rules and query.
func TestTriageExtractAgrees(t *testing.T) {
	v := triageFixture(t, nil)
	rules, err := CompileTriageRules([]TriageRule{{Bucket: "drafts", Match: "prstate:draft"}, {Bucket: "rest"}}, Fields())
	if err != nil {
		t.Fatal(err)
	}
	srv := newServerOpts(t, HandlerOptions{Source: StaticSource{V: v}, Triage: rules})
	for _, q := range []string{"", "ticket:*", "repo:acme/web"} {
		tr, err := BuildTriage(v, rules, q)
		if err != nil {
			t.Fatal(err)
		}
		md, err := Extract(v, Fields(), nil, ExtractArgs{View: "triage", Query: q, Triage: rules})
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range tr.Buckets {
			if h := "\n## " + b.Name + " (" + strconv.Itoa(len(b.Items)) + ")\n"; !strings.Contains(md, h) {
				t.Errorf("q=%q: the extract lacks %q:\n%s", q, h, md)
			}
		}
		if n := strings.Count(md, "\n- ["); n != tr.Total {
			t.Errorf("q=%q: the extract has %d items, the JSON %d", q, n, tr.Total)
		}
		if md != RenderTriage(tr, false, time.UTC) {
			t.Errorf("q=%q: the extract is not the worklist renderer's output without checkboxes", q)
		}
		_, body := get(t, srv.URL+ExtractPath+"?"+url.Values{"view": {"triage"}, "q": {q}}.Encode())
		if body != md {
			t.Errorf("q=%q: the route serves\n%s\nExtract renders\n%s", q, body, md)
		}
	}
	for _, args := range []ExtractArgs{{View: "triage", Cols: []string{"repo"}}, {View: "triage", Sprint: "1"}} {
		if err := CheckExtractArgs(Fields(), args); err == nil {
			t.Errorf("%+v: no error", args)
		}
	}
}
