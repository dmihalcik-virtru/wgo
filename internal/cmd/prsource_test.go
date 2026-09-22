package cmd

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtru/wgo/internal/prcache"
	"github.com/virtru/wgo/models"
)

// TestPRLookupProvenance pins when a cache Result earns a provenance block on
// the context. The rule is narrow on purpose: only a failure over non-current
// data is worth telling the user about.
func TestPRLookupProvenance(t *testing.T) {
	fetched := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	refs := []models.PRRef{{Number: 42, State: "open"}}

	tests := []struct {
		name       string
		res        prcache.Result
		wantRefs   int
		wantLookup bool
		wantErr    string
		wantAt     bool
	}{
		{
			name:     "a fresh successful lookup is unremarkable",
			res:      prcache.Result{PRs: refs, State: prcache.Fresh, FetchedAt: fetched},
			wantRefs: 1,
		},
		{
			name:     "stale data with a refresh in flight is not a failure",
			res:      prcache.Result{PRs: refs, State: prcache.Stale, FetchedAt: fetched},
			wantRefs: 1,
		},
		{
			name:     "a branch with no PRs is not a failure",
			res:      prcache.Result{PRs: []models.PRRef{}, State: prcache.Fresh, FetchedAt: fetched},
			wantRefs: 0,
		},
		{
			name: "a failure over surviving refs reports both",
			res: prcache.Result{
				PRs: refs, State: prcache.Stale, FetchedAt: fetched,
				Err: errors.New("503 bad gateway"),
			},
			wantRefs:   1,
			wantLookup: true,
			wantErr:    "503 bad gateway",
			wantAt:     true,
		},
		{
			name: "a failure with nothing cached reports no fetch time",
			res: prcache.Result{
				State: prcache.Miss,
				Err:   errors.New("no GitHub credentials"),
			},
			wantLookup: true,
			wantErr:    "no GitHub credentials",
		},
		{
			name: "a failure a fresh success already superseded stays quiet",
			res: prcache.Result{
				PRs: refs, State: prcache.Fresh, FetchedAt: fetched,
				Err: errors.New("stale error"),
			},
			wantRefs: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRefs, gotLookup := prLookup(tt.res)
			assert.Len(t, gotRefs, tt.wantRefs)

			if !tt.wantLookup {
				assert.Nil(t, gotLookup)
				return
			}
			require.NotNil(t, gotLookup)
			assert.Equal(t, tt.wantErr, gotLookup.Error)
			if tt.wantAt {
				require.NotNil(t, gotLookup.FetchedAt)
				assert.True(t, fetched.Equal(*gotLookup.FetchedAt))
			} else {
				assert.Nil(t, gotLookup.FetchedAt)
			}
		})
	}
}
