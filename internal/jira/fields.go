package jira

import (
	"context"
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
	{"team assignment", FieldTeam},
	{"epic link", FieldEpicLink},
}

// DiscoverFieldIDs reads twg field update-metadata data, matching custom
// fields by their display names. It also accepts legacy names maps in cached
// test fixtures and can identify a sprint field by its value shape.
func DiscoverFieldIDs(raw []byte) (FieldIDs, error) {
	var doc struct {
		Names  map[string]string `json:"names"`
		Fields json.RawMessage   `json:"fields"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return FieldIDs{}, fmt.Errorf("parse work item: %w", err)
	}
	if len(doc.Fields) > 0 && doc.Fields[0] == '[' {
		var metadata []struct {
			ID   string `json:"id"`
			Key  string `json:"key"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(doc.Fields, &metadata); err != nil {
			return FieldIDs{}, fmt.Errorf("parse field metadata: %w", err)
		}
		for _, field := range metadata {
			id := field.ID
			if !strings.HasPrefix(id, "customfield_") {
				id = field.Key
			}
			if strings.HasPrefix(id, "customfield_") && field.Name != "" {
				doc.Names = appendName(doc.Names, id, field.Name)
			}
		}
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
		var fields map[string]json.RawMessage
		if len(doc.Fields) > 0 && json.Unmarshal(doc.Fields, &fields) == nil {
			found.Sprint = sprintFieldByShape(fields)
		} else if json.Unmarshal(raw, &fields) == nil {
			found.Sprint = sprintFieldByShape(fields)
		}
	}
	return found, nil
}

func appendName(names map[string]string, id, name string) map[string]string {
	if names == nil {
		names = make(map[string]string)
	}
	names[id] = name
	return names
}

// DiscoverFieldIDsContext fetches the field metadata for a work item without
// changing it. The result can be cached for other tickets at the same site.
func DiscoverFieldIDsContext(ctx context.Context, ticket string) (FieldIDs, error) {
	data, err := runContext(ctx, "jira", "workitem", "field", "update-metadata", "--id", ticket)
	if err != nil {
		return FieldIDs{}, err
	}
	return DiscoverFieldIDs(data)
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
