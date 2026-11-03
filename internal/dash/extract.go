package dash

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// ExtractArgs selects what the Markdown extract shows (spec/gh-100.md,
// requirements 16, 23 and 24). `wgo dash --md` and GET /api/extract fill it
// from their flags and parameters and call the same Extract, so both print
// the same bytes for the same snapshot.
type ExtractArgs struct {
	Query string
	// Cols are field names printed per row as "name: value".
	Cols []string
	// Group sections the rows: effort (the default), repo or none.
	Group string
	// Layout is bullets (the default) or table (GitHub-flavoured Markdown).
	Layout string
	// View is rows (the default). gh-105 adds sprint, with Sprint naming one.
	View   string
	Sprint string
}

// ErrExtractArgs marks an extract request that cannot be served as asked:
// a bad parameter or a view that is not available yet. Query errors are
// *QueryError instead.
var ErrExtractArgs = errors.New("extract")

func extractArgsError(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrExtractArgs}, args...)...)
}

// extractInput is what a renderer works from: the snapshot's view and
// index, the evaluated query and the request.
type extractInput struct {
	v       *View
	ix      *queryIndex
	res     *QueryResult
	args    ExtractArgs
	filters []SavedFilter
}

// extractRenderers maps each extract view to its renderer. gh-105 registers
// "sprint" here, beside "rows".
var extractRenderers = map[string]func(*extractInput) (string, error){
	"rows": renderRows,
}

// pendingViews names extract views a later spec provides.
var pendingViews = map[string]string{"sprint": "gh-105 (the sprint view)"}

// Extract renders v's snapshot as Markdown for args, resolving saved filter
// names for the heading from filters.
func Extract(v *View, reg *Registry, filters []SavedFilter, args ExtractArgs) (string, error) {
	if err := CheckExtractArgs(reg, args); err != nil {
		return "", err
	}
	view := args.View
	if view == "" {
		view = "rows"
	}
	q, err := ParseQuery(args.Query, reg)
	if err != nil {
		return "", err
	}
	ix, err := v.queryIndex(reg)
	if err != nil {
		return "", err
	}
	return extractRenderers[view](&extractInput{v: v, ix: ix, res: ix.evaluate(q), args: args, filters: filters})
}

// CheckExtractArgs reports what is wrong with args other than the query,
// so wgo dash --md can fail before collecting anything.
func CheckExtractArgs(reg *Registry, args ExtractArgs) error {
	view := args.View
	if view == "" {
		view = "rows"
	}
	if _, ok := extractRenderers[view]; !ok {
		if owner, pending := pendingViews[view]; pending {
			return extractArgsError("the %s view is not available yet: it arrives with %s. The rows view (the default) is available", view, owner)
		}
		return extractArgsError("unknown view %q; the available view is rows", view)
	}
	if args.Sprint != "" && view != "sprint" {
		if _, ok := extractRenderers["sprint"]; !ok {
			return extractArgsError("a sprint is chosen for the sprint view, which is not available yet: it arrives with %s", pendingViews["sprint"])
		}
		return extractArgsError("a sprint only applies to the sprint view")
	}
	switch args.Group {
	case "", "effort", "repo", "none":
	default:
		return extractArgsError("unknown group %q; use effort, repo or none", args.Group)
	}
	switch args.Layout {
	case "", "bullets", "table":
	default:
		return extractArgsError("unknown layout %q; use bullets or table", args.Layout)
	}
	for _, c := range args.Cols {
		if _, ok := reg.Field(c); ok {
			continue
		}
		if owner, planned := reg.Planned(c); planned {
			return extractArgsError("column %s is not available yet: it arrives with %s. Fields: %s", c, owner, strings.Join(reg.Names(), ", "))
		}
		if s := closest(strings.ToLower(c), reg.Names()); s != "" {
			return extractArgsError("unknown column %q; did you mean %s? Fields: %s", c, s, strings.Join(reg.Names(), ", "))
		}
		return extractArgsError("unknown column %q. Fields: %s", c, strings.Join(reg.Names(), ", "))
	}
	return nil
}

