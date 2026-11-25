package jira

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFactsTwgFlattened(t *testing.T) {
	raw := flattenFixture(t, readTestdata(t, "view_all.json"))
	f, notes, err := ParseFacts(raw, siteIDs)
	require.NoError(t, err)
	assert.Empty(t, notes)
	assert.Equal(t, "TEAM-123", f.Key)
	assert.Equal(t, "Refresh API tokens before they expire", f.Summary)
	assert.Equal(t, "Token lifecycle", f.Parent.Summary)
	require.NotNil(t, f.StoryPoints)
	assert.Equal(t, 5.0, *f.StoryPoints)
}

func TestDiscoverSprintFieldFromFlattenedFallback(t *testing.T) {
	raw := flattenFixture(t, readTestdata(t, "view_all.json"))
	ids, err := DiscoverFieldIDs(raw)
	require.NoError(t, err)
	assert.Equal(t, "customfield_10020", ids.Sprint)
	assert.Empty(t, ids.StoryPoints)
}

func TestDiscoverFieldIDsFromTwgMetadata(t *testing.T) {
	fields := map[string]any{"fields": []map[string]string{
		{"id": "customfield_10016", "name": "Story Points"},
		{"id": "customfield_10020", "name": "Sprint"},
		{"id": "customfield_10001", "name": "Team Assignment"},
		{"id": "customfield_10014", "name": "Epic Link"},
		{"id": "summary", "name": "Summary"},
	}}
	args := fakeTwg(t, v2(t, fields))
	ids, err := DiscoverFieldIDsContext(context.Background(), "TEAM-123")
	require.NoError(t, err)
	assert.Equal(t, siteIDs, ids)
	assert.Equal(t, []string{"jira", "workitem", "field", "update-metadata", "--id", "TEAM-123", "-o", "json", "--output-summary", "none"}, readArgs(t, args))
}

func TestBulkGetIssuesKeepsPerItemErrors(t *testing.T) {
	data := map[string]any{"items": []map[string]any{
		{"input": "TEAM-1", "ok": true, "data": map[string]any{"key": "TEAM-1", "summary": "One", "url": "https://jira.example/browse/TEAM-1"}},
		{"input": "TEAM-2", "ok": false, "error": map[string]string{"code": "ISSUE_NOT_FOUND", "message": "not found"}},
	}}
	args := fakeTwg(t, v2(t, data))
	items, err := BulkGetIssuesJSONContext(context.Background(), []string{"TEAM-1", "TEAM-2"}, "*all")
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.JSONEq(t, `{"key":"TEAM-1","summary":"One","url":"https://jira.example/browse/TEAM-1"}`, string(items[0].Data))
	assert.ErrorContains(t, items[1].Err, "ISSUE_NOT_FOUND")
	assert.Equal(t, []string{"jira", "workitem", "bulk-get", "TEAM-1", "TEAM-2", "--full", "-o", "json", "--output-summary", "none"}, readArgs(t, args))
}

func TestListSprintsPaginatesEachState(t *testing.T) {
	dir := t.TempDir()
	active0 := filepath.Join(dir, "active0")
	active1 := filepath.Join(dir, "active1")
	future := filepath.Join(dir, "future")
	require.NoError(t, os.WriteFile(active0, v2(t, map[string]any{"sprints": []any{map[string]any{"id": 1, "name": "A", "state": "active"}}, "total": 2, "isLast": false}), 0o644))
	require.NoError(t, os.WriteFile(active1, v2(t, map[string]any{"sprints": []any{map[string]any{"id": 2, "name": "B", "state": "active"}}, "total": 2, "isLast": true}), 0o644))
	require.NoError(t, os.WriteFile(future, v2(t, map[string]any{"sprints": []any{map[string]any{"id": 3, "name": "C", "state": "future"}}, "total": 1, "isLast": true}), 0o644))
	withFakeTwg(t, "case \"$*\" in *'--state future'*) /bin/cat '"+future+"' ;; *'--start-at 1'*) /bin/cat '"+active1+"' ;; *) /bin/cat '"+active0+"' ;; esac")
	sprints, err := ListSprintsContext(context.Background(), 42, "active,future", true)
	require.NoError(t, err)
	require.Len(t, sprints, 3)
	assert.Equal(t, []int{1, 2, 3}, []int{sprints[0].ID, sprints[1].ID, sprints[2].ID})
}

