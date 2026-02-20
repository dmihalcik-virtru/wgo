package bujo

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseDateArg parses flexible date arguments:
//   - "today" → today
//   - "yesterday" → yesterday
//   - "-3d" or "-3" → 3 days ago
//   - "YYYY-MM-DD" → that date
func ParseDateArg(arg string, now time.Time) (time.Time, error) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	switch strings.ToLower(arg) {
	case "", "today":
		return today, nil
	case "yesterday":
		return today.AddDate(0, 0, -1), nil
	}

	// "-Nd" or "-N"
	if strings.HasPrefix(arg, "-") {
		s := strings.TrimPrefix(arg, "-")
		s = strings.TrimSuffix(s, "d")
		n, err := strconv.Atoi(s)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid date offset %q: %w", arg, err)
		}
		return today.AddDate(0, 0, -n), nil
	}

	// YYYY-MM-DD
	t, err := time.ParseInLocation("2006-01-02", arg, now.Location())
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date %q: expected YYYY-MM-DD", arg)
	}
	return t, nil
}

// FormatDate formats a time as YYYY-MM-DD.
func FormatDate(t time.Time) string {
	return t.Format("2006-01-02")
}
