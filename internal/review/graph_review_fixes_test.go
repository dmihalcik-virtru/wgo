package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildBotReviewersGetEdges(t *testing.T) {
	g := Build(&Run{Me: "dana", Rows: []Row{
		{Kind: "pr_authored", URL: mine, Repo: "o/r", Number: 1,
			Reviewers:    map[string]map[string]int{"gemini-code-assist": {"COMMENTED": 2}},
			BotReviewers: []string{"coderabbitai", "gemini-code-assist"}},
	}})
	e := edges(g)
	assert.Equal(t, 1, e[edgeKey{"person:coderabbitai", "pr:" + mine, EdgeReviewed}])
	assert.Equal(t, 2, e[edgeKey{"person:gemini-code-assist", "pr:" + mine, EdgeReviewed}], "not counted twice")
	assert.True(t, node(t, g, "person:coderabbitai").Bot)
}

func TestBuildSkippedKinds(t *testing.T) {
	g := Build(&Run{Me: "dana", Rows: []Row{
		{Kind: "pr_commented", URL: theirs}, {Kind: "pr_commented", URL: stacked}, {Kind: "issue_filed"},
	}})
	assert.Equal(t, map[string]int{"pr_commented": 2, "issue_filed": 1}, g.Skipped)
}

func TestBuildCardsMergePerField(t *testing.T) {
	g := Build(&Run{Me: "dana",
		Rows: []Row{{Kind: "pr_authored", URL: mine, Repo: "o/r", Outcome: "ledger", Complexity: "L"}},
		Cards: []CardItem{
			{URLs: []string{mine}, Outcome: "first", Complexity: "M"},
			{URLs: []string{mine}, Complexity: "H"},
		}})
	pr := node(t, g, "pr:"+mine)
	assert.Equal(t, "first", pr.Outcome, "a later blank field keeps the earlier card's value")
	assert.Equal(t, "H", pr.Complexity)
}

func TestBuildTeamsUnknown(t *testing.T) {
	build := func(p People) *Graph {
		return Build(&Run{Me: "dana", People: p})
	}
	counterparts := map[string]Counterpart{"alice": {Discussed: 1}, "bob": {Discussed: 1}, "carol": {Discussed: 1}}
	teams := map[string][]string{"alice": {"o/devs"}, "bob": {"x/other"}}

	g := build(People{Counterparts: counterparts, Teams: teams, MyTeams: []string{"o/devs"}})
	assert.Equal(t, map[string]int{"my teams": 1, "outside my teams": 1, "teams unknown": 1}, g.Facets["person"])
	assert.False(t, node(t, g, "person:carol").TeamsKnown)
	assert.True(t, node(t, g, "person:bob").TeamsKnown)

	g = build(People{Counterparts: counterparts, Teams: teams})
	assert.Equal(t, map[string]int{"teams unknown": 3}, g.Facets["person"], "no my_teams: nobody is provably outside")
	assert.False(t, node(t, g, "person:bob").Outside)

	g = build(People{Counterparts: counterparts})
	assert.Equal(t, map[string]int{"teams unknown": 3}, g.Facets["person"], "no people.json teams")
}

func TestLoadCoverageWithoutMe(t *testing.T) {
	dir := writeRun(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "coverage.json"), []byte(`{}`), 0o644))
	run, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "me", run.Me)
	assert.Equal(t, []string{"the user's login in coverage.json"}, run.Missing)
}

func TestLoadCardsErrors(t *testing.T) {
	dir := writeRun(t)

	// cards that is a file, not a directory, is an error, not "no cards".
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "cards")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cards"), nil, 0o644))
	_, err := Load(dir)
	require.ErrorContains(t, err, "list cards")

	// Non-json files and subdirectories are ignored; cards load in name order.
	require.NoError(t, os.Remove(filepath.Join(dir, "cards")))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cards", "sub.json"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cards", "notes.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cards", "b.json"), []byte(`{"items":[{"id":"b"}]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cards", "a.json"), []byte(`{"items":[{"id":"a"}]}`), 0o644))
	run, err := Load(dir)
	require.NoError(t, err)
	require.Len(t, run.Cards, 2)
	assert.Equal(t, "a", run.Cards[0].ID)
	assert.Empty(t, run.Missing)

	require.NoError(t, os.RemoveAll(filepath.Join(dir, "cards")))
	run, err = Load(dir)
	require.NoError(t, err)
	assert.True(t, strings.Contains(strings.Join(run.Missing, ","), "cards/"))
}
