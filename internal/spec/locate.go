package spec

import (
	"os"
	"path/filepath"
	"regexp"
)

func FindByTicket(repoRoot, ticket string) (string, error) {
	rel := filepath.Join("spec", ticket+".md")
	abs := filepath.Join(repoRoot, rel)
	if _, err := os.Stat(abs); err == nil {
		return abs, nil
	}
	return "", os.ErrNotExist
}

func FindByBranch(repoRoot, branch string) (string, error) {
	ticket := ParseTicketFromBranch(branch)
	if ticket == "" {
		return "", os.ErrNotExist
	}
	return FindByTicket(repoRoot, ticket)
}

var ticketRe = regexp.MustCompile(`^([A-Z]+-\d+)`)

func ParseTicketFromBranch(branch string) string {
	matches := ticketRe.FindStringSubmatch(branch)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}
