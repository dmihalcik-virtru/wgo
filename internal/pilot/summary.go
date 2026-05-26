// Package pilot generates pilot workflow summary reports.
package pilot

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Options controls what data is collected and how output is rendered.
type Options struct {
	Since  time.Time
	Until  time.Time
	Team   []string // GitHub handles; empty = current user only
	Output string   // file path; "" = stdout
}

// Metrics holds all aggregated pilot data.
type Metrics struct {
	Since  time.Time
	Until  time.Time
	Team   []string
	Period string

	SpecsCreated      int
	SpecsUpdated      int
	PRsMerged         int
	DriftEventsCaught int
	PreCommitBlocks   int
	NoSpecOverrides   int

	SpecEditsByAuthor map[string]int // handle → count of spec commits
	SpecCycleTimes    []time.Duration
	ReviewIterations  []int

	Warnings []string
}

func (m *Metrics) MedianCycleTime() time.Duration {
	if len(m.SpecCycleTimes) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(m.SpecCycleTimes))
	copy(sorted, m.SpecCycleTimes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

func (m *Metrics) MedianReviewIter() float64 {
	if len(m.ReviewIterations) == 0 {
		return 0
	}
	sorted := make([]int, len(m.ReviewIterations))
	copy(sorted, m.ReviewIterations)
	sort.Ints(sorted)
	return float64(sorted[len(sorted)/2])
}

// Collect gathers all pilot metrics for the given repos over the options window.
// Errors from individual sources are collected as warnings rather than aborting.
func Collect(repos []string, logsDir string, opts Options) (*Metrics, error) {
	m := &Metrics{
		Since:             opts.Since,
		Until:             opts.Until,
		Team:              opts.Team,
		Period:            fmt.Sprintf("%s to %s", opts.Since.Format("2006-01-02"), opts.Until.Format("2006-01-02")),
		SpecEditsByAuthor: make(map[string]int),
	}

	sinceStr := opts.Since.Format("2006-01-02")
	untilStr := opts.Until.Format("2006-01-02")

	// Collect git-based metrics across all repos
	for _, repoRoot := range repos {
		if _, err := os.Stat(repoRoot); err != nil {
			continue
		}

		// Specs created (diff-filter=A means added)
		created, err := gitLogSpecFiles(repoRoot, sinceStr, untilStr, "--diff-filter=A")
		if err != nil {
			m.Warnings = append(m.Warnings, fmt.Sprintf("specs created (%s): %v", repoRoot, err))
		} else {
			m.SpecsCreated += len(created)
			for _, e := range created {
				m.SpecEditsByAuthor[e.author]++
			}
		}

		// Specs updated (all commits touching spec/*.md)
		updated, err := gitLogSpecFiles(repoRoot, sinceStr, untilStr, "")
		if err != nil {
			m.Warnings = append(m.Warnings, fmt.Sprintf("specs updated (%s): %v", repoRoot, err))
		} else {
			m.SpecsUpdated += len(updated)
		}

		// [no-spec] overrides
		count, err := gitCountNoSpec(repoRoot, sinceStr, untilStr)
		if err != nil {
			m.Warnings = append(m.Warnings, fmt.Sprintf("no-spec overrides (%s): %v", repoRoot, err))
		} else {
			m.NoSpecOverrides += count
		}
	}

	// Collect GitHub metrics for each team member
	for _, handle := range opts.Team {
		prs, err := ghListMergedPRs(handle, sinceStr, untilStr)
		if err != nil {
			m.Warnings = append(m.Warnings, fmt.Sprintf("PRs for %s: %v", handle, err))
			continue
		}
		m.PRsMerged += len(prs)

		for _, pr := range prs {
			iters, err := ghReviewIterations(pr.owner, pr.repo, pr.number)
			if err == nil {
				m.ReviewIterations = append(m.ReviewIterations, iters)
			}
		}
	}

	// Walk daily logs for drift events and pre-commit blocks
	if logsDir != "" {
		driftCount, blockCount, err := walkLogs(logsDir, opts.Since, opts.Until)
		if err != nil {
			m.Warnings = append(m.Warnings, fmt.Sprintf("daily logs: %v", err))
		} else {
			m.DriftEventsCaught = driftCount
			m.PreCommitBlocks = blockCount
		}
	}

	return m, nil
}

// specCommit holds minimal data from git log for spec file commits.
type specCommit struct {
	hash   string
	author string
	time   time.Time
}

// gitLogSpecFiles returns commits touching spec/*.md files in the given time window.
// extraFilter is optional (e.g., "--diff-filter=A").
func gitLogSpecFiles(repoRoot, since, until, extraFilter string) ([]specCommit, error) {
	args := []string{"log", "--format=%H\x1f%ae\x1f%aI"}
	if extraFilter != "" {
		args = append(args, extraFilter)
	}
	args = append(args,
		"--since="+since,
		"--until="+until,
		"--", "spec/*.md",
	)
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var result []specCommit
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) < 3 {
			continue
		}
		t, _ := time.Parse(time.RFC3339, parts[2])
		result = append(result, specCommit{hash: parts[0], author: parts[1], time: t})
	}
	return result, nil
}