// renderRows is the rows view: an optional heading, sections, one bullet
// (or table row) per row, and the provenance footer.
func renderRows(x *extractInput) (string, error) {
	var b strings.Builder
	if x.args.Query != "" {
		title := filterNameFor(x.filters, x.args.Query)
		if title == "" {
			title = x.args.Query
		}
		b.WriteString("## " + mdText(title) + "\n\n")
	}
	rows := slices.Clone(x.res.rowIdx)
	sort.SliceStable(rows, func(a, z int) bool {
		i, j := rows[a], rows[z]
		if ki, kj := kindRank(x.ix.kinds[i]), kindRank(x.ix.kinds[j]); ki != kj {
			return ki < kj
		}
		if li, lj := strings.ToLower(x.label(i)), strings.ToLower(x.label(j)); li != lj {
			return li < lj
		}
		return x.ix.ids[i] < x.ix.ids[j]
	})
	if len(rows) == 0 {
		b.WriteString("No rows match.\n\n")
	}
	for _, sec := range x.sections(rows) {
		if sec.title != "" {
			b.WriteString("### " + mdText(sec.title) + "\n\n")
		}
		if x.args.Layout == "table" {
			x.writeTable(&b, sec.rows)
		} else {
			for _, i := range sec.rows {
				x.writeBullet(&b, i)
			}
		}
		b.WriteString("\n")
	}
	x.writeFooter(&b)
	return b.String(), nil
}

func kindRank(k NodeKind) int {
	if r, ok := kindOrder[k]; ok {
		return r
	}
	return len(kindOrder)
}

func (x *extractInput) label(i int) string {
	if s, ok := x.ix.nodes[i]["label"].(string); ok && s != "" {
		return s
	}
	return x.ix.ids[i]
}

