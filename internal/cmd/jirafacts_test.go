package cmd

import (
	"errors"
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
// fields are read by name, the custom ones are unknown with a note, and
// the entry is marked partial so it is retried soon.
func TestFetchTicketFactsFallsBack(t *testing.T) {
	jiraHome(t, "")
	calls := stubIssueJSON(t, map[string]string{jira.FactFieldList: viewAll})
	info, err := fetchTicketFacts("TEAM-123")
	require.NoError(t, err)
	assert.Equal(t, 2, *calls)
	assert.Equal(t, "In Review", info.Status)
	assert.Nil(t, info.Sprints)
	assert.False(t, info.StoryPointsKnown)
	assert.True(t, info.Partial)
	require.Len(t, info.Notes, 1)
	assert.Contains(t, info.Notes[0], "acli refused to return every Jira field")
	assert.NotContains(t, info.Notes[0], "TEAM-123", "notes name the cause, not the ticket")

	// When both fail, both errors are returned.
	stubIssueJSON(t, map[string]string{})
	_, err = fetchTicketFacts("TEAM-123")
	assert.ErrorContains(t, err, "*all")

	// A retry that fails for another reason reports that reason first.
	old := getIssueJSON
	getIssueJSON = func(ticket, fields string) ([]byte, error) {
		if fields == jira.FactFields {
			return nil, errors.New("Error: field '*all' is not allowed")
		}
		return nil, errors.New("Error: unauthorized: use 'acli jira auth login' to authenticate")
	}
	t.Cleanup(func() { getIssueJSON = old })
	_, err = fetchTicketFacts("TEAM-123")
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "Error: unauthorized"), "got %q", err)
	assert.ErrorContains(t, err, "not allowed")
}

// TestFetchTicketFactsDoesNotFallBackOnOtherErrors: an expired login fails
// any field list, so it costs one acli call, not two.
func TestFetchTicketFactsDoesNotFallBackOnOtherErrors(t *testing.T) {
	jiraHome(t, "")
	calls := 0
	old := getIssueJSON
	getIssueJSON = func(ticket, fields string) ([]byte, error) {
		calls++
		return nil, errors.New("get issue TEAM-123: exit status 1\n✗ Error: unauthorized: use 'acli jira auth login' to authenticate")
	}
	t.Cleanup(func() { getIssueJSON = old })
	_, err := fetchTicketFacts("TEAM-123")
	assert.ErrorContains(t, err, "unauthorized")
	assert.Equal(t, 1, calls)
}

// TestRunRefreshJiraLoadsFieldsConfig: the statusline's background warmer
// writes the same entry wgo dash reads, so it must honour [jira.fields]
// even though nothing loaded config before it.
func TestRunRefreshJiraLoadsFieldsConfig(t *testing.T) {
	home := jiraHome(t, "") // config in memory has no [jira.fields]
	t.Setenv("PATH", t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(home, ".wgo", "config.toml"),
		[]byte("[jira.fields]\nstory_points = \"customfield_10016\"\nteam = \"customfield_10001\"\n"), 0o644))
	stubIssueJSON(t, map[string]string{jira.FactFields: viewAll})
	old := jiraFetcherFn
	jiraFetcherFn = newJiraFetcher
	t.Cleanup(func() { jiraFetcherFn = old })

	require.NoError(t, runRefreshJira("TEAM-123"))
	info, state := jiracache.Read("TEAM-123", time.Hour)
	require.Equal(t, jiracache.Fresh, state)
	require.NotNil(t, info.StoryPoints, "story points lost: [jira.fields] was not loaded")
	assert.Equal(t, 5.0, *info.StoryPoints)
	assert.Equal(t, "Platform", info.Team)
	assert.NotContains(t, strings.Join(info.Notes, "\n"), "set story_points", "no note asks for a key that is set")
}

// TestACLISiteHostIsAskedOnce: many tickets cost one site lookup per
// siteHostTTL, and none when jira.site is configured.
func TestACLISiteHostIsAskedOnce(t *testing.T) {
	jiraHome(t, "")
	calls := 0
	old := siteHostFn
	siteHostFn = func() (string, error) { calls++; return "acme.example", nil }
	t.Cleanup(func() { siteHostFn = old })
	siteHostMemo.Lock()
	siteHostMemo.at = time.Time{}
	siteHostMemo.Unlock()
	t.Cleanup(func() {
		siteHostMemo.Lock()
		siteHostMemo.at = time.Time{}
		siteHostMemo.Unlock()
	})
	stubIssueJSON(t, map[string]string{jira.FactFields: viewAll})
	for range 5 {
		info, err := jiraFetcher{}.FetchJira("TEAM-123")
		require.NoError(t, err)
		assert.Equal(t, "acme.example", info.Site)
	}
	assert.Equal(t, 1, calls)

	jiraHome(t, "[jira]\nsite = \"acme.example\"\n")
	siteHostMemo.Lock()
	siteHostMemo.at = time.Time{}
	siteHostMemo.Unlock()
	assert.Empty(t, acliSiteHost())
	assert.Equal(t, 1, calls, "a configured site needs no acli call")
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
