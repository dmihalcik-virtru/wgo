package dash

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// This file resyncs a worklist with a fresh triage (gh-133 requirements 11
// and 12): it ticks the PRs that closed or merged, appends the open PRs the
// file lacks, and reports what moved, so an agent picking a triage pass
// back up needs only this result, not the whole file.

// Resync is the outcome of resyncing a worklist.
type Resync struct {
	// Done lists the items ticked by this resync.
	Done []ResyncDone `json:"done"`
	// Left counts the unticked items per bucket, in file order, including
	// the PRs appended under New since.
	Left []SectionCount `json:"left"`
	// New lists the open PRs the file did not hold, now appended.
	New []ResyncNew `json:"new"`
	// Moved lists unticked items whose PR the rules now put in another
	// bucket: a draft? PR made a draft again, a land PR that lost its
	// approval.
	Moved []ResyncMoved `json:"moved"`
	// Open is the number of open PRs in the fresh triage.
	Open int `json:"open"`
	// Sources are the triage's: how current the open PR list is.
	Sources []TriageSource `json:"sources"`
	// Incomplete is the triage's.
	Incomplete []string `json:"incomplete,omitempty"`
	// TickSkipped is set when the open PR list was incomplete, so nothing
	// was ticked: a PR missing from it may still be open.
	TickSkipped bool `json:"tick_skipped,omitempty"`
}

// ResyncDone is an item ticked because its PR is no longer open.
type ResyncDone struct {
	URL     string `json:"url"`
	Section string `json:"section"`
	// State is merged or closed, or gone when the PR dropped out of the
	// open-authored list, or the snapshot does not hold it at all (it closed
	// before the closed-PR search reaches), with its closed state unknown.
	State string    `json:"state"`
	At    time.Time `json:"at,omitzero"`
}

// SectionCount is the number of unticked items in a section. Section is
// the heading's bucket name, its count stripped.
type SectionCount struct {
	Section string `json:"section"`
	Left    int    `json:"left"`
}

// ResyncNew is an open PR added to the worklist.
type ResyncNew struct {
	Bucket string     `json:"bucket"`
	Item   TriageItem `json:"item"`
}

// ResyncMoved is an item in a section other than its bucket.
type ResyncMoved struct {
	URL     string `json:"url"`
	Section string `json:"section"`
	Bucket  string `json:"bucket"`
}

// sectionBucket is the bucket name of a "name (count)" section heading.
func sectionBucket(section string) string {
	if i := strings.LastIndex(section, " ("); i > 0 && strings.HasSuffix(section, ")") {
		return section[:i]
	}
	return section
}

// ResyncWorklist resyncs the worklist lines with t, built from v. Items a
// person ticked stay ticked whatever their PR's state; unticked items whose
// PR is no longer open are ticked with " · merged <date>", " · closed
// <date>" or " · no longer open" appended. A PR v still holds open is never
// ticked, and nothing is while t.InventoryComplete is false. Open PRs the
// file lacks are appended under "## New since <time>", grouped by bucket.
// It returns the new lines.
func ResyncWorklist(lines []string, v *View, t *Triage, loc *time.Location) ([]string, *Resync) {
	r := &Resync{Done: []ResyncDone{}, Left: []SectionCount{}, New: []ResyncNew{}, Moved: []ResyncMoved{}, Open: t.Total, Incomplete: t.Incomplete, Sources: t.Sources}
	r.TickSkipped = !t.InventoryComplete
	open := map[string]string{} // URL to bucket
	for _, b := range t.Buckets {
		for _, it := range b.Items {
			open[it.URL] = b.Name
		}
	}
	gone := map[string]*PRInfo{}
	// stillOpen are the PRs the snapshot holds open outside the buckets: not
	// the operator's, such as a teammate's PR added by hand. Only a PR that
	// dropped out of the open-authored list is ticked while it reads open.
	stillOpen := map[string]bool{}
	for i := range v.Snapshot.Nodes {
		p := v.Snapshot.Nodes[i].PR
		switch {
		case p == nil || p.URL == "":
		case p.State != "open":
			gone[p.URL] = p
		case !slices.Contains(t.Dropped, p.URL):
			stillOpen[p.URL] = true
		}
	}
	out := append([]string(nil), lines...)
	listed := map[string]bool{}
	left := map[string]int{}
	var order []string
	countLeft := func(section string) {
		bucket := sectionBucket(section)
		if _, seen := left[bucket]; !seen {
			order = append(order, bucket)
		}
		left[bucket]++
	}
	for _, it := range ParseWorklist(lines) {
		listed[it.URL] = true
		if it.Checked {
			continue
		}
		bucket, isOpen := open[it.URL]
		if !isOpen && !r.TickSkipped && !stillOpen[it.URL] {
			d := ResyncDone{URL: it.URL, Section: it.Section, State: "gone"}
			suffix := " · no longer open"
			if p := gone[it.URL]; p != nil {
				d.State, d.At = p.State, p.ClosedAt
				if p.State == "merged" {
					d.At = p.MergedAt
				}
				suffix = " · " + p.State
				if !d.At.IsZero() {
					suffix += " " + d.At.In(loc).Format("2006-01-02")
				}
			}
			out[it.Line] = strings.Replace(out[it.Line], "- [ ]", "- [x]", 1) + suffix
			r.Done = append(r.Done, d)
			continue
		}
		countLeft(it.Section)
		// Items placed directly under "## New since" have no bucket heading.
		if isOpen && it.Section != "" && sectionBucket(it.Section) != bucket && !strings.HasPrefix(it.Section, "New since") {
			r.Moved = append(r.Moved, ResyncMoved{URL: it.URL, Section: it.Section, Bucket: bucket})
		}
	}
	var added []string
	for _, b := range t.Buckets {
		var items []TriageItem
		for _, it := range b.Items {
			if !listed[it.URL] {
				items = append(items, it)
				r.New = append(r.New, ResyncNew{Bucket: b.Name, Item: it})
				countLeft(b.Name)
			}
		}
		if len(items) > 0 {
			added = append(added, "", "### "+mdText(b.Name)+" ("+strconv.Itoa(len(items))+")", "")
			for _, it := range items {
				added = append(added, TriageItemLine(it, true, loc))
			}
		}
	}
	if len(added) > 0 {
		for len(out) > 0 && out[len(out)-1] == "" {
			out = out[:len(out)-1]
		}
		out = append(out, "", "## New since "+t.GeneratedAt.In(loc).Format("2006-01-02 15:04"))
		out = append(out, added...)
		out = append(out, "")
	}
	for _, bucket := range order {
		r.Left = append(r.Left, SectionCount{Section: bucket, Left: left[bucket]})
	}
	return out, r
}