func (x *extractInput) str(i int, path string) string {
	segs, _ := parsePath(path)
	vals, _ := walkPath(x.ix.nodes[i], segs)
	for _, v := range vals {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func (x *extractInput) title(i int) string {
	if t := x.str(i, "pr.title"); t != "" {
		return t
	}
	return x.str(i, "ticket.title")
}

func (x *extractInput) url(i int) string {
	u := x.str(i, "pr.url")
	if u == "" {
		u = x.str(i, "ticket.url")
	}
	l := strings.ToLower(u)
	if !strings.HasPrefix(l, "https://") && !strings.HasPrefix(l, "http://") {
		return ""
	}
	return strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29", "<", "%3C", ">", "%3E").Replace(u)
}

func (x *extractInput) linkedLabel(i int) string {
	if u := x.url(i); u != "" {
		return "[" + mdText(x.label(i)) + "](" + u + ")"
	}
	return mdText(x.label(i))
}

type section struct {
	title string
	rows  []int
}

// effortGroupRank orders efforts as live.js's L.effortOrder does.
var effortGroupRank = map[string]int{GroupEffort: 0, GroupTicket: 1, GroupTheme: 2, GroupUngrouped: 3}

// sections splits rows by effort (a row in several efforts is listed under
// each), by repository, or not at all.
func (x *extractInput) sections(rows []int) []section {
	switch x.args.Group {
	case "none":
		return []section{{rows: rows}}
	case "repo":
		byRepo := map[string][]int{}
		var unknown, none []int
		for _, i := range rows {
			v := x.ix.values["repo"][i]
			switch {
			case v.na:
				none = append(none, i)
			case v.unknown || len(v.disp) == 0:
				unknown = append(unknown, i)
			default:
				for _, r := range v.disp {
					byRepo[r] = append(byRepo[r], i)
				}
			}
		}
		var out []section
		for _, r := range slices.Sorted(maps.Keys(byRepo)) {
			out = append(out, section{title: r, rows: byRepo[r]})
		}
		if len(unknown) > 0 {
			out = append(out, section{title: "Repository unknown", rows: unknown})
		}
		if len(none) > 0 {
			out = append(out, section{title: "No repository", rows: none})
		}
		return out
	}
	byEffort := map[int][]int{}
	var none []int
	for _, i := range rows {
		if len(x.ix.efforts[i]) == 0 {
			none = append(none, i)
		}
		for _, e := range x.ix.efforts[i] {
			byEffort[e] = append(byEffort[e], i)
		}
	}
	efforts := slices.Collect(maps.Keys(byEffort))
	rank := func(e int) int {
		g := x.str(e, "effort.group")
		if r, ok := effortGroupRank[g]; ok {
			return r
		}
		return 2
	}
	sort.Slice(efforts, func(a, z int) bool {
		i, j := efforts[a], efforts[z]
		if ri, rj := rank(i), rank(j); ri != rj {
			return ri < rj
		}
		if li, lj := strings.ToLower(x.label(i)), strings.ToLower(x.label(j)); li != lj {
			return li < lj
		}
		return x.ix.ids[i] < x.ix.ids[j]
	})
	var out []section
	for _, e := range efforts {
		out = append(out, section{title: x.label(e), rows: byEffort[e]})
	}
	if len(none) > 0 {
		out = append(out, section{title: "No effort", rows: none})
	}
	return out
}

// valueText is a field's value on node i as text: "unknown" when unknown,
// "none" when known and empty, and "" when the field does not apply.
func (x *extractInput) valueText(name string, i int) string {
	v := x.ix.values[name][i]
	f, _ := x.ix.reg.Field(name)
	switch {
	case v.na:
		return ""
	case v.unknown:
		return "unknown"
	case f.Type == FieldNumber || f.Type == FieldDuration:
		if len(v.nums) == 0 {
			return "none"
		}
		var parts []string
		for _, n := range v.nums {
			if f.Type == FieldDuration {
				parts = append(parts, fmt.Sprintf("%.0fd", n))
			} else {
				parts = append(parts, strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", n), "0"), "."))
			}
		}
		return strings.Join(parts, ", ")
	case len(v.disp) == 0:
		return "none"
	}
	return strings.Join(v.disp, ", ")
}

func (x *extractInput) writeBullet(b *strings.Builder, i int) {
	b.WriteString("- " + x.linkedLabel(i))
	if t := x.title(i); t != "" {
		b.WriteString(" — " + mdText(t))
	}
	for _, c := range x.args.Cols {
		if s := x.valueText(c, i); s != "" {
			b.WriteString(" · " + c + ": " + mdText(s))
		}
	}
	b.WriteString("\n")
}

func (x *extractInput) writeTable(b *strings.Builder, rows []int) {
	head := append([]string{"Item", "Title"}, x.args.Cols...)
	b.WriteString("| " + strings.Join(head, " | ") + " |\n")
	b.WriteString(strings.Repeat("| --- ", len(head)) + "|\n")
	for _, i := range rows {
		cells := []string{x.linkedLabel(i), cell(x.title(i))}
		for _, c := range x.args.Cols {
			cells = append(cells, cell(x.valueText(c, i)))
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
}

// cell is a table cell: escaped, on one line, and "—" rather than blank
// for a value that does not apply.
func cell(s string) string {
	if s == "" {
		return "—"
	}
	return mdText(s)
}

// writeFooter states the provenance: snapshot time and generation, how
// many rows of how many nodes, the query, and each source's freshness.
func (x *extractInput) writeFooter(b *strings.Builder) {
	s := x.v.Snapshot
	b.WriteString("---\n\n")
	fmt.Fprintf(b, "Snapshot %s · generation %d · %d of %d nodes\n\n", s.GeneratedAt.Format("2006-01-02 15:04 -07:00"), s.Generation, len(x.res.rowIdx), x.ix.total)
	if x.args.Query == "" {
		b.WriteString("Query: none (every node)\n\n")
	} else {
		b.WriteString("Query: " + mdCode(x.args.Query) + "\n\n")
	}
	var parts []string
	for _, name := range slices.Sorted(maps.Keys(s.Sources)) {
		st := s.Sources[name]
		p := name + " " + freshText(st.State)
		var detail []string
		for _, c := range []struct {
			n int
			f Freshness
		}{{st.Fresh, Fresh}, {st.Stale, Stale}, {st.Unknown, Unknown}, {st.Error, Error}} {
			if c.n > 0 {
				detail = append(detail, fmt.Sprintf("%d %s", c.n, freshText(c.f)))
			}
		}
		if !st.OldestFetch.IsZero() {
			detail = append(detail, "oldest fetch "+st.OldestFetch.In(s.GeneratedAt.Location()).Format("2006-01-02 15:04 -07:00"))
		}
		if len(detail) > 0 {
			p += " (" + strings.Join(detail, ", ") + ")"
		}
		parts = append(parts, mdText(p))
	}
	if len(parts) == 0 {
		parts = []string{"none recorded"}
	}
	b.WriteString("Sources: " + strings.Join(parts, " · ") + "\n")
	for _, w := range x.res.Warnings {
		b.WriteString("\nNote: " + mdText(w) + "\n")
	}
}

// freshText names a freshness the way the page does: error is "unavailable".
func freshText(f Freshness) string {
	switch f {
	case Fresh, Stale:
		return string(f)
	case Error:
		return "unavailable"
	}
	return "unknown"
}

// mdText escapes s for Markdown inline text and keeps it on one line. An
// underscore inside a word (github_prs) is left alone: GitHub never reads
// it as emphasis.
func mdText(s string) string {
	rs := []rune(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s))
	word := func(i int) bool {
		return i >= 0 && i < len(rs) && (unicode.IsLetter(rs[i]) || unicode.IsDigit(rs[i]))
	}
	var b strings.Builder
	for i, r := range rs {
		switch {
		case r == '_' && word(i-1) && word(i+1):
		case strings.ContainsRune("\\`*_[]<>|", r) || (i == 0 && r == '#'):
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// mdCode wraps s in a code span long enough to hold its backticks.
func mdCode(s string) string {
	fence := "`"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		return fence + " " + s + " " + fence
	}
	return fence + s + fence
}
