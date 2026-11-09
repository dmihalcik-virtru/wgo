package jira

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FactFields is the --fields value GetIssueJSON is asked for when reading a
// ticket's facts. Story points, sprint and team are custom fields whose IDs
// differ per site, and naming one in --fields is reported to fail on some
// acli versions, so every field is requested and ParseFacts picks out the
// ones it knows.
const FactFields = "*all"

// FactFieldList is the fallback for FactFields: every system field
// ParseFacts reads, by name. It carries no custom field.
const FactFieldList = "key,summary,status,issuetype,priority,assignee,labels,components,parent,resolution,resolutiondate,created,updated"

// SearchFactFields are the fields a sprint ticket search asks for: the
// subset of ParseFacts' fields that `acli jira workitem search --fields`
// accepts. It rejects custom fields, parent, components, resolution,
// resolutiondate, created and updated before making any request.
var SearchFactFields = []string{"key", "summary", "status", "issuetype", "priority", "assignee", "labels"}

// Logical names of the site-specific fields, as [jira.fields] keys them.
const (
	FieldStoryPoints = "story_points"
	FieldSprint      = "sprint"
	FieldTeam        = "team"
	FieldEpicLink    = "epic_link"
)

// FieldIDs are a site's IDs (customfield_NNNNN) for the custom fields
// ParseFacts reads. An empty ID is not read.
type FieldIDs struct {
	StoryPoints string
	Sprint      string
	Team        string
	// EpicLink is the legacy epic link. Next-gen (team-managed) projects,
	// and Jira Cloud generally, report the epic through parent instead.
	EpicLink string
}

// Get returns the ID for a logical field name.
func (f FieldIDs) Get(name string) string {
	switch name {
	case FieldStoryPoints:
		return f.StoryPoints
	case FieldSprint:
		return f.Sprint
	case FieldTeam:
		return f.Team
	case FieldEpicLink:
		return f.EpicLink
	}
	return ""
}

// Set sets the ID for a logical field name; an unknown name is ignored.
func (f *FieldIDs) Set(name, id string) {
	switch name {
	case FieldStoryPoints:
		f.StoryPoints = id
	case FieldSprint:
		f.Sprint = id
	case FieldTeam:
		f.Team = id
	case FieldEpicLink:
		f.EpicLink = id
	}
}

// FieldNames lists the logical field names in a stable order.
var FieldNames = []string{FieldStoryPoints, FieldSprint, FieldTeam, FieldEpicLink}

// fieldLabel names a logical field in a note.
var fieldLabel = map[string]string{
	FieldStoryPoints: "story points",
	FieldSprint:      "sprints",
	FieldTeam:        "team",
	FieldEpicLink:    "epic link",
}

// FieldLabel names a logical field for people, such as "story points".
func FieldLabel(name string) string { return fieldLabel[name] }

// SprintRef names a sprint a ticket is or was in.
type SprintRef struct {
	ID   int
	Name string
	// State is "active", "future" or "closed"; empty when unrecognised.
	State string
}

// Parent is a ticket's epic or parent issue.
type Parent struct {
	Key, Summary, Type string
}

