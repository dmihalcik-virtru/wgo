package jira

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The testdata documents follow the shape acli prints for `workitem view
// --json` and `workitem search --json` (Jira's REST issue JSON, unchanged),
// with every value invented. See spec/gh-98.md's delivery notes for what
// could and could not be checked against a live site.

// siteIDs are the custom field IDs of the invented site in testdata.
var siteIDs = FieldIDs{StoryPoints: "customfield_10016", Sprint: "customfield_10020", Team: "customfield_10001", EpicLink: "customfield_10014"}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return data
}

func ptr(f float64) *float64 { return &f }

// TestParseFactsViewAll reads every field in the spec's table from a full
// `workitem view --fields '*all'` document.
func TestParseFactsViewAll(t *testing.T) {
	f, notes, err := ParseFacts(readTestdata(t, "view_all.json"), siteIDs)
	require.NoError(t, err)
	assert.Empty(t, notes)
	want := Facts{
		Key:              "TEAM-123",
		Summary:          "Refresh API tokens before they expire",
		Status:           "In Review",
		StatusCategory:   "in_progress",
		Type:             "Story",
		Priority:         "High",
		Assignee:         "Alice",
		AssigneeID:       "acct-alice",
		Sprints:          []SprintRef{{ID: 1230, Name: "Sprint 11", State: "closed"}, {ID: 1234, Name: "Sprint 12", State: "active"}},
		StoryPoints:      ptr(5),
		StoryPointsKnown: true,
		Parent:           &Parent{Key: "TEAM-100", Summary: "Token lifecycle", Type: "Epic"},
		Team:             "Platform",
		Components:       []string{"api", "web"},
		Labels:           []string{"backend", "tokens"},
		CreatedAt:        time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
		UpdatedAt:        time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC),
	}
	assert.Equal(t, want.CreatedAt.Unix(), f.CreatedAt.Unix())
	assert.Equal(t, want.UpdatedAt.Unix(), f.UpdatedAt.Unix())
	f.CreatedAt, f.UpdatedAt = want.CreatedAt, want.UpdatedAt
	assert.Equal(t, want, f)
}

// TestParseFactsResolved reads a resolved ticket's resolution and date.
func TestParseFactsResolved(t *testing.T) {
	raw := `{"key":"TEAM-9","fields":{"resolution":{"id":"10000","name":"Done"},"resolutiondate":"2026-10-28T10:00:00.000+0000",
		"status":{"name":"Closed","statusCategory":{"key":"done"}}}}`
	f, _, err := ParseFacts([]byte(raw), FieldIDs{})
	require.NoError(t, err)
	assert.Equal(t, "Done", f.Resolution)
	assert.Equal(t, time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC).Unix(), f.ResolvedAt.Unix())
	assert.Equal(t, "done", f.StatusCategory)
}

// TestParseFactsSearchResults reads the fields `workitem search` allows. The
// fields it was not asked for stay unknown: nil lists, story points not
// known, and no note, since no custom field was requested.
func TestParseFactsSearchResults(t *testing.T) {
	var list []json.RawMessage
	require.NoError(t, json.Unmarshal(readTestdata(t, "search_sprint.json"), &list))
	require.Len(t, list, 2)

	f, notes, err := ParseFacts(list[0], FieldIDs{})
	require.NoError(t, err)
	assert.Empty(t, notes)
	assert.Equal(t, "TEAM-124", f.Key)
	assert.Equal(t, "todo", f.StatusCategory)
	assert.Equal(t, "Task", f.Type)
	assert.Equal(t, "acct-bob", f.AssigneeID)
	assert.NotNil(t, f.Labels, "a reported empty label list is none, not unknown")
	assert.Empty(t, f.Labels)
	assert.Nil(t, f.Components, "components were not requested: unknown")
	assert.Nil(t, f.Sprints)
	assert.False(t, f.StoryPointsKnown)
	assert.Nil(t, f.Parent)

	f, _, err = ParseFacts(list[1], FieldIDs{})
	require.NoError(t, err)
	assert.Equal(t, "done", f.StatusCategory)
	assert.Empty(t, f.Priority, "null priority")
	assert.Empty(t, f.Assignee, "unassigned")
	assert.Equal(t, []string{"backend"}, f.Labels)
}

