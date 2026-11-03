package dash

import (
	"cmp"
	"slices"
	"sort"
	"strings"
)

// SortKey is one key of a row sort (spec/gh-101.md, requirement 5): a query
// field, ascending unless Desc. The analysis table writes its sort as
// sort=-waiting,size; wgo dash --md --sort and GET /api/extract?sort= take
// the same text, so Copy as Markdown lists the rows in the table's order.
type SortKey struct {
	Field string
	Desc  bool
}

// ParseSort reads sort keys as the page writes them: field names, each
// optionally prefixed with "-" for descending.
func ParseSort(keys []string) []SortKey {
	out := make([]SortKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, SortKey{Field: strings.TrimPrefix(k, "-"), Desc: strings.HasPrefix(k, "-")})
	}
	return out
}

// checkSort reports a sort key that names no registered field.
func checkSort(reg *Registry, keys []string) error {
	for _, k := range keys {
		name := strings.TrimPrefix(k, "-")
		if _, ok := reg.Field(name); ok {
			continue
		}
		if name == "" {
			return extractArgsError("empty sort key %q; write a field name such as -updated_ago (a leading - sorts descending)", k)
		}
		if owner, planned := reg.Planned(name); planned {
			return extractArgsError("sort key %s is not available yet: it arrives with %s. Fields: %s", name, owner, strings.Join(reg.Names(), ", "))
		}
		if s := closest(strings.ToLower(name), reg.Names()); s != "" {
			return extractArgsError("unknown sort key %q; did you mean %s? Fields: %s", name, s, strings.Join(reg.Names(), ", "))
		}
		return extractArgsError("unknown sort key %q. Fields: %s", name, strings.Join(reg.Names(), ", "))
	}
	return nil
}

// Sort value classes: whatever the direction, known values come first,
// then known-empty ones ("none"), then unknown ones, then rows the field
// does not apply to. live-table.js (T.sortValue) orders them the same way.
const (
	sortKnown = iota
	sortEmpty
	sortUnknown
	sortNA
)

// sortValue is a field's value on a node reduced to one comparable value:
// the first number, the latest day, a closed enum's position in its values
// (then its text), an effort list's labels, or the values' lowercased text.
type sortValue struct {
	class int
	rank  int
	isNum bool
	num   float64
	str   string
}

func (ix *queryIndex) sortValue(f Field, i int) sortValue {
	v := ix.values[f.Name][i]
	switch {
	case v.na, v.unknown && f.Name == "waiting" && ix.waitingNA(i):
		return sortValue{class: sortNA}
	case v.unknown:
		return sortValue{class: sortUnknown}
	}
	switch {
	case f.Type == FieldNumber || f.Type == FieldDuration:
		if len(v.nums) == 0 {
			return sortValue{class: sortEmpty}
		}
		return sortValue{isNum: true, num: v.nums[0]}
	case f.Derive == DeriveEfforts:
		// Efforts sort by the labels the table shows, not their keys.
		if len(ix.efforts[i]) == 0 {
			return sortValue{class: sortEmpty}
		}
		labels := make([]string, len(ix.efforts[i]))
		for k, e := range ix.efforts[i] {
			labels[k] = strings.ToLower(ix.rowLabel(e))
		}
		return sortValue{str: strings.Join(labels, ",")}
	case len(v.norm) == 0:
		return sortValue{class: sortEmpty}
	case f.Type == FieldDate:
		return sortValue{str: slices.Max(v.norm)}
	case f.Type == FieldEnum && len(f.Values) > 0:
		r := slices.Index(f.Values, v.norm[0])
		if r < 0 {
			r = len(f.Values)
		}
		return sortValue{rank: r, str: v.norm[0]}
	}
	return sortValue{str: strings.Join(v.norm, ",")}
}

// waitingNA reports that node i's PR is not waiting on anyone by design:
// gh-99 sets waiting_since only for the reviewer and nobody moves and for
// an author move decided by changes requested (the blocking review's
// time), so a known author or none move without it is "does not apply",
// not unknown. live-table.js (T.waitingNA) decides the same way.
func (ix *queryIndex) waitingNA(i int) bool {
	pr, _ := ix.nodes[i]["pr"].(map[string]any)
	facts, _ := pr["facts"].(map[string]any)
	switch move, _ := facts["move"].(string); move {
	case "none":
		return true
	case "author":
		reasons, _ := facts["reasons"].([]any)
		for _, r := range reasons {
			if m, ok := r.(map[string]any); ok && m["code"] == "changes_requested" {
				return false
			}
		}
		return true
	}
	return false
}

// compareSortValues orders a and b for one key. Only known values follow
// the direction: the other classes stay last in both.
func compareSortValues(a, b sortValue, desc bool) int {
	if a.class != b.class {
		return cmp.Compare(a.class, b.class)
	}
	if a.class != sortKnown {
		return 0
	}
	c := cmp.Compare(a.rank, b.rank)
	if c == 0 {
		if a.isNum {
			c = cmp.Compare(a.num, b.num)
		} else {
			c = strings.Compare(a.str, b.str)
		}
	}
	if desc {
		return -c
	}
	return c
}

// rowLabel is node i's label, or its ID when it has none.
func (ix *queryIndex) rowLabel(i int) string {
	if s, ok := ix.nodes[i]["label"].(string); ok && s != "" {
		return s
	}
	return ix.ids[i]
}

// sortRows sorts rows (node indices) in place by keys, then by
// case-insensitive label and ID, always ascending. With no keys it is the
// default order: kind, label, ID. Keys must name registered fields.
func (ix *queryIndex) sortRows(rows []int, keys []SortKey) {
	fields := make([]Field, len(keys))
	vals := make([][]sortValue, len(keys))
	for k, key := range keys {
		fields[k], _ = ix.reg.Field(key.Field)
		vals[k] = make([]sortValue, len(rows))
		for p, i := range rows {
			vals[k][p] = ix.sortValue(fields[k], i)
		}
	}
	labels := make([]string, len(rows))
	for p, i := range rows {
		labels[p] = strings.ToLower(ix.rowLabel(i))
	}
	idx := make([]int, len(rows)) // positions into rows, sorted
	for p := range idx {
		idx[p] = p
	}
	sort.SliceStable(idx, func(a, z int) bool {
		pa, pz := idx[a], idx[z]
		for k, key := range keys {
			if c := compareSortValues(vals[k][pa], vals[k][pz], key.Desc); c != 0 {
				return c < 0
			}
		}
		if len(keys) == 0 {
			if ka, kz := kindRank(ix.kinds[rows[pa]]), kindRank(ix.kinds[rows[pz]]); ka != kz {
				return ka < kz
			}
		}
		if labels[pa] != labels[pz] {
			return labels[pa] < labels[pz]
		}
		return ix.ids[rows[pa]] < ix.ids[rows[pz]]
	})
	sorted := make([]int, len(rows))
	for p, q := range idx {
		sorted[p] = rows[q]
	}
	copy(rows, sorted)
}
