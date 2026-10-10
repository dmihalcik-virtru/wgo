package dash

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/virtru/wgo/internal/issuecache"
	"github.com/virtru/wgo/internal/jiracache"
	"github.com/virtru/wgo/internal/prcache"
)

// JobKind names a remote refresh job.
type JobKind string

// Job kinds.
const (
	JobPR    JobKind = "pr"
	JobJira  JobKind = "jira"
	JobIssue JobKind = "issue"
)

// Job is one remote cache entry to refresh.
type Job struct {
	Kind JobKind
	// PR lookups.
	RemoteURL, RepoPath, Branch string
	// Jira lookups.
	Ticket string
	// GitHub issue lookups.
	Issue issuecache.Key
}

func (j Job) key() string {
	switch j.Kind {
	case JobPR:
		return "pr\x00" + j.RemoteURL + "\x00" + j.RepoPath + "\x00" + j.Branch
	case JobJira:
		return "jira\x00" + j.Ticket
	default:
		return "issue\x00" + j.Issue.String()
	}
}

// Fetchers are the network lookups a Refresher may call. Any may be nil, in
// which case jobs of that kind are skipped.
type Fetchers struct {
	PR    prcache.Fetcher
	Jira  jiracache.Fetcher
	Issue issuecache.Fetcher
	// Missing optionally says why a kind has no fetcher ("acli not on
	// PATH"), so a skipped batch can explain itself.
	Missing map[JobKind]string
}

// describe names j for a failure message.
func (j Job) describe() string {
	switch j.Kind {
	case JobPR:
		return "pr " + j.Branch
	case JobJira:
		return "jira " + j.Ticket
	default:
		return "issue " + j.Issue.String()
	}
}

// RefresherOptions tune a Refresher.
type RefresherOptions struct {
	// Workers bounds concurrent fetches (2 when zero).
	Workers int
	// Queue bounds pending jobs (256 when zero). Jobs beyond it are dropped
	// and counted in the batch's BatchResult; they stay non-fresh and are
	// offered again on the next collection.
	Queue int
	// Backoff is the per-key refresh lease window (30s when zero). A key
	// whose refresh was attempted within the window, by this or any other
	// wgo process, is skipped.
	Backoff time.Duration
	// IgnoreLeases fetches even when a lease is held, and takes none: the
	// explicit `wgo dash --refresh`, matching --refresh elsewhere in wgo.
	IgnoreLeases bool
}

// Refresher warms remote caches in the background with a bounded worker
// pool. It writes only the caches; collections pick the data up from there.
type Refresher struct {
	f    Fetchers
	opts RefresherOptions

	queue chan *inflight
	wg    sync.WaitGroup

	mu      sync.Mutex
	pending map[string]*inflight
	closed  bool
}

// inflight is one queued or running job and every batch waiting on it. A
// job submitted again while pending joins the existing one instead of being
// fetched twice.
type inflight struct {
	job     Job
	waiters []*batch
}

type batch struct {
	wg  sync.WaitGroup
	mu  sync.Mutex
	res BatchResult
}

func (b *batch) finish(j Job, err error) {
	if err != nil {
		b.mu.Lock()
		b.res.Failed = append(b.res.Failed, j.describe()+": "+err.Error())
		b.mu.Unlock()
	}
	b.wg.Done()
}

// BatchResult is how one Submit batch went.
type BatchResult struct {
	// Jobs counts the batch's jobs that have a fetcher. Jobs without one
	// (no gh, no acli) are not failures: that integration is just absent.
	Jobs int
	// Skipped counts, per kind, the jobs passed over because that kind has
	// no fetcher. Not failures: see Jobs.
	Skipped map[JobKind]int
	// Failed describes each job whose fetch or cache write failed.
	Failed []string
	// Dropped counts jobs that never ran: beyond the queue bound, or
	// submitted after Close.
	Dropped int
}

// Err summarises the failed and dropped jobs, or returns nil when every job
// ran cleanly.
func (r BatchResult) Err() error {
	if len(r.Failed) == 0 && r.Dropped == 0 {
		return nil
	}
	var parts []string
	if len(r.Failed) > 0 {
		failed := slices.Clone(r.Failed)
		sort.Strings(failed)
		const maxListed = 3
		more := ""
		if len(failed) > maxListed {
			more = fmt.Sprintf("; and %d more", len(failed)-maxListed)
			failed = failed[:maxListed]
		}
		parts = append(parts, fmt.Sprintf("%d of %d lookups failed (%s%s)", len(r.Failed), r.Jobs, strings.Join(failed, "; "), more))
	}
	if r.Dropped > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d lookups not run (queue full)", r.Dropped, r.Jobs))
	}
	return fmt.Errorf("remote refresh: %s", strings.Join(parts, ", "))
}

