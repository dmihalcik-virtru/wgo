package dash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/virtru/wgo/internal/atomicfile"
)

// File names under the dash cache directory (~/.wgo/cache/dash).
const (
	SnapshotFile = "snapshot.json"
	BaselineFile = "last-seen.json"
)

// ringSize is how many recent snapshots Acknowledge can name.
const ringSize = 8

// Errors returned by Dash.
var (
	ErrUnknownWorkspace = errors.New("unknown workspace id")
	ErrStaleWorkspace   = errors.New("workspace is no longer discovered")
	ErrUnknownGen       = errors.New("unknown snapshot generation")
	ErrOldGeneration    = errors.New("generation is older than the acknowledged baseline")
	// ErrPersist wraps a failed snapshot write. The snapshot was still
	// published in memory.
	ErrPersist = errors.New("persist snapshot")
)

// View is an immutable published state: a snapshot, its delta against the
// baseline, and both pre-encoded so serving them only encodes the small
// diagnostics list. Treat every field as read-only: Snapshot and Delta are
// shared with the encoded body.
type View struct {
	Snapshot *Snapshot
	Delta    *Delta
	// Loaded is true when Snapshot was read from disk, not collected by this
	// process.
	Loaded bool
	// Diagnostics are Dash-level problems the snapshot itself cannot carry:
	// a persisted snapshot or baseline that could not be loaded (until a
	// good one is written), a failed collection (until one succeeds), a
	// failed snapshot write (until one succeeds), or failed remote lookups
	// (until a refresh runs cleanly).
	Diagnostics  []string
	snapshotJSON []byte
	deltaJSON    []byte
	// index is the snapshot's query index, built on first use and shared by
	// the copies withDiagnostics makes.
	index *lazyIndex
}

// lazyIndex builds a view's query index once, for the canonical registry.
type lazyIndex struct {
	once sync.Once
	ix   *queryIndex
	err  error
}

func newView(s *Snapshot, d *Delta, loaded bool, diags []string) (*View, error) {
	sj, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return newViewJSON(s, sj, d, loaded, diags)
}

// newViewJSON is newView with the snapshot already encoded as sj, which is
// served as is.
func newViewJSON(s *Snapshot, sj []byte, d *Delta, loaded bool, diags []string) (*View, error) {
	dj, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return &View{Snapshot: s, Delta: d, Loaded: loaded, Diagnostics: diags, snapshotJSON: sj, deltaJSON: dj, index: &lazyIndex{}}, nil
}

// queryIndex returns the query index of v's snapshot for reg, built once
// for the canonical registry. It decodes the encoded snapshot, so fields
// that only a later build models (a fixture's) still resolve.
func (v *View) queryIndex(reg *Registry) (*queryIndex, error) {
	if reg != defaultRegistry || v.index == nil {
		return newQueryIndex(v.snapshotJSON, reg)
	}
	v.index.once.Do(func() { v.index.ix, v.index.err = newQueryIndex(v.snapshotJSON, reg) })
	return v.index.ix, v.index.err
}

// Query parses expr and runs it over v's snapshot with the canonical
// registry.
func (v *View) Query(expr string) (*QueryResult, error) {
	q, err := ParseQuery(expr, Fields())
	if err != nil {
		return nil, err
	}
	ix, err := v.queryIndex(Fields())
	if err != nil {
		return nil, err
	}
	return ix.evaluate(q), nil
}

// Env returns what v's queries resolve @me, @people and relative dates
// against (queryEnv); the live page's bootstrap carries its people.
func (v *View) Env() QueryEnv {
	ix, err := v.queryIndex(Fields())
	if err != nil {
		return QueryEnv{}
	}
	return ix.env
}

// StaticSource serves one fixed view, such as a fixture, read-only.
type StaticSource struct{ V *View }

// Current implements ViewSource.
func (s StaticSource) Current() *View { return s.V }

// LoadFixture reads a snapshot from path — a bare snapshot or a whole
// /api/snapshot body — for wgo dash --fixture. It must be a valid snapshot;
// its JSON is served and queried as written, so fields this build does not
// model (a later spec's) still reach the page and the query.
func LoadFixture(path string) (*View, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("fixture %s: %w", path, err)
	}
	raw := json.RawMessage(data)
	if inner, ok := top["snapshot"]; ok && len(inner) > 0 && inner[0] == '{' {
		raw = inner
	}
	var s Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("fixture %s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("fixture %s is not a valid dash snapshot: %w", path, err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, fmt.Errorf("fixture %s: %w", path, err)
	}
	return newViewJSON(&s, compact.Bytes(), computeDelta(nil, &s), false, nil)
}

