package bujo

import (
	"fmt"
	"strings"
	"time"
)

// DailyLog represents the log file for a single day.
type DailyLog struct {
	Date      time.Time
	Completed []LogEntry
	Cancelled []LogEntry
	Events    []BranchEvent
}

// LogEntry represents a completed or cancelled task entry in the daily log.
type LogEntry struct {
	Time   time.Time
	Text   string
	Branch string
	Note   string
}

// BranchEvent represents a git branch event recorded during the day.
type BranchEvent struct {
	Time   time.Time
	Kind   string // "created", "pushed"
	Repo   string
	Branch string
}

// ParseDailyLog parses a daily log markdown file.
func ParseDailyLog(content string, date time.Time) (*DailyLog, error) {
	log := &DailyLog{Date: date}

	lines := strings.Split(content, "\n")
	var section string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "## ") {
			section = strings.TrimPrefix(trimmed, "## ")
			continue
		}

		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		item := strings.TrimPrefix(trimmed, "- ")

		switch section {
		case "Completed":
			e := parseLogEntry(item)
			log.Completed = append(log.Completed, e)
		case "Cancelled":
			e := parseLogEntry(item)
			log.Cancelled = append(log.Cancelled, e)
		case "Branch Events":
			e := parseBranchEvent(item)
			if e != nil {
				log.Events = append(log.Events, *e)
			}
		}
	}

	return log, nil
}

// parseLogEntry parses a log entry line like:
// ✓ [10:32] Task text #repo:branch — note
func parseLogEntry(s string) LogEntry {
	e := LogEntry{}

	// Strip leading bullet
	for _, prefix := range []string{"✓ ", "✗ "} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			break
		}
	}

	// Parse [HH:MM] time
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end > 0 {
			timeStr := s[1:end]
			s = strings.TrimSpace(s[end+1:])
			// Parse HH:MM relative to zero time
			t, err := time.Parse("15:04", timeStr)
			if err == nil {
				e.Time = t
			}
		}
	}

	// Split on " — " for note
	if idx := strings.Index(s, " — "); idx != -1 {
		e.Note = strings.TrimSpace(s[idx+3:])
		s = strings.TrimSpace(s[:idx])
	}

	e.Text = s
	return e
}

// parseBranchEvent parses a branch event line like:
// 🌿 [08:45] Created feature/auth-oauth in auth-service
func parseBranchEvent(s string) *BranchEvent {
	e := &BranchEvent{}

	// Strip emoji prefix
	for _, prefix := range []string{"🌿 ", "📤 "} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			break
		}
	}

	// Parse [HH:MM] time
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end > 0 {
			timeStr := s[1:end]
			s = strings.TrimSpace(s[end+1:])
			t, err := time.Parse("15:04", timeStr)
			if err == nil {
				e.Time = t
			}
		}
	}

	// "Created branch in repo" or "Pushed repo:branch"
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "created ") {
		e.Kind = "created"
		rest := s[len("created "):]
		if idx := strings.Index(rest, " in "); idx != -1 {
			e.Branch = strings.TrimSpace(rest[:idx])
			e.Repo = strings.TrimSpace(rest[idx+4:])
		} else {
			e.Branch = rest
		}
	} else if strings.HasPrefix(lower, "pushed ") {
		e.Kind = "pushed"
		rest := s[len("pushed "):]
		if idx := strings.Index(rest, ":"); idx != -1 {
			e.Repo = rest[:idx]
			e.Branch = rest[idx+1:]
		} else {
			e.Branch = rest
		}
	} else {
		return nil
	}

	return e
}

// Render renders the daily log to markdown.
func (dl *DailyLog) Render() string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("# %s\n", FormatDate(dl.Date)))

	if len(dl.Completed) > 0 {
		b.WriteString("\n## Completed\n")
		for _, e := range dl.Completed {
			b.WriteString(dl.renderLogEntry("✓", e))
		}
	}

	if len(dl.Cancelled) > 0 {
		b.WriteString("\n## Cancelled\n")
		for _, e := range dl.Cancelled {
			b.WriteString(dl.renderLogEntry("✗", e))
		}
	}

	if len(dl.Events) > 0 {
		b.WriteString("\n## Branch Events\n")
		for _, ev := range dl.Events {
			b.WriteString(dl.renderBranchEvent(ev))
		}
	}

	return b.String()
}

func (dl *DailyLog) renderLogEntry(bullet string, e LogEntry) string {
	timeStr := ""
	if !e.Time.IsZero() {
		timeStr = fmt.Sprintf("[%s] ", e.Time.Format("15:04"))
	}
	line := fmt.Sprintf("- %s %s%s", bullet, timeStr, e.Text)
	if e.Note != "" {
		line += " — " + e.Note
	}
	return line + "\n"
}

func (dl *DailyLog) renderBranchEvent(ev BranchEvent) string {
	timeStr := ""
	if !ev.Time.IsZero() {
		timeStr = fmt.Sprintf("[%s] ", ev.Time.Format("15:04"))
	}
	emoji := "🌿"
	if ev.Kind == "pushed" {
		emoji = "📤"
	}
	var desc string
	switch ev.Kind {
	case "created":
		desc = fmt.Sprintf("Created %s in %s", ev.Branch, ev.Repo)
	case "pushed":
		desc = fmt.Sprintf("Pushed %s:%s", ev.Repo, ev.Branch)
	default:
		desc = ev.Branch
	}
	return fmt.Sprintf("- %s %s%s\n", emoji, timeStr, desc)
}

// AddCompleted appends a completed entry to the log.
func (dl *DailyLog) AddCompleted(text, note string) {
	dl.Completed = append(dl.Completed, LogEntry{
		Time: time.Now(),
		Text: text,
		Note: note,
	})
}

// AddCancelled appends a cancelled entry to the log.
func (dl *DailyLog) AddCancelled(text, note string) {
	dl.Cancelled = append(dl.Cancelled, LogEntry{
		Time: time.Now(),
		Text: text,
		Note: note,
	})
}

// AddBranchEvent appends a branch event to the log.
func (dl *DailyLog) AddBranchEvent(kind, repo, branch string) {
	dl.Events = append(dl.Events, BranchEvent{
		Time:   time.Now(),
		Kind:   kind,
		Repo:   repo,
		Branch: branch,
	})
}
