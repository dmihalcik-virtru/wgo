package bujo

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDateArg(t *testing.T) {
	now := time.Date(2026, 2, 20, 10, 0, 0, 0, time.UTC)
	today := time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		arg  string
		want time.Time
	}{
		{"today", today},
		{"", today},
		{"yesterday", today.AddDate(0, 0, -1)},
		{"-3d", today.AddDate(0, 0, -3)},
		{"-3", today.AddDate(0, 0, -3)},
		{"2026-02-15", time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)},
	}

	for _, tc := range tests {
		got, err := ParseDateArg(tc.arg, now)
		require.NoError(t, err, "ParseDateArg(%q)", tc.arg)
		assert.True(t, got.Equal(tc.want), "ParseDateArg(%q): got %v, want %v", tc.arg, got, tc.want)
	}

	// Error case
	_, err := ParseDateArg("invalid", now)
	assert.Error(t, err, "expected error for 'invalid'")
}
