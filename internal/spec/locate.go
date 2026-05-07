package spec

import (
	"os"
	"path/filepath"
	"regexp"
)

var ticketRe = regexp.MustCompile(`^([A-Z]+-\d+)(?:-|$)`)

// ParseTicketFromBranch extracts a Jira-style ticket ID from the start of
// a branch name. Returns "" if the branch does not begin with PROJECT-NNN
// followed by a dash or end-of-string.
//
//	"WGO-101"          → "WGO-101"
//	"WGO-101-spec-foo" → "WGO-101"
//	"feature-WGO-101"  → ""
//	"not-a-ticket"     → ""
func ParseTicketFromBranch(branch string) string {
	m := ticketRe.FindStringSubmatch(branch)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// FindByTicket returns the absolute path to spec/<TICKET>.md under
// repoRoot. The returned error wraps os.ErrNotExist when the file is
// missing.
func FindByTicket(repoRoot, ticket string) (string, error) {
	p := filepath.Join(repoRoot, "spec", ticket+".md")
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

// FindByBranch parses the ticket from branch and delegates to FindByTicket.
// Returns os.ErrNotExist when the branch has no ticket prefix.
func FindByBranch(repoRoot, branch string) (string, error) {
	ticket := ParseTicketFromBranch(branch)
	if ticket == "" {
		return "", os.ErrNotExist
	}
	return FindByTicket(repoRoot, ticket)
}
