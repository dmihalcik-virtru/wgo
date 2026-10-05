package dash

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/virtru/wgo/internal/review"
)

// maxRunLabel bounds a review run label.
const maxRunLabel = 128

// ValidRunLabel reports whether label can name a review run directory: a
// single plain path element. It rejects empty and over-long labels,
// separators, "..", NUL and other control characters, and a leading dot
// (which also covers "." and hidden directories). The dashboard only ever
// serves runs it found itself under the runs directory, so this is a second
// line of defence, not the only one.
func ValidRunLabel(label string) bool {
	if label == "" || len(label) > maxRunLabel || strings.HasPrefix(label, ".") || strings.Contains(label, "..") {
		return false
	}
	for _, r := range label {
		if r == '/' || r == '\\' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// reviewRun is one loaded run with its pre-rendered page and lookup maps.
type reviewRun struct {
	label string
	mod   time.Time
	page  *page
	json  []byte // the graph, encoded exactly as `wgo review graph --json`
	// node IDs by exact PR URL and by ticket key / ticket URL
	prByURL, ticketByKey, ticketByURL map[string]string
}

type reviewState struct {
	runs    []*reviewRun // newest first
	byLabel map[string]*reviewRun
	errs    []string
	version uint64
}

// ReviewIndex holds the year-in-review runs the dashboard can cross-link to
// and serve. Scan reads the runs directory (in the background loop); the
// HTTP handlers only read what the last scan published.
type ReviewIndex struct {
	dir   string
	state atomic.Pointer[reviewState]
	mu    sync.Mutex // serializes Scan

	linksMu  sync.Mutex
	linksKey [2]uint64
	links    []byte
}

// NewReviewIndex indexes runs under runsDir (review.RunsDir(wgoDir)).
func NewReviewIndex(runsDir string) *ReviewIndex {
	ix := &ReviewIndex{dir: runsDir}
	ix.state.Store(&reviewState{byLabel: map[string]*reviewRun{}})
	return ix
}

// runMod is the newest modification time among a run's inputs, so a rewrite
// of the ledger or the cards is noticed even when the directory's own mtime
// does not change.
func runMod(dir string, fi os.FileInfo) time.Time {
	mod := fi.ModTime()
	for _, name := range []string{"ledger.jsonl", "people.json", "coverage.json", "cards"} {
		if st, err := os.Lstat(filepath.Join(dir, name)); err == nil && st.ModTime().After(mod) {
			mod = st.ModTime()
		}
	}
	return mod
}

// Scan reloads changed runs. Only real directories (not symlinks) with a
// valid label directly under the runs directory are considered. A missing
// runs directory is not an error: there is just nothing to link to.
//
// Every failure is recorded for Errors (and so shown on the page): an
// unreadable runs directory keeps the previously loaded runs and is also
// returned; a run that cannot be read or loaded is skipped.
func (ix *ReviewIndex) Scan() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	old := ix.state.Load()
	entries, err := os.ReadDir(ix.dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		err = fmt.Errorf("review runs in %s: %w", ix.dir, err)
		next := *old
		next.errs = []string{err.Error()}
		if !slices.Equal(next.errs, old.errs) {
			next.version++
		}
		ix.state.Store(&next)
		return err
	}
	next := &reviewState{byLabel: map[string]*reviewRun{}, version: old.version}
	changed := false
	for _, e := range entries {
		// DirEntry types come from lstat, so a symlink is never IsDir.
		if !e.IsDir() || !ValidRunLabel(e.Name()) {
			continue
		}
		dir := filepath.Join(ix.dir, e.Name())
		fi, err := e.Info()
		if err != nil {
			next.errs = append(next.errs, fmt.Sprintf("review run %s: %v", e.Name(), err))
			continue
		}
		mod := runMod(dir, fi)
		if r := old.byLabel[e.Name()]; r != nil && r.mod.Equal(mod) {
			next.runs = append(next.runs, r)
			next.byLabel[r.label] = r
			continue
		}
		r, err := loadReviewRun(dir, e.Name(), mod)
		if err != nil {
			next.errs = append(next.errs, fmt.Sprintf("review run %s: %v", e.Name(), err))
			continue
		}
		changed = true
		next.runs = append(next.runs, r)
		next.byLabel[r.label] = r
	}
	if len(next.runs) != len(old.runs) || !slices.Equal(next.errs, old.errs) {
		changed = true
	}
	sort.SliceStable(next.runs, func(i, j int) bool {
		if !next.runs[i].mod.Equal(next.runs[j].mod) {
			return next.runs[i].mod.After(next.runs[j].mod)
		}
		return next.runs[i].label > next.runs[j].label
	})
	if changed {
		next.version++
	}
	ix.state.Store(next)
	return nil
}

// reviewBoot is the bootstrap of a review page served by the dashboard.
type reviewBoot struct {
	Mode   string `json:"mode"`
	Served bool   `json:"served"`
	Run    string `json:"run"`
	Lookup string `json:"lookup"`
	Live   string `json:"live"`
}

func loadReviewRun(dir, label string, mod time.Time) (*reviewRun, error) {
	run, err := review.Load(dir)
	if err != nil {
		return nil, err
	}
	g := review.Build(run)
	var js bytes.Buffer
	enc := json.NewEncoder(&js)
	enc.SetIndent("", " ")
	if err := enc.Encode(g); err != nil {
		return nil, err
	}
	pg, err := newPage(review.Page{
		Mode:   review.ModeReview,
		Graph:  g,
		Boot:   reviewBoot{Mode: string(review.ModeReview), Served: true, Run: label, Lookup: "/lookup", Live: "/"},
		Addons: []string{"web/served.js"},
	})
	if err != nil {
		return nil, err
	}
	r := &reviewRun{label: label, mod: mod, page: pg, json: js.Bytes(),
		prByURL: map[string]string{}, ticketByKey: map[string]string{}, ticketByURL: map[string]string{}}
	for _, n := range g.Nodes {
		switch n.Kind {
		case "pr":
			if n.URL != "" {
				r.prByURL[n.URL] = n.ID
			}
		case "ticket":
			r.ticketByKey[strings.TrimPrefix(n.ID, "ticket:")] = n.ID
			if n.URL != "" {
				r.ticketByURL[n.URL] = n.ID
			}
		}
	}
	return r, nil
}

func (ix *ReviewIndex) run(label string) *reviewRun {
	if ix == nil || !ValidRunLabel(label) {
		return nil
	}
	return ix.state.Load().byLabel[label]
}

// Errors lists what the last scan could not read or load.
func (ix *ReviewIndex) Errors() []string {
	if ix == nil {
		return nil
	}
	return ix.state.Load().errs
}

// ReviewLink points a live entity at its neighbourhood in a review run.
type ReviewLink struct {
	Run string `json:"run"`
	URL string `json:"url"`
	// Matches are the exact identity matches behind the link: the live
	// node and the review node it equals.
	Matches []ReviewMatch `json:"matches"`
}

// ReviewMatch is one exact identity match.
type ReviewMatch struct {
	Entity     string `json:"entity"`
	ReviewNode string `json:"review_node"`
}

// ReviewLinks maps live node IDs to review runs.
type ReviewLinks struct {
	Efforts  map[string]ReviewLink `json:"efforts"`
	Entities map[string]ReviewLink `json:"entities"`
	// Errors are the review-run scan failures, shown with the page's
	// other diagnostics. Never null.
	Errors []string `json:"errors"`
}

// reviewNodeFor returns the review node a live PR or ticket node is
// identical to in run, matching only exact PR URLs, exact ticket keys and
// exact ticket URLs. Titles and labels are never compared.
func reviewNodeFor(run *reviewRun, n *Node) string {
	switch {
	case n.PR != nil:
		if n.PR.URL != "" {
			return run.prByURL[n.PR.URL]
		}
	case n.Ticket != nil:
		if n.Ticket.System == "jira" && n.Ticket.Key != "" {
			if id := run.ticketByKey[n.Ticket.Key]; id != "" {
				return id
			}
		}
		if n.Ticket.URL != "" {
			return run.ticketByURL[n.Ticket.URL]
		}
	}
	return ""
}

// focusURL links to a run's page focused on review node IDs.
func focusURL(label string, ids []string) string {
	esc := make([]string, len(ids))
	for i, id := range ids {
		esc[i] = strings.ReplaceAll(url.PathEscape(id), ",", "%2C")
	}
	return "/review/" + url.PathEscape(label) + "/#focus=" + strings.Join(esc, ",")
}

// computeLinks joins a snapshot with the runs (newest first). Each PR or
// ticket links to the newest run containing it; each effort links to the
// newest run containing any of its PRs or tickets, focused on all of them.
func computeLinks(s *Snapshot, runs []*reviewRun) ReviewLinks {
	out := ReviewLinks{Efforts: map[string]ReviewLink{}, Entities: map[string]ReviewLink{}}
	if s == nil || len(runs) == 0 {
		return out
	}
	idx := indexSnapshot(s)
	type hit struct {
		rank   int
		entity string
		node   string
	}
	byEffort := map[string][]hit{}
	for i := range s.Nodes {
		n := &s.Nodes[i]
		if n.PR == nil && n.Ticket == nil {
			continue
		}
		for rank, run := range runs {
			id := reviewNodeFor(run, n)
			if id == "" {
				continue
			}
			out.Entities[n.ID] = ReviewLink{Run: run.label, URL: focusURL(run.label, []string{id}), Matches: []ReviewMatch{{n.ID, id}}}
			for _, eff := range idx.effortsOf(n.ID) {
				byEffort[eff] = append(byEffort[eff], hit{rank, n.ID, id})
			}
			break
		}
	}
	for eff, hits := range byEffort {
		best := hits[0].rank
		for _, h := range hits {
			best = min(best, h.rank)
		}
		var ids []string
		var matches []ReviewMatch
		seen := map[string]bool{}
		for _, h := range hits {
			if h.rank != best || seen[h.node] {
				continue
			}
			seen[h.node] = true
			ids = append(ids, h.node)
			matches = append(matches, ReviewMatch{h.entity, h.node})
		}
		sort.Strings(ids)
		sort.Slice(matches, func(i, j int) bool { return matches[i].Entity < matches[j].Entity })
		out.Efforts[eff] = ReviewLink{Run: runs[best].label, URL: focusURL(runs[best].label, ids), Matches: matches}
	}
	return out
}

// linksJSON returns the encoded links for v, computed once per snapshot
// generation and index version (an in-memory join; no I/O).
func (ix *ReviewIndex) linksJSON(v *View) []byte {
	st := ix.state.Load()
	var s *Snapshot
	var gen uint64
	if v != nil {
		s, gen = v.Snapshot, v.Snapshot.Generation
	}
	key := [2]uint64{gen, st.version}
	ix.linksMu.Lock()
	defer ix.linksMu.Unlock()
	if ix.links != nil && ix.linksKey == key {
		return ix.links
	}
	links := computeLinks(s, st.runs)
	links.Errors = append([]string{}, st.errs...)
	b, err := json.Marshal(links)
	if err != nil {
		Logf("dash: encode review links: %v", err)
		b = []byte(`{"efforts":{},"entities":{},"errors":["review links could not be encoded"]}`)
	}
	ix.links, ix.linksKey = append(b, '\n'), key
	return ix.links
}

// snapIndex is a snapshot's incoming adjacency, for walking a node back to
// its efforts.
type snapIndex struct {
	s   *Snapshot
	inc map[string][]string
	ids map[string]*Node
}

func indexSnapshot(s *Snapshot) *snapIndex {
	ix := &snapIndex{s: s, inc: map[string][]string{}, ids: map[string]*Node{}}
	for i := range s.Nodes {
		ix.ids[s.Nodes[i].ID] = &s.Nodes[i]
	}
	for _, e := range s.Edges {
		ix.inc[e.Target] = append(ix.inc[e.Target], e.Source)
	}
	return ix
}

// effortsOf returns the efforts id belongs to, sorted.
func (ix *snapIndex) effortsOf(id string) []string {
	seen := map[string]bool{id: true}
	queue := []string{id}
	var out []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if n := ix.ids[cur]; n != nil && n.Kind == KindEffort {
			out = append(out, cur)
		}
		for _, src := range ix.inc[cur] {
			if !seen[src] {
				seen[src] = true
				queue = append(queue, src)
			}
		}
	}
	sort.Strings(out)
	return out
}
