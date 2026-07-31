package sync

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
)

// --- fakes ---

type fakeJJ struct {
	entries []jj.LogEntry
	pushed  [][]string
}

func (f *fakeJJ) GitFetch(string, string, []string) error      { return nil }
func (f *fakeJJ) Log(string, string) ([]jj.LogEntry, error)    { return f.entries, nil }
func (f *fakeJJ) RemoteURLs(string) (map[string]string, error) { return nil, nil }
func (f *fakeJJ) GitPush(_ string, opts jj.PushOpts) (jj.PushResult, error) {
	f.pushed = append(f.pushed, opts.Bookmarks)
	return jj.PushResult{}, nil
}

type fakeGH struct {
	prs         map[string]*github.PRInfo // by head branch (open)
	bodies      map[int]string
	created     []github.CreatePROpts
	baseUpdates map[int]string
	bodyUpdates map[int]string
	nextNum     int
}

func newFakeGH() *fakeGH {
	return &fakeGH{
		prs:         map[string]*github.PRInfo{},
		bodies:      map[int]string{},
		baseUpdates: map[int]string{},
		bodyUpdates: map[int]string{},
		nextNum:     100,
	}
}

func (g *fakeGH) GetPRStatus(_, branch string) (*github.PRInfo, error) {
	return g.prs[branch], nil
}
func (g *fakeGH) UpdatePRBase(_ string, n int, base string) error {
	g.baseUpdates[n] = base
	return nil
}
func (g *fakeGH) GetPRBody(_ string, n int) (string, error) { return g.bodies[n], nil }
func (g *fakeGH) UpdatePRBody(_ string, n int, body string) error {
	g.bodyUpdates[n] = body
	g.bodies[n] = body
	return nil
}
func (g *fakeGH) CreatePR(_ string, opts github.CreatePROpts) (github.PRInfo, error) {
	g.created = append(g.created, opts)
	g.nextNum++
	pr := github.PRInfo{Number: g.nextNum, State: "open", Branch: opts.Head, BaseRefName: opts.Base, IsDraft: opts.Draft}
	g.prs[opts.Head] = &pr
	return pr, nil
}

type fakeLinker struct {
	available bool
	linked    [][]string
}

func (l *fakeLinker) Available(string) bool { return l.available }
func (l *fakeLinker) Link(_ string, branches []string) error {
	l.linked = append(l.linked, branches)
	return nil
}

// linearEntries returns jj log entries for trunk ← a ← b ← c (a/b/c bookmarked).
func linearEntries() []jj.LogEntry {
	return []jj.LogEntry{
		{ChangeID: "ca", Bookmarks: []string{"a"}, Parents: []string{"trunk"}},
		{ChangeID: "cb", Bookmarks: []string{"b"}, Parents: []string{"ca"}},
		{ChangeID: "cc", Bookmarks: []string{"c"}, Parents: []string{"cb"}},
	}
}

func openPR(num int, head, base string) *github.PRInfo {
	return &github.PRInfo{Number: num, State: "open", Branch: head, BaseRefName: base}
}

// --- tests ---

func TestSync_CreatePRs_BaseSelection(t *testing.T) {
	jjc := &fakeJJ{entries: linearEntries()}
	ghc := newFakeGH() // no existing PRs
	opts := Options{DefaultBase: "main", CreatePRs: true, GHStackMode: "off"}

	res, err := Sync(jjc, ghc, "/repo", opts)
	require.NoError(t, err)
	require.Len(t, ghc.created, 3)

	// Created bottom→top with the right bases.
	byHead := map[string]github.CreatePROpts{}
	for _, c := range ghc.created {
		byHead[c.Head] = c
		assert.True(t, c.Draft, "PRs are opened as drafts")
	}
	assert.Equal(t, "main", byHead["a"].Base)
	assert.Equal(t, "a", byHead["b"].Base)
	assert.Equal(t, "b", byHead["c"].Base)

	// Each new bookmark was pushed before opening its PR.
	var pushedAll []string
	for _, p := range jjc.pushed {
		pushedAll = append(pushedAll, p...)
	}
	assert.ElementsMatch(t, []string{"a", "b", "c"}, pushedAll)
	assert.Len(t, res.Created, 3)
}

func TestSync_CreatePRs_DryRunCreatesNothing(t *testing.T) {
	jjc := &fakeJJ{entries: linearEntries()}
	ghc := newFakeGH()
	opts := Options{DefaultBase: "main", CreatePRs: true, GHStackMode: "off", DryRun: true}

	res, err := Sync(jjc, ghc, "/repo", opts)
	require.NoError(t, err)
	assert.Empty(t, ghc.created, "dry-run creates no PRs")
	assert.Empty(t, jjc.pushed, "dry-run pushes nothing")
	assert.Len(t, res.Created, 3, "dry-run still reports what would be created")
}

