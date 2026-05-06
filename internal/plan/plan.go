// Package plan provides plan file parsing and rendering.
package plan

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/bujo"
)

// Plan represents the parsed plan file.
type Plan struct {
	ActiveBranches map[string]BranchEntry // key: "repo:branch"
	Efforts        map[string]EffortEntry
	Tasks          []bujo.Task
	Notes          string
	RawContent     string // For preserving manual edits
}

// BranchEntry represents an entry in the Active Branches section.
type BranchEntry struct {
	Repo      string
	Branch    string
	Reason    string
	SpecPath  string
	CreatedAt time.Time
}

// EffortEntry represents an effort in the Efforts section.
type EffortEntry struct {
	Name        string
	Description string
	Branches    []string // "repo:branch" format
	SpecPath    string
}

// Parse parses plan file content.
func Parse(content string) (*Plan, error) {
	plan := &Plan{
		ActiveBranches: make(map[string]BranchEntry),
		Efforts:        make(map[string]EffortEntry),
		RawContent:     content,
	}

	if err := plan.parseSections(content); err != nil {
		return nil, err
	}

	return plan, nil
}

// parseSections parses the various sections of the plan file.
func (p *Plan) parseSections(content string) error {
	lines := strings.Split(content, "\n")

	var currentSection string
	var sectionStart int
	var notesLines []string

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check for section headers
		if strings.HasPrefix(trimmed, "## ") {
			// Save previous section if needed
			if currentSection == "Notes" && sectionStart < i {
				notesLines = append(notesLines, lines[sectionStart:i]...)
			}

			sectionName := strings.TrimPrefix(trimmed, "## ")
			currentSection = sectionName
			sectionStart = i + 1
		} else if currentSection == "Active Branches" {
			p.parseActiveBranchLine(line)
		} else if currentSection == "Tasks" {
			if task := bujo.ParseTask(line); task != nil && task.Text != "" {
				p.Tasks = append(p.Tasks, *task)
			}
		}
	}

	// Handle remaining notes section
	if currentSection == "Notes" && sectionStart < len(lines) {
		notesLines = append(notesLines, lines[sectionStart:]...)
	}

	p.Notes = strings.TrimSpace(strings.Join(notesLines, "\n"))
	return nil
}

// parseActiveBranchLine parses a single line from the Active Branches section.
func (p *Plan) parseActiveBranchLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	// Format: "- **repo/branch** — description"
	if !strings.HasPrefix(line, "- ") {
		return
	}

	line = strings.TrimPrefix(line, "- ")

	// Extract branch info from **...**
	re := regexp.MustCompile(`\*\*([^:]+):([^\*]+)\*\*\s*(?:—|-)?\s*(.*?)(?:\s+📄\s+(\S+))?$`)
	matches := re.FindStringSubmatch(line)
	if len(matches) < 4 {
		// Try alternate format
		if strings.Contains(line, ":") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				repo := strings.Trim(parts[0], "* ")
				rest := parts[1]

				if idx := strings.Index(rest, "—"); idx != -1 {
					branch := strings.TrimSpace(rest[:idx])
					reason, specPath := parseReasonAndSpecPath(rest[idx+1:])

					key := repo + ":" + branch
					p.ActiveBranches[key] = BranchEntry{
						Repo:     repo,
						Branch:   branch,
						Reason:   reason,
						SpecPath: specPath,
					}
				}
			}
		}
		return
	}

	repo := matches[1]
	branch := matches[2]
	reason := matches[3]
	specPath := ""
	if len(matches) >= 5 {
		specPath = strings.TrimSpace(matches[4])
	}

	key := repo + ":" + branch
	p.ActiveBranches[key] = BranchEntry{
		Repo:     repo,
		Branch:   branch,
		Reason:   strings.TrimSpace(reason),
		SpecPath: specPath,
	}
}

