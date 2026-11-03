package dash

import (
	"fmt"
	"slices"
	"strings"
)

// FieldType is the type of a query field (gh-100): how its values are
// compared and which operands it takes.
type FieldType string

// Field types. Number, duration and date fields are ordered and take ranges.
const (
	FieldEnum     FieldType = "enum"     // one value; Values lists a closed set
	FieldSet      FieldType = "set"      // any number of values
	FieldNumber   FieldType = "number"   // numbers
	FieldDuration FieldType = "duration" // days, derived from timestamps
	FieldDate     FieldType = "date"     // local calendar days (YYYY-MM-DD)
	FieldText     FieldType = "text"     // case-insensitive substring match
	FieldBool     FieldType = "bool"     // true or false
)

// ordered reports whether a field of type t takes a range.
func (t FieldType) ordered() bool {
	return t == FieldNumber || t == FieldDuration || t == FieldDate
}

// Derive names a value computation that a JSON path cannot express. Both
// evaluators (query.go and live.js) implement the same set.
const (
	// DeriveDaysSince is the days from each timestamp at Paths to the
	// snapshot's generated_at, so a snapshot always gives the same answer.
	DeriveDaysSince = "days_since"
	// DeriveCount is how many values Paths hold.
	DeriveCount = "count"
	// DeriveActivityDays is the union of the local activity days
	// (Counts.Activity) of the efforts a node belongs to.
	DeriveActivityDays = "activity_days"
	// DeriveLinkedRepos is the union of Paths over the PR and bookmark nodes
	// with an edge into the node; with no such edge it is unknown.
	DeriveLinkedRepos = "linked_repos"
	// DeriveEfforts is the keys of the efforts a node belongs to, found by
	// walking edges backwards (live.js L.effortsOf).
	DeriveEfforts = "efforts"
	// DerivePRState is today's PR-state bucket: open, draft, merged or closed
	// for a PR (a draft is open and is_draft); for a bookmark, unknown when its
	// lookup has no data and none when it is known to have no PR. It does not
	// apply to a bookmark that has PRs: their own nodes carry the states.
	DerivePRState = "prstate"
)

// Field is one entry of the query field registry. Paths are dotted JSON paths
// into a node: a list on the way is searched element by element, and a
// segment may carry filters — [k=v], [k!=v], [k] (k set) or [!k] (k unset) —
// that keep only matching objects, e.g. pr.involvement[role=review_requested].person
// or pr[state=merged].merged_at. Several paths mean "any of".
type Field struct {
	// Name is the query name.
	Name  string    `json:"name"`
	Label string    `json:"label"`
	Type  FieldType `json:"type"`
	// Kinds are the node kinds the field applies to.
	Kinds []NodeKind `json:"kinds"`
	Paths []string   `json:"paths,omitempty"`
	// Derive, when set, computes the values instead of reading Paths
	// directly (days_since, count and linked_repos read Paths themselves).
	Derive string `json:"derive,omitempty"`
	// DeriveKinds limits Derive to these kinds; other kinds read Paths
	// directly. Empty means every kind in Kinds.
	DeriveKinds []NodeKind `json:"derive_kinds,omitempty"`
	// Map normalizes values, keyed by the lowercased value; the key ""
	// supplies the value when the node has none.
	Map map[string]string `json:"map,omitempty"`
	// Unit names the unit of number, duration and date values.
	Unit string `json:"unit,omitempty"`
	// Values is the ordered closed set of an enum, when it has one.
	Values []string `json:"values,omitempty"`
	// Scale is a chart hint for ordered fields: linear or log.
	Scale string `json:"scale,omitempty"`
}

// queryKinds lists every node kind, in display order. A field that applies
// to "all" kinds lists these.
var queryKinds = []NodeKind{KindEffort, KindWorkspace, KindBookmark, KindPR, KindTicket, KindAgent}

func kindNames(ks []NodeKind) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = string(k)
	}
	return out
}

