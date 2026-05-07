package spec

import (
	"os"
	"path/filepath"
	"regexp"
)

var ticketRe = regexp.MustCompile(`^([A-Z]+-\d+)`)

func ParseTicketFromBranch(branch string) string {
	matches := ticketRe.FindStringSubmatch(branch)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

func FindByTicket(repoRoot, ticket string) (string, error) {
	if ticket == "" {
		return "", os.ErrNotExist
	}
	path := filepath.Join(repoRoot, "spec", ticket+".md")
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path, nil
	}
	return abs, nil
}

func FindByBranch(repoRoot, branch string) (string, error) {
	ticket := ParseTicketFromBranch(branch)
	return FindByTicket(repoRoot, ticket)
}
