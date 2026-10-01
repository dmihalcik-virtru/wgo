package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	mine      = "https://github.com/o/r/pull/1"
	stacked   = "https://github.com/o/r/pull/2"
	theirs    = "https://github.com/o/r/pull/9"
	releasePR = "https://github.com/o/r/pull/50"
)

// writeRun lays down a minimal run directory exercising every edge kind.
func writeRun(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "2026-test")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cards"), 0o755))
	files := map[string]string{
		"coverage.json": `{"me":"dana"}`,
		"people.json": `{"my_teams":["o/devs"],
			"teams":{"alice":["o/devs"],"bob":["x/other"],"gemini-code-assist":[]},
			"counterparts":{"alice":{"reviewed_my_prs":2},"bob":{"built_on_my_prs":1},"carol":{"discussed":3}}}`,
		"cards/c.json": `{"items":[{"id":"T-1","urls":["` + mine + `"],"outcome":"shipped-with-rework","complexity":"H"}]}`,
		"ledger.jsonl": strings.Join([]string{
			`{"kind":"pr_authored","url":"` + mine + `","repo":"o/r","number":1,"title":"one","state":"merged","created":"2026-02-03T00:00:00Z","tickets":["T-1"],"additions":10,"deletions":5,"outcome":"shipped","complexity":"L","reviewers":{"alice":{"APPROVED":1,"COMMENTED":2},"gemini-code-assist":{"COMMENTED":1}},"downstream_others":["bob"],"cross_refs":[{"url":"` + stacked + `"},{"url":"` + releasePR + `"}]}`,
			`{"kind":"pr_authored","url":"` + stacked + `","repo":"o/r","number":2,"title":"two","state":"open","created":"2026-03-01T00:00:00Z","tickets":[],"outcome":"in-flight","complexity":"M"}`,
			`{"kind":"pr_reviewed","url":"` + theirs + `","repo":"o/r","number":9,"title":"nine","author":"alice","state":"merged","created":"2026-01-01T00:00:00Z","my_review_states":{"APPROVED":1,"COMMENTED":2}}`,
			`{"kind":"jira","key":"T-1","summary":"the ticket","url":"https://j.example.net/browse/T-1","status":"Done","parent":"E-1","story_points":3,"created":"2026-01-15T00:00:00Z"}`,
			``,
		}, "\n"),
	}
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	return dir
}

type edgeKey struct{ src, dst, kind string }

func edges(g *Graph) map[edgeKey]int {
	m := map[edgeKey]int{}
	for _, e := range g.Edges {
		m[edgeKey{e.Source, e.Target, e.Kind}] = e.Weight
	}
	return m
}

func node(t *testing.T, g *Graph, id string) Node {
	t.Helper()
	for _, n := range g.Nodes {
		if n.ID == id {
			return n
		}
	}
	require.Failf(t, "missing node", "%s", id)
	return Node{}
}

func TestBuildEdges(t *testing.T) {
	run, err := Load(writeRun(t))
	require.NoError(t, err)
	g := Build(run)
	e := edges(g)

	assert.Equal(t, "dana", g.Me)
	assert.Equal(t, 1, e[edgeKey{"person:dana", "pr:" + mine, EdgeAuthored}])
	assert.Equal(t, 3, e[edgeKey{"person:alice", "pr:" + mine, EdgeReviewed}], "weight is total review events")
	assert.Equal(t, 1, e[edgeKey{"person:bob", "pr:" + mine, EdgeBuiltOn}])
	assert.Equal(t, 3, e[edgeKey{"person:dana", "pr:" + theirs, EdgeIReviewed}])
	assert.Equal(t, 1, e[edgeKey{"person:alice", "pr:" + theirs, EdgeWrote}])
	assert.Equal(t, 1, e[edgeKey{"pr:" + mine, "ticket:T-1", EdgeTicket}])
	assert.Equal(t, 1, e[edgeKey{"ticket:T-1", "ticket:E-1", EdgeParent}])
	assert.Equal(t, 3, e[edgeKey{"person:carol", "person:dana", EdgeDiscussed}], "discussion-only collaborators come from people.json")

	// A cross-ref to a PR outside the ledger adds no node and no edge.
	assert.Equal(t, 1, e[edgeKey{"pr:" + stacked, "pr:" + mine, EdgeRefs}])
	for _, n := range g.Nodes {
		assert.NotEqual(t, "pr:"+releasePR, n.ID)
	}
}

