// Package plan provides plan file parsing and rendering.
package plan

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Plan represents the parsed plan file.
type Plan struct {
	ActiveBranches map[string]BranchEntry // key: "repo:branch"
	Efforts        map[string]EffortEntry
	Notes          string
	RawContent     string // For preserving manual edits
}

// BranchEntry represents an entry in the Active Branches section.
type BranchEntry struct {
	Repo      string
	Branch    string
	Reason    string
	CreatedAt time.Time
}

// EffortEntry represents an effort in the Efforts section.
type EffortEntry struct {
	Name        string
	Description string
	Branches    []string // "repo:branch" format
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
	re := regexp.MustCompile(`\*\*([^:]+):([^\*]+)\*\*\s*(?:—|-)?\s*(.*)`)
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
					reason := strings.TrimSpace(rest[idx+1:])

					key := repo + ":" + branch
					p.ActiveBranches[key] = BranchEntry{
						Repo:   repo,
						Branch: branch,
						Reason: reason,
					}
				}
			}
		}
		return
	}

	repo := matches[1]
	branch := matches[2]
	reason := matches[3]

	key := repo + ":" + branch
	p.ActiveBranches[key] = BranchEntry{
		Repo:   repo,
		Branch: branch,
		Reason: reason,
	}
}

// Render renders the plan back to a string.
func (p *Plan) Render() string {
	var buf strings.Builder

	buf.WriteString("# Plan\n\n")
	buf.WriteString("## Active Branches\n\n")

	// Write active branches
	for key, entry := range p.ActiveBranches {
		if key == "" {
			continue
		}
		buf.WriteString(fmt.Sprintf("- **%s:%s** — %s\n", entry.Repo, entry.Branch, entry.Reason))
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
func (p *Plan) AddBranch(repo, branch, reason string) {
	key := repo + ":" + branch
	p.ActiveBranches[key] = BranchEntry{
		Repo:      repo,
		Branch:    branch,
		Reason:    reason,
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