// withDiagnostics returns a copy of v carrying diags. The encoded snapshot
// and delta are shared: they are immutable.
func (v *View) withDiagnostics(diags []string) *View {
	c := *v
	c.Diagnostics = diags
	return &c
}

// WriteJSON writes the API body for v: status, generation, snapshot age at
// now, the snapshot and the delta.
func (v *View) WriteJSON(w io.Writer, now time.Time) error {
	body, err := v.encodeJSON(now)
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// QueryOutput is the "query" object wgo dash --json --query adds.
type QueryOutput struct {
	*QueryResult
	// Filters are the saved filters, with the reason any is hidden.
	Filters []SavedFilter `json:"filters"`
}

// WriteQueryJSON writes the API body for v with its snapshot's nodes and
// edges limited to res's visible set, and a "query" object after the delta.
func (v *View) WriteQueryJSON(w io.Writer, now time.Time, res *QueryResult, filters []SavedFilter) error {
	snap, err := v.visibleSnapshotJSON(res)
	if err != nil {
		return err
	}
	q, err := json.Marshal(QueryOutput{QueryResult: res, Filters: filters})
	if err != nil {
		return err
	}
	body, err := v.encodeBody(now, snap, q)
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// visibleSnapshotJSON is the encoded snapshot with only the nodes res
// keeps visible and the edges between them; the rest is kept as encoded.
func (v *View) visibleSnapshotJSON(res *QueryResult) ([]byte, error) {
	if !res.Filtered() {
		return v.snapshotJSON, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(v.snapshotJSON, &top); err != nil {
		return nil, err
	}
	var nodes, edges, keptN, keptE []json.RawMessage
	_ = json.Unmarshal(top["nodes"], &nodes)
	_ = json.Unmarshal(top["edges"], &edges)
	for _, n := range nodes {
		var id struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(n, &id) == nil && res.Visible(id.ID) {
			keptN = append(keptN, n)
		}
	}
	for _, e := range edges {
		var ends struct {
			Source string `json:"source"`
			Target string `json:"target"`
		}
		if json.Unmarshal(e, &ends) == nil && res.Visible(ends.Source) && res.Visible(ends.Target) {
			keptE = append(keptE, e)
		}
	}
	var err error
	if top["nodes"], err = json.Marshal(nonNilRaw(keptN)); err != nil {
		return nil, err
	}
	if top["edges"], err = json.Marshal(nonNilRaw(keptE)); err != nil {
		return nil, err
	}
	return json.Marshal(top)
}

func nonNilRaw(l []json.RawMessage) []json.RawMessage {
	if l == nil {
		return []json.RawMessage{}
	}
	return l
}

// encodeJSON builds the body WriteJSON writes, so a caller can tell an
// encoding failure (nothing sent yet) from a failed write.
func (v *View) encodeJSON(now time.Time) ([]byte, error) {
	return v.encodeBody(now, v.snapshotJSON, nil)
}

// encodeBody builds the API body with snap as the snapshot and, when set,
// query as a trailing "query" member.
func (v *View) encodeBody(now time.Time, snap, query []byte) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(len(snap) + len(v.deltaJSON) + len(query) + 128)
	age := now.Sub(v.Snapshot.GeneratedAt).Seconds()
	if age < 0 {
		age = 0
	}
	buf.WriteString(`{"status":"ready","generation":`)
	buf.WriteString(strconv.FormatUint(v.Snapshot.Generation, 10))
	buf.WriteString(`,"age_seconds":`)
	buf.WriteString(strconv.FormatFloat(age, 'f', 1, 64))
	buf.WriteString(`,"loaded_from_disk":`)
	buf.WriteString(strconv.FormatBool(v.Loaded))
	buf.WriteString(`,"diagnostics":`)
	diags := v.Diagnostics
	if diags == nil {
		diags = []string{}
	}
	dj, err := json.Marshal(diags)
	if err != nil {
		return nil, err
	}
	buf.Write(dj)
	buf.WriteString(`,"snapshot":`)
	buf.Write(snap)
	buf.WriteString(`,"delta":`)
	buf.Write(v.deltaJSON)
	if query != nil {
		buf.WriteString(`,"query":`)
		buf.Write(query)
	}
	buf.WriteString("}\n")
	return buf.Bytes(), nil
}

// Options configure a Dash.
type Options struct {
	// Dir holds snapshot.json and last-seen.json.
	Dir       string
	Collector *Collector
	// Refresher warms remote caches; nil disables remote refresh.
	Refresher *Refresher
	Now       func() time.Time
}

// Dash holds the current snapshot, persists it, and owns the last-seen
// baseline.
type Dash struct {
	opts Options
	now  func() time.Time

	view atomic.Pointer[View]

	refreshMu sync.Mutex // serializes collections
	latest    *localState

	mu       sync.Mutex // guards gen, ring, baseline and publishing
	gen      uint64
	ring     []*Snapshot
	baseline *Baseline

	// The current Dash-level problems, guarded by mu and served in every
	// view's diagnostics; see View.Diagnostics. Each is cleared when its
	// condition clears.
	snapshotLoadErr string // persisted snapshot unreadable; cleared by a good write
	baselineLoadErr string // last-seen.json unreadable; cleared by Acknowledge
	collectErr      string // the last collection failed
	persistErr      string // the last snapshot write failed
	refreshErr      string // the last remote refresh had failures
	// skipDiag is informational, not an error: remote lookups skipped for
	// want of an integration. Cleared when the next refresh starts.
	skipDiag string
	// deadlineDiags are informational too: what a waited-for refresh left,
	// or finished late, at its deadline. Cleared when the next refresh
	// starts.
	deadlineDiags []string
}

// Logf, when set, receives Dash faults that have no caller to return to,
// such as a failed background republish. cmd wires it to WGO_DEBUG.
var Logf func(format string, args ...any)

func logf(format string, args ...any) {
	if Logf != nil {
		Logf(format, args...)
	}
}

// diagnosticsLocked lists the current Dash-level diagnostics. d.mu is held.
func (d *Dash) diagnosticsLocked() []string {
	var out []string
	if d.snapshotLoadErr != "" {
		out = append(out, "ignoring persisted snapshot: "+d.snapshotLoadErr)
	}
	if d.baselineLoadErr != "" {
		out = append(out, "ignoring last-seen baseline: "+d.baselineLoadErr)
	}
	if d.collectErr != "" {
		out = append(out, d.collectErr)
	}
	if d.persistErr != "" {
		out = append(out, "snapshot not saved: "+d.persistErr)
	}
	if d.refreshErr != "" {
		out = append(out, d.refreshErr)
	}
	if d.skipDiag != "" {
		out = append(out, d.skipDiag)
	}
	out = append(out, d.deadlineDiags...)
	return out
}

// restampLocked republishes the current view with the current diagnostics.
func (d *Dash) restampLocked() {
	if cur := d.view.Load(); cur != nil {
		d.view.Store(cur.withDiagnostics(d.diagnosticsLocked()))
	}
}

// setDiagLocked sets one diagnostic field and republishes the current view
// if it changed. d.mu is held.
func (d *Dash) setDiagLocked(field *string, msg string) {
	if *field != msg {
		*field = msg
		d.restampLocked()
	}
}

// recordRefreshError makes a remote refresh failure visible in the view and
// in the debug log; a nil err clears it.
func (d *Dash) recordRefreshError(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	msg := ""
	if err != nil {
		msg = err.Error()
		logf("dash: %v", err)
	}
	d.setDiagLocked(&d.refreshErr, msg)
}

// recordSkipped shows which lookups the last batch skipped for want of an
// integration; "" clears it. It does not affect any error.
func (d *Dash) recordSkipped(msg string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.setDiagLocked(&d.skipDiag, msg)
}

// recordDeadline shows what a waited-for refresh left, or finished late, at
// its deadline; nil clears it. It does not affect any error.
func (d *Dash) recordDeadline(msgs []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !slices.Equal(d.deadlineDiags, msgs) {
		d.deadlineDiags = msgs
		d.restampLocked()
	}
}

// recordCollectError makes a failed collection visible on the view still
// being served, which is then older than it looks; a nil err clears it.
func (d *Dash) recordCollectError(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	msg := ""
	if err != nil {
		msg = "collection failed: " + err.Error()
		if cur := d.view.Load(); cur != nil {
			msg += "; showing the snapshot from " + cur.Snapshot.GeneratedAt.Format(time.RFC3339)
		}
	}
	d.setDiagLocked(&d.collectErr, msg)
}

// Open loads the persisted snapshot and baseline, if any, and publishes the
// snapshot immediately so it can be served before the first collection.
func Open(opts Options) (*Dash, error) {
	d := &Dash{opts: opts, now: opts.Now}
	if d.now == nil {
		d.now = time.Now
	}
	b, err := loadBaseline(filepath.Join(opts.Dir, BaselineFile))
	if err != nil {
		logf("dash: load baseline: %v", err)
		d.baselineLoadErr = err.Error()
	}
	if b != nil {
		d.baseline = b
		d.gen = b.Generation
	}
	s, err := loadSnapshot(filepath.Join(opts.Dir, SnapshotFile))
	if err != nil {
		logf("dash: load snapshot: %v", err)
		d.snapshotLoadErr = err.Error()
	}
	if s != nil {
		if s.Generation > d.gen {
			d.gen = s.Generation
		}
		v, err := newView(s, computeDelta(d.baseline, s), true, d.diagnosticsLocked())
		if err != nil {
			return nil, err
		}
		d.ring = append(d.ring, s)
		d.view.Store(v)
	}
	return d, nil
}

func loadSnapshot(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Schema != SchemaVersion {
		return nil, fmt.Errorf("snapshot schema %d, want %d", s.Schema, SchemaVersion)
	}
	return &s, nil
}

// Current returns the published view, or nil before anything is available.
// It never blocks on a collection.
func (d *Dash) Current() *View { return d.view.Load() }

// Diagnostics lists the current Dash-level problems. Unlike a view's copy, it
// is available before the first snapshot, so a failed first collection can
// be shown instead of an endless "loading".
func (d *Dash) Diagnostics() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.diagnosticsLocked()
}

// Publish assigns s the next generation, computes its delta, swaps it in
// and persists it atomically. s must not be modified afterwards.
//
// The new view is served even when persisting fails: the newest data is
// still the right data to show. The failure is returned and also carried in
// the view's diagnostics until a later write succeeds.
func (d *Dash) Publish(s *Snapshot) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gen++
	s.Generation = d.gen
	v, err := newView(s, computeDelta(d.baseline, s), false, d.diagnosticsLocked())
	if err != nil {
		return err
	}
	d.ring = append(d.ring, s)
	if len(d.ring) > ringSize {
		d.ring = d.ring[len(d.ring)-ringSize:]
	}
	d.view.Store(v)
	if d.opts.Dir == "" {
		return nil
	}
	if err := atomicfile.Write(filepath.Join(d.opts.Dir, SnapshotFile), v.snapshotJSON, 0o600); err != nil {
		logf("dash: persist snapshot: %v", err)
		d.setDiagLocked(&d.persistErr, err.Error())
		return fmt.Errorf("%w: %w", ErrPersist, err)
	}
	// A good snapshot.json replaced whatever could not be loaded.
	d.setDiagLocked(&d.snapshotLoadErr, "")
	d.setDiagLocked(&d.persistErr, "")
	return nil
}

