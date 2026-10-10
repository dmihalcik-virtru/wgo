package dash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/discovery"
	"github.com/virtru/wgo/internal/effort"
	"github.com/virtru/wgo/internal/jj"
	"github.com/virtru/wgo/internal/store"
)

// fakeReader is a wsReader that needs no jj. Each call sleeps for delay while
// counted as in flight; the hooks override a call's result per workspace.
type fakeReader struct {
	delay             time.Duration
	inflight, maxSeen atomic.Int32

	log      func(repo, revset string) ([]jj.LogEntry, error)
	nearest  func(ws string) (string, error)
	countRev func(repo, revset string) (int, error)
}

func (f *fakeReader) enter() func() {
	n := f.inflight.Add(1)
	for {
		m := f.maxSeen.Load()
		if n <= m || f.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(f.delay)
	return func() { f.inflight.Add(-1) }
}

func (f *fakeReader) Log(repo, revset string) ([]jj.LogEntry, error) {
	defer f.enter()()
	if f.log != nil {
		return f.log(repo, revset)
	}
	return defaultLog(repo, revset), nil
}

func (f *fakeReader) NearestBookmark(ws string) (string, error) {
	defer f.enter()()
	if f.nearest != nil {
		return f.nearest(ws)
	}
	return "feature-" + filepath.Base(ws), nil
}

func (f *fakeReader) CountRevset(repo, revset string) (int, error) {
	defer f.enter()()
	if f.countRev != nil {
		return f.countRev(repo, revset)
	}
	return 1, nil
}

func defaultLog(repo, revset string) []jj.LogEntry {
	id := filepath.Base(repo)
	if revset == "@" {
		return []jj.LogEntry{{ChangeID: "wc-" + id, Empty: true, AuthorTimestamp: time.Unix(1_800_000_000, 0)}}
	}
	return []jj.LogEntry{{ChangeID: "ch-" + id, Description: "work in " + id, AuthorTimestamp: time.Unix(1_800_000_000, 0)}}
}

// readerCollector is a Collector whose jj reads all go through fr; c.jj is nil.
func readerCollector(t *testing.T, fr *fakeReader, src effort.Source, roots ...string) *Collector {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	now := time.Unix(1_800_000_000, 0)
	c := NewCollector(Config{
		Discover: func() ([]discovery.DiscoveredRepo, error) {
			var repos []discovery.DiscoveredRepo
			for _, r := range roots {
				repos = append(repos, discovery.DiscoveredRepo{Path: r, Name: filepath.Base(r)})
			}
			return repos, nil
		},
		Store: src,
		Now:   func() time.Time { return now },
	})
	c.reader = func(context.Context) wsReader { return fr }
	return c
}

func tempRoots(t *testing.T, n int) []string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var roots []string
	for i := range n {
		r := filepath.Join(base, fmt.Sprintf("ws%02d", i))
		if err := os.MkdirAll(r, 0o755); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, r)
	}
	return roots
}

