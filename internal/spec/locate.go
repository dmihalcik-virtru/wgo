package spec

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var ticketRe = regexp.MustCompile(`(?i)^([A-Za-z]+-\d+)`)

func ParseTicketFromBranch(branch string) string {
	matches := ticketRe.FindStringSubmatch(branch)
	if len(matches) > 1 {
		return strings.ToUpper(matches[1])
	}
	return ""
}

// FindByTicket returns the absolute path of <repoRoot>/spec/<ticket>.md.
//
// The match is case-insensitive and the returned path carries the real on-disk
// filename. Tickets reach us uppercased (see ParseTicketFromBranch) while specs
// for GitHub issues are conventionally lowercase on disk — spec/gh-9.md.
// Returning a path built from the ticket instead would yield links that resolve
// only on a case-insensitive filesystem and 404 on github.com and Linux CI.
//
// Callers distinguish the failure modes: a missing or empty spec/ reports
// fs.ErrNotExist, while an unreadable one reports the underlying error.
func FindByTicket(repoRoot, ticket string) (string, error) {
	if ticket == "" {
		return "", fs.ErrNotExist
	}
	dir := filepath.Join(repoRoot, "spec")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	want := ticket + ".md"
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(e.Name(), want) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		abs, err := filepath.Abs(path)
		if err != nil {
			return path, nil
		}
		return abs, nil
	}
	return "", fs.ErrNotExist
}

func FindByBranch(repoRoot, branch string) (string, error) {
	ticket := ParseTicketFromBranch(branch)
	return FindByTicket(repoRoot, ticket)
}