// RefreshOptions control a refresh.
type RefreshOptions struct {
	// Remote also warms non-fresh remote cache entries through the
	// Refresher, then republishes with the new data.
	Remote bool
	// Wait blocks until the remote refresh has finished and been
	// published. Otherwise it continues in the background.
	Wait bool
	// Timeout bounds how long Wait starts remote lookups, follow-up rounds
	// included; zero waits for all of them. At the deadline no more start,
	// and the snapshot is published with whatever has been fetched: the
	// lookups left are reported as not run, not as failures. PR inventory
	// fetches running then are waited for, up to deadlineGrace times
	// Timeout more, since a large set can take longer than any one
	// deadline and would otherwise never be fetched.
	Timeout time.Duration
}

// Refresh collects local state, publishes a snapshot built from it and the
// caches, and optionally refreshes remote caches. Collections are
// serialized. It never advances the last-seen baseline.
//
// A failed or cancelled collection publishes nothing: the last good view
// stays up, marked with the failure. With Wait, failed remote lookups are
// returned as well as shown in the view.
func (d *Dash) Refresh(ctx context.Context, opts RefreshOptions) error {
	d.refreshMu.Lock()
	defer d.refreshMu.Unlock()
	c := d.opts.Collector
	ls, err := c.collectLocal(ctx)
	if err == nil {
		// A cancelled collection marks every workspace unreadable; publishing
		// it would replace the last good snapshot with an all-error one.
		err = ctx.Err()
	}
	d.recordCollectError(err)
	if err != nil {
		return err
	}
	d.latest = ls
	d.recordSkipped("")
	d.recordDeadline(nil)
	s, jobs := d.assemble(ls)
	// A failed write leaves the snapshot published in memory, so the remote
	// refresh still goes ahead; the write error is returned at the end.
	persistErr := d.Publish(s)
	if persistErr != nil && !errors.Is(persistErr, ErrPersist) {
		return persistErr
	}
	if !opts.Remote || d.opts.Refresher == nil {
		return persistErr
	}
	if opts.Wait {
		jobs = waitedJobs(jobs)
	}
	if len(jobs) == 0 {
		d.recordRefreshError(nil) // everything is fresh: nothing failed
		return persistErr
	}
	b := d.opts.Refresher.submit(jobs)
	if opts.Wait {
		s, res, err := d.awaitRounds(ctx, ls, jobs, b, opts.Timeout)
		if err != nil {
			return err
		}
		// Every problem is shown; only failed lookups fail the command. Jobs
		// beyond the queue bound, or left at the deadline, were not run, so
		// their data stays stale or unknown, which the snapshot already says.
		d.recordRefreshError(res.Err())
		d.recordSkipped(d.opts.Refresher.skipSummary(res))
		d.recordDeadline(res.deadlineSummary())
		remoteErr := res.failedErr()
		if err := d.Publish(s); err != nil {
			return errors.Join(err, remoteErr)
		}
		return remoteErr
	}
	go func() {
		res := <-b.done
		d.refreshMu.Lock()
		defer d.refreshMu.Unlock()
		if d.latest != ls {
			// A newer collection superseded this one. Its own batch waits on
			// any of these jobs still running and republishes their data.
			return
		}
		// assemble only reads caches and reports misses as unknown, so it
		// has no error to return. A failed write is already shown as
		// "snapshot not saved"; only an encoding failure needs recording.
		s, _ := d.assemble(ls)
		err := res.Err()
		d.recordSkipped(d.opts.Refresher.skipSummary(res))
		if perr := d.Publish(s); perr != nil && !errors.Is(perr, ErrPersist) {
			err = errors.Join(err, fmt.Errorf("background republish: %w", perr))
		}
		d.recordRefreshError(err)
	}()
	return persistErr
}

