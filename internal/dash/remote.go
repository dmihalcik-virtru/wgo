package dash

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/inventory"
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
	// JobInventory fetches one person's PR inventory set (gh-97).
	JobInventory JobKind = "inventory"
	// JobIdentity looks up the authenticated GitHub login, which the
	// inventory needs to know whose PRs to fetch.
	JobIdentity JobKind = "identity"
	// JobSprints refreshes [jira] board's sprints and their tickets (gh-98).
	JobSprints JobKind = "sprints"
	// JobTimelines fetches the review histories of up to
	// MaxTimelinesPerCycle PRs, one after another (gh-104).
	JobTimelines JobKind = "timelines"
	// JobTimelinePrune drops cached histories of PRs the dash has not seen
	// for inventory.TimelineMaxUnseen; Timelines lists the PRs seen now.
	JobTimelinePrune JobKind = "timeline-prune"
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
	// Inventory lookups: the set, and the start of the closed-recent
	// window. Since is not part of the key: a set is fetched once.
	Inventory inventory.Key
	Since     time.Time
	// Sprint lookups: the board, and the Jira identities whose tickets
	// each active sprint is searched for (sorted).
	Board     int
	Assignees []string
	// Timeline lookups: the PRs, in the order to fetch them.
	Timelines []TimelineRequest
}

// TimelineRequest is one PR whose history a JobTimelines job fetches, and the
// updated_at it was found at: a history already fetched for it or later
// (by another wgo process, say) is not fetched again.
type TimelineRequest struct {
	Key       inventory.TimelineKey
	UpdatedAt time.Time
}

