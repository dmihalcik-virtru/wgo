package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/jira"
	"github.com/virtru/wgo/internal/jiracache"
)

// viewAll is acli's `workitem view --fields '*all' --json` for an invented
// ticket: story points in customfield_10016, sprints in customfield_10020,
// the team in customfield_10001, and another number field, customfield_10500.
const viewAll = `{"key":"TEAM-123","fields":{
	"summary":"Refresh API tokens","status":{"name":"In Review","statusCategory":{"key":"indeterminate"}},
	"issuetype":{"name":"Story"},"priority":{"name":"High"},"assignee":{"accountId":"acct-alice","displayName":"Alice"},
	"labels":[],"components":[{"name":"api"}],"parent":{"key":"TEAM-100","fields":{"summary":"Token lifecycle","issuetype":{"name":"Epic"}}},
	"resolution":null,"resolutiondate":null,"created":"2026-10-01T09:00:00.000+0000","updated":"2026-10-29T14:00:00.000+0000",
	"customfield_10001":{"id":"0a1b","name":"Platform"},"customfield_10016":5,"customfield_10500":42,
	"customfield_10020":[{"id":1234,"name":"Sprint 12","state":"active","boardId":42}]}}`

// jiraHome points HOME at a temp dir with configTOML as its config, loads
// it, and restores the default config when the test ends.
func jiraHome(t *testing.T, configTOML string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeConfig := func(content string) {
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".wgo"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(home, ".wgo", "config.toml"), []byte(content), 0o644))
		require.NoError(t, config.Init())
	}
	writeConfig(configTOML)
	t.Cleanup(func() { writeConfig("") })
	return home
}

// stubIssueJSON replaces the acli view seam with canned output per --fields
// value, counting calls.
func stubIssueJSON(t *testing.T, byFields map[string]string) *int {
	t.Helper()
	calls := 0
	old := getIssueJSON
	getIssueJSON = func(ticket, fields string) ([]byte, error) {
		calls++
		out, ok := byFields[fields]
		if !ok {
			return nil, fmt.Errorf("get issue %s: exit status 1\nError: field %q is not allowed", ticket, fields)
		}
		return []byte(out), nil
	}
	t.Cleanup(func() { getIssueJSON = old })
	return &calls
}

// TestFetchTicketFactsDiscoversAndCachesFields: with no [jira.fields], the
// sprint field is discovered and cached for reuse; story points and team
// stay unknown, each with a note naming its key.
func TestFetchTicketFactsDiscoversAndCachesFields(t *testing.T) {
	home := jiraHome(t, "")
	stubIssueJSON(t, map[string]string{jira.FactFields: viewAll})

	info, err := fetchTicketFacts("TEAM-123")
	require.NoError(t, err)
	assert.Equal(t, "In Review", info.Status, "the statusline's status is unchanged")
	assert.Equal(t, "Alice", info.Assignee)
	assert.Equal(t, "Refresh API tokens", info.Summary)
	assert.Equal(t, []jiracache.SprintRef{{ID: 1234, Name: "Sprint 12", State: "active"}}, info.Sprints)
	assert.Nil(t, info.StoryPoints)
	assert.False(t, info.StoryPointsKnown, "never guessed from another number field")
	assert.Empty(t, info.Team)
	joined := strings.Join(info.Notes, "\n")
	assert.Contains(t, joined, "set story_points in [jira.fields]")
	assert.Contains(t, joined, "set team in [jira.fields]")
	assert.NotContains(t, joined, "set sprint in", "the sprint field was discovered")
	assert.NotContains(t, joined, "epic_link", "Jira Cloud reports epics as parent")

	f, ok := jiracache.ReadFields(jiracache.FieldsTTL)
	require.True(t, ok, "discovery is cached")
	assert.Equal(t, jiracache.Fields{Sprint: "customfield_10020"}, f)
	_, err = os.Stat(filepath.Join(home, ".wgo", "cache", "jira", "fields.json"))
	require.NoError(t, err)

	// The cached ID is reused for a ticket in no sprint, whose value shows
	// nothing to discover: its sprints are known to be none.
	noSprint := strings.Replace(viewAll, `[{"id":1234,"name":"Sprint 12","state":"active","boardId":42}]`, `null`, 1)
	stubIssueJSON(t, map[string]string{jira.FactFields: noSprint})
	info, err = fetchTicketFacts("TEAM-123")
	require.NoError(t, err)
	assert.NotNil(t, info.Sprints)
	assert.Empty(t, info.Sprints)
}

