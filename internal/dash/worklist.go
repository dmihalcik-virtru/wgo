package dash

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// This file renders triage buckets as Markdown (gh-133): the worklist wgo
// pr triage writes, with a checkbox per PR, and the triage extract, which
// is the same without them. It also reads a worklist back. Only item lines
// carry meaning; anything else a person writes in the file is theirs.

// WorklistHeading starts the first line of a worklist or triage extract.
const WorklistHeading = "# Open PR triage, "

// RenderTriage renders t as Markdown. With checkboxes it is a worklist;
// without, the triage extract. Times are shown in loc.
func RenderTriage(t *Triage, checkboxes bool, loc *time.Location) string {
	var b strings.Builder
	at := t.GeneratedAt.In(loc)
	fmt.Fprintf(&b, "%s%s\n\n", WorklistHeading, at.Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "%d open %s by %s.", t.Total, plural(t.Total, "PR", "PRs"), mdText(t.Me))
	if checkboxes {
		b.WriteString(" Close, merge or draft them on GitHub, or tick a box yourself; `wgo pr triage --resync` ticks the ones that closed or merged.")
	}
	b.WriteString("\n")
	if len(t.Sources) > 0 {
		var parts []string
		for _, s := range t.Sources {
			p := mdText(s.Name) + " " + freshText(s.State)
			if !s.FetchedAt.IsZero() {
				p += ", fetched " + s.FetchedAt.In(loc).Format("2006-01-02 15:04")
			}
			parts = append(parts, p)
		}
		fmt.Fprintf(&b, "\nSources: %s\n", strings.Join(parts, " · "))
	}
	if len(t.Incomplete) > 0 {
		fmt.Fprintf(&b, "\n> **Incomplete:** %s. Re-run before acting on what they cover.\n", mdText(strings.Join(t.Incomplete, "; ")))
	}
	for _, bk := range t.Buckets {
		writeTriageSection(&b, bk.Name, bk.Note, bk.Items, checkboxes, loc)
	}
	return b.String()
}

func writeTriageSection(b *strings.Builder, name, note string, items []TriageItem, checkboxes bool, loc *time.Location) {
	fmt.Fprintf(b, "\n## %s (%d)\n\n", mdText(name), len(items))
	if note != "" {
		b.WriteString(mdText(note) + "\n\n")
	}
	for _, it := range items {
		b.WriteString(TriageItemLine(it, checkboxes, loc) + "\n")
	}
}

// TriageItemLine is one PR's line: `- [ ] [repo#N](url) draft|ready ·
// updated <date> · [TICKET](url) <status> · <human reviewers> · <title>`,
// then the PRs stacked on it and the fields that kept an earlier rule from
// deciding. "no ticket" and "human reviews unknown" stand in for missing
// parts; without checkbox the prefix is "- ".
func TriageItemLine(it TriageItem, checkbox bool, loc *time.Location) string {
	var parts []string
	label := it.Ref
	if i := strings.IndexByte(label, '/'); i >= 0 {
		label = label[i+1:]
	}
	state := "ready"
	if it.Draft {
		state = "draft"
	}
	parts = append(parts, mdLink(label, it.URL)+" "+state)
	if !it.UpdatedAt.IsZero() {
		parts = append(parts, "updated "+it.UpdatedAt.In(loc).Format("2006-01-02"))
	}
	if len(it.Tickets) == 0 {
		parts = append(parts, "no ticket")
	}
	for _, tk := range it.Tickets {
		parts = append(parts, mdLink(tk.Key, tk.URL)+" "+mdText(cmpOr(tk.Status, "status unknown")))
	}
	if it.HumanReviews == nil {
		parts = append(parts, "human reviews unknown")
	} else if len(it.HumanReviews) > 0 {
		var rs []string
		for _, r := range it.HumanReviews {
			rs = append(rs, mdText(r.Author)+" "+strings.ReplaceAll(strings.ToLower(r.State), "_", " "))
		}
		parts = append(parts, strings.Join(rs, ", "))
	}
	if it.Title != "" {
		parts = append(parts, mdText(it.Title))
	}
	if len(it.StackChildren) > 0 {
		var cs []string
		for _, c := range it.StackChildren {
			cs = append(cs, mdLink(strconv.Itoa(c.Number), c.URL))
		}
		parts = append(parts, "stack: base of "+strings.Join(cs, ", "))
	}
	if len(it.Unknown) > 0 {
		parts = append(parts, "unknown: "+strings.Join(it.Unknown, ", "))
	}
	prefix := "- "
	if checkbox {
		prefix = "- [ ] "
	}
	return prefix + strings.Join(parts, " · ")
}

// mdLink is [text](url), or text alone when url is not http(s). A number
// is shown as #N.
func mdLink(text, url string) string {
	t := mdText(text)
	if _, err := strconv.Atoi(text); err == nil {
		t = "#" + text
	}
	if u := safeMDURL(url); u != "" {
		return "[" + t + "](" + u + ")"
	}
	return t
}

// worklistItemRe recognizes a worklist item by its prefix alone: a checkbox
// and a link to a GitHub pull request.
var worklistItemRe = regexp.MustCompile(`^- \[([ xX])\] \[[^\]]*\]\((https://github\.com/[^/\s()]+/[^/\s()]+/pull/[0-9]+)\)`)

// WorklistItem is one item line of a worklist.
type WorklistItem struct {
	// Line is the item's 0-based line number.
	Line    int
	Checked bool
	URL     string
	// Section is the bucket heading above the item: a "## " heading, or a
	// "### " bucket heading inside a "## New since" section.
	Section string
}

// ParseWorklist returns the item lines of a worklist.
func ParseWorklist(lines []string) []WorklistItem {
	var out []WorklistItem
	section := ""
	inNewSince := false
	for i, l := range lines {
		if strings.HasPrefix(l, "## ") {
			section = strings.TrimSpace(l[3:])
			inNewSince = strings.HasPrefix(section, "New since ")
			continue
		}
		if inNewSince && strings.HasPrefix(l, "### ") {
			section = strings.TrimSpace(l[4:])
			continue
		}
		if m := worklistItemRe.FindStringSubmatch(l); m != nil {
			out = append(out, WorklistItem{Line: i, Checked: m[1] != " ", URL: m[2], Section: section})
		}
	}
	return out
}
