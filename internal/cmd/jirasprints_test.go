package cmd

import (
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
	listSprints = func(board int, state string, paginate bool) ([]jira.Sprint, error) {
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
	searchIssuesJSON = func(jql string, fields []string, limit int) ([]json.RawMessage, error) {
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
			return []json.RawMessage{json.RawMessage(`{"key":"TEAM-900","fields":{"summary":"Translate","issuetype":{"name":"Task"}}}`)}, nil
		}
		return nil, errors.New("search issues: exit status 1\nError: unauthorized")
	}

	got, err := sprintFetcher{}.FetchSprints(42, []string{"acct-bob", "currentUser()"})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"sprint = 1234 AND assignee in ('acct-bob',currentUser()) ORDER BY updated DESC",
		"sprint = 1240 AND assignee in ('acct-bob',currentUser()) ORDER BY updated DESC",
		"sprint = 1241 AND assignee in ('acct-bob',currentUser()) ORDER BY updated DESC",
	}, jqls, "only active sprints are searched")
	require.Len(t, got.Sprints, 4)

	s12 := got.Sprints[0]
	assert.Equal(t, "Ship token refresh", s12.Goal)
	assert.True(t, s12.Start.Equal(start))
	assert.True(t, s12.Truncated)
	assert.Len(t, s12.Tickets, jiracache.MaxSprintTickets)
	assert.Equal(t, "todo", s12.Tickets[0].Info.StatusCategory)

	w7 := got.Sprints[1]
	assert.False(t, w7.Truncated)
	require.Len(t, w7.Tickets, 1)
	assert.Equal(t, jiracache.SprintTicket{Key: "TEAM-900", Info: jiracache.Info{Summary: "Translate", Type: "Task"}}, w7.Tickets[0])

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
	listSprints = func(int, string, bool) ([]jira.Sprint, error) {
		return nil, errors.New("list sprints for board 42: exit status 1\n✗ Error: unauthorized: use 'acli jira auth login' to authenticate")
	}
	_, err := sprintFetcher{}.FetchSprints(42, []string{"currentUser()"})
	assert.EqualError(t, err, "list sprints for board 42: exit status 1: ✗ Error: unauthorized: use 'acli jira auth login' to authenticate")
}

func TestACLIErrorLine(t *testing.T) {
	assert.Equal(t, "a: b", acliErrorLine("a\n\n  b  \n"))
	assert.Len(t, []rune(acliErrorLine(strings.Repeat("x", 400))), 300)
}
