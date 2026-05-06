package spec

import (
	"os"
	"path/filepath"
	"regexp"
)

var ticketFromBranchRe = regexp.MustCompile(`^([A-Z]+-\d+)`)

// FindByTicket locates the canonical spec file for a ticket.
func FindByTicket(repoRoot, ticket string) (string, error) {
	if ticket == "" {
		return "", os.ErrNotExist
	}

	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", err
	}

	path := filepath.Join(absRoot, "spec", ticket+".md")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", os.ErrNotExist
		}
		return "", err
	}

	return path, nil
}

// FindByBranch locates the canonical spec file for a branch.
func FindByBranch(repoRoot, branch string) (string, error) {
	ticket := ParseTicketFromBranch(branch)
	if ticket == "" {
		return "", os.ErrNotExist
	}
	return FindByTicket(repoRoot, ticket)
}

// ParseTicketFromBranch extracts a ticket identifier from a branch name.
func ParseTicketFromBranch(branch string) string {
	matches := ticketFromBranchRe.FindStringSubmatch(branch)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}
