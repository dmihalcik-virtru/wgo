package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/jira"
	"github.com/virtru/wgo/internal/jiracache"
)

// TestFetchSprints reads the active and future sprints, searches each active
// one for the assignees' tickets, bounds the search, and records a failed
// search on its sprint.
func TestFetchSprints(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir()) // jira.SiteHost finds no acli
	oldList, oldSearch := listSprints, searchIssuesJSON
	t.Cleanup(func() { listSprints, searchIssuesJSON = oldList, oldSearch })
	start := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	listSprints = func(_ context.Context, board int, state string, paginate bool) ([]jira.Sprint, error) {
		assert.Equal(t, 42, board)
		assert.Equal(t, "active,future", state)
		return []jira.Sprint{
			{ID: 1234, Name: "Sprint 12", Goal: "Ship token refresh", State: "active", StartDate: start, EndDate: start.AddDate(0, 0, 14)},
			{ID: 1240, Name: "Web 7", State: "active"},
			{ID: 1241, Name: "Web 8", State: "active"},
			{ID: 1235, Name: "Sprint 13", State: "future"},
		}, nil
	}
	var jqls []string
	searchIssuesJSON = func(_ context.Context, jql string, fields []string, limit int) ([]json.RawMessage, error) {
		jqls = append(jqls, jql)
		assert.Equal(t, jira.SearchFactFields, fields)
		assert.Equal(t, jiracache.MaxSprintTickets+1, limit)
		switch {
		case strings.HasPrefix(jql, "sprint = 1234 "):
			var out []json.RawMessage
			for i := range jiracache.MaxSprintTickets + 1 {
				out = append(out, json.RawMessage(fmt.Sprintf(`{"key":"TEAM-%d","fields":{"summary":"t","status":{"name":"To Do","statusCategory":{"key":"new"}},"labels":[]}}`, i+1)))
			}
			return out, nil
		case strings.HasPrefix(jql, "sprint = 1240 "):
			return []json.RawMessage{
				json.RawMessage(`{"key":"TEAM-900","fields":{"summary":"Translate","issuetype":{"name":"Task"}}}`),
				json.RawMessage(`{"key":"TEAM-901","fields":{"summary":"Nobody's","assignee":null}}`),
			}, nil
		}
		return nil, errors.New("search issues: exit status 1\nError: unauthorized")
	}

	got, err := sprintFetcher{}.FetchSprints(42, []string{"acct-bob", "currentUser()"})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"sprint = 1234 AND (assignee in ('acct-bob',currentUser()) OR assignee is EMPTY) ORDER BY updated DESC",
		"sprint = 1240 AND (assignee in ('acct-bob',currentUser()) OR assignee is EMPTY) ORDER BY updated DESC",
		"sprint = 1241 AND (assignee in ('acct-bob',currentUser()) OR assignee is EMPTY) ORDER BY updated DESC",
		// The failed search is retried without identities, which fails too,
		// so no assignee is searched alone.
		"sprint = 1241 AND assignee is EMPTY ORDER BY updated DESC",
	}, jqls, "only active sprints are searched")
	assert.Empty(t, got.Notes, "a search that fails for everyone is not blamed on an identity")
	require.Len(t, got.Sprints, 4)

	s12 := got.Sprints[0]
	assert.Equal(t, "Ship token refresh", s12.Goal)
	assert.True(t, s12.Start.Equal(start))
	assert.True(t, s12.Truncated)
	assert.Len(t, s12.Tickets, jiracache.MaxSprintTickets)
	assert.Equal(t, "todo", s12.Tickets[0].Info.StatusCategory)

	w7 := got.Sprints[1]
	assert.False(t, w7.Truncated)
	require.Len(t, w7.Tickets, 2)
	assert.Equal(t, jiracache.SprintTicket{Key: "TEAM-900", Info: jiracache.Info{Summary: "Translate", Type: "Task"}}, w7.Tickets[0])
	assert.Equal(t, "TEAM-901", w7.Tickets[1].Key, "an unassigned ticket is found")
	assert.Empty(t, w7.Tickets[1].Info.Assignee)

	w8 := got.Sprints[2]
	assert.Nil(t, w8.Tickets)
	assert.Equal(t, "search issues: exit status 1: Error: unauthorized", w8.Error, "acli's error on one line")

	s13 := got.Sprints[3]
	assert.Equal(t, "future", s13.State)
	assert.Nil(t, s13.Tickets, "future sprints are not searched")
}