// Render renders the plan back to a string.
func (p *Plan) Render() string {
	var buf strings.Builder

	buf.WriteString("# Plan\n\n")

	// Write tasks section if there are any
	if len(p.Tasks) > 0 {
		buf.WriteString("## Tasks\n\n")
		for _, task := range p.Tasks {
			buf.WriteString(task.Render() + "\n")
		}
		buf.WriteString("\n")
	}

	buf.WriteString("## Active Branches\n\n")

	// Write active branches
	for key, entry := range p.ActiveBranches {
		if key == "" {
			continue
		}
		line := fmt.Sprintf("- **%s:%s** — %s", entry.Repo, entry.Branch, entry.Reason)
		if entry.SpecPath != "" {
			line += fmt.Sprintf(" 📄 %s", entry.SpecPath)
		}
		buf.WriteString(line + "\n")
	}

	// Write efforts if any
	if len(p.Efforts) > 0 {
		buf.WriteString("\n## Efforts\n\n")
		for _, effort := range p.Efforts {
			buf.WriteString(fmt.Sprintf("### %s\n", effort.Name))
			if effort.Description != "" {
				buf.WriteString(effort.Description + "\n\n")
			}
			for _, branch := range effort.Branches {
				buf.WriteString(fmt.Sprintf("- %s\n", branch))
			}
			buf.WriteString("\n")
		}
	}

	// Write notes
	if p.Notes != "" {
		buf.WriteString("## Notes\n\n")
		buf.WriteString(p.Notes)
		if !strings.HasSuffix(p.Notes, "\n") {
			buf.WriteString("\n")
		}
	} else {
		buf.WriteString("## Notes\n")
	}

	return buf.String()
}

// AddBranch adds or updates a branch entry.
func (p *Plan) AddBranch(repo, branch, reason string, specPath ...string) {
	key := repo + ":" + branch
	existing := p.ActiveBranches[key]

	resolvedSpecPath := existing.SpecPath
	if len(specPath) > 0 {
		resolvedSpecPath = specPath[0]
	}

	createdAt := existing.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	p.ActiveBranches[key] = BranchEntry{
		Repo:      repo,
		Branch:    branch,
		Reason:    reason,
		SpecPath:  resolvedSpecPath,
		CreatedAt: createdAt,
	}
}

func parseReasonAndSpecPath(text string) (string, string) {
	text = strings.TrimSpace(text)
	re := regexp.MustCompile(`^(.*?)(?:\s+📄\s+(\S+))?$`)
	matches := re.FindStringSubmatch(text)
	if len(matches) < 2 {
		return text, ""
	}
	specPath := ""
	if len(matches) >= 3 {
		specPath = strings.TrimSpace(matches[2])
	}
	return strings.TrimSpace(matches[1]), specPath
}

// GetBranch retrieves a branch entry.
func (p *Plan) GetBranch(repo, branch string) *BranchEntry {
	key := repo + ":" + branch
	if entry, exists := p.ActiveBranches[key]; exists {
		return &entry
	}
	return nil
}

// RemoveBranch removes a branch entry.
func (p *Plan) RemoveBranch(repo, branch string) {
	key := repo + ":" + branch
	delete(p.ActiveBranches, key)
}

// AddTask appends a new task to the Tasks list.
func (p *Plan) AddTask(bullet bujo.BulletType, text string) {
	p.Tasks = append(p.Tasks, bujo.Task{
		Bullet: bullet,
		Text:   text,
		Refs:   bujo.ParseRefs(text),
	})
}

// RemoveTask removes the first task matching pattern and returns it (or nil if not found).
func (p *Plan) RemoveTask(pattern string) *bujo.Task {
	for i, t := range p.Tasks {
		if t.MatchesPattern(pattern) {
			removed := p.Tasks[i]
			p.Tasks = append(p.Tasks[:i], p.Tasks[i+1:]...)
			return &removed
		}
	}
	return nil
}

// UpdateTask updates the bullet type of the first task matching pattern.
func (p *Plan) UpdateTask(pattern string, bullet bujo.BulletType) *bujo.Task {
	for i, t := range p.Tasks {
		if t.MatchesPattern(pattern) {
			p.Tasks[i].Bullet = bullet
			updated := p.Tasks[i]
			return &updated
		}
	}
	return nil
}

// GetPendingTasks returns all tasks that are open, in-progress, or priority.
func (p *Plan) GetPendingTasks() []bujo.Task {
	var out []bujo.Task
	for _, t := range p.Tasks {
		if t.IsPending() {
			out = append(out, t)
		}
	}
	return out
}

// GetTasksForBranch returns tasks that reference the given repo:branch.
func (p *Plan) GetTasksForBranch(repo, branch string) []bujo.Task {
	var out []bujo.Task
	for _, t := range p.Tasks {
		for _, ref := range t.Refs {
			if ref.Repo == repo && ref.Branch == branch {
				out = append(out, t)
				break
			}
		}
	}
	return out
}
