package jira

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSprintsSingleDocument(t *testing.T) {
	raw := `{"sprints":[
		{"id":7677,"name":"PQ 🗝️ 26s6","goal":"ML-KEM","state":"closed",
		 "startDate":"2026-06-05T14:49:34.525Z","endDate":"2026-06-24T16:00:00.000Z"}
	]}`
	sprints, err := parseSprints(raw)
	require.NoError(t, err)
	require.Len(t, sprints, 1)
	assert.Equal(t, 7677, sprints[0].ID)
	assert.Equal(t, "PQ 🗝️ 26s6", sprints[0].Name)
	assert.Equal(t, "closed", sprints[0].State)
	assert.False(t, sprints[0].EndDate.IsZero(), "end date should parse")
	assert.Equal(t, 2026, sprints[0].EndDate.Year())
}

// acli --paginate emits one JSON object per page, concatenated. parseSprints must
// accumulate across all of them.
func TestParseSprintsPaginatedMultiDocument(t *testing.T) {
	raw := `{"sprints":[{"id":1,"name":"A","endDate":"2025-01-01T00:00:00.000Z"}]}
{"sprints":[{"id":2,"name":"B","endDate":"2026-06-24T16:00:00.000Z"}]}
{"sprints":[{"id":3,"name":"C","endDate":"2026-06-30T00:00:00.000Z"}]}`
	sprints, err := parseSprints(raw)
	require.NoError(t, err)
	require.Len(t, sprints, 3)
	ids := []int{sprints[0].ID, sprints[1].ID, sprints[2].ID}
	assert.Equal(t, []int{1, 2, 3}, ids)
}

func TestInFlightJQL(t *testing.T) {
	got := InFlightJQL(7677, "", []string{"In Progress", "In Review", "In QA"})
	want := "sprint = 7677 AND assignee = currentUser() AND status in ('In Progress','In Review','In QA')"
	assert.Equal(t, want, got)
}

func TestInFlightJQLWithAssignee(t *testing.T) {
	got := InFlightJQL(7677, "dmihalcik@virtru.com", []string{"In Progress"})
	want := "sprint = 7677 AND assignee = 'dmihalcik@virtru.com' AND status in ('In Progress')"
	assert.Equal(t, want, got)
}

func TestAllInFlightJQL(t *testing.T) {
	got := AllInFlightJQL("", []string{"In Progress", "In Review"})
	want := "assignee = currentUser() AND status in ('In Progress','In Review')"
	assert.Equal(t, want, got)
}

func TestQuoteJQLEscapesQuotes(t *testing.T) {
	got := InFlightJQL(1, "o'brien", []string{"In Progress"})
	assert.Contains(t, got, `assignee = 'o\'brien'`)
}