func TestReaderWorkersBounded(t *testing.T) {
	fr := &fakeReader{delay: 20 * time.Millisecond}
	c := readerCollector(t, fr, memSource{}, tempRoots(t, 12)...)
	c.cfg.Workers = 3
	s, _, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(workspaces(s)); got != 12 {
		t.Fatalf("want 12 workspaces, got %d", got)
	}
	if m := fr.maxSeen.Load(); m > 3 || m < 2 {
		t.Fatalf("max in-flight reads = %d, want 2..3 for Workers=3", m)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestReaderErrorIsolation: a failure in any of the three reader calls marks
// only that workspace.
func TestReaderErrorIsolation(t *testing.T) {
	stages := []string{"log @", "nearest bookmark", "log window"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			roots := tempRoots(t, 4)
			victim := roots[2]
			boom := errors.New("jj exploded: " + stage)
			fr := &fakeReader{
				log: func(repo, revset string) ([]jj.LogEntry, error) {
					if repo == victim && ((stage == "log @" && revset == "@") || (stage == "log window" && revset != "@")) {
						return nil, boom
					}
					return defaultLog(repo, revset), nil
				},
				nearest: func(ws string) (string, error) {
					if ws == victim && stage == "nearest bookmark" {
						return "", boom
					}
					return "feature-" + filepath.Base(ws), nil
				},
			}
			c := readerCollector(t, fr, memSource{}, roots...)
			s, _, err := c.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range roots {
				wi := s.Node(WorkspaceID(w, w)).Workspace
				if w == victim {
					if !strings.Contains(wi.Error, "jj exploded") || len(wi.Changes) != 0 {
						t.Fatalf("victim: %+v", wi)
					}
				} else if wi.Error != "" || len(wi.Changes) != 1 {
					t.Fatalf("healthy workspace %s affected: %+v", w, wi)
				}
			}
			if st := s.Sources[SourceJJ]; st.State != Error || st.Error != 1 || st.Fresh != 3 {
				t.Fatalf("jj source: %+v", st)
			}
			if err := s.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReaderCountFailureMarksTruncationUnknown(t *testing.T) {
	roots := tempRoots(t, 3)
	unknown, known, small := roots[0], roots[1], roots[2]
	full := func(repo string) []jj.LogEntry {
		var out []jj.LogEntry
		for i := range ChangeWindow {
			out = append(out, jj.LogEntry{ChangeID: fmt.Sprintf("%s-%03d", filepath.Base(repo), i), Description: "d",
				AuthorTimestamp: time.Unix(1_800_000_000, 0)})
		}
		return out
	}
	fr := &fakeReader{
		log: func(repo, revset string) ([]jj.LogEntry, error) {
			if revset == "@" || repo == small {
				return defaultLog(repo, revset), nil
			}
			return full(repo), nil
		},
		countRev: func(repo, _ string) (int, error) {
			if repo == unknown {
				return 0, errors.New("revset timed out")
			}
			return ChangeWindow + 25, nil
		},
	}
	c := readerCollector(t, fr, memSource{}, roots...)
	s, _, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wi := func(root string) *WorkspaceInfo { return s.Node(WorkspaceID(root, root)).Workspace }
	if w := wi(unknown); !w.ChangesTruncatedUnknown || w.ChangesTruncated != 0 || w.Error != "" {
		t.Fatalf("unknown: %+v", w)
	}
	if w := wi(known); w.ChangesTruncatedUnknown || w.ChangesTruncated != 25 {
		t.Fatalf("known: %+v", w)
	}
	if w := wi(small); w.ChangesTruncatedUnknown || w.ChangesTruncated != 0 {
		t.Fatalf("small: %+v", w)
	}
	want := fmt.Sprintf("workspace %s: could not count changes beyond the newest %d: ", unknown, ChangeWindow)
	found := 0
	for _, d := range s.Diagnostics {
		if strings.HasPrefix(d, want) && strings.Contains(d, "revset timed out") {
			found++
		}
	}
	if found != 1 || len(s.Diagnostics) < 1 {
		t.Fatalf("diagnostics: %v", s.Diagnostics)
	}

	// Both the unknown and the counted truncation show in the delta summary.
	base := &Baseline{Schema: BaselineSchema, Workspaces: map[string]BaselineWorkspace{}}
	d := computeDelta(base, s)
	if d.Summary.Truncated != 2 {
		t.Fatalf("delta truncated = %d, want 2: %+v", d.Summary.Truncated, d.Workspaces)
	}
	var sawUnknown bool
	for _, w := range d.Workspaces {
		if w.ID == WorkspaceID(unknown, unknown) && w.TruncatedUnknown {
			sawUnknown = true
		}
	}
	if !sawUnknown {
		t.Fatalf("workspace delta lacks the unknown marker: %+v", d.Workspaces)
	}

	// The marker persists in the baseline and keeps counting while the
	// workspace fits again; an old baseline without the field still loads.
	b := baselineFrom(s, time.Now())
	if !b.Workspaces[WorkspaceID(unknown, unknown)].TruncatedUnknown {
		t.Fatal("baseline lost the unknown marker")
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var round Baseline
	if err := json.Unmarshal(raw, &round); err != nil || !round.Workspaces[WorkspaceID(unknown, unknown)].TruncatedUnknown {
		t.Fatalf("round trip: %v %+v", err, round)
	}
	var old Baseline
	if err := json.Unmarshal([]byte(`{"schema":1,"workspaces":{"ws-x":{"label":"x","changes":["a"],"truncated":3,"known":true}}}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Workspaces["ws-x"].TruncatedUnknown || old.Workspaces["ws-x"].Truncated != 3 {
		t.Fatalf("old baseline: %+v", old)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

// errStateSource fails LoadState and serves an empty plan.
type errStateSource struct{ memSource }

func (errStateSource) LoadState() (*store.State, error) {
	return nil, errors.New("state.json: permission denied")
}

func TestReaderLoadStateFailure(t *testing.T) {
	c := readerCollector(t, &fakeReader{}, errStateSource{}, tempRoots(t, 1)...)
	s, _, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := "load state (~/.wgo/state.json): state.json: permission denied; agent sessions, state-only efforts and annotations are not shown"
	found := false
	for _, d := range s.Diagnostics {
		found = found || d == want
	}
	if !found {
		t.Fatalf("diagnostics: %v", s.Diagnostics)
	}
	if st := s.Sources[SourcePlan]; st.State != Error || st.Detail != want {
		t.Fatalf("plan source: %+v", st)
	}
	if st := s.Sources[SourceAgents]; st.State != Error {
		t.Fatalf("agents source: %+v", st)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestReaderSymlinkDedup: one workspace reached through three spellings is
// one workspace with a stable ID.
func TestReaderSymlinkDedup(t *testing.T) {
	roots := tempRoots(t, 1)
	repo := roots[0]
	link := repo + "-link"
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	spellings := []string{repo, link, repo + string(filepath.Separator) + "."}
	var ids []string
	for _, order := range [][]string{spellings, {spellings[2], spellings[1], spellings[0]}} {
		c := readerCollector(t, &fakeReader{}, memSource{}, order...)
		s, _, err := c.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		wss := workspaces(s)
		if len(wss) != 1 {
			t.Fatalf("want one workspace, got %d: %v", len(wss), keys(wss))
		}
		for _, n := range s.Nodes {
			if n.Workspace != nil {
				ids = append(ids, n.ID)
			}
		}
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if ids[0] != ids[1] || ids[0] != WorkspaceID(repo, repo) {
		t.Fatalf("ids differ across discovery order: %v, want %s", ids, WorkspaceID(repo, repo))
	}
}