func TestFetchSprintsListFailure(t *testing.T) {
	old := listSprints
	t.Cleanup(func() { listSprints = old })
	listSprints = func(context.Context, int, string, bool) ([]jira.Sprint, error) {
		return nil, errors.New("list sprints for board 42: exit status 1\n✗ Error: unauthorized: use 'acli jira auth login' to authenticate")
	}
	_, err := sprintFetcher{}.FetchSprints(42, []string{"currentUser()"})
	assert.EqualError(t, err, "list sprints for board 42: exit status 1: ✗ Error: unauthorized: use 'acli jira auth login' to authenticate")
}

func TestACLIErrorLine(t *testing.T) {
	assert.Equal(t, "a: b", acliErrorLine("a\n\n  b  \n"))
	assert.Len(t, []rune(acliErrorLine(strings.Repeat("x", 400))), 300)
}

// TestFetchSprintsSurvivesARejectedIdentity: Jira rejects a search naming
// an identity it does not know. The other assignees' tickets are still
// found, the bad identity is named with its config key, and later sprints
// leave it out.
func TestFetchSprintsSurvivesARejectedIdentity(t *testing.T) {
	jiraHome(t, "[pair]\nteammate = \"bob\"\nteammate_jira = \"acct-gone\"\n")
	t.Setenv("PATH", t.TempDir())
	oldList, oldSearch := listSprints, searchIssuesJSON
	t.Cleanup(func() { listSprints, searchIssuesJSON = oldList, oldSearch })
	listSprints = func(context.Context, int, string, bool) ([]jira.Sprint, error) {
		return []jira.Sprint{{ID: 1234, Name: "Sprint 12", State: "active"}, {ID: 1240, Name: "Web 7", State: "active"}}, nil
	}
	var jqls []string
	searchIssuesJSON = func(_ context.Context, jql string, fields []string, limit int) ([]json.RawMessage, error) {
		jqls = append(jqls, jql)
		if strings.Contains(jql, "acct-gone") {
			return nil, errors.New("search issues: exit status 1\nError: The value 'acct-gone' does not exist for the field 'assignee'.")
		}
		return []json.RawMessage{json.RawMessage(`{"key":"TEAM-1","fields":{"summary":"mine"}}`)}, nil
	}
	got, err := sprintFetcher{}.FetchSprints(42, []string{"acct-gone", "currentUser()"})
	require.NoError(t, err)
	require.Len(t, got.Sprints, 2)
	for _, s := range got.Sprints {
		assert.Empty(t, s.Error)
		require.Len(t, s.Tickets, 1, "sprint %d", s.ID)
		assert.Equal(t, "TEAM-1", s.Tickets[0].Key)
	}
	assert.Equal(t, []string{
		"sprint = 1234 AND (assignee in ('acct-gone',currentUser()) OR assignee is EMPTY) ORDER BY updated DESC",
		"sprint = 1234 AND assignee is EMPTY ORDER BY updated DESC",
		"sprint = 1234 AND assignee in ('acct-gone') ORDER BY updated DESC",
		"sprint = 1234 AND assignee in (currentUser()) ORDER BY updated DESC",
		"sprint = 1240 AND (assignee in (currentUser()) OR assignee is EMPTY) ORDER BY updated DESC",
	}, jqls, "the rejected identity is left out of later sprints")
	require.Len(t, got.Notes, 1)
	assert.Contains(t, got.Notes[0], `"acct-gone"`)
	assert.Contains(t, got.Notes[0], "[pair] teammate_jira")
	assert.Contains(t, got.Notes[0], "does not exist")
}

// TestFetchSprintsTransientFailureIsNotAnIdentity: a per-assignee retry that
// fails for a reason other than an unknown identity (here a rate limit)
// fails the sprint's search, so its earlier tickets are kept, rather than
// blaming the identity and dropping its tickets.
func TestFetchSprintsTransientFailureIsNotAnIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	oldList, oldSearch := listSprints, searchIssuesJSON
	t.Cleanup(func() { listSprints, searchIssuesJSON = oldList, oldSearch })
	listSprints = func(context.Context, int, string, bool) ([]jira.Sprint, error) {
		return []jira.Sprint{{ID: 1234, Name: "Sprint 12", State: "active"}, {ID: 1240, Name: "Web 7", State: "active"}}, nil
	}
	var jqls []string
	searchIssuesJSON = func(_ context.Context, jql string, fields []string, limit int) ([]json.RawMessage, error) {
		jqls = append(jqls, jql)
		if strings.Contains(jql, "acct-bob") && strings.HasPrefix(jql, "sprint = 1234 ") {
			return nil, errors.New("search issues: exit status 1\nError: 429 Too Many Requests")
		}
		return []json.RawMessage{json.RawMessage(`{"key":"TEAM-1","fields":{"summary":"mine"}}`)}, nil
	}
	got, err := sprintFetcher{}.FetchSprints(42, []string{"acct-bob", "currentUser()"})
	require.NoError(t, err)
	assert.Empty(t, got.Notes, "a rate limit is not blamed on an identity")
	require.Len(t, got.Sprints, 2)
	assert.Nil(t, got.Sprints[0].Tickets)
	assert.Contains(t, got.Sprints[0].Error, "429")
	assert.Equal(t, "sprint = 1240 AND (assignee in ('acct-bob',currentUser()) OR assignee is EMPTY) ORDER BY updated DESC", jqls[len(jqls)-1],
		"the identity is still searched in later sprints")
	assert.Len(t, got.Sprints[1].Tickets, 1)
}

