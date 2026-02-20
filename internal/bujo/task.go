// Package bujo provides bullet journal task tracking for wgo.
package bujo

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// BulletType represents the type of a bullet journal entry.
type BulletType string

const (
	BulletOpen       BulletType = "○" // todo
	BulletInProgress BulletType = "◉" // in progress
	BulletDone       BulletType = "✓" // done
	BulletCancelled  BulletType = "✗" // cancelled
	BulletMigrated   BulletType = "→" // deferred
	BulletPriority   BulletType = "!" // priority
	BulletNote       BulletType = "-" // note (passthrough)
)

// TaskRef holds a reference to a repo, branch, PR, or issue.
type TaskRef struct {
	Repo   string // repo name or path fragment
	Branch string // branch name (optional)
	PR     int    // GitHub PR number (optional, from #123 syntax)
	Issue  int    // GitHub Issue number (optional, from !123 syntax)
	URL    string // resolved GitHub URL, populated at display time
}

// Task represents a bullet journal task entry.
type Task struct {
	Bullet      BulletType
	Text        string
	Refs        []TaskRef
	CompletedAt time.Time
	Note        string
	Raw         string // original line for passthrough
}

// IsPending returns true if the task is actionable (open or in progress or priority).
func (t *Task) IsPending() bool {
	return t.Bullet == BulletOpen || t.Bullet == BulletInProgress || t.Bullet == BulletPriority
}

// IsDone returns true if the task is done or cancelled.
func (t *Task) IsDone() bool {
	return t.Bullet == BulletDone || t.Bullet == BulletCancelled
}

// refPattern matches task references in text:
//   - #repo:branch
//   - #repo#42 or #repo/42 (PR)
//   - !repo#7 or !repo/7 (issue)
//   - https://github.com/org/repo/pull/42
//   - https://github.com/org/repo/issues/7
var (
	refBranch  = regexp.MustCompile(`#([A-Za-z0-9_.-]+):([A-Za-z0-9_./:-]+)`)
	refPR      = regexp.MustCompile(`#([A-Za-z0-9_.-]+)[#/](\d+)`)
	refIssue   = regexp.MustCompile(`!([A-Za-z0-9_.-]+)[#/](\d+)`)
	refURLPR   = regexp.MustCompile(`https://github\.com/[^/]+/([^/]+)/pull/(\d+)`)
	refURLIssue = regexp.MustCompile(`https://github\.com/[^/]+/([^/]+)/issues/(\d+)`)
)

// ParseRefs extracts TaskRefs from a line of text.
func ParseRefs(text string) []TaskRef {
	var refs []TaskRef
	seen := make(map[string]bool)

	addRef := func(r TaskRef) {
		key := fmt.Sprintf("%s:%s:%d:%d", r.Repo, r.Branch, r.PR, r.Issue)
		if !seen[key] {
			seen[key] = true
			refs = append(refs, r)
		}
	}

	// URL PRs
	for _, m := range refURLPR.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[2])
		addRef(TaskRef{Repo: m[1], PR: n, URL: m[0]})
	}
	// URL issues
	for _, m := range refURLIssue.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[2])
		addRef(TaskRef{Repo: m[1], Issue: n, URL: m[0]})
	}
	// #repo:branch — must check before #repo#N to avoid false matches
	for _, m := range refBranch.FindAllStringSubmatch(text, -1) {
		addRef(TaskRef{Repo: m[1], Branch: m[2]})
	}
	// #repo#N or #repo/N (PR) — only if not already matched as branch
	for _, m := range refPR.FindAllStringSubmatch(text, -1) {
		// skip if this match overlaps a branch ref (contains ":")
		if strings.Contains(m[0], ":") {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		addRef(TaskRef{Repo: m[1], PR: n})
	}
	// !repo#N (issue)
	for _, m := range refIssue.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[2])
		addRef(TaskRef{Repo: m[1], Issue: n})
	}

	return refs
}

// bulletPrefixes maps Unicode bullet characters to BulletType.
var bulletPrefixes = []struct {
	prefix string
	btype  BulletType
}{
	{"○ ", BulletOpen},
	{"◉ ", BulletInProgress},
	{"✓ ", BulletDone},
	{"✗ ", BulletCancelled},
	{"→ ", BulletMigrated},
	{"! ", BulletPriority},
	{"x ", BulletDone}, // alternate done
	{"~ ", BulletCancelled}, // alternate cancelled
}

// ParseTask parses a single line into a Task.
// Returns nil if the line is not a recognized bullet entry.
func ParseTask(line string) *Task {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}

	for _, bp := range bulletPrefixes {
		if strings.HasPrefix(trimmed, bp.prefix) {
			text := strings.TrimPrefix(trimmed, bp.prefix)
			refs := ParseRefs(text)
			return &Task{
				Bullet: bp.btype,
				Text:   text,
				Refs:   refs,
				Raw:    line,
			}
		}
	}

	// Passthrough: not a recognized bullet
	return &Task{
		Bullet: BulletNote,
		Text:   trimmed,
		Raw:    line,
	}
}

// Render renders a task to a string line.
func (t *Task) Render() string {
	if t.Bullet == BulletNote {
		return t.Raw
	}
	return string(t.Bullet) + " " + t.Text
}

// MatchesPattern returns true if the task text contains the pattern (case-insensitive).
func (t *Task) MatchesPattern(pattern string) bool {
	return strings.Contains(strings.ToLower(t.Text), strings.ToLower(pattern))
}
