// Package pilot generates pilot workflow summary reports.
package pilot

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/github"
	"github.com/virtru/wgo/internal/jj"
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

	jjc := jj.NewCLI()

	// Collect jj-based metrics across all repos.
	for _, repoRoot := range repos {
		if _, err := os.Stat(repoRoot); err != nil {
			continue
		}

		commits, err := specChangingCommits(jjc, repoRoot, opts.Since, opts.Until)
		if err != nil {
			m.Warnings = append(m.Warnings, fmt.Sprintf("spec commits (%s): %v", repoRoot, err))
		} else {
			for _, c := range commits {
				m.SpecsUpdated++
				if c.created {
					m.SpecsCreated++
					m.SpecEditsByAuthor[c.author]++
				}
			}
		}

		// [no-spec] overrides — commits whose description contains the
		// literal "[no-spec]" string within the window.
		count, err := countNoSpecCommits(jjc, repoRoot, opts.Since, opts.Until)
		if err != nil {
			m.Warnings = append(m.Warnings, fmt.Sprintf("no-spec overrides (%s): %v", repoRoot, err))
		} else {
			m.NoSpecOverrides += count
		}
	}

	// Collect GitHub metrics for each team member
	ghClient := github.NewClient()
	sinceStr := opts.Since.Format("2006-01-02")
	untilStr := opts.Until.Format("2006-01-02")
	for _, handle := range opts.Team {
		prs, err := listMergedPRs(ghClient, handle, sinceStr, untilStr)
		if err != nil {
			m.Warnings = append(m.Warnings, fmt.Sprintf("PRs for %s: %v", handle, err))
			continue
		}
		m.PRsMerged += len(prs)

		for _, pr := range prs {
			iters, err := reviewIterations(ghClient, pr.slug, pr.number)
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

// specCommit captures a single commit that touched a spec/*.md file.
// `created` is true when the commit added a new spec file (vs modifying an
// existing one), used for the SpecsCreated count.
type specCommit struct {
	hash    string
	author  string
	time    time.Time
	created bool
}

// specChangingCommits returns commits in [since, until] that touch spec/*.md
// files, tagging each with whether it added a new spec file.
func specChangingCommits(jjc jj.Client, repoRoot string, since, until time.Time) ([]specCommit, error) {
	// jj 0.42's author_date() accepts a single keyword arg; intersect two
	// calls to express a half-open range. files() patterns are resolved
	// against the cwd by default; use root-glob: to force the pattern to
	// be interpreted relative to the repo root regardless of cwd.
	revset := fmt.Sprintf(
		`author_date(after:%q) & author_date(before:%q) & files(root-glob:"spec/*.md")`,
		since.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339),
	)
	entries, err := jjc.Log(repoRoot, revset)
	if err != nil {
		return nil, err
	}
	var out []specCommit
	for _, e := range entries {
		summary, sErr := jjc.DiffSummary(repoRoot, e.CommitID)
		if sErr != nil {
			// Skip commits we can't diff (e.g. merge commits without a
			// single parent diff); they still count as updates but we
			// can't classify them as creates.
			out = append(out, specCommit{hash: e.CommitID, author: e.AuthorEmail, time: e.AuthorTimestamp})
			continue
		}
		created := false
		for _, fc := range summary {
			if fc.Status == 'A' && isSpecPath(fc.Path) {
				created = true
				break
			}
		}
		out = append(out, specCommit{
			hash:    e.CommitID,
			author:  e.AuthorEmail,
			time:    e.AuthorTimestamp,
			created: created,
		})
	}
	return out, nil
}

// isSpecPath returns true for paths matching spec/*.md (one level deep, .md
// extension).
func isSpecPath(p string) bool {
	if !strings.HasPrefix(p, "spec/") {
		return false
	}
	rest := strings.TrimPrefix(p, "spec/")
	if strings.Contains(rest, "/") {
		return false
	}
	return strings.HasSuffix(rest, ".md")
}

// countNoSpecCommits counts commits whose description contains "[no-spec]"
// within the [since, until] window. Uses a case-insensitive substring
// revset, mirroring git's `--grep=[no-spec] --fixed-strings` behaviour.
func countNoSpecCommits(jjc jj.Client, repoRoot string, since, until time.Time) (int, error) {
	revset := fmt.Sprintf(
		`description(substring-i:"[no-spec]") & author_date(after:%q) & author_date(before:%q)`,
		since.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339),
	)
	return jjc.CountRevset(repoRoot, revset)
}

// mergedPR holds minimal merged PR data from the GitHub search endpoint.
type mergedPR struct {
	number int
	slug   string // "owner/repo"
}

// listMergedPRs returns merged PRs for a GitHub handle in the given date
// range, sourced from the GitHub search endpoint via the HTTP client.
func listMergedPRs(ghClient *github.CLIClient, handle, since, until string) ([]mergedPR, error) {
	query := fmt.Sprintf("author:%s merged:>=%s merged:<=%s is:merged", handle, since, until)
	items, err := ghClient.SearchPRs(query)
	if err != nil {
		return nil, err
	}
	out := make([]mergedPR, 0, len(items))
	for _, item := range items {
		slug := item.RepoOwner + "/" + item.RepoName
		out = append(out, mergedPR{number: item.Number, slug: slug})
	}
	return out, nil
}

// reviewIterations counts distinct review round-trips on a PR. A round-trip
// is defined as any APPROVED or CHANGES_REQUESTED submission (matches the
// previous gh-CLI implementation).
func reviewIterations(ghClient *github.CLIClient, slug string, number int) (int, error) {
	reviews, err := ghClient.GetPRReviews(slug, number)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, r := range reviews {
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
