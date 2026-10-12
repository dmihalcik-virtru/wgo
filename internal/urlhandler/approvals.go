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
// user has allowed wgo:// links to open, and which they recently declined.
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

// Decline is a workspace whose confirmation the user declined (Cancel,
// Escape, no, or a dialog left to time out). Links to it are refused
// without asking until DeclineCooldown has passed.
type Decline struct {
	WorkspaceID string    `json:"workspace_id"`
	DeclinedAt  time.Time `json:"declined_at"`
}

// DeclineCooldown is how long after a decline links to that workspace are
// refused without a dialog, so a page that fires the same link again and
// again cannot wear the user down into clicking Open.
const DeclineCooldown = 5 * time.Minute

type approvalsDoc struct {
	Version   int        `json:"version"`
	Approvals []Approval `json:"approvals"`
	Declines  []Decline  `json:"declines,omitempty"`
}

var (
	errCorruptApprovals = errors.New("not valid JSON")
	errNewerApprovals   = errors.New("was written by a newer wgo")
)

// Approvals is the store of confirmed and recently declined workspaces, a
// small JSON file written atomically.
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
		return approvalsDoc{}, fmt.Errorf("%s is %w: %w", a.path, errCorruptApprovals, err)
	}
	if doc.Version > approvalsVersion {
		return approvalsDoc{}, fmt.Errorf("%s %w (version %d)", a.path, errNewerApprovals, doc.Version)
	}
	return doc, nil
}

func (a *Approvals) save(doc approvalsDoc) error {
	doc.Version = approvalsVersion
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(a.path, append(b, '\n'), 0o600)
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
// it and clearing any decline. A corrupt file is replaced rather than
// blocking the approval the user just gave, and warnf (when set) is told
// the earlier approvals were lost. A file that cannot be read, or that a
// newer wgo wrote, is left alone and the error returned.
func (a *Approvals) Approve(id, path string, now time.Time, warnf func(format string, args ...any)) error {
	doc, err := a.load()
	switch {
	case errors.Is(err, errCorruptApprovals):
		if warnf != nil {
			warnf("replacing corrupt %s; earlier wgo:// approvals were lost and those workspaces will ask again (%v)", a.path, err)
		}
		doc = approvalsDoc{}
	case err != nil:
		return err
	}
	kept := doc.Approvals[:0]
	for _, ap := range doc.Approvals {
		if ap.WorkspaceID != id {
			kept = append(kept, ap)
		}
	}
	doc.Approvals = append(kept, Approval{WorkspaceID: id, Path: path, ApprovedAt: now.UTC()})
	doc.Declines = recentDeclines(doc.Declines, now, id)
	return a.save(doc)
}

// Decline records that the user declined id at now, starting its
// DeclineCooldown. Declines that have already cooled down are dropped. An
// unreadable, corrupt or newer file is left alone and the error returned:
// a decline is not worth losing approvals over.
func (a *Approvals) Decline(id string, now time.Time) error {
	doc, err := a.load()
	if err != nil {
		return err
	}
	doc.Declines = append(recentDeclines(doc.Declines, now, id), Decline{WorkspaceID: id, DeclinedAt: now.UTC()})
	return a.save(doc)
}

// CoolingDown reports whether id was declined less than DeclineCooldown
// before now, and if so when links to it will ask again. An unreadable
// file is an error, which callers treat as not cooling down: the dialog
// still defaults to Cancel.
func (a *Approvals) CoolingDown(id string, now time.Time) (until time.Time, ok bool, err error) {
	doc, err := a.load()
	if err != nil {
		return time.Time{}, false, err
	}
	for _, d := range doc.Declines {
		if d.WorkspaceID == id && coolingDown(d, now) && d.DeclinedAt.Add(DeclineCooldown).After(until) {
			until, ok = d.DeclinedAt.Add(DeclineCooldown), true
		}
	}
	return until, ok, nil
}

// coolingDown reports whether d is still in its cooldown at now. A decline
// stamped in the future (the clock went back) is ignored rather than
// trusted to block links for longer than DeclineCooldown.
func coolingDown(d Decline, now time.Time) bool {
	age := now.Sub(d.DeclinedAt)
	return age >= 0 && age < DeclineCooldown
}

// recentDeclines returns the declines still cooling down at now, except
// any for id.
func recentDeclines(ds []Decline, now time.Time, id string) []Decline {
	var kept []Decline
	for _, d := range ds {
		if d.WorkspaceID != id && coolingDown(d, now) {
			kept = append(kept, d)
		}
	}
	return kept
}

// LockFile is the single-instance lock `wgo open` takes under ~/.wgo.
const LockFile = "url-handler.lock"

// LockPath is the lock file in the wgo base directory (~/.wgo).
func LockPath(baseDir string) string { return filepath.Join(baseDir, LockFile) }