func TestTwgCommentWatcherAndMutations(t *testing.T) {
	args := fakeTwg(t, v2(t, []map[string]any{{"id": "9", "author": map[string]string{"displayName": "Alice"}, "body": "Hi"}}))
	comments, err := GetCommentsContext(context.Background(), "TEAM-1", 10)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "Hi", comments[0].Body)
	assert.Equal(t, []string{"jira", "workitem", "comment", "query", "--issue-id", "TEAM-1", "--first", "10", "--order-by", "+created", "-o", "json", "--output-summary", "none"}, readArgs(t, args))

	fakeTwg(t, v2(t, map[string]any{"watchers": []map[string]string{{"accountId": "a", "displayName": "Alice"}}}))
	watchers, err := GetWatchersContext(context.Background(), "TEAM-1")
	require.NoError(t, err)
	assert.Equal(t, "a", watchers[0].AccountID)

	args = fakeTwg(t, v2(t, map[string]string{"key": "TEAM-7"}))
	key, err := CreateIssueContext(context.Background(), "TEAM", "New", "Task", "Body")
	require.NoError(t, err)
	assert.Equal(t, "TEAM-7", key)
	assert.Equal(t, []string{"jira", "workitem", "create", "--space", "TEAM", "--summary", "New", "--type", "Task", "--yes", "--description", "Body", "--description-format", "markdown", "-o", "json", "--output-summary", "none"}, readArgs(t, args))

	args = fakeTwg(t, v2(t, map[string]string{"key": "TEAM-7"}))
	require.NoError(t, UpdateIssueContext(context.Background(), "TEAM-7", "Changed", "Body"))
	assert.True(t, strings.Contains(strings.Join(readArgs(t, args), " "), "jira workitem update --id TEAM-7 --summary Changed --description Body --description-format markdown"))

	args = fakeTwg(t, v2(t, map[string]string{"key": "TEAM-7"}))
	require.NoError(t, TransitionIssueContext(context.Background(), "TEAM-7", "Done"))
	assert.Equal(t, []string{"jira", "workitem", "transition", "--id", "TEAM-7", "--transition-id", "Done", "-o", "json", "--output-summary", "none"}, readArgs(t, args))
}

func TestTwgStructuredAuthFailureOnBulkGet(t *testing.T) {
	withFakeTwg(t, `echo '{"apiVersion":"v2","ok":false,"error":{"code":"AUTH_REQUIRED","kind":"authentication","message":"login required"}}'; exit 1`)
	_, err := BulkGetIssuesJSONContext(context.Background(), []string{"TEAM-1"}, "*all")
	require.ErrorIs(t, err, ErrNotLoggedIn)
}

func TestTwgSearchRejectsRepeatedCursor(t *testing.T) {
	data := map[string]any{"issues": []any{map[string]any{"key": "TEAM-1"}}, "pageInfo": map[string]any{"hasNextPage": true, "nextCursor": "repeat"}}
	fakeTwg(t, v2(t, data))
	_, err := SearchIssuesJSONContext(context.Background(), "project = TEAM", []string{"key"}, 3)
	require.ErrorContains(t, err, "repeated or empty pagination cursor")
}

func TestTwgRejectsMalformedEnvelope(t *testing.T) {
	withFakeTwg(t, `echo '{"apiVersion":"v2","command":"jira.workitem.get"}'`)
	_, err := GetIssueJSONContext(context.Background(), "TEAM-1", "summary")
	require.ErrorContains(t, err, "response has no data")
	withFakeTwg(t, `echo 'not json'`)
	_, err = GetIssueJSONContext(context.Background(), "TEAM-1", "summary")
	require.ErrorContains(t, err, "invalid JSON envelope")
}
