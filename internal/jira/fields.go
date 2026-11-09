package jira

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// fieldNames maps a Jira field name, lowercased, to the logical field it
// is. Earlier names win when a site has both.
var fieldNames = []struct{ name, field string }{
	{"story points", FieldStoryPoints},
	{"story point estimate", FieldStoryPoints},
	{"sprint", FieldSprint},
	{"team", FieldTeam},
	{"epic link", FieldEpicLink},
}

// DiscoverFieldIDs finds a site's custom field IDs in acli's JSON for one
// work item. When the document carries Jira's "names" map (field ID to
// name) it matches by name: "Story Points" (or "Story point estimate"),
// "Sprint", "Team" and "Epic Link". Otherwise it can find only the sprint
// field, whose value has a shape no other field has, and only on a ticket
// that is or was in a sprint. acli has no command that lists a site's
// fields, so the others usually need [jira.fields]. The result holds only
// the IDs found.
func DiscoverFieldIDs(raw []byte) (FieldIDs, error) {
	var doc struct {
		Names  map[string]string          `json:"names"`
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return FieldIDs{}, fmt.Errorf("parse work item: %w", err)
	}
	var found FieldIDs
	ids := make([]string, 0, len(doc.Names))
	for id := range doc.Names {
		ids = append(ids, id)
	}
	slices.Sort(ids) // a stable pick when two fields share a name
	for _, fn := range fieldNames {
		if found.Get(fn.field) != "" {
			continue
		}
		for _, id := range ids {
			if strings.EqualFold(strings.TrimSpace(doc.Names[id]), fn.name) {
				found.Set(fn.field, id)
				break
			}
		}
	}
	if found.Sprint == "" {
		found.Sprint = sprintFieldByShape(doc.Fields)
	}
	return found, nil
}

// sprintFieldByShape returns the one custom field whose value is a
// non-empty list of sprints, or "" when there is none or more than one.
func sprintFieldByShape(fields map[string]json.RawMessage) string {
	match := ""
	for id, raw := range fields {
		if !strings.HasPrefix(id, "customfield_") || isNull(raw) {
			continue
		}
		if !looksLikeSprints(raw) {
			continue
		}
		if match != "" {
			return "" // ambiguous: never guess
		}
		match = id
	}
	return match
}

// legacySprintPrefix starts every element of the sprint field's old string
// form.
const legacySprintPrefix = "com.atlassian.greenhopper.service.sprint.Sprint@"

// looksLikeSprints reports whether raw is a non-empty list whose every
// element is a sprint: an object with a positive id, a name and a sprint
// state, or a legacy sprint string. It is stricter than parseSprintField, which
// reads a field already known to be the sprint field.
func looksLikeSprints(raw json.RawMessage) bool {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil || len(list) == 0 {
		return false
	}
	for _, el := range list {
		var legacy string
		if json.Unmarshal(el, &legacy) == nil {
			if _, ok := parseLegacySprint(legacy); !ok || !strings.HasPrefix(legacy, legacySprintPrefix) {
				return false
			}
			continue
		}
		var o struct {
			ID    *int   `json:"id"`
			Name  string `json:"name"`
			State string `json:"state"`
		}
		if json.Unmarshal(el, &o) != nil || o.ID == nil || *o.ID <= 0 || o.Name == "" || sprintState(o.State) == "" {
			return false
		}
	}
	return true
}
