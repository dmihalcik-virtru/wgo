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
// baseline, and both pre-encoded so serving them costs no encoding.
type View struct {
	Snapshot *Snapshot
	Delta    *Delta
	// Loaded is true when Snapshot was read from disk, not collected by this
	// process.
	Loaded bool
	// Diagnostics are Dash-level problems the snapshot itself cannot carry:
	// a persisted snapshot or baseline that could not be loaded, a failed
	// snapshot write, or a failed background republish. They are served with
	// the view and cleared once the condition clears.
	Diagnostics  []string
	snapshotJSON []byte
	deltaJSON    []byte
}

func newView(s *Snapshot, d *Delta, loaded bool, diags []string) (*View, error) {
	sj, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	dj, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return &View{Snapshot: s, Delta: d, Loaded: loaded, Diagnostics: diags, snapshotJSON: sj, deltaJSON: dj}, nil
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
	var buf bytes.Buffer
	buf.Grow(len(v.snapshotJSON) + len(v.deltaJSON) + 128)
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
		return err
	}
	buf.Write(dj)
	buf.WriteString(`,"snapshot":`)
	buf.Write(v.snapshotJSON)
	buf.WriteString(`,"delta":`)
	buf.Write(v.deltaJSON)
	buf.WriteString("}\n")
	_, err = w.Write(buf.Bytes())
	return err
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

	// Diagnostics records load problems (a corrupt snapshot, say). They are
	// also served in every view's diagnostics.
	Diagnostics []string
	// persistErr and refreshErr are the current write and background
	// republish failures, guarded by mu.
	persistErr string
	refreshErr string
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
	out = append(out, d.Diagnostics...)
	if d.persistErr != "" {
		out = append(out, "snapshot not saved: "+d.persistErr)
	}
	if d.refreshErr != "" {
		out = append(out, "background refresh failed: "+d.refreshErr)
	}
	return out
}

// restampLocked republishes the current view with the current diagnostics.
func (d *Dash) restampLocked() {
	if cur := d.view.Load(); cur != nil {
		d.view.Store(cur.withDiagnostics(d.diagnosticsLocked()))
	}
}

// recordRefreshError makes a background failure visible in the view and
// in the debug log; a nil err clears it.
func (d *Dash) recordRefreshError(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	msg := ""
	if err != nil {
		msg = err.Error()
		logf("dash: background refresh: %v", err)
	}
	if msg != d.refreshErr {
		d.refreshErr = msg
		d.restampLocked()
	}
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
		d.Diagnostics = append(d.Diagnostics, "ignoring last-seen baseline: "+err.Error())
		b = nil
	}
	d.baseline = b
	if b != nil {
		d.gen = b.Generation
	}
	s, err := loadSnapshot(filepath.Join(opts.Dir, SnapshotFile))
	if err != nil {
		d.Diagnostics = append(d.Diagnostics, "ignoring persisted snapshot: "+err.Error())
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
	werr := atomicfile.Write(filepath.Join(d.opts.Dir, SnapshotFile), v.snapshotJSON, 0o600)
	msg := ""
	if werr != nil {
		msg = werr.Error()
		logf("dash: persist snapshot: %v", werr)
	}
	if msg != d.persistErr {
		d.persistErr = msg
		d.restampLocked()
	}
	if werr != nil {
		return fmt.Errorf("%w: %w", ErrPersist, werr)
	}
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
}

// Refresh collects local state, publishes a snapshot built from it and the
// caches, and optionally refreshes remote caches. Collections are
// serialized. It never advances the last-seen baseline.
func (d *Dash) Refresh(ctx context.Context, opts RefreshOptions) error {
	d.refreshMu.Lock()
	defer d.refreshMu.Unlock()
	c := d.opts.Collector
	ls, err := c.collectLocal(ctx)
	if err != nil {
		return err
	}
	d.latest = ls
	s, jobs := c.assemble(ls)
	// A failed write leaves the snapshot published in memory, so the remote
	// refresh still goes ahead; the write error is returned at the end.
	persistErr := d.Publish(s)
	if persistErr != nil && !errors.Is(persistErr, ErrPersist) {
		return persistErr
	}
	if !opts.Remote || d.opts.Refresher == nil || len(jobs) == 0 {
		return persistErr
	}
	done := d.opts.Refresher.Submit(jobs)
	if opts.Wait {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
		s, _ := c.assemble(ls)
		return d.Publish(s)
	}
	go func() {
		<-done
		d.refreshMu.Lock()
		defer d.refreshMu.Unlock()
		if d.latest != ls {
			return // a newer collection already read the warmed caches
		}
		// assemble only reads caches and reports misses as unknown, so it
		// has no error to return; Publish can fail to encode or persist.
		s, _ := c.assemble(ls)
		d.recordRefreshError(d.Publish(s))
	}()
	return persistErr
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