func TestBuildNodeAttributes(t *testing.T) {
	run, err := Load(writeRun(t))
	require.NoError(t, err)
	g := Build(run)

	pr := node(t, g, "pr:"+mine)
	assert.Equal(t, "shipped-with-rework", pr.Outcome, "card judgement overrides the ledger")
	assert.Equal(t, "H", pr.Complexity)
	assert.Equal(t, 15, pr.Churn)
	assert.Equal(t, "2026-02", pr.Month)
	assert.False(t, pr.Ticketless)
	assert.True(t, node(t, g, "pr:"+stacked).Ticketless)
	assert.False(t, node(t, g, "pr:"+theirs).Mine)

	assert.True(t, node(t, g, "person:dana").Me)
	assert.True(t, node(t, g, "person:gemini-code-assist").Bot)
	assert.False(t, node(t, g, "person:alice").Outside, "alice shares o/devs")
	assert.True(t, node(t, g, "person:bob").Outside)
	assert.False(t, node(t, g, "person:gemini-code-assist").Outside, "bots are never outside-team collaborators")

	tk := node(t, g, "ticket:T-1")
	assert.Equal(t, "Done", tk.State)
	require.NotNil(t, tk.Points)
	assert.InDelta(t, 3.0, *tk.Points, 0)

	// E-1 is only named as a parent (no Jira row); its link is derived from the host seen on T-1.
	assert.Equal(t, "https://j.example.net/browse/E-1", node(t, g, "ticket:E-1").URL)

	assert.Equal(t, map[string]int{"has ticket": 1, "none": 1}, g.Facets["ticket"])
}

func TestBuildIsDeterministic(t *testing.T) {
	run, err := Load(writeRun(t))
	require.NoError(t, err)
	a, b := Build(run), Build(run)
	assert.Equal(t, a, b)
}

func TestLoadRequiresLedger(t *testing.T) {
	_, err := Load(t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "year-in-review run directory")
}

func TestLoadToleratesMissingOptionalFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ledger.jsonl"), []byte(`{"kind":"pr_authored","url":"u","repo":"o/r","number":1}`+"\n"), 0o644))
	run, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "me", run.Me)
	assert.Len(t, Build(run).Nodes, 3, "me, the PR and its repo; no tickets or people")
}

func TestResolve(t *testing.T) {
	wgo := t.TempDir()
	runs := RunsDir(wgo)
	require.NoError(t, os.MkdirAll(filepath.Join(runs, "old"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(runs, "new"), 0o755))
	require.NoError(t, os.Chtimes(filepath.Join(runs, "old"), mtime(-2), mtime(-2)))

	got, err := Resolve(wgo, "")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(runs, "new"), got, "default is the most recent run")

	got, err = Resolve(wgo, "old")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(runs, "old"), got)

	got, err = Resolve(wgo, runs)
	require.NoError(t, err)
	assert.Equal(t, runs, got, "an existing path wins")

	_, err = Resolve(wgo, "nope")
	require.ErrorContains(t, err, `no run "nope"`)

	_, err = Resolve(t.TempDir(), "")
	require.ErrorContains(t, err, "run the /year-in-review skill")
}

func TestIsBot(t *testing.T) {
	for _, l := range []string{"gemini-code-assist", "dependabot[bot]", "opentdf-automation", "copilot-swe-agent", "virtru-internal"} {
		assert.True(t, IsBot(l), l)
	}
	for _, l := range []string{"alice", "c-r33d", "strantalis"} {
		assert.False(t, IsBot(l), l)
	}
}

func mtime(days int) time.Time { return time.Now().AddDate(0, 0, days) }
