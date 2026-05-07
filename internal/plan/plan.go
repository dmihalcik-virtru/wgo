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
	SpecPath  string // relative to repo root, e.g. "spec/WGO-101.md"
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

// activeBranchRe matches "- **repo:branch** — reason [📄 spec/path]".
// Group 1: repo, group 2: branch, group 3: reason, group 4: optional spec path.
var activeBranchRe = regexp.MustCompile(`\*\*([^:]+):([^\*]+)\*\*\s*(?:—|-)?\s*(.*?)\s*(?:📄\s+(\S+))?$`)

// parseActiveBranchLine parses a single line from the Active Branches section.
func (p *Plan) parseActiveBranchLine(line string) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "- ") {
		return
	}
	line = strings.TrimPrefix(line, "- ")

	if matches := activeBranchRe.FindStringSubmatch(line); len(matches) >= 4 {
		entry := BranchEntry{
			Repo:   matches[1],
			Branch: matches[2],
			Reason: matches[3],
		}
		if len(matches) >= 5 {
			entry.SpecPath = matches[4]
		}
		p.ActiveBranches[entry.Repo+":"+entry.Branch] = entry
		return
	}

	// Fallback for legacy format without ** delimiters: "repo:branch — reason".
	repo, rest, ok := strings.Cut(line, ":")
	if !ok {
		return
	}
	branch, reason, ok := strings.Cut(rest, "—")
	if !ok {
		return
	}
	repo = strings.Trim(repo, "* ")
	branch = strings.TrimSpace(branch)
	reason = strings.TrimSpace(reason)
	p.ActiveBranches[repo+":"+branch] = BranchEntry{
		Repo:   repo,
		Branch: branch,
		Reason: reason,
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
			line += " 📄 " + entry.SpecPath
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

// AddBranch adds or updates a branch entry. The optional specPath argument
// records the spec file path (relative to the repo root).
func (p *Plan) AddBranch(repo, branch, reason string, specPath ...string) {
	sp := ""
	if len(specPath) > 0 {
		sp = specPath[0]
	}
	p.ActiveBranches[repo+":"+branch] = BranchEntry{
		Repo:      repo,
		Branch:    branch,
		Reason:    reason,
		SpecPath:  sp,
		CreatedAt: time.Now(),
	}
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