// assemble builds a snapshot of ls and the caches, showing any pause of
// the Refresher's on the sources it holds up.
func (d *Dash) assemble(ls *localState) (*Snapshot, []Job) {
	s, jobs := d.opts.Collector.assemble(ls)
	if r := d.opts.Refresher; r != nil {
		r.markPaused(s)
	}
	return s, jobs
}

// waitedJobs leaves out of jobs those a waited-for refresh neither runs nor
// waits for: PR timelines are refreshed by the background Refresher only
// (gh-104 requirement 2), so `wgo dash --json --refresh` never waits on
// dozens of serial timeline fetches.
func waitedJobs(jobs []Job) []Job {
	return slices.DeleteFunc(slices.Clone(jobs), func(j Job) bool { return j.Kind == JobTimelines || j.Kind == JobTimelinePrune })
}

// maxFollowUps bounds the extra rounds of lookups a waited-for refresh runs
// for jobs that only fetched data reveals: once the authenticated login is
// known, that person's inventory (gh-97) and a sprint search for their own
// Jira identity (gh-98); once the inventory or a sprint search is in, the
// tickets they name. The third follow-up gives a newly discovered ticket
// another round to be fetched. A background refresh leaves them to its next
// collection.
const maxFollowUps = 3

// awaitRounds waits for the batch b of jobs, then assembles ls again. Jobs
// the new data revealed that no earlier round submitted are submitted and
// waited for in turn, until a round reveals none or after maxFollowUps
// rounds. It returns the last snapshot and every round's results together:
// a job dropped by a full queue in any round counts as dropped, not failed,
// and is not submitted again. Once timeout (when not zero) has passed, it
// starts nothing more (see finishAtDeadline) and assembles what has been
// fetched.
func (d *Dash) awaitRounds(ctx context.Context, ls *localState, jobs []Job, b *batch, timeout time.Duration) (*Snapshot, BatchResult, error) {
	tried := map[string]bool{}
	for _, j := range jobs {
		tried[j.key()] = true
	}
	var expired <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		expired = t.C
	}
	var total BatchResult
	for round := 0; ; round++ {
		select {
		case res := <-b.done:
			total.add(res)
		case <-expired:
			total.add(d.finishAtDeadline(ctx, b, timeout))
			s, next := d.assemble(ls)
			// What the data fetched so far reveals is left too: the tickets
			// of an inventory set finished past the deadline, say. What data
			// not fetched would reveal cannot be counted.
			for _, j := range waitedJobs(next) {
				if !tried[j.key()] {
					tried[j.key()] = true
					total.Unfinished++
				}
			}
			markDeadline(s, total)
			return s, total, nil
		case <-ctx.Done():
			return nil, total, ctx.Err()
		}
		s, next := d.assemble(ls)
		var more []Job
		for _, j := range waitedJobs(next) {
			if !tried[j.key()] {
				tried[j.key()] = true
				more = append(more, j)
			}
		}
		if len(more) == 0 || round == maxFollowUps {
			return s, total, nil
		}
		b = d.opts.Refresher.submit(more)
	}
}