func (j Job) key() string {
	switch j.Kind {
	case JobPR:
		return "pr\x00" + j.RemoteURL + "\x00" + j.RepoPath + "\x00" + j.Branch
	case JobJira:
		return "jira\x00" + j.Ticket
	case JobInventory:
		return "inventory\x00" + strings.ToLower(j.Inventory.String())
	case JobIdentity:
		return "identity"
	case JobSprints:
		return "sprints\x00" + strconv.Itoa(j.Board) + "\x00" + strings.Join(j.Assignees, "\x00")
	case JobTimelines:
		// One key for every batch: a batch submitted while another is
		// pending joins it, so at most one is ever in flight, and a
		// waited-for refresh's follow-up rounds queue no second one.
		return "timelines"
	case JobTimelinePrune:
		return "timeline-prune"
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
	// Inventory runs inventory searches and identity lookups.
	Inventory inventory.Fetcher
	// Sprints reads [jira] board's sprints (gh-98).
	Sprints jiracache.SprintFetcher
	// Timelines reads PR review histories (gh-104).
	Timelines inventory.TimelineFetcher
	// Missing optionally says why a kind has no fetcher ("acli not on
	// PATH"), so a skipped batch can explain itself.
	Missing map[JobKind]string
	// JiraLogin optionally says whether acli is logged in: nil when it is,
	// an error wrapping jira.ErrNotLoggedIn when it is not, any other
	// error when it cannot tell. It confirms that Jira lookups failing
	// without saying why (acli's `workitem view` does not) failed for a
	// lapsed login, so the rest are paused (see jiraPause).
	JiraLogin func(context.Context) error
}

// describe names j for a failure message.
func (j Job) describe() string {
	switch j.Kind {
	case JobPR:
		return "pr " + j.Branch
	case JobJira:
		return "jira " + j.Ticket
	case JobInventory:
		return "inventory " + j.Inventory.String()
	case JobIdentity:
		return "GitHub identity"
	case JobSprints:
		return "jira sprints of board " + strconv.Itoa(j.Board)
	case JobTimelines:
		return fmt.Sprintf("timelines of %d %s", len(j.Timelines), plural(len(j.Timelines), "PR", "PRs"))
	case JobTimelinePrune:
		return "pruning PR timelines"
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
	// CloseWait bounds how long Close waits for running lookups once it
	// has cancelled them; zero waits until they end. Most stop at once,
	// but a bookmark's PR lookup cannot be cancelled. A one-shot command
	// sets it: with IgnoreLeases and atomic cache writes, leaving a lookup
	// behind at exit is safe.
	CloseWait time.Duration
}

// Refresher warms remote caches in the background with a bounded worker
// pool. It writes only the caches; collections pick the data up from there.
type Refresher struct {
	f    Fetchers
	opts RefresherOptions
	// ctx is cancelled by Close, so the long fetches that can take it (an
	// inventory set is many pages, each retried) stop instead of holding
	// shutdown up. Each running job's context derives from it.
	ctx    context.Context
	cancel context.CancelFunc

	wg sync.WaitGroup

	// jira pauses Jira lookups while acli is logged out.
	jira *jiraPause

	mu      sync.Mutex
	pending map[string]*inflight
	// queue holds the jobs waiting for a worker, one list per priority
	// rank (jobRank), each first come first served; queued counts them.
	// ready wakes the workers. All under mu.
	queue  [numRanks][]*inflight
	queued int
	ready  *sync.Cond
	closed bool
}

// inflight is one queued or running job and every batch waiting on it. A
// job submitted again while pending joins the existing one instead of being
// fetched twice.
type inflight struct {
	job     Job
	waiters []*batch
	// cancel, set while the job runs, cancels its context with a cause.
	cancel context.CancelCauseFunc
}

// expired reports whether every batch waiting on f has passed its deadline,
// so f need not start. r.mu is held.
func (f *inflight) expired() bool {
	for _, b := range f.waiters {
		if !b.expired {
			return false
		}
	}
	return len(f.waiters) > 0
}

type batch struct {
	wg  sync.WaitGroup
	mu  sync.Mutex
	res BatchResult
	// left counts the accepted jobs not yet finished, under mu.
	left int
	// done yields res once every accepted job has finished, then closes.
	done chan BatchResult
	// progressed is signalled, without blocking, as each job finishes.
	progressed chan struct{}
	// expired is set, under the Refresher's mu, once the batch's waited-for
	// refresh has passed its deadline: its jobs that have not started do
	// not start.
	expired bool
}

// Job priority ranks, most urgent first. A waited-for refresh with a
// deadline gets as far down the list as it can. The PR inventory comes
// first: it is the backbone of the snapshot, and the attention facts need
// it. Open-authored and review-requested come before closed-recent because
// whose move an open PR is depends on them alone; closed-recent feeds the
// shipped counts. The GitHub login comes before all, since the inventory of
// "me" needs it; the board's sprints, which name more tickets, next; then
// the bookmarks' PRs; then PR timelines and their pruning; the ticket
// lookups, which the inventory and the sprints reveal by the hundred, last.
//
// There is no aging: a job of a lower rank waits while higher ones keep
// coming. So the timelines come before the tickets: each is one deduped job
// (see Job.key), which cannot flood the queue, whereas the served
// dashboard's tickets, resubmitted every refresh, can outpace the workers
// and would starve them for good. A waited-for refresh runs neither
// (waitedJobs).
const (
	rankIdentity = iota
	rankOpenAuthored
	rankReviewRequested
	rankClosedRecent
	rankSprints
	rankPR
	rankTimelines
	rankTicket
	numRanks
)

// jobRank is j's priority rank.
func jobRank(j Job) int {
	switch j.Kind {
	case JobIdentity:
		return rankIdentity
	case JobInventory:
		switch j.Inventory.Set {
		case inventory.OpenAuthored:
			return rankOpenAuthored
		case inventory.ReviewRequested:
			return rankReviewRequested
		}
		return rankClosedRecent
	case JobSprints:
		return rankSprints
	case JobPR:
		return rankPR
	case JobJira, JobIssue:
		return rankTicket
	}
	return rankTimelines
}

// accept counts one more job this batch waits for. r.mu is held.
func (b *batch) accept() {
	b.wg.Add(1)
	b.mu.Lock()
	b.left++
	b.mu.Unlock()
}

// progress is the batch's result so far and how many of its jobs have not
// finished yet.
func (b *batch) progress() (BatchResult, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	res := b.res
	res.Failed = slices.Clone(res.Failed)
	res.Skipped = maps.Clone(res.Skipped)
	return res, b.left
}

func (b *batch) finish(j Job, err error) {
	var paused *pausedError
	b.mu.Lock()
	switch {
	case errors.As(err, &paused):
		b.res.JiraSkipped++
		if b.res.JiraSkipReason == "" {
			b.res.JiraSkipReason = paused.reason
		}
	case errors.Is(err, errNotStarted):
		b.res.Unfinished++
	case errors.Is(err, errInterrupted):
		b.res.Unfinished++
		if j.Kind == JobInventory {
			b.res.Interrupted++
		}
	case err != nil:
		b.res.Failed = append(b.res.Failed, j.describe()+": "+err.Error())
	}
	b.left--
	b.mu.Unlock()
	select {
	case b.progressed <- struct{}{}:
	default:
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
	// JiraSkipped counts the Jira ticket and sprint lookups the Jira pause
	// accounted for: not run while acli was logged out, or run into the
	// lapsed login that paused the rest. Not failures: the jira source
	// carries JiraSkipReason, the pause's reason.
	JiraSkipped    int
	JiraSkipReason string
	// Unfinished counts the jobs a waited-for refresh left when its
	// deadline, Deadline, passed: not started, interrupted, or still
	// running when it stopped waiting. Not failures: the snapshot printed
	// shows their data as it was.
	Unfinished int
	Deadline   time.Duration
	// Finishing counts the PR inventory fetches that were running at the
	// deadline, which the refresh waited for, Overrun past it, up to
	// deadlineGrace times the deadline; Interrupted counts those cancelled
	// then, recorded as such in the inventory cache.
	Finishing   int
	Overrun     time.Duration
	Interrupted int
}

// add folds another batch's result into r.
func (r *BatchResult) add(o BatchResult) {
	r.Jobs += o.Jobs
	r.Dropped += o.Dropped
	r.Failed = append(r.Failed, o.Failed...)
	r.JiraSkipped += o.JiraSkipped
	if r.JiraSkipReason == "" {
		r.JiraSkipReason = o.JiraSkipReason
	}
	r.Unfinished += o.Unfinished
	if o.Deadline != 0 {
		r.Deadline = o.Deadline
	}
	r.Finishing += o.Finishing
	r.Overrun += o.Overrun
	r.Interrupted += o.Interrupted
	for k, n := range o.Skipped {
		if r.Skipped == nil {
			r.Skipped = map[JobKind]int{}
		}
		r.Skipped[k] += n
	}
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

// failedErr is Err for the failed jobs alone, leaving out the dropped ones.
func (r BatchResult) failedErr() error {
	return BatchResult{Jobs: r.Jobs, Failed: r.Failed}.Err()
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
	r := &Refresher{f: f, opts: opts, pending: map[string]*inflight{}, jira: newJiraPause(f.JiraLogin)}
	r.ready = sync.NewCond(&r.mu)
	r.ctx, r.cancel = context.WithCancel(context.Background())
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
// Workers take queued jobs by priority (jobRank), in submission order within
// a rank.
func (r *Refresher) Submit(jobs []Job) <-chan BatchResult {
	return r.submit(jobs).done
}

// submit is Submit, returning the batch so that its progress can be read.
func (r *Refresher) submit(jobs []Job) *batch {
	b := &batch{done: make(chan BatchResult, 1), progressed: make(chan struct{}, 1)}
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
				b.accept()
				f.waiters = append(f.waiters, b)
			}
			continue
		}
		if r.queued >= r.opts.Queue {
			b.res.Dropped++
			continue
		}
		f := &inflight{job: j, waiters: []*batch{b}}
		// Workers take jobs under r.mu, so this Add lands first.
		b.accept()
		r.pending[k] = f
		rank := jobRank(j)
		r.queue[rank] = append(r.queue[rank], f)
		r.queued++
		r.ready.Signal()
	}
	r.mu.Unlock()
	go func() {
		b.wg.Wait()
		b.mu.Lock()
		res := b.res
		b.mu.Unlock()
		b.done <- res
		close(b.done)
	}()
	return b
}

func (r *Refresher) supports(j Job) bool {
	switch j.Kind {
	case JobPR:
		return r.f.PR != nil
	case JobJira:
		return r.f.Jira != nil
	case JobIssue:
		return r.f.Issue != nil
	case JobInventory, JobIdentity:
		return r.f.Inventory != nil
	case JobSprints:
		return r.f.Sprints != nil
	case JobTimelines:
		return r.f.Timelines != nil
	case JobTimelinePrune:
		return true // local cache upkeep: no fetcher needed
	}
	return false
}

// errRefresherClosed fails a job that was still queued when Close was called.
var errRefresherClosed = errors.New("not run: the refresher was closed")

// errNotStarted ends a job whose batches had all passed their deadline
// before a worker took it.
var errNotStarted = errors.New("not run: refresh deadline reached")

// errInterrupted ends a job cancelled at its batch's hard deadline.
var errInterrupted = errors.New("interrupted at the refresh deadline")

// Close stops accepting jobs, fails the ones still queued without fetching
// them, cancels the running fetches (all but a bookmark's PR lookup stop:
// acli is killed, GitHub requests abandoned), and waits for the running
// ones, at most CloseWait when set. Letting the queue drain would hold
// shutdown hostage to every pending network call.
func (r *Refresher) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.ready.Broadcast()
	r.mu.Unlock()
	r.cancel()
	if r.opts.CloseWait <= 0 {
		r.wg.Wait()
		return
	}
	stopped := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(r.opts.CloseWait):
		logf("dash: leaving lookups that are still running after %v", r.opts.CloseWait)
	}
}

