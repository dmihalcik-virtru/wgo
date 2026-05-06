package cmd

import (
	"os"
	"path/filepath"
	"time"

	specdoc "github.com/virtru/wgo/internal/spec"
)

type branchSpecInfo struct {
	Ticket  string
	RelPath string
	Status  string
	Updated time.Time
	Missing bool
}

func findBranchSpec(repoRoot, branch string) (*branchSpecInfo, error) {
	ticket := specdoc.ParseTicketFromBranch(branch)
	if ticket == "" {
		return nil, nil
	}

	absPath, err := specdoc.FindByBranch(repoRoot, branch)
	if err != nil {
		if os.IsNotExist(err) {
			return &branchSpecInfo{
				Ticket:  ticket,
				Missing: true,
			}, nil
		}
		return nil, err
	}

	relPath, err := filepath.Rel(repoRoot, absPath)
	if err != nil {
		relPath = absPath
	}

	info := &branchSpecInfo{
		Ticket:  ticket,
		RelPath: filepath.ToSlash(relPath),
	}

	specFile, err := specdoc.Parse(absPath)
	if err != nil {
		return info, nil
	}

	info.Status = string(specFile.Frontmatter.Status)
	info.Updated = specFile.Frontmatter.Updated
	return info, nil
}