// TestFetchTicketFactsConfiguredFields: [jira.fields] overrides discovery,
// reads story points and team, and "none" silences a field's note.
func TestFetchTicketFactsConfiguredFields(t *testing.T) {
	jiraHome(t, `[jira.fields]
story_points = "customfield_10016"
sprint = "customfield_10500"
team = "customfield_10001"
epic_link = "none"
`)
	// A stale discovery that disagrees with the config.
	require.NoError(t, jiracache.WriteFields(jiracache.Fields{Sprint: "customfield_10020", StoryPoints: "customfield_10500"}))
	stubIssueJSON(t, map[string]string{jira.FactFields: viewAll})

	info, err := fetchTicketFacts("TEAM-123")
	require.NoError(t, err)
	require.NotNil(t, info.StoryPoints)
	assert.Equal(t, 5.0, *info.StoryPoints)
	assert.True(t, info.StoryPointsKnown)
	assert.Equal(t, "Platform", info.Team)
	assert.Nil(t, info.Sprints, "the configured sprint field wins, and holds a number")
	require.Len(t, info.Notes, 1)
	assert.Contains(t, info.Notes[0], "[jira.fields] sprint")
	assert.Contains(t, info.Notes[0], "customfield_10500")

	f, _ := jiracache.ReadFields(jiracache.FieldsTTL)
	assert.Equal(t, "customfield_10020", f.Sprint, "with every field configured, discovery does not run")
}

// TestFetchTicketFactsUnestimated: a mapped story points field that is null
// is known: unestimated, not unknown.
func TestFetchTicketFactsUnestimated(t *testing.T) {
	jiraHome(t, "[jira.fields]\nstory_points = \"customfield_10016\"\nteam = \"none\"\n")
	stubIssueJSON(t, map[string]string{jira.FactFields: strings.Replace(viewAll, `"customfield_10016":5`, `"customfield_10016":null`, 1)})
	info, err := fetchTicketFacts("TEAM-123")
	require.NoError(t, err)
	assert.Nil(t, info.StoryPoints)
	assert.True(t, info.StoryPointsKnown)
	assert.Empty(t, info.Notes)
}

func TestFetchTicketFactsIgnoresInvalidFieldIDs(t *testing.T) {
	jiraHome(t, "[jira.fields]\nstory_points = \"Story Points\"\n")
	stubIssueJSON(t, map[string]string{jira.FactFields: viewAll})
	info, err := fetchTicketFacts("TEAM-123")
	require.NoError(t, err)
	assert.False(t, info.StoryPointsKnown)
	assert.Contains(t, strings.Join(info.Notes, "\n"), `ignoring [jira.fields] story_points = "Story Points"`)
}

// TestFetchTicketFactsFallsBack: should acli refuse '*all', the system
// fields are read by name and the custom ones are noted as unknown.
func TestFetchTicketFactsFallsBack(t *testing.T) {
	jiraHome(t, "")
	calls := stubIssueJSON(t, map[string]string{jira.FactFieldList: viewAll})
	info, err := fetchTicketFacts("TEAM-123")
	require.NoError(t, err)
	assert.Equal(t, 2, *calls)
	assert.Equal(t, "In Review", info.Status)
	assert.Nil(t, info.Sprints)
	assert.False(t, info.StoryPointsKnown)
	require.Len(t, info.Notes, 1)
	assert.Contains(t, info.Notes[0], "acli could not return every field")

	// When both fail, the first error is returned.
	stubIssueJSON(t, map[string]string{})
	_, err = fetchTicketFacts("TEAM-123")
	assert.ErrorContains(t, err, "*all")
}

// TestStatuslineJiraReadGainsNoCall: the statusline's Jira lookup is still
// one cache read, for an entry written before the facts and for one
// carrying them. Neither fresh nor stale nor missing entries make a
// synchronous acli call.
func TestStatuslineJiraReadGainsNoCall(t *testing.T) {
	home := jiraHome(t, "")
	dir := filepath.Join(home, ".wgo", "cache", "jira")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	old := fmt.Sprintf(`{"info":{"status":"In Review","assignee":"Alice"},"fetched_at":%q}`, time.Now().UTC().Format(time.RFC3339))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "TEAM-1.json"), []byte(old), 0o644))
	five := 5.0
	require.NoError(t, jiracache.Write("TEAM-2", jiracache.Info{Status: "Done", Assignee: "Bob", Summary: "x", StatusCategory: "done",
		Sprints: []jiracache.SprintRef{{ID: 1234}}, StoryPoints: &five, StoryPointsKnown: true, Labels: []string{}, Notes: []string{"n"}}))

	f := &stubJiraFetcher{info: jiracache.Info{Status: "fetched"}}
	installJiraFetcher(t, f)
	views := stubIssueJSON(t, map[string]string{}) // counts any acli view
	for ticket, want := range map[string]string{"TEAM-1": "In Review", "TEAM-2": "Done", "TEAM-3": ""} {
		status, _, _ := resolveJiraStatus(ticket, contextOptions{LocalOnly: true})
		assert.Equal(t, want, status, ticket)
	}
	assert.Equal(t, 0, f.count(), "the statusline hot path made a synchronous fetch")
	assert.Zero(t, *views, "the statusline hot path ran acli")
	_, ok := jiracache.ReadFields(jiracache.FieldsTTL)
	assert.False(t, ok, "field discovery never runs on the hot path")
}
