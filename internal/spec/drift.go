package spec

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DriftKind classifies why a spec/branch pair is drifting.
type DriftKind string

const (
	DriftStale     DriftKind = "stale"     // commits after spec.Updated touching non-spec files
	DriftUntracked DriftKind = "untracked" // branch parses to TICKET but no spec/<TICKET>.md
	DriftOrphaned  DriftKind = "orphaned"  // spec exists, not terminal, no live branch references it
	DriftSpecOnly  DriftKind = "spec_only" // spec edited recently but no impl commits
)

// DriftReport describes a single drift condition.
type DriftReport struct {
	Kind     DriftKind
	Branch   string // empty for orphaned
	Spec     string // empty for untracked
	Detail   string // human-readable
	Severity int    // 0=info, 1=warn, 2=error
}

// DetectForBranch returns drift reports for a single branch in repoRoot.
func DetectForBranch(repoRoot, branch string) ([]DriftReport, error) {
	ticket := ParseTicketFromBranch(branch)
	if ticket == "" {
		return nil, nil
	}

	specPath, err := FindByTicket(repoRoot, ticket)
	if err != nil {
		return []DriftReport{{
			Kind:     DriftUntracked,
			Branch:   branch,
			Detail:   fmt.Sprintf("no spec/%s.md found", ticket),
			Severity: 1,
		}}, nil
	}

	sf, parseErr := Parse(specPath)
	if parseErr != nil || sf.Frontmatter.Ticket == "" {
		return nil, nil
	}

	var reports []DriftReport

	if !sf.Frontmatter.Updated.IsZero() {
		count, err := countImplCommitsAfter(repoRoot, branch, sf.Frontmatter.Updated)
		if err == nil && count > 0 {
			relSpec, _ := filepath.Rel(repoRoot, specPath)
			reports = append(reports, DriftReport{
				Kind:     DriftStale,
				Branch:   branch,
				Spec:     relSpec,
				Detail:   fmt.Sprintf("%d commit(s) since spec updated %s", count, sf.Frontmatter.Updated.Format("2006-01-02")),
				Severity: 1,
			})
		}
	}

	return reports, nil
}

// DetectAll returns drift reports for all branches and orphaned specs in repoRoot.
func DetectAll(repoRoot string) ([]DriftReport, error) {
	branches, err := listBranches(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}

	var reports []DriftReport
	for _, branch := range branches {
		branchReports, err := DetectForBranch(repoRoot, branch)
		if err != nil {
			continue
		}
		reports = append(reports, branchReports...)
	}

	orphaned, _ := detectOrphaned(repoRoot, branches)
	reports = append(reports, orphaned...)

	return reports, nil
}

// countImplCommitsAfter counts commits on branch since `since` touching files outside spec/.
func countImplCommitsAfter(repoRoot, branch string, since time.Time) (int, error) {
	sinceStr := since.UTC().Format("2006-01-02T15:04:05Z")
	out, err := exec.Command("git", "-C", repoRoot, "log", branch,
		"--after="+sinceStr, "--name-only", "--format=%H").Output()
	if err != nil {
		return 0, err
	}

	count := 0
	inCommit := false
	hasNonSpec := false

	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			if inCommit && hasNonSpec {
				count++
			}
			inCommit = false
			hasNonSpec = false
			continue
		}
		if isHexStr(line, 40) {
			if inCommit && hasNonSpec {
				count++
			}
			inCommit = true
			hasNonSpec = false
			continue
		}
		if inCommit && !strings.HasPrefix(line, "spec/") {
			hasNonSpec = true
		}
	}
	if inCommit && hasNonSpec {
		count++
	}

	return count, nil
}

func isHexStr(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func listBranches(repoRoot string) ([]string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "branch", "--format=%(refname:short)").Output()
	if err != nil {
		return nil, err
	}
	var branches []string
	for _, line := range strings.Split(string(out), "\n") {
		if b := strings.TrimSpace(line); b != "" {
			branches = append(branches, b)
		}
	}
	return branches, nil
}

func detectOrphaned(repoRoot string, liveBranches []string) ([]DriftReport, error) {
	specDir := filepath.Join(repoRoot, "spec")
	entries, err := os.ReadDir(specDir)
	if err != nil {
		return nil, nil
	}

	branchSet := make(map[string]bool, len(liveBranches))
	for _, b := range liveBranches {
		branchSet[b] = true
	}

	terminal := map[Status]bool{
		StatusShipped:   true,
		StatusAbandoned: true,
	}

	var reports []DriftReport
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		specPath := filepath.Join(specDir, entry.Name())
		sf, err := Parse(specPath)
		if err != nil || sf.Frontmatter.Ticket == "" {
			continue
		}
		if terminal[sf.Frontmatter.Status] {
			continue
		}

		hasLive := false
		for _, b := range sf.Frontmatter.Branches {
			// branches may be "owner/repo:branch-name" format
			parts := strings.SplitN(b, ":", 2)
			name := b
			if len(parts) == 2 {
				name = parts[1]
			}
			if branchSet[name] {
				hasLive = true
				break
			}
		}
		if !hasLive {
			ticket := sf.Frontmatter.Ticket
			for b := range branchSet {
				if strings.HasPrefix(strings.ToUpper(b), strings.ToUpper(ticket)) {
					hasLive = true
					break
				}
			}
		}

		if !hasLive {
			relSpec, _ := filepath.Rel(repoRoot, specPath)
			reports = append(reports, DriftReport{
				Kind:   DriftOrphaned,
				Spec:   relSpec,
				Detail: fmt.Sprintf("spec %s (%s) has no live branch", sf.Frontmatter.Ticket, sf.Frontmatter.Status),
			})
		}
	}

	return reports, nil
}
