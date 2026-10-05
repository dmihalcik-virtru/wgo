package urlhandler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/virtru/wgo/internal/atomicfile"
)

// ApprovalsFile is the file under ~/.wgo that records which workspaces the
// user has allowed wgo:// links to open.
const ApprovalsFile = "url-handler-approvals.json"

// approvalsVersion is the schema version of the approvals file.
const approvalsVersion = 1

// Approval is one confirmed workspace: its ID and the canonical path the
// user saw in the confirmation dialog. A later open of the same ID at a
// different path asks again.
type Approval struct {
	WorkspaceID string    `json:"workspace_id"`
	Path        string    `json:"path"`
	ApprovedAt  time.Time `json:"approved_at"`
}

type approvalsDoc struct {
	Version   int        `json:"version"`
	Approvals []Approval `json:"approvals"`
}

// Approvals is the store of confirmed workspaces, a small JSON file written
// atomically.
type Approvals struct {
	path string
}

// NewApprovals returns the store kept in the file at path.
func NewApprovals(path string) *Approvals { return &Approvals{path: path} }

// ApprovalsPath is the approvals file in the wgo base directory (~/.wgo).
func ApprovalsPath(baseDir string) string { return filepath.Join(baseDir, ApprovalsFile) }

// Path is the file the store reads and writes.
func (a *Approvals) Path() string { return a.path }

func (a *Approvals) load() (approvalsDoc, error) {
	var doc approvalsDoc
	b, err := os.ReadFile(a.path)
	if errors.Is(err, fs.ErrNotExist) {
		return approvalsDoc{Version: approvalsVersion}, nil
	}
	if err != nil {
		return doc, err
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return doc, fmt.Errorf("%s is not valid JSON: %w", a.path, err)
	}
	if doc.Version > approvalsVersion {
		return doc, fmt.Errorf("%s was written by a newer wgo (version %d)", a.path, doc.Version)
	}
	return doc, nil
}

// Approved reports whether id was confirmed at exactly path. An unreadable
// file is an error, which callers treat as not approved.
func (a *Approvals) Approved(id, path string) (bool, error) {
	doc, err := a.load()
	if err != nil {
		return false, err
	}
	for _, ap := range doc.Approvals {
		if ap.WorkspaceID == id && ap.Path == path {
			return true, nil
		}
	}
	return false, nil
}

// Approve records id as confirmed at path, replacing any earlier path for
// it. A corrupt file is replaced rather than blocking the approval the user
// just gave; a file from a newer wgo is left alone.
func (a *Approvals) Approve(id, path string, now time.Time) error {
	doc, err := a.load()
	if err != nil {
		if doc.Version > approvalsVersion {
			return err
		}
		doc = approvalsDoc{}
	}
	doc.Version = approvalsVersion
	kept := doc.Approvals[:0]
	for _, ap := range doc.Approvals {
		if ap.WorkspaceID != id {
			kept = append(kept, ap)
		}
	}
	doc.Approvals = append(kept, Approval{WorkspaceID: id, Path: path, ApprovedAt: now.UTC()})
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(a.path, append(b, '\n'), 0o600)
}
