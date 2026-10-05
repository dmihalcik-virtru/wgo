package dash

import (
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
}

// RefresherOptions tune a Refresher.
type RefresherOptions struct {
	// Workers bounds concurrent fetches (2 when zero).
	Workers int
	// Queue bounds pending jobs (256 when zero). Jobs beyond it are dropped:
	// they stay non-fresh and are offered again on the next collection.
	Queue int
	// Backoff is the per-key refresh lease window (30s when zero). A key
	// refreshed within the window, by this or any other wgo process, is
	// skipped.
	Backoff time.Duration
	// IgnoreLeases fetches even when a lease is held: the explicit
	// `wgo dash --refresh`, matching --refresh elsewhere in wgo.
	IgnoreLeases bool
}

// Refresher warms remote caches in the background with a bounded worker
// pool. It writes only the caches; collections pick the data up from there.
type Refresher struct {
	f    Fetchers
	opts RefresherOptions

	queue chan *batchJob
	wg    sync.WaitGroup

	mu      sync.Mutex
	pending map[string]bool
	closed  bool
}

type batchJob struct {
	job  Job
	done *sync.WaitGroup
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
	r := &Refresher{f: f, opts: opts, queue: make(chan *batchJob, opts.Queue), pending: map[string]bool{}}
	for i := 0; i < opts.Workers; i++ {
		r.wg.Add(1)
		go r.work()
	}
	return r
}

// Submit enqueues jobs without blocking and returns a channel closed once
// every accepted job has finished. Jobs already pending, or beyond the queue
// bound, are dropped.
func (r *Refresher) Submit(jobs []Job) <-chan struct{} {
	done := make(chan struct{})
	var batch sync.WaitGroup
	r.mu.Lock()
	if !r.closed {
		for _, j := range jobs {
			k := j.key()
			if r.pending[k] || !r.supports(j) {
				continue
			}
			batch.Add(1)
			select {
			case r.queue <- &batchJob{job: j, done: &batch}:
				r.pending[k] = true
			default:
				batch.Done()
			}
		}
	}
	r.mu.Unlock()
	go func() {
		batch.Wait()
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
	for bj := range r.queue {
		r.run(bj.job)
		r.mu.Lock()
		delete(r.pending, bj.job.key())
		r.mu.Unlock()
		bj.done.Done()
	}
}

// run performs one job behind the shared refresh lease.
func (r *Refresher) run(j Job) {
	switch j.Kind {
	case JobPR:
		if r.opts.IgnoreLeases || prcache.LockRefresh(j.RemoteURL, j.RepoPath, j.Branch, r.opts.Backoff) {
			prcache.Resolve(r.f.PR, j.RemoteURL, j.RepoPath, j.Branch, prcache.Opts{Synchronous: true})
		}
	case JobJira:
		if r.opts.IgnoreLeases || jiracache.LockRefresh(j.Ticket, r.opts.Backoff) {
			_, _, _ = jiracache.Resolve(r.f.Jira, j.Ticket, jiracache.Opts{Synchronous: true})
		}
	case JobIssue:
		if r.opts.IgnoreLeases || issuecache.LockRefresh(j.Issue, r.opts.Backoff) {
			issuecache.Refresh(r.f.Issue, j.Issue)
		}
	}
}