// deadlineGrace is how many times the deadline a waited-for refresh waits,
// past it, for the PR inventory fetches running at it: in all it waits at
// most 1 + deadlineGrace times the deadline.
const deadlineGrace = 2

// interruptWait bounds the wait for interrupted fetches to stop and record
// the interruption.
const interruptWait = 5 * time.Second

// finishAtDeadline ends batch b at its deadline: its lookups not yet started
// never start, and the running ones are cancelled, all but the PR inventory
// fetches. It waits for those to finish, so that a large set's result is
// cached rather than lost, up to deadlineGrace times the deadline; then it
// interrupts them, and the inventory cache records the interruption. It
// returns b's result, with what is still running counted as unfinished.
func (d *Dash) finishAtDeadline(ctx context.Context, b *batch, timeout time.Duration) BatchResult {
	r := d.opts.Refresher
	finishing := r.expire(b)
	start := time.Now()
	if finishing > 0 {
		grace := time.NewTimer(deadlineGrace * timeout)
		defer grace.Stop()
		var giveUp <-chan time.Time
	wait:
		for r.runningInventory(b) > 0 {
			select {
			case <-b.progressed:
			case <-grace.C:
				r.interrupt(b)
				giveUp = time.After(interruptWait)
			case <-giveUp:
				break wait
			case <-ctx.Done():
				break wait
			}
		}
	}
	res, left := b.progress()
	res.Unfinished += left
	res.Deadline, res.Finishing = timeout, finishing
	if finishing > 0 {
		res.Overrun = time.Since(start)
	}
	return res
}