// TestParseFactsAbsentCustomFields: configured custom fields that the
// document lacks stay unknown, and a note names each field and its key.
func TestParseFactsAbsentCustomFields(t *testing.T) {
	raw := `{"key":"TEAM-7","fields":{"summary":"x","labels":[]}}`
	f, notes, err := ParseFacts([]byte(raw), siteIDs)
	require.NoError(t, err)
	assert.Nil(t, f.StoryPoints)
	assert.False(t, f.StoryPointsKnown, "an absent story points field is unknown, not unestimated")
	assert.Nil(t, f.Sprints)
	assert.Empty(t, f.Team)
	assert.Nil(t, f.Parent)
	require.Len(t, notes, 4)
	for _, key := range []string{"[jira.fields] story_points", "[jira.fields] sprint", "[jira.fields] team", "[jira.fields] epic_link"} {
		assert.True(t, strings.Contains(strings.Join(notes, "\n"), key), "no note names %s: %v", key, notes)
	}
	// The note is the same for every ticket, so it is reported once.
	_, again, _ := ParseFacts([]byte(strings.Replace(raw, "TEAM-7", "TEAM-8", 1)), siteIDs)
	assert.Equal(t, notes, again)
}

// TestParseFactsCustomFieldShapes covers the shapes custom fields take
// across sites, and that anything unrecognised is unknown with a note
// naming the field's [jira.fields] key.
func TestParseFactsCustomFieldShapes(t *testing.T) {
	type check func(t *testing.T, f Facts)
	for _, tc := range []struct {
		name, field, value string
		ids                FieldIDs
		odd                string // the [jira.fields] key a note must name; "" for none
		check              check
	}{
		{"points number", "customfield_10016", `3`, siteIDs, "", func(t *testing.T, f Facts) {
			assert.Equal(t, ptr(3), f.StoryPoints)
			assert.True(t, f.StoryPointsKnown)
		}},
		{"points fraction", "customfield_10016", `0.5`, siteIDs, "", func(t *testing.T, f Facts) { assert.Equal(t, ptr(0.5), f.StoryPoints) }},
		{"points string", "customfield_10016", `" 8 "`, siteIDs, "", func(t *testing.T, f Facts) { assert.Equal(t, ptr(8), f.StoryPoints) }},
		{"points null is unestimated", "customfield_10016", `null`, siteIDs, "", func(t *testing.T, f Facts) {
			assert.Nil(t, f.StoryPoints)
			assert.True(t, f.StoryPointsKnown, "null is known: unestimated")
		}},
		{"points empty string is unestimated", "customfield_10016", `""`, siteIDs, "", func(t *testing.T, f Facts) {
			assert.Nil(t, f.StoryPoints)
			assert.True(t, f.StoryPointsKnown)
		}},
		{"points object", "customfield_10016", `{"value":3}`, siteIDs, "story_points", func(t *testing.T, f Facts) {
			assert.Nil(t, f.StoryPoints)
			assert.False(t, f.StoryPointsKnown)
		}},
		{"points array", "customfield_10016", `[3]`, siteIDs, "story_points", func(t *testing.T, f Facts) { assert.False(t, f.StoryPointsKnown) }},
		{"points word", "customfield_10016", `"large"`, siteIDs, "story_points", func(t *testing.T, f Facts) { assert.False(t, f.StoryPointsKnown) }},
		{"points field unmapped", "customfield_10016", `3`, FieldIDs{}, "", func(t *testing.T, f Facts) {
			assert.Nil(t, f.StoryPoints)
			assert.False(t, f.StoryPointsKnown, "without a field ID story points are unknown")
		}},

		{"sprint legacy strings", "customfield_10020",
			`["com.atlassian.greenhopper.service.sprint.Sprint@1a2b[id=1230,rapidViewId=42,state=CLOSED,name=Sprint 11, the long one,goal=<null>,startDate=2026-10-06T08:00:00.000Z,endDate=2026-10-20T08:00:00.000Z,completeDate=2026-10-20T09:00:00.000Z,sequence=1230]",
			  "com.atlassian.greenhopper.service.sprint.Sprint@3c4d[id=1234,rapidViewId=42,state=ACTIVE,name=Sprint 12,goal=Ship it,startDate=2026-10-20T08:00:00.000Z,endDate=2026-11-03T08:00:00.000Z,completeDate=<null>,sequence=1234]"]`,
			siteIDs, "", func(t *testing.T, f Facts) {
				assert.Equal(t, []SprintRef{{ID: 1230, Name: "Sprint 11, the long one", State: "closed"}, {ID: 1234, Name: "Sprint 12", State: "active"}}, f.Sprints)
			}},
		{"sprint single object", "customfield_10020", `{"id":1235,"name":"Sprint 13","state":"FUTURE"}`, siteIDs, "", func(t *testing.T, f Facts) {
			assert.Equal(t, []SprintRef{{ID: 1235, Name: "Sprint 13", State: "future"}}, f.Sprints)
		}},
		{"sprint null is none", "customfield_10020", `null`, siteIDs, "", func(t *testing.T, f Facts) {
			assert.NotNil(t, f.Sprints)
			assert.Empty(t, f.Sprints)
		}},
		{"sprint odd state", "customfield_10020", `[{"id":1236,"name":"Sprint 14","state":"paused"}]`, siteIDs, "", func(t *testing.T, f Facts) {
			assert.Equal(t, []SprintRef{{ID: 1236, Name: "Sprint 14"}}, f.Sprints, "an unknown state is left empty")
		}},
		{"sprint string", "customfield_10020", `"Sprint 12"`, siteIDs, "sprint", func(t *testing.T, f Facts) { assert.Nil(t, f.Sprints) }},
		{"sprint without id", "customfield_10020", `[{"name":"Sprint 12"}]`, siteIDs, "sprint", func(t *testing.T, f Facts) { assert.Nil(t, f.Sprints) }},
		{"sprint partly unreadable", "customfield_10020", `[{"id":1234,"name":"Sprint 12","state":"active"}, 7]`, siteIDs, "sprint", func(t *testing.T, f Facts) {
			assert.Nil(t, f.Sprints, "a partial list is never passed off as whole")
		}},

		{"team option", "customfield_10001", `{"self":"x","value":"Web","id":"10100"}`, siteIDs, "", func(t *testing.T, f Facts) { assert.Equal(t, "Web", f.Team) }},
		{"team atlassian team", "customfield_10001", `{"id":"0a1b","name":"Platform","title":"Platform"}`, siteIDs, "", func(t *testing.T, f Facts) { assert.Equal(t, "Platform", f.Team) }},
		{"team title only", "customfield_10001", `{"id":"0a1b","title":"Platform"}`, siteIDs, "", func(t *testing.T, f Facts) { assert.Equal(t, "Platform", f.Team) }},
		{"team string", "customfield_10001", `"Web"`, siteIDs, "", func(t *testing.T, f Facts) { assert.Equal(t, "Web", f.Team) }},
		{"team options array", "customfield_10001", `[{"value":"Web"},{"value":"API"}]`, siteIDs, "", func(t *testing.T, f Facts) { assert.Equal(t, "Web, API", f.Team) }},
		{"team empty array", "customfield_10001", `[]`, siteIDs, "", func(t *testing.T, f Facts) { assert.Empty(t, f.Team) }},
		{"team null", "customfield_10001", `null`, siteIDs, "", func(t *testing.T, f Facts) { assert.Empty(t, f.Team) }},
		{"team bare id", "customfield_10001", `17`, siteIDs, "team", func(t *testing.T, f Facts) { assert.Empty(t, f.Team) }},
		{"team object without a name", "customfield_10001", `{"id":"0a1b"}`, siteIDs, "team", func(t *testing.T, f Facts) { assert.Empty(t, f.Team) }},

		{"epic link key", "customfield_10014", `"TEAM-100"`, siteIDs, "", func(t *testing.T, f Facts) {
			assert.Equal(t, &Parent{Key: "TEAM-100"}, f.Parent)
		}},
		{"epic link object", "customfield_10014", `{"key":"TEAM-100","summary":"Token lifecycle"}`, siteIDs, "", func(t *testing.T, f Facts) {
			assert.Equal(t, &Parent{Key: "TEAM-100", Summary: "Token lifecycle"}, f.Parent)
		}},
		{"epic link null", "customfield_10014", `null`, siteIDs, "", func(t *testing.T, f Facts) { assert.Nil(t, f.Parent) }},
		{"epic link number", "customfield_10014", `10100`, siteIDs, "epic_link", func(t *testing.T, f Facts) { assert.Nil(t, f.Parent) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Every other configured custom field is present as null, so
			// only the field under test can produce a note.
			fields := map[string]json.RawMessage{"summary": json.RawMessage(`"x"`)}
			for _, name := range FieldNames {
				if id := tc.ids.Get(name); id != "" {
					fields[id] = json.RawMessage(`null`)
				}
			}
			fields[tc.field] = json.RawMessage(tc.value)
			raw, err := json.Marshal(map[string]any{"key": "TEAM-5", "fields": fields})
			require.NoError(t, err)
			f, notes, err := ParseFacts(raw, tc.ids)
			require.NoError(t, err)
			if tc.odd == "" {
				assert.Empty(t, notes)
			} else {
				require.Len(t, notes, 1)
				assert.Contains(t, notes[0], "TEAM-5")
				assert.Contains(t, notes[0], "[jira.fields] "+tc.odd)
				assert.Contains(t, notes[0], tc.field)
				assert.Contains(t, notes[0], "unknown")
			}
			tc.check(t, f)
		})
	}
}