// next takes the most urgent queued job, waiting for one; nil once the
// Refresher is closed and the queue is empty. r.mu is held.
func (r *Refresher) next() *inflight {
	for r.queued == 0 && !r.closed {
		r.ready.Wait()
	}
	for rank := range r.queue {
		if q := r.queue[rank]; len(q) > 0 {
			f := q[0]
			q[0] = nil
			r.queue[rank] = q[1:]
			r.queued--
			return f
		}
	}
	return nil
}

func (r *Refresher) work() {
	defer r.wg.Done()
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		f := r.next()
		if f == nil {
			return
		}
		var err error
		switch {
		case r.closed:
			err = errRefresherClosed
		case f.expired():
			err = errNotStarted
		default:
			ctx, cancel := context.WithCancelCause(r.ctx)
			f.cancel = cancel
			r.mu.Unlock()
			err = r.run(ctx, f.job)
			if err != nil && errors.Is(context.Cause(ctx), errInterrupted) {
				err = fmt.Errorf("%w: %w", errInterrupted, err)
			}
			cancel(nil)
			r.mu.Lock()
		}
		delete(r.pending, f.job.key())
		waiters := f.waiters
		r.mu.Unlock()
		for _, b := range waiters {
			b.finish(f.job, err)
		}
		r.mu.Lock()
	}
}