// markDeadline says on github_inventory, when it is not fresh, that the
// refresh deadline came first.
func markDeadline(s *Snapshot, res BatchResult) {
	st, ok := s.Sources[SourceGitHubInventory]
	if !ok || st.State == Fresh || res.Unfinished == 0 {
		return
	}
	note := "the refresh deadline (" + FormatSeconds(res.Deadline) + ") was reached"
	if st.Detail != "" {
		note = st.Detail + "; " + note
	}
	st.Detail = note
	s.Sources[SourceGitHubInventory] = st
}

// Acknowledge records the snapshot with generation gen as seen: it becomes
// the baseline for "since last look". This is the only way the baseline
// advances.
func (d *Dash) Acknowledge(gen uint64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var s *Snapshot
	for _, r := range d.ring {
		if r.Generation == gen {
			s = r
		}
	}
	if s == nil {
		return fmt.Errorf("%w: %d", ErrUnknownGen, gen)
	}
	if d.baseline != nil && gen < d.baseline.Generation {
		return fmt.Errorf("%w: %d < %d", ErrOldGeneration, gen, d.baseline.Generation)
	}
	b := baselineFrom(s, d.now())
	if d.opts.Dir != "" {
		if err := saveBaseline(filepath.Join(d.opts.Dir, BaselineFile), b); err != nil {
			return err
		}
	}
	d.baseline = b
	d.baselineLoadErr = ""
	if cur := d.view.Load(); cur != nil {
		v, err := newView(cur.Snapshot, computeDelta(b, cur.Snapshot), cur.Loaded, d.diagnosticsLocked())
		if err != nil {
			return err
		}
		d.view.Store(v)
	}
	return nil
}

// Resolve checks id against the currently discovered workspaces (not the
// snapshot, which may be stale). An ID that is malformed or was never seen
// is ErrUnknownWorkspace; one in the current snapshot that is no longer
// discovered is ErrStaleWorkspace.
func (d *Dash) Resolve(id string) (Target, error) {
	if !ValidWorkspaceID(id) {
		return Target{}, ErrUnknownWorkspace
	}
	found, err := d.opts.Collector.Discovered()
	if err != nil {
		return Target{}, err
	}
	if t, ok := found[id]; ok {
		return t, nil
	}
	if v := d.Current(); v != nil && v.Snapshot.Node(id) != nil {
		return Target{}, ErrStaleWorkspace
	}
	return Target{}, ErrUnknownWorkspace
}