// Facts are a ticket's facts as Jira reported them. A field the document
// does not carry is zero: a string is empty, a list nil. A reported empty
// list is non-nil, so nil (unknown) and [] (none) stay apart.
type Facts struct {
	Key     string
	Summary string
	Status  string
	// StatusCategory is "todo", "in_progress" or "done", from the status's
	// standard category key; empty when unknown.
	StatusCategory string
	Type           string
	Priority       string
	Assignee       string
	AssigneeID     string
	// Sprints is nil when unknown (no sprint field ID, or an unreadable
	// value) and empty when the ticket is in no sprint.
	Sprints     []SprintRef
	StoryPoints *float64
	// StoryPointsKnown is set when the story points field was read: then a
	// nil StoryPoints means unestimated rather than unknown.
	StoryPointsKnown bool
	Parent           *Parent
	Team             string
	Components       []string
	Labels           []string
	Resolution       string
	ResolvedAt       time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// rawIssue is one work item as acli prints it: Jira's REST issue JSON.
type rawIssue struct {
	Key    string                     `json:"key"`
	Fields map[string]json.RawMessage `json:"fields"`
}

// ParseFacts reads a ticket's facts from acli's JSON for one work item
// (`workitem view --json`, or one element of `workitem search --json`). ids
// name the custom fields to read. Custom field values vary in shape across
// sites; one ParseFacts cannot read stays unknown, and a note says which
// field and [jira.fields] key it was. A configured field the document lacks
// is unknown without a note: issue types outside a field's context lack it.
// Notes never name the ticket, so callers can count them by cause. A
// document that is not a work item is an error.
func ParseFacts(raw []byte, ids FieldIDs) (Facts, []string, error) {
	var is rawIssue
	if err := json.Unmarshal(raw, &is); err != nil {
		return Facts{}, nil, fmt.Errorf("parse work item: %w", err)
	}
	if is.Key == "" {
		return Facts{}, nil, fmt.Errorf("parse work item: no key")
	}
	f := Facts{Key: is.Key}
	fields := is.Fields
	str := func(name string) string {
		var s string
		if json.Unmarshal(fields[name], &s) == nil {
			return s
		}
		return ""
	}
	named := func(name string) string {
		var v struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(fields[name], &v) == nil {
			return v.Name
		}
		return ""
	}
	at := func(name string) time.Time {
		t, _ := parseJiraTime(str(name))
		return t
	}

	f.Summary = str("summary")
	if raw, ok := fields["status"]; ok {
		var st IssueStatus
		if json.Unmarshal(raw, &st) == nil {
			f.Status = st.Name
			f.StatusCategory = statusCategory(st.StatusCategory.Key)
		}
	}
	f.Type = named("issuetype")
	f.Priority = named("priority")
	if raw, ok := fields["assignee"]; ok {
		var u *User
		if json.Unmarshal(raw, &u) == nil && u != nil {
			f.Assignee, f.AssigneeID = u.DisplayName, u.AccountID
		}
	}
	if raw, ok := fields["labels"]; ok {
		f.Labels = []string{}
		var labels []string
		if json.Unmarshal(raw, &labels) == nil {
			f.Labels = append(f.Labels, labels...)
		}
	}
	if raw, ok := fields["components"]; ok {
		f.Components = []string{}
		var comps []struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &comps) == nil {
			for _, c := range comps {
				if c.Name != "" {
					f.Components = append(f.Components, c.Name)
				}
			}
		}
	}
	if raw, ok := fields["parent"]; ok {
		f.Parent = parseParent(raw)
	}
	f.Resolution = named("resolution")
	f.ResolvedAt = at("resolutiondate")
	f.CreatedAt = at("created")
	f.UpdatedAt = at("updated")

	var notes []string
	// odd notes a value of a shape ParseFacts cannot read. The note names
	// the cause, not the ticket, so the same cause on many tickets can be
	// reported once with a count.
	odd := func(name, id string, raw json.RawMessage) {
		notes = append(notes, fmt.Sprintf("Jira's %s field %s ([jira.fields] %s) holds an unrecognised %s value, so those tickets' %s are unknown",
			fieldLabel[name], id, name, jsonKind(raw), fieldLabel[name]))
	}
	// read returns a configured custom field's value. ok is false when there
	// is nothing to parse: no ID, or a document without the field. A field
	// is routinely missing from issue types outside its context (story
	// points on a bug), so that is unknown without a note.
	read := func(name string) (id string, raw json.RawMessage, ok bool) {
		id = ids.Get(name)
		if id == "" {
			return "", nil, false
		}
		raw, ok = fields[id]
		return id, raw, ok
	}

	if id, raw, ok := read(FieldStoryPoints); ok {
		if v, known := parseNumber(raw); known {
			f.StoryPoints, f.StoryPointsKnown = v, true
		} else {
			odd(FieldStoryPoints, id, raw)
		}
	}
	if id, raw, ok := read(FieldSprint); ok {
		if v, known := parseSprintField(raw); known {
			f.Sprints = v
		} else {
			odd(FieldSprint, id, raw)
		}
	}
	if id, raw, ok := read(FieldTeam); ok {
		if v, known := displayValue(raw); known {
			f.Team = v
		} else {
			odd(FieldTeam, id, raw)
		}
	}
	// The legacy epic link stands in only for a ticket without a parent.
	if f.Parent == nil {
		if id, raw, ok := read(FieldEpicLink); ok {
			if p, known := parseEpicLink(raw); known {
				f.Parent = p
			} else {
				odd(FieldEpicLink, id, raw)
			}
		}
	}
	return f, notes, nil
}

// statusCategory maps Jira's standard status category key onto the dash's
// vocabulary, or "" when it is not one of the three.
func statusCategory(key string) string {
	switch key {
	case "new":
		return "todo"
	case "indeterminate":
		return "in_progress"
	case "done":
		return "done"
	}
	return ""
}

