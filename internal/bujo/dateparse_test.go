package bujo

import (
	"testing"
	"time"
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
		if err != nil {
			t.Errorf("ParseDateArg(%q): unexpected error: %v", tc.arg, err)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("ParseDateArg(%q): got %v, want %v", tc.arg, got, tc.want)
		}
	}

	// Error case
	_, err := ParseDateArg("invalid", now)
	if err == nil {
		t.Error("expected error for 'invalid'")
	}
}