// NewRefresher starts a Refresher's workers.
func NewRefresher(f Fetchers, opts RefresherOptions) *Refresher {
	if opts.Workers <= 0 {
		opts.Workers = 2
	}
	if opts.Queue <= 0 {
		opts.Queue = 256
	}
	if opts.Backoff <= 0 {
		opts.Backoff = 30 * time.Second
	}
	r := &Refresher{f: f, opts: opts, queue: make(chan *inflight, opts.Queue), pending: map[string]*inflight{}}
	for i := 0; i < opts.Workers; i++ {
		r.wg.Add(1)
		go r.work()
	}
	return r
}

// Submit enqueues jobs without blocking. The returned channel yields the
// batch's result, then closes, once every accepted job has finished. A job
// already pending from an earlier batch is not fetched again, but this batch
// waits for it too. Jobs beyond the queue bound are dropped and counted.
func (r *Refresher) Submit(jobs []Job) <-chan BatchResult {
	b := &batch{}
	r.mu.Lock()
	for _, j := range jobs {
		if !r.supports(j) {
			if b.res.Skipped == nil {
				b.res.Skipped = map[JobKind]int{}
			}
			b.res.Skipped[j.Kind]++
			continue
		}
		b.res.Jobs++
		if r.closed {
			b.res.Dropped++
			continue
		}
		k := j.key()
		if f := r.pending[k]; f != nil {
			if !slices.Contains(f.waiters, b) {
				b.wg.Add(1)
				f.waiters = append(f.waiters, b)
			}
			continue
		}
		f := &inflight{job: j, waiters: []*batch{b}}
		select {
		case r.queue <- f:
			// Workers finish a job under r.mu, so this Add lands first.
			b.wg.Add(1)
			r.pending[k] = f
		default:
			b.res.Dropped++
		}
	}
	r.mu.Unlock()
	done := make(chan BatchResult, 1)
	go func() {
		b.wg.Wait()
		b.mu.Lock()
		res := b.res
		b.mu.Unlock()
		done <- res
		close(done)
	}()
	return done
}

func (r *Refresher) supports(j Job) bool {
	switch j.Kind {
	case JobPR:
		return r.f.PR != nil
	case JobJira:
		return r.f.Jira != nil
	case JobIssue:
		return r.f.Issue != nil
	}
	return false
}

// Close stops accepting jobs and waits for in-flight ones.
func (r *Refresher) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	close(r.queue)
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *Refresher) work() {
	defer r.wg.Done()
	for f := range r.queue {
		err := r.run(f.job)
		r.mu.Lock()
		delete(r.pending, f.job.key())
		waiters := f.waiters
		r.mu.Unlock()
		for _, b := range waiters {
			b.finish(f.job, err)
		}
	}
}

// run performs one job, behind the shared refresh lease unless IgnoreLeases
// is set. A held lease is not a failure: another refresher has the key.
func (r *Refresher) run(j Job) error {
	switch j.Kind {
	case JobPR:
		if r.opts.IgnoreLeases || prcache.LockRefresh(j.RemoteURL, j.RepoPath, j.Branch, r.opts.Backoff) {
			res := prcache.Resolve(r.f.PR, j.RemoteURL, j.RepoPath, j.Branch, prcache.Opts{Synchronous: true})
			return errors.Join(res.Err, res.WriteErr)
		}
	case JobJira:
		if r.opts.IgnoreLeases || jiracache.LockRefresh(j.Ticket, r.opts.Backoff) {
			_, _, err := jiracache.Resolve(r.f.Jira, j.Ticket, jiracache.Opts{Synchronous: true})
			return err
		}
	case JobIssue:
		if r.opts.IgnoreLeases || issuecache.LockRefresh(j.Issue, r.opts.Backoff) {
			return issuecache.Refresh(r.f.Issue, j.Issue).Err
		}
	}
	return nil
}

// skipSummary describes the jobs res skipped for want of a fetcher, or ""
// when none were. It is informational, never an error.
func (r *Refresher) skipSummary(res BatchResult) string {
	kinds := make([]JobKind, 0, len(res.Skipped))
	for k := range res.Skipped {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	var parts []string
	for _, k := range kinds {
		label := string(k)
		switch k {
		case JobPR:
			label = "GitHub PR"
		case JobIssue:
			label = "GitHub issue"
		case JobJira:
			label = "Jira"
		}
		p := fmt.Sprintf("%d %s lookups skipped", res.Skipped[k], label)
		if why := r.f.Missing[k]; why != "" {
			p += ": " + why
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "; ")
}