// parseParent reads the parent field: {key, fields: {summary, issuetype}}.
func parseParent(raw json.RawMessage) *Parent {
	var p struct {
		Key    string `json:"key"`
		Fields struct {
			Summary   string `json:"summary"`
			IssueType struct {
				Name string `json:"name"`
			} `json:"issuetype"`
		} `json:"fields"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Key == "" {
		return nil
	}
	return &Parent{Key: p.Key, Summary: p.Fields.Summary, Type: p.Fields.IssueType.Name}
}

// parseEpicLink reads a legacy epic link: the epic's key as a string, or
// an object carrying one. null is no epic.
func parseEpicLink(raw json.RawMessage) (*Parent, bool) {
	if isNull(raw) {
		return nil, true
	}
	var key string
	if json.Unmarshal(raw, &key) == nil {
		if key = strings.TrimSpace(key); key == "" {
			return nil, true
		}
		return &Parent{Key: key}, true
	}
	var obj struct {
		Key     string `json:"key"`
		Summary string `json:"summary"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Key != "" {
		return &Parent{Key: obj.Key, Summary: obj.Summary}, true
	}
	return nil, false
}

// parseNumber reads a number field: a JSON number or a numeric string. null
// and "" are no value. known is false for any other shape.
func parseNumber(raw json.RawMessage) (v *float64, known bool) {
	if isNull(raw) {
		return nil, true
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return &n, true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s = strings.TrimSpace(s); s == "" {
			return nil, true
		}
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return &n, true
		}
	}
	return nil, false
}

// parseSprintField reads the sprint field: an array of sprint objects, of
// legacy "com.atlassian.greenhopper.service.sprint.Sprint@…[id=…,…]"
// strings, or a single sprint object. null is no sprint. known is false if
// any element is unreadable, so a partial list is never passed off as whole.
func parseSprintField(raw json.RawMessage) (out []SprintRef, known bool) {
	if isNull(raw) {
		return []SprintRef{}, true
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		if s, ok := parseSprintObject(raw); ok {
			return []SprintRef{s}, true
		}
		return nil, false
	}
	out = []SprintRef{}
	for _, el := range list {
		s, ok := parseSprintObject(el)
		if !ok {
			var legacy string
			if json.Unmarshal(el, &legacy) != nil {
				return nil, false
			}
			if s, ok = parseLegacySprint(legacy); !ok {
				return nil, false
			}
		}
		out = append(out, s)
	}
	return out, true
}

// parseSprintObject reads {"id": 1234, "name": "…", "state": "active"}.
func parseSprintObject(raw json.RawMessage) (SprintRef, bool) {
	var o struct {
		ID    *int   `json:"id"`
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &o) != nil || o.ID == nil || *o.ID <= 0 {
		return SprintRef{}, false
	}
	return SprintRef{ID: *o.ID, Name: o.Name, State: sprintState(o.State)}, true
}

// parseLegacySprint reads the sprint field's old string form:
// com.atlassian.greenhopper.service.sprint.Sprint@1a2b[id=1234,rapidViewId=42,state=ACTIVE,name=Sprint 12,…].
// A name may itself contain commas, so a comma ends a value only when the
// next text is another key=.
func parseLegacySprint(s string) (SprintRef, bool) {
	open, close := strings.IndexByte(s, '['), strings.LastIndexByte(s, ']')
	if open < 0 || close < open {
		return SprintRef{}, false
	}
	vals := map[string]string{}
	key := ""
	for _, part := range strings.Split(s[open+1:close], ",") {
		if k, v, ok := strings.Cut(part, "="); ok && isIdent(k) {
			key = k
			vals[key] = v
			continue
		}
		if key == "" {
			return SprintRef{}, false
		}
		vals[key] += "," + part
	}
	id, err := strconv.Atoi(vals["id"])
	if err != nil || id <= 0 {
		return SprintRef{}, false
	}
	name := vals["name"]
	if name == "<null>" {
		name = ""
	}
	return SprintRef{ID: id, Name: name, State: sprintState(vals["state"])}, true
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// sprintState lowercases a sprint state, or returns "" for one outside
// active, future and closed.
func sprintState(s string) string {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case "active", "future", "closed":
		return s
	}
	return ""
}

// displayValue reads a select-like field for display: a string, an object
// with a name, title, value or displayName (an option, a user, an Atlassian
// team), or an array of those, joined with ", ". null and [] are no value.
// known is false for a bare number (an ID without a name) or any other
// shape.
func displayValue(raw json.RawMessage) (string, bool) {
	if isNull(raw) {
		return "", true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s), true
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		for _, k := range []string{"name", "title", "value", "displayName"} {
			var v string
			if json.Unmarshal(obj[k], &v) == nil && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v), true
			}
		}
		return "", false
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		var names []string
		for _, el := range list {
			v, ok := displayValue(el)
			if !ok {
				return "", false
			}
			if v != "" {
				names = append(names, v)
			}
		}
		return strings.Join(names, ", "), true
	}
	return "", false
}

func isNull(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// jsonKind names the kind of a JSON value for a note.
func jsonKind(raw json.RawMessage) string {
	b := bytes.TrimSpace(raw)
	if len(b) == 0 {
		return "empty"
	}
	switch b[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	}
	return "number"
}
