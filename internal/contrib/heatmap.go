package contrib

import (
	"fmt"
	"strings"
	"time"
)

// intensity levels: 0=empty, 1-4
var levels = []string{"·", "░", "▒", "▓", "█"}

func intensity(n int) string {
	switch {
	case n == 0:
		return levels[0]
	case n <= 3:
		return levels[1]
	case n <= 7:
		return levels[2]
	case n <= 14:
		return levels[3]
	default:
		return levels[4]
	}
}

// RenderHeatmap renders a contributions heatmap to a string.
// weeks is the number of weeks to show (default 12).
func RenderHeatmap(total DayCount, weeks int, now time.Time) string {
	if weeks <= 0 {
		weeks = 12
	}

	// Build grid: rows = days of week (0=Sun..6=Sat), cols = weeks
	// Find the Sunday that starts `weeks` weeks ago
	startSunday := now.AddDate(0, 0, -int(now.Weekday())-7*(weeks-1))
	startSunday = time.Date(startSunday.Year(), startSunday.Month(), startSunday.Day(), 0, 0, 0, 0, now.Location())

	// Build week columns
	type week [7]string
	cols := make([]week, weeks)
	monthLabels := make([]string, weeks)

	for w := 0; w < weeks; w++ {
		sunday := startSunday.AddDate(0, 0, w*7)
		// Show month abbreviation in first week of month
		if sunday.Day() <= 7 {
			monthLabels[w] = sunday.Format("Jan")
		}
		for d := 0; d < 7; d++ {
			day := sunday.AddDate(0, 0, d)
			dateStr := day.Format("2006-01-02")
			n := total[dateStr]
			cols[w][d] = intensity(n)
		}
	}

	var b strings.Builder

	// Month labels row (2 chars per column: char + 1 space)
	b.WriteString("    ")
	prevMonth := ""
	for w := 0; w < weeks; w++ {
		lbl := monthLabels[w]
		if lbl != "" && lbl != prevMonth {
			// Write month abbreviation compressed to 2 chars
			b.WriteString(lbl[:2])
			prevMonth = lbl
		} else {
			b.WriteString("  ")
		}
	}
	b.WriteString("\n")

	// Day rows: Mon, Wed, Fri (indices 1, 3, 5)
	dayNames := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	showRows := []int{1, 3, 5} // Mon, Wed, Fri
	for _, d := range showRows {
		fmt.Fprintf(&b, "%-3s ", dayNames[d])
		for w := 0; w < weeks; w++ {
			b.WriteString(cols[w][d] + " ")
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString("· 0  ░ 1-3  ▒ 4-7  ▓ 8-14  █ 15+\n")

	return b.String()
}

// TopRepos returns up to n repos sorted by total commits descending.
func TopRepos(activities []RepoActivity, n int) []RepoActivity {
	// Simple insertion sort (small N)
	sorted := make([]RepoActivity, len(activities))
	copy(sorted, activities)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Total > sorted[j-1].Total; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	if n > len(sorted) {
		n = len(sorted)
	}
	return sorted[:n]
}