// TestFetchSprintsNotesUnreadableResults: a search result that cannot be
// parsed is left out, and a note says the sprint's list is incomplete.
func TestFetchSprintsNotesUnreadableResults(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	oldList, oldSearch := listSprints, searchIssuesJSON
	t.Cleanup(func() { listSprints, searchIssuesJSON = oldList, oldSearch })
	listSprints = func(context.Context, int, string, bool) ([]jira.Sprint, error) {
		return []jira.Sprint{{ID: 1234, Name: "Sprint 12", State: "active"}}, nil
	}
	searchIssuesJSON = func(context.Context, string, []string, int) ([]json.RawMessage, error) {
		return []json.RawMessage{json.RawMessage(`{"key":"TEAM-1","fields":{}}`), json.RawMessage(`"not an issue"`)}, nil
	}
	got, err := sprintFetcher{}.FetchSprints(42, []string{"currentUser()"})
	require.NoError(t, err)
	assert.Len(t, got.Sprints[0].Tickets, 1)
	require.Len(t, got.Notes, 1)
	assert.Contains(t, got.Notes[0], "1 of Sprint 12's search results could not be read")
}

// TestFetchSprintsCutsPagedResults: paginated results beyond the bound are
// cut locally and the sprint marked truncated, however acli pages them.
func TestFetchSprintsCutsPagedResults(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	oldList, oldSearch := listSprints, searchIssuesJSON
	t.Cleanup(func() { listSprints, searchIssuesJSON = oldList, oldSearch })
	listSprints = func(context.Context, int, string, bool) ([]jira.Sprint, error) {
		return []jira.Sprint{{ID: 1234, Name: "Sprint 12", State: "active"}}, nil
	}
	searchIssuesJSON = func(context.Context, string, []string, int) ([]json.RawMessage, error) {
		var out []json.RawMessage
		for i := range 250 { // more than the limit asked for
			out = append(out, json.RawMessage(fmt.Sprintf(`{"key":"TEAM-%d","fields":{}}`, i+1)))
		}
		return out, nil
	}
	got, err := sprintFetcher{}.FetchSprints(42, []string{"currentUser()"})
	require.NoError(t, err)
	assert.Len(t, got.Sprints[0].Tickets, jiracache.MaxSprintTickets)
	assert.True(t, got.Sprints[0].Truncated)
}

// TestSprintSearchCutShortRejectsNobody: once the refresh's context is done,
// a failed search (acli killed) is no verdict on an identity: nobody is
// reported as rejected or left out, and the board fails with the context's
// error rather than returning sprints whose searches failed.
func TestSprintSearchCutShortRejectsNobody(t *testing.T) {
	jiraHome(t, "")
	t.Setenv("PATH", t.TempDir())
	oldList, oldSearch := listSprints, searchIssuesJSON
	t.Cleanup(func() { listSprints, searchIssuesJSON = oldList, oldSearch })
	ctx, cancel := context.WithCancel(context.Background())
	listSprints = func(context.Context, int, string, bool) ([]jira.Sprint, error) {
		return []jira.Sprint{{ID: 1234, Name: "Sprint 12", State: "active"}}, nil
	}
	searches := 0
	searchIssuesJSON = func(_ context.Context, jql string, _ []string, _ int) ([]json.RawMessage, error) {
		searches++
		if searches == 2 {
			cancel() // the deadline passes during the per-person retry
		}
		return nil, errors.New("search issues: signal: killed")
	}
	rejected := map[string]string{}
	_, _, _, msg := sprintTickets(ctx, 1234, []string{"acct-1", "acct-2", "currentUser()"}, rejected)
	assert.Empty(t, rejected, "no identity is blamed for a killed search")
	assert.Equal(t, "search issues: signal: killed", msg)

	got, err := sprintFetcher{}.FetchSprintsContext(ctx, 42, []string{"acct-1", "currentUser()"})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, got.Sprints)
}