// fieldRegistry is the canonical registry: the fields this snapshot can
// answer today. gh-97, gh-98 and gh-99 append their fields here as they
// land (see plannedFields).
var fieldRegistry = []Field{
	{Name: "kind", Label: "Kind", Type: FieldEnum, Kinds: queryKinds, Paths: []string{"kind"}, Values: kindNames(queryKinds)},
	{Name: "text", Label: "Text (label, title, ID)", Type: FieldText, Kinds: queryKinds, Paths: []string{"label", "pr.title", "ticket.title", "id"}},
	{Name: "effort", Label: "Effort", Type: FieldSet, Kinds: queryKinds, Derive: DeriveEfforts},
	{Name: "repo", Label: "Repository", Type: FieldSet, Kinds: []NodeKind{KindWorkspace, KindBookmark, KindPR, KindTicket},
		Paths: []string{"workspace.repo_slug", "bookmark.repo_slug", "pr.repo"}, Derive: DeriveLinkedRepos, DeriveKinds: []NodeKind{KindTicket}},
	{Name: "prstate", Label: "PR state", Type: FieldEnum, Kinds: []NodeKind{KindPR, KindBookmark}, Derive: DerivePRState,
		Values: []string{"open", "draft", "merged", "closed", "none", "unknown"}},
	{Name: "review", Label: "Review decision", Type: FieldEnum, Kinds: []NodeKind{KindPR}, Paths: []string{"pr.review_decision"},
		Map: map[string]string{"": "none", "review_required": "none"}, Values: []string{"approved", "changes_requested", "none"}},
	{Name: "checks", Label: "CI checks", Type: FieldEnum, Kinds: []NodeKind{KindPR}, Paths: []string{"pr.checks"},
		Values: []string{"success", "failure", "pending", "none"}},
	{Name: "freshness", Label: "Data freshness", Type: FieldEnum, Kinds: []NodeKind{KindPR, KindTicket, KindBookmark},
		Paths: []string{"pr.freshness", "ticket.freshness", "bookmark.pr_lookup"}, Values: []string{"fresh", "stale", "unknown", "error"}},
	{Name: "activity", Label: "Local activity", Type: FieldDate, Kinds: queryKinds, Derive: DeriveActivityDays, Unit: "day", Scale: "linear"},
	{Name: "liveness", Label: "Agent liveness", Type: FieldEnum, Kinds: []NodeKind{KindAgent}, Paths: []string{"agent.liveness"},
		Values: []string{"active", "live", "uncertain"}},
	{Name: "ticket_status", Label: "Ticket status", Type: FieldEnum, Kinds: []NodeKind{KindTicket}, Paths: []string{"ticket.status"}},
	{Name: "assignee", Label: "Assignee", Type: FieldEnum, Kinds: []NodeKind{KindTicket}, Paths: []string{"ticket.assignee"}},
	// gh-97 and gh-98 add the PR (merged_at if merged, closed_at if closed,
	// else updated_at) and ticket (resolved_at, else updated_at) paths.
	{Name: "when", Label: "When", Type: FieldDate, Kinds: []NodeKind{KindEffort, KindWorkspace}, Derive: DeriveActivityDays, Unit: "day", Scale: "linear"},
}

// plannedFields names the fields of the epic's catalogue (spec/gh-100.md,
// requirement 3) that a data spec has yet to register, with that spec. A
// query naming one gets "not available yet" instead of "unknown field", and
// a saved filter using one is hidden with that reason. A registered field
// takes precedence over its entry here.
var plannedFields = map[string]string{
	"author": "gh-97", "requested_from": "gh-97", "reviewed_by": "gh-97", "reviewer": "gh-97",
	"created": "gh-97", "merged": "gh-97", "closed": "gh-97", "updated": "gh-97", "ready": "gh-97",
	"age": "gh-97", "updated_ago": "gh-97", "additions": "gh-97", "deletions": "gh-97", "files": "gh-97",
	"mergeable": "gh-97", "check_failing": "gh-97", "ticket": "gh-97", "source": "gh-97",
	"status_category": "gh-98", "sprint": "gh-98", "sprint_state": "gh-98", "points": "gh-98",
	"epic": "gh-98", "team": "gh-98", "type": "gh-98", "priority": "gh-98", "component": "gh-98",
	"label": "gh-98", "resolved": "gh-98",
	"move": "gh-99", "move_people": "gh-99", "waiting_on": "gh-99", "reason": "gh-99", "waiting": "gh-99",
	"ci": "gh-99", "size": "gh-99", "size_bucket": "gh-99", "stale": "gh-99", "mismatch": "gh-99",
	"linked_prs": "gh-99",
}

