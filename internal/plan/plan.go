// Package plan provides plan file parsing and rendering.
//
// The plan file is a human-edited markdown document. Parse keeps the raw
// lines of the whole file alongside the parsed view, and Render re-renders
// only what a caller actually changed: every byte the parser does not own
// (the preamble, unknown sections, duplicate sections, fenced code, stray text
// inside an effort block) is written back exactly as it was read.
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/bujo"
)

// Section describes a "## " section the parser does not own. It is
// informational: Render writes these sections from the raw file lines, so
// editing a Section value has no effect on the output.
type Section struct {
	Header  string // e.g., "## Custom Section"
	Content string // Raw lines below the header until the next section
	Index   int    // Position among all "## " sections in the file
}

// Plan represents the parsed plan file.
type Plan struct {
	ActiveBranches    map[string]BranchEntry // key: "repo:branch"
	Efforts           map[string]EffortEntry // key: stable effort ID
	Tasks             []bujo.Task
	Notes             string
	RawContent        string    // The content Parse was given
	UnmanagedSections []Section // Sections we don't parse/manage, in file order
	EffortOrder       []string  // Effort IDs in file order (first occurrence of each heading)
	// Diagnostics lists ambiguous or malformed content the parser kept
	// verbatim instead of interpreting: duplicate sections, duplicate effort
	// headings, malformed effort entries and stray text.
	Diagnostics []string

	diagLines []int     // 0-based file line of each diagnostic, for ordering
	doc       *document // raw file model; nil for plans built in code // raw file model; nil for plans built in code
}

// BranchEntry represents an entry in the Active Branches section.
type BranchEntry struct {
	Repo     string
	Branch   string
	Reason   string
	SpecPath string
	// Parents is the optional list of parent branch labels rendered after
	// the reason as "↳ on <parent>[, <parent>...]". Parsed tolerantly from
	// the plan file; the source of truth is state.json.
	Parents   []string
	CreatedAt time.Time
}

// EffortEntry represents an effort in the Efforts section.
type EffortEntry struct {
	ID          string // Stable ID (from state or generated)
	Name        string
	Description string
	Branches    []string // "repo:bookmark" format
	SpecPath    string
}

var effortIDSanitizer = regexp.MustCompile(`[^a-z0-9]+`)

// GenerateEffortID creates a stable ID from an effort name: the normalized
// name plus a short hash of the heading text alone, so editing the
// description keeps the ID and renaming the heading deliberately makes a new
// one.
func GenerateEffortID(name string) string {
	name = strings.TrimSpace(name)
	normalized := effortIDSanitizer.ReplaceAllString(strings.ToLower(name), "-")
	normalized = strings.Trim(normalized, "-")

	h := sha256.Sum256([]byte(name))
	return normalized + "-" + hex.EncodeToString(h[:])[:8]
}

// Parse parses plan file content. It never fails on malformed content:
// anything it cannot interpret is kept verbatim and reported in Diagnostics.
func Parse(content string) (*Plan, error) {
	p := &Plan{
		ActiveBranches: make(map[string]BranchEntry),
		Efforts:        make(map[string]EffortEntry),
		RawContent:     content,
	}
	p.doc = parseDocument(content, p)
	return p, nil
}

var branchLineRe = regexp.MustCompile(`\*\*([^:]+):([^\*]+)\*\*\s*(?:—|-)?\s*(.*)`)

// parseActiveBranchLine parses a single line from the Active Branches
// section. ok is false for lines that are not branch entries.
func parseActiveBranchLine(line string) (BranchEntry, bool) {
	line = strings.TrimSpace(line)

	// Format: "- **repo:branch** — description" or "- **repo:branch** — description 📄 spec/path.md"
	if !strings.HasPrefix(line, "- ") {
		return BranchEntry{}, false
	}
	line = strings.TrimPrefix(line, "- ")

	// Extract spec path if present (trailing 📄 ...)
	specPath := ""
	if idx := strings.Index(line, " 📄 "); idx != -1 {
		specPath = strings.TrimSpace(line[idx+len(" 📄 "):])
		line = strings.TrimSpace(line[:idx])
	}

	matches := branchLineRe.FindStringSubmatch(line)
	if len(matches) < 4 {
		// Alternate format: "repo:branch — reason"
		repo, rest, ok := strings.Cut(line, ":")
		if !ok {
			return BranchEntry{}, false
		}
		idx := strings.Index(rest, "—")
		if idx == -1 {
			return BranchEntry{}, false
		}
		return BranchEntry{
			Repo:     strings.Trim(repo, "* "),
			Branch:   strings.TrimSpace(rest[:idx]),
			Reason:   strings.TrimSpace(rest[idx+len("—"):]),
			SpecPath: specPath,
		}, true
	}

	reason, parents := splitParentSuffix(matches[3])
	return BranchEntry{
		Repo:     matches[1],
		Branch:   matches[2],
		Reason:   reason,
		SpecPath: specPath,
		Parents:  parents,
	}, true
}

