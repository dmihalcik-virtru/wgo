package spec

import (
	"os"
	"path/filepath"
	"regexp"
)

var ticketRe = regexp.MustCompile(`[A-Z]+-\d+`)

// ParseTicketFromBranch extracts the first Jira-style ticket ID from a branch
// name (e.g. "WGO-101-spec-foo" → "WGO-101"). Returns "" if none found.
func ParseTicketFromBranch(branch string) string {
	return ticketRe.FindString(branch)
}

// FindByTicket returns the absolute path to spec/<ticket>.md under repoRoot,
// or os.ErrNotExist if the file does not exist.
func FindByTicket(repoRoot, ticket string) (string, error) {
	p := filepath.Join(repoRoot, "spec", ticket+".md")
	if _, err := os.Stat(p); err != nil {
		return "", os.ErrNotExist
	}
	return p, nil
}

// FindByBranch parses the ticket from branch then calls FindByTicket.
func FindByBranch(repoRoot, branch string) (string, error) {
	ticket := ParseTicketFromBranch(branch)
	if ticket == "" {
		return "", os.ErrNotExist
	}
	return FindByTicket(repoRoot, ticket)
}
