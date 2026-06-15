package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/jj"
)

// driftClient is the jj surface drift detection needs. Tests inject a fake.
type driftClient interface {
	Log(repo, revset string) ([]jj.LogEntry, error)
	BookmarkList(repo string, opts jj.BookmarkListOpts) ([]jj.Bookmark, error)
}

// jjClient is the package-level jj client; tests may override it.
var jjClient driftClient = jj.NewCLI()

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

// countImplCommitsAfter counts commits on branch since `since` touching files
// outside spec/. Uses jj's `files()` revset with a negated `spec` fileset to
// have jj do the filtering server-side.
func countImplCommitsAfter(repoRoot, branch string, since time.Time) (int, error) {
	sinceStr := since.UTC().Format("2006-01-02T15:04:05Z")
	revset := fmt.Sprintf(
		`ancestors(bookmarks(exact:%q)) & author_date(after:%q) & files(~root:"spec")`,
		branch, sinceStr,
	)
	entries, err := jjClient.Log(repoRoot, revset)
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

func listBranches(repoRoot string) ([]string, error) {
	bookmarks, err := jjClient.BookmarkList(repoRoot, jj.BookmarkListOpts{Local: true})
	if err != nil {
		return nil, err
	}
	branches := make([]string, 0, len(bookmarks))
	for _, b := range bookmarks {
		branches = append(branches, b.Name)
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