// Registry is a set of query fields by name.
type Registry struct {
	fields  []Field
	byName  map[string]int
	planned map[string]string
}

// RegistryJSON is the registry as served to the page and stored in
// testdata/fields.json for the JS tests.
type RegistryJSON struct {
	Fields  []Field           `json:"fields"`
	Planned map[string]string `json:"planned,omitempty"`
}

var defaultRegistry = mustRegistry(fieldRegistry, plannedFields)

// Fields returns the canonical query field registry.
func Fields() *Registry { return defaultRegistry }

func mustRegistry(fs []Field, planned map[string]string) *Registry {
	r, err := NewRegistry(fs, planned)
	if err != nil {
		panic(err)
	}
	return r
}

var derives = []string{DeriveDaysSince, DeriveCount, DeriveActivityDays, DeriveLinkedRepos, DeriveEfforts, DerivePRState}

// NewRegistry checks fs and builds a registry; a later field with the name
// of an earlier one replaces it. planned may be nil.
func NewRegistry(fs []Field, planned map[string]string) (*Registry, error) {
	r := &Registry{byName: map[string]int{}, planned: map[string]string{}}
	for _, f := range fs {
		if !validFieldName(f.Name) {
			return nil, fmt.Errorf("query field %q: names are lowercase letters, digits and _", f.Name)
		}
		switch f.Type {
		case FieldEnum, FieldSet, FieldNumber, FieldDuration, FieldDate, FieldText, FieldBool:
		default:
			return nil, fmt.Errorf("query field %s: unknown type %q", f.Name, f.Type)
		}
		if len(f.Kinds) == 0 {
			return nil, fmt.Errorf("query field %s: no kinds", f.Name)
		}
		for _, k := range append(slices.Clone(f.Kinds), f.DeriveKinds...) {
			if !slices.Contains(queryKinds, k) {
				return nil, fmt.Errorf("query field %s: unknown node kind %q; kinds are %s", f.Name, k, strings.Join(kindNames(queryKinds), ", "))
			}
		}
		for _, v := range f.Values {
			if v != strings.ToLower(v) {
				return nil, fmt.Errorf("query field %s: value %q must be lowercase, since values are matched lowercased; write %q", f.Name, v, strings.ToLower(v))
			}
		}
		for k := range f.Map {
			if k != strings.ToLower(k) {
				return nil, fmt.Errorf("query field %s: map key %q must be lowercase, since values are looked up lowercased", f.Name, k)
			}
		}
		switch f.Scale {
		case "", "linear", "log":
		default:
			return nil, fmt.Errorf("query field %s: scale %q is not linear or log", f.Name, f.Scale)
		}
		if f.Derive != "" && !slices.Contains(derives, f.Derive) {
			return nil, fmt.Errorf("query field %s: unknown derive %q", f.Name, f.Derive)
		}
		if f.Derive == "" && len(f.Paths) == 0 {
			return nil, fmt.Errorf("query field %s: no paths and no derive", f.Name)
		}
		for _, p := range f.Paths {
			if _, err := parsePath(p); err != nil {
				return nil, fmt.Errorf("query field %s: %w", f.Name, err)
			}
		}
		if i, ok := r.byName[f.Name]; ok {
			r.fields[i] = f
			continue
		}
		r.byName[f.Name] = len(r.fields)
		r.fields = append(r.fields, f)
	}
	for name, owner := range planned {
		if _, ok := r.byName[name]; !ok {
			r.planned[name] = owner
		}
	}
	return r, nil
}

