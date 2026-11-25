package dash

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/jiracache"
)

type countingJiraBatch struct {
	mu    sync.Mutex
	sizes []int
}

func (f *countingJiraBatch) FetchJira(ticket string) (jiracache.Info, error) {
	return jiracache.Info{}, fmt.Errorf("single fetch called for %s", ticket)
}

func (f *countingJiraBatch) FetchJiraBatchContext(_ context.Context, tickets []string) (map[string]jiracache.FetchOutcome, error) {
	f.mu.Lock()
	f.sizes = append(f.sizes, len(tickets))
	f.mu.Unlock()
	out := make(map[string]jiracache.FetchOutcome, len(tickets))
	for _, ticket := range tickets {
		out[ticket] = jiracache.FetchOutcome{Info: jiracache.Info{Summary: "from bulkfetch", Status: "In Progress"}}
	}
	return out, nil
}

func TestJiraRefreshBatchesTicketJobs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fetcher := &countingJiraBatch{}
	r := NewRefresher(Fetchers{Jira: fetcher}, RefresherOptions{Workers: 2, IgnoreLeases: true})
	defer r.Close()
	jobs := make([]Job, 45)
	for i := range jobs {
		jobs[i] = Job{Kind: JobJira, Ticket: fmt.Sprintf("TEAM-%d", i+1)}
	}
	res := <-r.Submit(jobs)
	require.NoError(t, res.Err())
	fetcher.mu.Lock()
	sizes := append([]int(nil), fetcher.sizes...)
	fetcher.mu.Unlock()
	assert.LessOrEqual(t, len(sizes), 3, "45 tickets should need at most three bulk requests")
	for _, n := range sizes {
		assert.LessOrEqual(t, n, 20)
	}
	for _, job := range jobs {
		info, state := jiracache.Read(job.Ticket, time.Hour)
		assert.Equal(t, jiracache.Fresh, state)
		assert.Equal(t, "from bulkfetch", info.Summary)
	}
}