// TestParseFactsParentWinsOverEpicLink: next-gen parent and the legacy epic
// link both map to parent; a ticket with a parent keeps it.
func TestParseFactsParentWinsOverEpicLink(t *testing.T) {
	raw := `{"key":"TEAM-6","fields":{"parent":{"key":"TEAM-200","fields":{"summary":"Sub-task parent","issuetype":{"name":"Story"}}},"customfield_10014":"TEAM-100"}}`
	f, notes, err := ParseFacts([]byte(raw), FieldIDs{EpicLink: "customfield_10014"})
	require.NoError(t, err)
	assert.Empty(t, notes)
	assert.Equal(t, &Parent{Key: "TEAM-200", Summary: "Sub-task parent", Type: "Story"}, f.Parent)
}

func TestParseFactsErrors(t *testing.T) {
	_, _, err := ParseFacts([]byte(`{not json`), siteIDs)
	assert.Error(t, err)
	_, _, err = ParseFacts([]byte(`{"fields":{}}`), siteIDs)
	assert.ErrorContains(t, err, "no key")
}

// TestDiscoverFieldIDsBySprintShape: acli's view has no names map, so only
// the sprint field is found, by its value; other number and option fields
// are never guessed at.
func TestDiscoverFieldIDsBySprintShape(t *testing.T) {
	ids, err := DiscoverFieldIDs(readTestdata(t, "view_all.json"))
	require.NoError(t, err)
	assert.Equal(t, FieldIDs{Sprint: "customfield_10020"}, ids)
}