func validFieldName(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// With returns a registry with fs added, each replacing any field of the same
// name. The shared test vectors use it to exercise fields a data spec has not
// registered yet.
func (r *Registry) With(fs ...Field) (*Registry, error) {
	return NewRegistry(append(slices.Clone(r.fields), fs...), r.planned)
}

// Field returns the named field.
func (r *Registry) Field(name string) (Field, bool) {
	i, ok := r.byName[name]
	if !ok {
		return Field{}, false
	}
	return r.fields[i], true
}

// List returns the fields in registry order.
func (r *Registry) List() []Field { return slices.Clone(r.fields) }

// Names returns the field names in registry order.
func (r *Registry) Names() []string {
	out := make([]string, len(r.fields))
	for i, f := range r.fields {
		out[i] = f.Name
	}
	return out
}

// Planned returns the spec that will register name, if it is planned and not
// registered.
func (r *Registry) Planned(name string) (string, bool) {
	o, ok := r.planned[name]
	return o, ok
}

// JSON returns the registry as served to the page.
func (r *Registry) JSON() RegistryJSON {
	return RegistryJSON{Fields: r.List(), Planned: r.planned}
}

// ordered returns the names of the fields that take ranges.
func (r *Registry) orderedNames() []string {
	var out []string
	for _, f := range r.fields {
		if f.Type.ordered() {
			out = append(out, f.Name)
		}
	}
	return out
}

// ---------- paths ----------

// pathSeg is one segment of a field path: a key and its filters.
type pathSeg struct {
	key     string
	filters []pathFilter
}

// pathFilter keeps an object when key equals (or, with not, differs from)
// val; with exists, when key is set (or, with not, unset).
type pathFilter struct {
	key    string
	val    string
	not    bool
	exists bool
}

func parsePath(p string) ([]pathSeg, error) {
	if p == "" {
		return nil, fmt.Errorf("empty path")
	}
	var segs []pathSeg
	for _, part := range splitPath(p) {
		key, rest, _ := strings.Cut(part, "[")
		if key == "" {
			return nil, fmt.Errorf("path %q: empty segment", p)
		}
		seg := pathSeg{key: key}
		if rest != "" {
			rest = "[" + rest
		}
		for rest != "" {
			if rest[0] != '[' {
				return nil, fmt.Errorf("path %q: expected [ in %q", p, rest)
			}
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return nil, fmt.Errorf("path %q: unclosed [", p)
			}
			body := rest[1:end]
			rest = rest[end+1:]
			var f pathFilter
			switch {
			case strings.Contains(body, "!="):
				f.key, f.val, _ = strings.Cut(body, "!=")
				f.not = true
			case strings.Contains(body, "="):
				f.key, f.val, _ = strings.Cut(body, "=")
			case strings.HasPrefix(body, "!"):
				f.key, f.exists, f.not = body[1:], true, true
			default:
				f.key, f.exists = body, true
			}
			if f.key == "" {
				return nil, fmt.Errorf("path %q: empty filter key", p)
			}
			seg.filters = append(seg.filters, f)
		}
		segs = append(segs, seg)
	}
	return segs, nil
}

// splitPath splits p on the dots outside brackets.
func splitPath(p string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '[':
			depth++
		case ']':
			depth--
		case '.':
			if depth == 0 {
				out = append(out, p[start:i])
				start = i + 1
			}
		}
	}
	return append(out, p[start:])
}

// truthy reports whether a JSON value counts as set for a [k] filter:
// present and not null, false, "", [] or {}.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// scalarString renders a JSON scalar the way live.js's String() does.
func scalarString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	case float64:
		return jsNumber(x), true
	}
	return "", false
}

func (f pathFilter) keep(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	val := m[f.key]
	if f.exists {
		return truthy(val) != f.not
	}
	s, ok := scalarString(val)
	return (ok && s == f.val) != f.not
}

// walkPath returns the scalars at segs under root and whether the path is
// known. It is known when it reaches its end, and also when a list on the way
// is present but empty, or none of its elements passes the segment's filters
// (a known empty selection). A missing key, or a filter that rejects an
// object rather than list elements (a condition such as pr[state=merged]),
// leaves it unknown.
func walkPath(root any, segs []pathSeg) (vals []any, known bool) {
	cur := []any{root}
	for _, seg := range segs {
		var next []any
		fromList := false
		for _, v := range cur {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			child, ok := m[seg.key]
			if !ok || child == nil {
				continue
			}
			if l, ok := child.([]any); ok {
				fromList = true
				next = append(next, l...)
			} else {
				next = append(next, child)
			}
		}
		if len(seg.filters) > 0 {
			var kept []any
			for _, v := range next {
				ok := true
				for _, f := range seg.filters {
					if !f.keep(v) {
						ok = false
						break
					}
				}
				if ok {
					kept = append(kept, v)
				}
			}
			next = kept
		}
		if len(next) == 0 {
			return nil, fromList
		}
		cur = next
	}
	for _, v := range cur {
		switch v.(type) {
		case string, bool, float64:
			vals = append(vals, v)
		}
	}
	return vals, true
}