func TestSync_MarkerPath_WhenGHStackOff(t *testing.T) {
	jjc := &fakeJJ{entries: linearEntries()}
	ghc := newFakeGH()
	ghc.prs = map[string]*github.PRInfo{
		"a": openPR(1, "a", "main"), "b": openPR(2, "b", "a"), "c": openPR(3, "c", "b"),
	}
	link := &fakeLinker{available: true} // available, but mode off
	opts := Options{DefaultBase: "main", GHStackMode: "off", Linker: link}

	res, err := Sync(jjc, ghc, "/repo", opts)
	require.NoError(t, err)
	assert.Empty(t, link.linked, "off mode never links")
	assert.NotEmpty(t, res.MarkerUpdates, "off mode writes markers")
}

func TestSync_NativePath_LinksAndSkipsMarkers(t *testing.T) {
	jjc := &fakeJJ{entries: linearEntries()}
	ghc := newFakeGH()
	ghc.prs = map[string]*github.PRInfo{
		"a": openPR(1, "a", "main"), "b": openPR(2, "b", "a"), "c": openPR(3, "c", "b"),
	}
	link := &fakeLinker{available: true}
	opts := Options{DefaultBase: "main", GHStackMode: "auto", Linker: link}

	res, err := Sync(jjc, ghc, "/repo", opts)
	require.NoError(t, err)
	require.Len(t, link.linked, 1)
	assert.Equal(t, []string{"a", "b", "c"}, link.linked[0], "linked bottom→top")
	assert.Empty(t, res.MarkerUpdates, "native path writes no markers")
	assert.Equal(t, []string{"a", "b", "c"}, res.Linked)
}

func TestSync_NativePath_StripsExistingMarker(t *testing.T) {
	jjc := &fakeJJ{entries: linearEntries()}
	ghc := newFakeGH()
	ghc.prs = map[string]*github.PRInfo{
		"a": openPR(1, "a", "main"), "b": openPR(2, "b", "a"), "c": openPR(3, "c", "b"),
	}
	// PR #2 has a pre-existing marker block that should be stripped on migration.
	marked := Marker{StackID: "b", Self: "b", Nodes: []MarkerNode{{Key: "b", Branch: "b"}}}.Render()
	ghc.bodies[2] = "Intro text.\n\n" + marked + "\n\nOutro."
	link := &fakeLinker{available: true}
	opts := Options{DefaultBase: "main", GHStackMode: "auto", Linker: link}

	res, err := Sync(jjc, ghc, "/repo", opts)
	require.NoError(t, err)
	require.Len(t, res.MarkerStrips, 1)
	assert.Equal(t, 2, res.MarkerStrips[0].PR)
	assert.NotContains(t, ghc.bodyUpdates[2], "wgo-stack", "marker removed from PR #2 body")
	assert.Contains(t, ghc.bodyUpdates[2], "Intro text.", "user text preserved")
	assert.Contains(t, ghc.bodyUpdates[2], "Outro.")
}

func TestSync_OnMode_ErrorsWhenUnavailable(t *testing.T) {
	jjc := &fakeJJ{entries: linearEntries()}
	ghc := newFakeGH()
	ghc.prs = map[string]*github.PRInfo{"a": openPR(1, "a", "main"), "b": openPR(2, "b", "a")}
	opts := Options{DefaultBase: "main", GHStackMode: "on", Linker: &fakeLinker{available: false}}

	_, err := Sync(jjc, ghc, "/repo", opts)
	require.ErrorIs(t, err, ErrGHStackUnavailable)
}

func TestSync_AutoFallsBackToMarkerWhenUnavailable(t *testing.T) {
	jjc := &fakeJJ{entries: linearEntries()}
	ghc := newFakeGH()
	ghc.prs = map[string]*github.PRInfo{"a": openPR(1, "a", "main"), "b": openPR(2, "b", "a")}
	link := &fakeLinker{available: false}
	opts := Options{DefaultBase: "main", GHStackMode: "auto", Linker: link}

	res, err := Sync(jjc, ghc, "/repo", opts)
	require.NoError(t, err)
	assert.Empty(t, link.linked)
	assert.NotEmpty(t, res.MarkerUpdates, "auto falls back to marker path when unavailable")
}

func TestSync_SinglePRSkipsLinking(t *testing.T) {
	jjc := &fakeJJ{entries: []jj.LogEntry{{ChangeID: "ca", Bookmarks: []string{"a"}, Parents: []string{"trunk"}}}}
	ghc := newFakeGH()
	ghc.prs = map[string]*github.PRInfo{"a": openPR(1, "a", "main")}
	link := &fakeLinker{available: true}
	opts := Options{DefaultBase: "main", GHStackMode: "auto", Linker: link}

	res, err := Sync(jjc, ghc, "/repo", opts)
	require.NoError(t, err)
	assert.Empty(t, link.linked, "a single-PR stack has no topology to link")
	assert.Empty(t, res.Linked)
	assert.Empty(t, res.MarkerUpdates, "native mode writes no markers even for a single PR")
}

func TestSync_InvalidMode(t *testing.T) {
	jjc := &fakeJJ{entries: linearEntries()}
	ghc := newFakeGH()
	_, err := Sync(jjc, ghc, "/repo", Options{GHStackMode: "bogus"})
	require.Error(t, err)
}