// expire marks b past its deadline: its jobs that have not started will not,
// unless another batch waits on them, and its running jobs are cancelled,
// all but the PR inventory fetches (see interrupt). It returns how many of
// those run on.
func (r *Refresher) expire(b *batch) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	b.expired = true
	n := 0
	for _, f := range r.pending {
		switch {
		case f.cancel == nil || !slices.Contains(f.waiters, b):
		case f.job.Kind == JobInventory:
			n++
		case f.expired():
			f.cancel(errInterrupted)
		}
	}
	return n
}

// runningInventory counts the PR inventory fetches b waits on that are
// running.
func (r *Refresher) runningInventory(b *batch) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, f := range r.pending {
		if f.cancel != nil && f.job.Kind == JobInventory && slices.Contains(f.waiters, b) {
			n++
		}
	}
	return n
}

// interrupt cancels the running jobs that only expired batches wait on, b's
// among them, with errInterrupted. A PR inventory fetch records the
// interruption (inventory.ErrInterrupted).
func (r *Refresher) interrupt(b *batch) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range r.pending {
		if f.cancel != nil && slices.Contains(f.waiters, b) && f.expired() {
			f.cancel(fmt.Errorf("%w (%w)", errInterrupted, inventory.ErrInterrupted))
		}
	}
}

