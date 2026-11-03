package dash

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

// SavedFilter is a named query expression: a built-in (spec/gh-100.md,
// requirement 22) or a [dash.filters] entry in config, which overrides a
// built-in of the same name.
type SavedFilter struct {
	Name    string `json:"name"`
	Expr    string `json:"expr"`
	Builtin bool   `json:"builtin,omitempty"`
	// Overrides is set on a config filter that replaces a built-in.
	Overrides bool `json:"overrides,omitempty"`
	// Hidden filters cannot run yet: a field they use is not registered
	// (its data spec has not landed), or the expression is invalid. Reason
	// says which.
	Hidden bool   `json:"hidden,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// builtinFilters are the saved filters every dash has, in display order.
var builtinFilters = []SavedFilter{
	{Name: "my-move", Expr: "move_people:@me"},
	{Name: "waiting-reviewers", Expr: "kind:pr;move:reviewer"},
	{Name: "push-reviewers", Expr: "kind:pr;move:reviewer,nobody;ci:green,systemic_only"},
	{Name: "nobody", Expr: "kind:pr;move:nobody"},
	{Name: "review-inbox", Expr: "kind:pr;requested_from:@me"},
	{Name: "shipped", Expr: "kind:pr;prstate:merged;merged:-7d.."},
	{Name: "close-candidates", Expr: "kind:pr;prstate:open,draft;updated_ago:>=90"},
	{Name: "jira-out-of-sync", Expr: "kind:ticket;mismatch:*"},
	{Name: "sprint", Expr: "kind:ticket;sprint_state:active"},
}

// BuiltinFilter returns the named built-in filter.
func BuiltinFilter(name string) (SavedFilter, bool) {
	for _, f := range builtinFilters {
		if f.Name == name {
			f.Builtin = true
			return f, true
		}
	}
	return SavedFilter{}, false
}

// MergeFilters returns the built-in filters, overridden by and followed by
// the configured ones (sorted by name), each checked against reg: a filter
// that does not parse is hidden with the reason.
func MergeFilters(configured map[string]string, reg *Registry) []SavedFilter {
	return mergeFilters(configured, nil, reg)
}

// MergeConfigFilters is MergeFilters for [dash.filters] as config decodes
// it (config.DashConfig.Filters): a dotted name ("release.v2") arrives as a
// nested table and is flattened back, and an entry that is not a string is
// hidden with the reason rather than failing.
func MergeConfigFilters(raw map[string]any, reg *Registry) []SavedFilter {
	exprs, bad := map[string]string{}, map[string]string{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			name := prefix + k
			switch x := v.(type) {
			case string:
				exprs[name] = x
			case map[string]any:
				walk(name+".", x)
			default:
				bad[name] = fmt.Sprintf("[dash.filters] %s in ~/.wgo/config.toml is %v, not a query string; write %q = \"kind:pr\"", name, v, name)
			}
		}
	}
	walk("", raw)
	return mergeFilters(exprs, bad, reg)
}

func mergeFilters(configured, bad map[string]string, reg *Registry) []SavedFilter {
	var out []SavedFilter
	for _, f := range builtinFilters {
		f.Builtin = true
		if expr, ok := configured[f.Name]; ok {
			f = SavedFilter{Name: f.Name, Expr: expr, Overrides: true}
		} else if why, ok := bad[f.Name]; ok {
			f = SavedFilter{Name: f.Name, Overrides: true, Hidden: true, Reason: why}
		}
		out = append(out, f)
	}
	var names []string
	for name := range configured {
		if _, builtin := BuiltinFilter(name); !builtin {
			names = append(names, name)
		}
	}
	for name := range bad {
		if _, builtin := BuiltinFilter(name); !builtin {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if why, ok := bad[name]; ok {
			out = append(out, SavedFilter{Name: name, Hidden: true, Reason: why})
		} else {
			out = append(out, SavedFilter{Name: name, Expr: configured[name]})
		}
	}
	for i := range out {
		if out[i].Reason == "" {
			out[i].Hidden, out[i].Reason = filterAvailability(out[i], reg)
		}
	}
	return out
}

// filterAvailability says whether f can run against reg, and if not why.
func filterAvailability(f SavedFilter, reg *Registry) (bool, string) {
	_, err := ParseQuery(f.Expr, reg)
	if err == nil {
		return false, ""
	}
	// Name every field a data spec has yet to register, not just the first.
	pending := map[string][]string{}
	for _, clause := range strings.Split(f.Expr, ";") {
		name, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(clause), "!"), ":")
		name = strings.TrimSpace(name)
		if !ok {
			continue
		}
		if owner, planned := reg.Planned(name); planned && !slices.Contains(pending[owner], name) {
			pending[owner] = append(pending[owner], name)
		}
	}
	if len(pending) > 0 {
		var parts []string
		for _, owner := range slices.Sorted(maps.Keys(pending)) {
			parts = append(parts, fmt.Sprintf("%s (arrives with %s)", strings.Join(pending[owner], ", "), owner))
		}
		return true, "needs fields not registered yet: " + strings.Join(parts, "; ")
	}
	where := "built-in filter"
	if !f.Builtin {
		where = "[dash.filters] " + f.Name + " in ~/.wgo/config.toml"
	}
	return true, where + " does not parse: " + err.Error()
}

// LookupFilter returns the saved filter named name from filters.
func LookupFilter(filters []SavedFilter, name string) (SavedFilter, bool) {
	for _, f := range filters {
		if f.Name == name {
			return f, true
		}
	}
	return SavedFilter{}, false
}

// filterNameFor returns the name of the visible saved filter whose
// expression is exactly expr, or "".
func filterNameFor(filters []SavedFilter, expr string) string {
	for _, f := range filters {
		if !f.Hidden && f.Expr == expr {
			return f.Name
		}
	}
	return ""
}
