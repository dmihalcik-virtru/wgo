package dash

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRefreshReportsSkippedIntegrationsWithoutError(t *testing.T) {
	f := newFixture(t)
	r := NewRefresher(Fetchers{Missing: map[JobKind]string{
		JobPR:    "no GitHub token or gh",
		JobIssue: "no GitHub token or gh",
		JobJira:  "acli not on PATH",
	}}, RefresherOptions{Workers: 2, IgnoreLeases: true})
	defer r.Close()
	d, err := Open(Options{Dir: t.TempDir(), Collector: f.collector(t, time.Now(), memSource{}), Refresher: r})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true}); err != nil {
		t.Fatalf("skipped integrations must not be an error: %v", err)
	}
	v := d.Current()
	if !hasDiag(v, "GitHub PR lookups skipped: no GitHub token or gh") || !hasDiag(v, "Jira lookups skipped: acli not on PATH") {
		t.Fatalf("diagnostics = %v", v.Diagnostics)
	}
	for _, diag := range v.Diagnostics {
		if strings.Contains(diag, "failed") {
			t.Fatalf("skip reported as failure: %q", diag)
		}
	}

	// Submit counts the skips and keeps them out of Err.
	res := <-r.Submit([]Job{{Kind: JobPR, Branch: "a"}, {Kind: JobPR, Branch: "b"}, {Kind: JobJira, Ticket: "X-1"}})
	if res.Skipped[JobPR] != 2 || res.Skipped[JobJira] != 1 || res.Jobs != 0 || res.Err() != nil {
		t.Fatalf("batch = %+v", res)
	}

	// The next refresh with working fetchers clears the diagnostic.
	bf := &blockingFetchers{release: make(chan struct{})}
	close(bf.release)
	r2 := NewRefresher(Fetchers{PR: bf, Jira: bf, Issue: bf}, RefresherOptions{Workers: 2, IgnoreLeases: true})
	defer r2.Close()
	d.opts.Refresher = r2
	if err := d.Refresh(context.Background(), RefreshOptions{Remote: true, Wait: true}); err != nil {
		t.Fatal(err)
	}
	if v := d.Current(); len(v.Diagnostics) != 0 {
		t.Fatalf("skip diagnostic not cleared: %v", v.Diagnostics)
	}
}