// renderBranchLine renders one Active Branches entry.
func renderBranchLine(entry BranchEntry) string {
	line := fmt.Sprintf("- **%s:%s** — %s", entry.Repo, entry.Branch, entry.Reason)
	if len(entry.Parents) > 0 {
		line += " ↳ on " + strings.Join(entry.Parents, ", ")
	}
	if entry.SpecPath != "" {
		line += " 📄 " + entry.SpecPath
	}
	return line
}

// splitParentSuffix peels off a trailing "↳ on parent1, parent2" marker from
// the reason text and returns (cleanedReason, parents). Returns (reason, nil)
// when no marker is present, so the function is safe to call unconditionally.
func splitParentSuffix(reason string) (string, []string) {
	idx := strings.Index(reason, " ↳ on ")
	if idx < 0 {
		return reason, nil
	}
	tail := strings.TrimSpace(reason[idx+len(" ↳ on "):])
	if tail == "" {
		return strings.TrimSpace(reason[:idx]), nil
	}
	var parents []string
	for _, p := range strings.Split(tail, ",") {
		if p = strings.TrimSpace(p); p != "" {
			parents = append(parents, p)
		}
	}
	return strings.TrimSpace(reason[:idx]), parents
}

// Render renders the plan back to a string. Content the caller did not change
// is written back byte-for-byte; see the package documentation.
func (p *Plan) Render() string {
	return p.RenderWithPair("", nil)
}

// RenderWithPair renders the plan and, when pairDisplayName is set and
// activeWith is non-empty, inserts a generated "## Active With" section after
// Active Branches. The activeWith map should be pre-computed by the caller via
// FindActiveWithBranches.
func (p *Plan) RenderWithPair(pairDisplayName string, activeWith map[string]BranchEntry) string {
	var extra []string
	if len(activeWith) > 0 && pairDisplayName != "" {
		extra = append(extra, "## Active With "+pairDisplayName, "")
		for _, key := range sortedKeys(activeWith) {
			if key == "" {
				continue
			}
			entry := activeWith[key]
			entry.Parents = nil
			extra = append(extra, renderBranchLine(entry))
		}
		extra = append(extra, "")
	}
	return p.render(extra)
}

// FindActiveWithBranches returns branches from ActiveBranches whose spec frontmatter
// lists both myAuthor and pairAuthor in the authors field. specRootFinder maps
// repo display names to their local filesystem paths.
func (p *Plan) FindActiveWithBranches(myAuthor, pairAuthor string, specRootFinder func(repo string) string) map[string]BranchEntry {
	result := make(map[string]BranchEntry)
	if myAuthor == "" || pairAuthor == "" {
		return result
	}
	for key, entry := range p.ActiveBranches {
		if entry.SpecPath == "" {
			continue
		}
		repoRoot := specRootFinder(entry.Repo)
		if repoRoot == "" {
			continue
		}
		fullPath := repoRoot + "/" + entry.SpecPath
		authors := readSpecAuthors(fullPath)
		if containsAuthorPlan(authors, myAuthor) && containsAuthorPlan(authors, pairAuthor) {
			result[key] = entry
		}
	}
	return result
}

// readSpecAuthors reads the authors list from a spec file's YAML frontmatter.
// Returns nil on any error (file missing, parse error, etc.).
func readSpecAuthors(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// Extract frontmatter between first pair of --- delimiters.
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return nil
	}
	rest := content[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil
	}
	fm := rest[:end]
	// Parse authors: line from YAML using a minimal approach to avoid the yaml dependency.
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "authors:") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(line, "authors:"))
		// Expect YAML flow sequence: [a, b, c]
		val = strings.Trim(val, "[]")
		var authors []string
		for _, a := range strings.Split(val, ",") {
			a = strings.TrimSpace(a)
			if a != "" {
				authors = append(authors, a)
			}
		}
		return authors
	}
	return nil
}

func containsAuthorPlan(authors []string, author string) bool {
	for _, a := range authors {
		if strings.EqualFold(a, author) {
			return true
		}
	}
	return false
}

// AddBranch adds or updates a branch entry.
func (p *Plan) AddBranch(repo, branch, reason string, specPath ...string) {
	key := repo + ":" + branch
	sp := ""
	if len(specPath) > 0 {
		sp = specPath[0]
	}
	p.ActiveBranches[key] = BranchEntry{
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