// TestDiscoverFieldIDsByName matches Jira's field names when the document
// carries the names map.
func TestDiscoverFieldIDsByName(t *testing.T) {
	raw := `{"key":"TEAM-1","fields":{},"names":{
		"customfield_10016":"Story point estimate","customfield_10028":"Story Points",
		"customfield_10020":"Sprint","customfield_10001":"Team","customfield_10014":"Epic Link","customfield_10500":"Team size"}}`
	ids, err := DiscoverFieldIDs([]byte(raw))
	require.NoError(t, err)
	assert.Equal(t, FieldIDs{StoryPoints: "customfield_10028", Sprint: "customfield_10020", Team: "customfield_10001", EpicLink: "customfield_10014"}, ids)

	raw = `{"key":"TEAM-1","fields":{},"names":{"customfield_10016":"Story point estimate"}}`
	ids, err = DiscoverFieldIDs([]byte(raw))
	require.NoError(t, err)
	assert.Equal(t, "customfield_10016", ids.StoryPoints, "a team-managed site's estimate field")
}

// TestDiscoverFieldIDsAmbiguousSprint finds nothing when two fields look
// like sprints, rather than pick one.
func TestDiscoverFieldIDsAmbiguousSprint(t *testing.T) {
	raw := `{"key":"TEAM-1","fields":{
		"customfield_10020":[{"id":1234,"name":"Sprint 12","state":"active"}],
		"customfield_10021":[{"id":1234,"name":"Sprint 12","state":"active"}]}}`
	ids, err := DiscoverFieldIDs([]byte(raw))
	require.NoError(t, err)
	assert.Empty(t, ids.Sprint)

	raw = `{"key":"TEAM-1","fields":{"customfield_10020":null,"customfield_10500":[{"id":3,"value":"x"}]}}`
	ids, err = DiscoverFieldIDs([]byte(raw))
	require.NoError(t, err)
	assert.Empty(t, ids.Sprint, "a ticket in no sprint shows nothing to discover")
}

func TestSprintTicketsJQL(t *testing.T) {
	got := SprintTicketsJQL(1234, []string{"currentUser()", "acct-bob", "o'brien@example.com"})
	assert.Equal(t, `sprint = 1234 AND assignee in (currentUser(),'acct-bob','o\'brien@example.com') ORDER BY updated DESC`, got)
}

// fakeACLI puts an acli on PATH that records its arguments, one per line,
// and prints out.
func fakeACLI(t *testing.T, out string) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	outFile := filepath.Join(dir, "out")
	require.NoError(t, os.WriteFile(outFile, []byte(out), 0o644))
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\ncat '" + outFile + "'\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "acli"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func readArgs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func TestGetIssueJSONAsksForEveryField(t *testing.T) {
	args := fakeACLI(t, string(readTestdata(t, "view_all.json")))
	raw, err := GetIssueJSON("TEAM-123", FactFields)
	require.NoError(t, err)
	assert.Equal(t, []string{"jira", "workitem", "view", "TEAM-123", "--json", "--fields", "*all"}, readArgs(t, args))
	f, _, err := ParseFacts(raw, siteIDs)
	require.NoError(t, err)
	assert.Equal(t, "TEAM-123", f.Key)
}

func TestSearchIssuesJSONBoundsTheSearch(t *testing.T) {
	args := fakeACLI(t, string(readTestdata(t, "search_sprint.json")))
	list, err := SearchIssuesJSON("sprint = 1234", SearchFactFields, 201)
	require.NoError(t, err)
	assert.Len(t, list, 2)
	assert.Equal(t, []string{"jira", "workitem", "search", "--jql", "sprint = 1234", "--json", "--limit", "201",
		"--fields", "key,summary,status,issuetype,priority,assignee,labels"}, readArgs(t, args))
}
