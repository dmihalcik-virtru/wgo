package status

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSince(t *testing.T) {
	now := time.Now()

	tests := []struct {
		input   string
		check   func(time.Time) bool
		wantErr bool
		desc    string
	}{
		{
			input: "1h",
			check: func(t time.Time) bool {
				diff := now.Sub(t)
				return diff >= 59*time.Minute && diff <= 61*time.Minute
			},
			desc: "1 hour ago",
		},
		{
			input: "30m",
			check: func(t time.Time) bool {
				diff := now.Sub(t)
				return diff >= 29*time.Minute && diff <= 31*time.Minute
			},
			desc: "30 minutes ago",
		},
		{
			input: "3d",
			check: func(t time.Time) bool {
				diff := now.Sub(t)
				return diff >= 71*time.Hour && diff <= 73*time.Hour
			},
			desc: "3 days ago",
		},
		{
			input: "1w",
			check: func(t time.Time) bool {
				diff := now.Sub(t)
				return diff >= 167*time.Hour && diff <= 169*time.Hour
			},
			desc: "1 week ago",
		},
		{
			input: "today",
			check: func(t time.Time) bool {
				y1, m1, d1 := now.Date()
				y2, m2, d2 := t.Date()
				return y1 == y2 && m1 == m2 && d1 == d2 && t.Hour() == 0 && t.Minute() == 0
			},
			desc: "today at midnight",
		},
		{
			input: "yesterday",
			check: func(t time.Time) bool {
				yesterday := now.AddDate(0, 0, -1)
				y1, m1, d1 := yesterday.Date()
				y2, m2, d2 := t.Date()
				return y1 == y2 && m1 == m2 && d1 == d2 && t.Hour() == 0 && t.Minute() == 0
			},
			desc: "yesterday at midnight",
		},
		{
			input: "",
			check: func(t time.Time) bool {
				return t.IsZero()
			},
			desc: "empty string returns zero time",
		},
		{
			input:   "abc",
			wantErr: true,
			desc:    "invalid input",
		},
		{
			input:   "5x",
			wantErr: true,
			desc:    "unknown unit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			result, err := ParseSince(tt.input)
			if tt.wantErr {
				assert.Error(t, err, "ParseSince(%q) expected error", tt.input)
				return
			}
			require.NoError(t, err, "ParseSince(%q)", tt.input)
			if tt.check != nil {
				assert.True(t, tt.check(result), "ParseSince(%q) = %v, check failed", tt.input, result)
			}
		})
	}
}