// gitCountNoSpec counts commits containing "[no-spec]" in their message.
func gitCountNoSpec(repoRoot, since, until string) (int, error) {
	cmd := exec.Command("git", "log",
		"--grep=[no-spec]",
		"--fixed-strings",
		"--format=%H",
		"--since="+since,
		"--until="+until,
	)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count, nil
}

// mergedPR holds minimal merged PR data from gh.
type mergedPR struct {
	number int
	owner  string
	repo   string
}

// ghListMergedPRs returns merged PRs for a GitHub handle in the given date range.
func ghListMergedPRs(handle, since, until string) ([]mergedPR, error) {
	query := fmt.Sprintf("author:%s merged:>=%s merged:<=%s", handle, since, until)
	cmd := exec.Command("gh", "pr", "list",
		"--search", query,
		"--state", "merged",
		"--json", "number,repository",
		"--limit", "100",
	)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh pr list: %s", strings.TrimSpace(stderr.String()))
	}
	var items []struct {
		Number     int `json:"number"`
		Repository struct {
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"repository"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &items); err != nil {
		return nil, fmt.Errorf("parse gh output: %w", err)
	}
	result := make([]mergedPR, 0, len(items))
	for _, item := range items {
		owner, repo, _ := strings.Cut(item.Repository.NameWithOwner, "/")
		result = append(result, mergedPR{number: item.Number, owner: owner, repo: repo})
	}
	return result, nil
}

