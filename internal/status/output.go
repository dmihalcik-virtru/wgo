package status

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/virtru/wgo/models"
)

// RenderTable writes activities as a formatted table.
func RenderTable(w io.Writer, activities []models.RepoActivity, verbose bool) {
	if verbose {
		fmt.Fprintf(w, "  %-3s %-18s %-20s %-10s %-10s %-8s %-8s %-14s %-30s %s\n",
			"#", "REPO", "BRANCH", "STATUS", "CHANGES", "COMMITS", "LINES", "ACTIVITY", "WHY", "PATH")
	} else {
		fmt.Fprintf(w, "  %-3s %-18s %-20s %-10s %-10s %-8s %s\n",
			"#", "REPO", "BRANCH", "STATUS", "CHANGES", "COMMITS", "ACTIVITY")
	}

	for i, a := range activities {
		var name string
		if a.IsWorktree {
			name = " +- " + truncate(a.Name, 14)
		} else {
			name = truncate(a.Name, 18)
		}
		branch := truncate(a.Branch, 20)
		state := string(a.State)
		changes := formatChanges(a.Status)
		commits := fmt.Sprintf("%d", a.RecentCommits)
		activity := formatTimeSince(a.LastActivity)

		if verbose {
			lines := formatLines(a.DiffStat)
			why := truncate(a.Annotation, 30)
			if why == "" {
				why = "—"
			}
			fmt.Fprintf(w, "  %-3d %-18s %-20s %-10s %-10s %-8s %-8s %-14s %-30s %s\n",
				i+1, name, branch, state, changes, commits, lines, activity, why, a.Path)
		} else {
			fmt.Fprintf(w, "  %-3d %-18s %-20s %-10s %-10s %-8s %s\n",
				i+1, name, branch, state, changes, commits, activity)
		}
	}
}

// RenderJSON writes activities as JSON.
func RenderJSON(w io.Writer, activities []models.RepoActivity) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(activities)
}

// RenderCSV writes activities as CSV.
func RenderCSV(w io.Writer, activities []models.RepoActivity, verbose bool) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	header := []string{"#", "repo", "branch", "status", "changes", "commits", "activity"}
	if verbose {
		header = append(header, "lines", "why", "path")
	}
	if err := cw.Write(header); err != nil {
		return err
	}

	for i, a := range activities {
		row := []string{
			fmt.Sprintf("%d", i+1),
			a.Name,
			a.Branch,
			string(a.State),
			formatChanges(a.Status),
			fmt.Sprintf("%d", a.RecentCommits),
			formatTimeSince(a.LastActivity),
		}
		if verbose {
			row = append(row,
				formatLines(a.DiffStat),
				a.Annotation,
				a.Path,
			)
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}

	return nil
}

// RenderWatchHeader writes the watch mode header.
func RenderWatchHeader(w io.Writer, activities []models.RepoActivity, since string, sortBy string) {
	now := time.Now().Format("15:04:05")

	var modified, clean, stale int
	for _, a := range activities {
		switch a.State {
		case models.StateModified, models.StateStaged, models.StateConflict:
			modified++
		case models.StateClean:
			clean++
		case models.StateStale:
			stale++
		}
	}

	fmt.Fprintf(w, "wgo status — Updated: %s\n", now)
	fmt.Fprintf(w, "Total: %d | Modified: %d | Clean: %d | Stale: %d\n",
		len(activities), modified, clean, stale)

	parts := []string{}
	if since != "" {
		parts = append(parts, fmt.Sprintf("Since: %s", since))
	}
	parts = append(parts, fmt.Sprintf("Sort: %s", sortBy))
	fmt.Fprintf(w, "%s\n\n", strings.Join(parts, " | "))
}

// formatChanges creates a compact change summary like "3M 1U".
func formatChanges(s models.GitStatus) string {
	if s.Modified == 0 && s.Added == 0 && s.Deleted == 0 && s.Untracked == 0 && s.Staged == 0 {
		return "-"
	}

	var parts []string
	if s.Modified > 0 {
		parts = append(parts, fmt.Sprintf("%dM", s.Modified))
	}
	if s.Added > 0 {
		parts = append(parts, fmt.Sprintf("%dA", s.Added))
	}
	if s.Deleted > 0 {
		parts = append(parts, fmt.Sprintf("%dD", s.Deleted))
	}
	if s.Untracked > 0 {
		parts = append(parts, fmt.Sprintf("%dU", s.Untracked))
	}
	return strings.Join(parts, " ")
}

// formatLines creates a compact line change summary like "+10 -3".
func formatLines(d models.DiffStat) string {
	if d.Insertions == 0 && d.Deletions == 0 {
		return "-"
	}
	return fmt.Sprintf("+%d -%d", d.Insertions, d.Deletions)
}

// formatTimeSince returns a human-friendly relative time string.
func formatTimeSince(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}

	diff := time.Since(t)

	if diff < time.Minute {
		return "just now"
	}
	if diff < time.Hour {
		m := int(diff.Minutes())
		if m == 1 {
			return "1 min ago"
		}
		return fmt.Sprintf("%d mins ago", m)
	}
	if diff < 24*time.Hour {
		h := int(diff.Hours())
		if h == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	}

	days := int(diff.Hours() / 24)
	if days == 1 {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", days)
}

// truncate shortens a string to maxLen, appending "…" if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return s[:maxLen]
	}
	return s[:maxLen-1] + "…"
}
