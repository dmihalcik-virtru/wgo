// Package status provides parallel status collection and dashboard rendering.
package status

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseSince converts a human-friendly duration string to an absolute time.
// Supported formats: "1h", "30m", "3d", "1w", "today", "yesterday".
func ParseSince(s string) (time.Time, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return time.Time{}, nil
	}

	now := time.Now()

	switch s {
	case "today":
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), nil
	case "yesterday":
		y, m, d := now.AddDate(0, 0, -1).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), nil
	}

	// Parse numeric durations like "1h", "30m", "3d", "1w"
	if len(s) < 2 {
		return time.Time{}, fmt.Errorf("invalid duration: %q", s)
	}

	unit := s[len(s)-1]
	numStr := s[:len(s)-1]
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid duration: %q", s)
	}

	switch unit {
	case 'm':
		return now.Add(-time.Duration(n) * time.Minute), nil
	case 'h':
		return now.Add(-time.Duration(n) * time.Hour), nil
	case 'd':
		return now.AddDate(0, 0, -n), nil
	case 'w':
		return now.AddDate(0, 0, -n*7), nil
	default:
		return time.Time{}, fmt.Errorf("unknown duration unit %q in %q (use m, h, d, or w)", string(unit), s)
	}
}
