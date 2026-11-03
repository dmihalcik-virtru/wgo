package dash

import (
	"fmt"
	"slices"
	"strings"
)

// DefaultFacets are the live page's facet panel, in order (spec/gh-102.md,
// requirement 1): whose move, CI, PR state, repository, author, reviewer,
// waiting, age, size, ticket status, sprint and Jira mismatch.
// web/live-facets.js holds the same list for a bootstrap without one.
var DefaultFacets = []string{"move", "ci", "prstate", "repo", "author", "reviewer", "waiting", "age", "size", "ticket_status", "sprint", "mismatch"}

// facetsKey names [dash] facets in the notes ResolveFacets writes.
const facetsKey = "[dash] facets in ~/.wgo/config.toml"

// ResolveFacets returns the facet panel's fields for [dash] facets
// (configured), checked against reg, with a note for each name it leaves
// out: one that is not a query field (with the closest field), text (a
// substring match has no values to count), or a repeat. Names are read
// trimmed and lowercased. With nothing configured it is DefaultFacets, less
// any field reg lacks; with nothing usable configured, DefaultFacets too,
// and a note saying so. The page reports the notes, and so does wgo dash on
// stderr: a bad name never stops the dashboard.
func ResolveFacets(configured []string, reg *Registry) (facets, notes []string) {
	for _, raw := range configured {
		name := strings.ToLower(strings.TrimSpace(raw))
		f, ok := reg.Field(name)
		switch {
		case name == "":
			notes = append(notes, facetsKey+" has an empty name; remove it")
		case !ok:
			msg := fmt.Sprintf("%s names %q, which is not a query field", facetsKey, raw)
			if s := closest(name, reg.Names()); s != "" {
				msg += "; did you mean " + s + "?"
			} else {
				msg += "."
			}
			notes = append(notes, msg+" Fields: "+strings.Join(reg.Names(), ", "))
		case f.Type == FieldText:
			notes = append(notes, fmt.Sprintf("%s names %s, which cannot be a facet: it matches substrings and has no values to count; filter by text in the query bar", facetsKey, name))
		case slices.Contains(facets, name):
			notes = append(notes, fmt.Sprintf("%s lists %s twice; the second is ignored", facetsKey, name))
		default:
			facets = append(facets, name)
		}
	}
	if len(facets) > 0 {
		return facets, notes
	}
	if len(configured) > 0 {
		notes = append(notes, facetsKey+" names no usable field, so the panel shows the default facets")
	}
	for _, name := range DefaultFacets {
		if _, ok := reg.Field(name); ok {
			facets = append(facets, name)
		}
	}
	return facets, notes
}