// run performs one job, behind the shared refresh lease unless IgnoreLeases
// is set. A held lease is not a failure: another refresher has the key. ctx
// is the job's: Close, or interrupt, cancels it.
func (r *Refresher) run(ctx context.Context, j Job) error {
	switch j.Kind {
	case JobPR:
		if r.opts.IgnoreLeases || prcache.LockRefresh(j.RemoteURL, j.RepoPath, j.Branch, r.opts.Backoff) {
			res := prcache.Resolve(r.f.PR, j.RemoteURL, j.RepoPath, j.Branch, prcache.Opts{Synchronous: true})
			return errors.Join(res.Err, res.WriteErr)
		}
	case JobJira:
		return r.jiraLookup(ctx, func() bool { return jiracache.LockRefresh(j.Ticket, r.opts.Backoff) }, func() error {
			_, _, err := jiracache.RefreshContext(ctx, r.f.Jira, j.Ticket)
			return err
		})
	case JobIssue:
		if r.opts.IgnoreLeases || issuecache.LockRefresh(j.Issue, r.opts.Backoff) {
			return issuecache.RefreshContext(ctx, r.f.Issue, j.Issue).Err
		}
	case JobInventory:
		if r.opts.IgnoreLeases {
			_, err := inventory.Refresh(ctx, r.f.Inventory, j.Inventory, j.Since)
			r.refreshStaleIdentity(ctx)
			return err
		}
		// A set that keeps failing (often on a late page, or rate limited)
		// waits out its back-off instead of refetching every lease window.
		if time.Now().Before(inventory.Read(j.Inventory, 0, j.Since, time.Now()).NotBefore()) {
			return nil
		}
		if inventory.LockRefresh(j.Inventory, r.opts.Backoff) {
			// Pages and retries can outlast the lease window; keep it held
			// so another wgo process does not fetch the set too.
			stop := inventory.KeepLease(j.Inventory, r.opts.Backoff/3)
			_, err := inventory.Refresh(ctx, r.f.Inventory, j.Inventory, j.Since)
			stop()
			r.refreshStaleIdentity(ctx)
			return err
		}
	case JobIdentity:
		if r.opts.IgnoreLeases || inventory.LockIdentityRefresh(r.opts.Backoff) {
			_, err := inventory.RefreshIdentity(ctx, r.f.Inventory)
			return err
		}
	case JobSprints:
		return r.jiraLookup(ctx, func() bool { return jiracache.LockSprintsRefresh(j.Board, r.opts.Backoff) }, func() error {
			return jiracache.RefreshSprintsContext(ctx, r.f.Sprints, j.Board, j.Assignees)
		})
	case JobTimelines:
		return r.runTimelines(ctx, j.Timelines)
	case JobTimelinePrune:
		if r.opts.IgnoreLeases || inventory.LockTimelinePrune(timelinePruneEvery) {
			seen := make([]inventory.TimelineKey, len(j.Timelines))
			for i, t := range j.Timelines {
				seen[i] = t.Key
			}
			n, err := inventory.PruneTimelines(seen, time.Now(), inventory.TimelineMaxUnseen)
			if n > 0 {
				logf("dash: pruned %d PR timelines not seen for %v", n, inventory.TimelineMaxUnseen)
			}
			return err
		}
	}
	return nil
}

// jiraLookup runs a Jira ticket or sprint lookup through the Jira pause:
// skipped while lookups are paused, behind its lease (taken by lease) unless
// IgnoreLeases is set. A lookup cut short by ctx tells the pause nothing.
func (r *Refresher) jiraLookup(ctx context.Context, lease func() bool, lookup func() error) error {
	ok, probe, reason := r.jira.admit()
	if !ok {
		return &pausedError{reason: reason}
	}
	if !r.opts.IgnoreLeases && !lease() {
		r.jira.release(probe)
		return nil
	}
	err := lookup()
	if err != nil && ctx.Err() != nil {
		r.jira.release(probe)
		return err
	}
	return r.jira.done(ctx, err, probe)
}

// timelinePruneEvery is how often cached PR timelines are pruned.
const timelinePruneEvery = 24 * time.Hour

// maxTransientTimelineFailures stops a timeline batch after this many fetches
// in a row failed with a server error or a timeout, each after its own
// retries: GitHub is struggling, and the rest can wait for a later cycle.
const maxTransientTimelineFailures = 3

// transientFailure reports whether err is a server error, a timeout or an
// incomplete response (github.Transient).
func transientFailure(err error) bool { return github.Transient(err) }