// ghReviewIterations counts distinct review round-trips on a PR.
// A round-trip is defined as at least one non-author review submission.
func ghReviewIterations(owner, repo string, number int) (int, error) {
	cmd := exec.Command("gh", "pr", "view", strconv.Itoa(number),
		"--repo", owner+"/"+repo,
		"--json", "reviews",
	)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return 0, err
	}
	var obj struct {
		Reviews []struct {
			State string `json:"state"`
		} `json:"reviews"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &obj); err != nil {
		return 0, err
	}
	count := 0
	for _, r := range obj.Reviews {
		if r.State == "CHANGES_REQUESTED" || r.State == "APPROVED" {
			count++
		}
	}
	return count, nil
}

// walkLogs scans daily log files for drift and pre-commit block markers.
func walkLogs(logsDir string, since, until time.Time) (driftCount, blockCount int, err error) {
	err = filepath.WalkDir(logsDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		// Parse date from filename (YYYY-MM-DD.md)
		datePart := strings.TrimSuffix(d.Name(), ".md")
		t, parseErr := time.ParseInLocation("2006-01-02", datePart, time.Local)
		if parseErr != nil {
			return nil
		}
		if t.Before(since) || t.After(until) {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		content := string(data)
		for _, line := range strings.Split(content, "\n") {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "drift detected") {
				driftCount++
			}
			if strings.Contains(lower, "pre-commit blocked") {
				blockCount++
			}
		}
		return nil
	})
	return driftCount, blockCount, err
}

// RenderMarkdown produces the pilot workflow summary as markdown.
func RenderMarkdown(m *Metrics, opts Options) string {
	var b strings.Builder

	teamLabel := strings.Join(opts.Team, " + ")
	if teamLabel == "" {
		teamLabel = "your team"
	}

	periodMonth := m.Since.Format("January 2006")

	fmt.Fprintf(&b, "# %s Pilot Workflow Summary — %s\n\n", periodMonth, teamLabel)
	fmt.Fprintf(&b, "_Period: %s_\n", m.Period)
	fmt.Fprintf(&b, "_Generated: %s_\n\n", time.Now().Format("2006-01-02 15:04 MST"))

	b.WriteString("## How we structured pairing\n")
	if len(m.SpecEditsByAuthor) > 0 {
		var authors []string
		for a := range m.SpecEditsByAuthor {
			authors = append(authors, a)
		}
		sort.Strings(authors)
		var parts []string
		for _, a := range authors {
			parts = append(parts, fmt.Sprintf("%s %d specs", a, m.SpecEditsByAuthor[a]))
		}
		fmt.Fprintf(&b, "- Spec authorship distribution: %s\n", strings.Join(parts, ", "))
	}
	b.WriteString("- > _your notes here: live pairing? async? rotation pattern?_\n\n")

	b.WriteString("## Spec → implementation handoff\n")
	if len(m.SpecCycleTimes) > 0 {
		med := m.MedianCycleTime()
		fmt.Fprintf(&b, "- Median time from spec creation to first impl commit: %s\n", formatDuration(med))
	}
	fmt.Fprintf(&b, "- %d specs created; %d commits to spec files total\n", m.SpecsCreated, m.SpecsUpdated)
	b.WriteString("- > _your notes here: how the handoff felt; tooling friction_\n\n")

	b.WriteString("## What worked\n")
	b.WriteString("- > _your notes here_\n\n")

	b.WriteString("## What we'd do differently\n")
	b.WriteString("- > _your notes here_\n\n")

	b.WriteString("## Metrics\n")
	b.WriteString("| Metric | Value |\n")
	b.WriteString("|---|---|\n")
	fmt.Fprintf(&b, "| Specs created | %d |\n", m.SpecsCreated)
	fmt.Fprintf(&b, "| Specs updated post-creation | %d |\n", m.SpecsUpdated)
	fmt.Fprintf(&b, "| PRs merged | %d |\n", m.PRsMerged)
	if m.MedianCycleTime() > 0 {
		fmt.Fprintf(&b, "| Median spec → PR cycle time | %s |\n", formatDuration(m.MedianCycleTime()))
	} else {
		b.WriteString("| Median spec → PR cycle time | n/a |\n")
	}
	if m.MedianReviewIter() > 0 {
		fmt.Fprintf(&b, "| Median review iterations per PR | %.0f |\n", m.MedianReviewIter())
	} else {
		b.WriteString("| Median review iterations per PR | n/a |\n")
	}
	fmt.Fprintf(&b, "| Drift events caught | %d |\n", m.DriftEventsCaught)
	fmt.Fprintf(&b, "| Pre-commit blocks | %d |\n", m.PreCommitBlocks)
	fmt.Fprintf(&b, "| `[no-spec]` overrides | %d |\n", m.NoSpecOverrides)

	b.WriteString("\n## Pilot checklist\n")
	b.WriteString("- [ ] Aligned on working style/schedule (recorded in spec frontmatter)\n")
	specFirstOK := m.SpecsCreated > 0
	checkmark(specFirstOK, &b, "Written and committed a spec before starting each work item")
	checkmark(true, &b, "Specs stored in /spec/ at repo root")
	checkmark(m.SpecsUpdated > m.SpecsCreated, &b, fmt.Sprintf("Specs updated when scope/approach changed (%d update events)", m.SpecsUpdated))
	b.WriteString("- [ ] Workflow summary submitted (this document)\n")

	if len(m.Warnings) > 0 {
		b.WriteString("\n---\n_Data collection warnings:_\n")
		for _, w := range m.Warnings {
			fmt.Fprintf(&b, "- _%s_\n", w)
		}
	}

	return b.String()
}

func checkmark(ok bool, b *strings.Builder, text string) {
	mark := " "
	if ok {
		mark = "x"
	}
	fmt.Fprintf(b, "- [%s] %s\n", mark, text)
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "n/a"
	}
	hours := int(d.Hours())
	if hours < 24 {
		return fmt.Sprintf("%dh %dm", hours, int(d.Minutes())%60)
	}
	days := hours / 24
	rem := hours % 24
	return fmt.Sprintf("%dd %dh", days, rem)
}

// RenderJSON serializes metrics as JSON.
func RenderJSON(m *Metrics) ([]byte, error) {
	type jsonOut struct {
		Period            string         `json:"period"`
		Team              []string       `json:"team"`
		SpecsCreated      int            `json:"specs_created"`
		SpecsUpdated      int            `json:"specs_updated"`
		PRsMerged         int            `json:"prs_merged"`
		MedianCycleTime   string         `json:"median_cycle_time"`
		MedianReviewIter  float64        `json:"median_review_iterations"`
		DriftEventsCaught int            `json:"drift_events_caught"`
		PreCommitBlocks   int            `json:"pre_commit_blocks"`
		NoSpecOverrides   int            `json:"no_spec_overrides"`
		SpecsByAuthor     map[string]int `json:"specs_by_author"`
		Warnings          []string       `json:"warnings,omitempty"`
	}
	out := jsonOut{
		Period:            m.Period,
		Team:              m.Team,
		SpecsCreated:      m.SpecsCreated,
		SpecsUpdated:      m.SpecsUpdated,
		PRsMerged:         m.PRsMerged,
		MedianCycleTime:   formatDuration(m.MedianCycleTime()),
		MedianReviewIter:  m.MedianReviewIter(),
		DriftEventsCaught: m.DriftEventsCaught,
		PreCommitBlocks:   m.PreCommitBlocks,
		NoSpecOverrides:   m.NoSpecOverrides,
		SpecsByAuthor:     m.SpecEditsByAuthor,
		Warnings:          m.Warnings,
	}
	return json.MarshalIndent(out, "", "  ")
}
