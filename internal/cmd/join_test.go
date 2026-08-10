package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/virtru/wgo/internal/plan"
)

func TestPlanDescriptionForBranch(t *testing.T) {
	tests := []struct {
		name   string
		branch string
		ticket string
		setup  func(p *plan.Plan)
		want   string
	}{
		{
			name:   "finds description from an add.go-formatted reason in another repo",
			branch: "DSPX-2674-remove-volume-directive",
			ticket: "DSPX-2674",
			setup: func(p *plan.Plan) {
				p.AddBranch("platform", "DSPX-2674-remove-volume-directive", "DSPX-2674: remove volume directive")
			},
			want: "remove volume directive",
		},
		{
			name:   "ignores entries for a different branch",
			branch: "DSPX-2674-remove-volume-directive",
			ticket: "DSPX-2674",
			setup: func(p *plan.Plan) {
				p.AddBranch("platform", "other-branch", "DSPX-2674: remove volume directive")
			},
			want: "",
		},
		{
			name:   "ignores entries whose reason doesn't follow the ticket-prefixed convention",
			branch: "DSPX-2674-remove-volume-directive",
			ticket: "DSPX-2674",
			setup: func(p *plan.Plan) {
				// The state-annotation fallback: no "TICKET: " prefix, just the raw branch slug.
				p.AddBranch("platform", "DSPX-2674-remove-volume-directive", "DSPX-2674-remove-volume-directive")
			},
			want: "",
		},
		{
			name:   "empty ticket never matches",
			branch: "some-branch",
			ticket: "",
			setup: func(p *plan.Plan) {
				p.AddBranch("platform", "some-branch", ": desc")
			},
			want: "",
		},
		{
			name:   "no matching entry at all",
			branch: "some-branch",
			ticket: "DSPX-1",
			setup:  func(p *plan.Plan) {},
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := plan.Parse("")
			assert.NoError(t, err)
			tt.setup(p)
			assert.Equal(t, tt.want, planDescriptionForBranch(p, tt.branch, tt.ticket))
		})
	}
}