// runTimelines fetches each PR's history in turn, serially as GitHub asks of
// API clients. A PR already fetched for its updated_at is skipped; without
// IgnoreLeases, so is one backing off after failures or whose lease another
// refresher holds. A rate limit, maxTransientTimelineFailures server errors
// or timeouts in a row, or Close, stops the batch: the rest wait for a later
// cycle.
func (r *Refresher) runTimelines(ctx context.Context, reqs []TimelineRequest) error {
	var failed []string
	transient, stopped := 0, ""
	for _, req := range reqs {
		if ctx.Err() != nil || time.Now().Before(inventory.TimelinesPausedUntil()) {
			break
		}
		if transient >= maxTransientTimelineFailures {
			stopped = fmt.Sprintf("; stopped after %d server errors or timeouts in a row", transient)
			break
		}
		prior := inventory.ReadTimeline(req.Key)
		if prior.Current(req.UpdatedAt) {
			continue
		}
		if !r.opts.IgnoreLeases && (time.Now().Before(prior.NotBefore()) || !inventory.LockTimelineRefresh(req.Key, r.opts.Backoff)) {
			continue
		}
		_, err := inventory.RefreshTimeline(ctx, r.f.Timelines, req.Key)
		switch {
		case err == nil:
			transient = 0
		case ctx.Err() == nil:
			failed = append(failed, err.Error())
			if transientFailure(err) {
				transient++
			} else {
				transient = 0
			}
		}
	}
	if len(failed) == 0 {
		return nil
	}
	const maxListed = 2
	n, more := len(failed), ""
	if n > maxListed {
		more = fmt.Sprintf("; and %d more", n-maxListed)
		failed = failed[:maxListed]
	}
	return fmt.Errorf("%d of %d failed (%s%s)%s", n, len(reqs), strings.Join(failed, "; "), more, stopped)
}

// refreshStaleIdentity re-reads the authenticated login once its cache has
// aged past inventory.IdentityTTL. Collections queue JobIdentity only while
// the login is unknown, so a known one is kept current from here, beside the
// inventory refreshes that rely on it. A failure keeps the old login and is
// only logged: the inventory job it rides on did not fail.
func (r *Refresher) refreshStaleIdentity(ctx context.Context) {
	if _, st := inventory.ReadIdentity(inventory.IdentityTTL, time.Now()); st == inventory.Fresh {
		return
	}
	if !inventory.LockIdentityRefresh(r.opts.Backoff) {
		return
	}
	if _, err := inventory.RefreshIdentity(ctx, r.f.Inventory); err != nil {
		logf("dash: refresh GitHub identity: %v", err)
	}
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
		case JobInventory:
			label = "GitHub PR inventory"
		case JobIdentity:
			label = "GitHub identity"
		case JobSprints:
			label = "Jira sprint"
		case JobTimelines:
			label = "GitHub PR timeline"
		}
		p := fmt.Sprintf("%d %s lookups skipped", res.Skipped[k], label)
		if why := r.f.Missing[k]; why != "" {
			p += ": " + why
		}
		parts = append(parts, p)
	}
	if n := res.JiraSkipped; n > 0 {
		parts = append(parts, fmt.Sprintf("%s; %d Jira %s skipped", res.JiraSkipReason, n, plural(n, "lookup", "lookups")))
	}
	return strings.Join(parts, "; ")
}

// deadlineSummary says what a waited-for refresh left unfinished at its
// deadline, and which PR inventory fetches it finished past it, or nil. It
// is informational, never an error.
func (res BatchResult) deadlineSummary() []string {
	var out []string
	dl := FormatSeconds(res.Deadline)
	if n := res.Unfinished; n > 0 {
		out = append(out, fmt.Sprintf("not run: refresh deadline (%s) reached; %d %s left", dl, n, plural(n, "lookup", "lookups")))
	}
	n, cut := res.Finishing, res.Interrupted
	switch {
	case n == 0:
	case cut == 0:
		out = append(out, fmt.Sprintf("finished %d PR inventory %s started before the refresh deadline (%s), %s past it",
			n, plural(n, "fetch", "fetches"), dl, res.Overrun.Round(100*time.Millisecond)))
	case cut >= n:
		out = append(out, fmt.Sprintf("cancelled %d PR inventory %s started before the refresh deadline (%s) at %s, %d times the deadline (fetched again on the next refresh)",
			cut, plural(cut, "fetch", "fetches"), dl, FormatSeconds((1+deadlineGrace)*res.Deadline), 1+deadlineGrace))
	default:
		out = append(out, fmt.Sprintf("finished %d of %d PR inventory fetches started before the refresh deadline (%s); cancelled %d at %s, %d times the deadline (fetched again on the next refresh)",
			n-cut, n, dl, cut, FormatSeconds((1+deadlineGrace)*res.Deadline), 1+deadlineGrace))
	}
	return out
}

// FormatSeconds writes a duration as --refresh-timeout takes it: whole hours
// as "1h", whole seconds as "60s", any other as time.Duration does.
func FormatSeconds(d time.Duration) string {
	switch {
	case d > 0 && d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d%time.Second == 0:
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	}
	return d.String()
}
