package dash

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"

	"github.com/virtru/wgo/internal/atomicfile"
)

// BaselineSchema is the last-seen.json layout version.
const BaselineSchema = 1

// Baseline is what the user last acknowledged seeing, persisted as
// ~/.wgo/cache/dash/last-seen.json. Only Dash.Acknowledge writes it; a
// background refresh never does.
type Baseline struct {
	Schema         int       `json:"schema"`
	Generation     uint64    `json:"acknowledged_generation"`
	AcknowledgedAt time.Time `json:"acknowledged_at"`
	// ChangeWindow is the per-workspace bound on recorded change IDs.
	ChangeWindow int                          `json:"change_window"`
	Workspaces   map[string]BaselineWorkspace `json:"workspaces"`
	Bookmarks    map[string]BaselineBookmark  `json:"bookmarks"`
	PRs          map[string]BaselinePR        `json:"prs"`
}

// BaselineWorkspace records a workspace's visible changes.
type BaselineWorkspace struct {
	Label     string   `json:"label"`
	Changes   []string `json:"changes"`
	Truncated int      `json:"truncated,omitempty"`
	// Known is false when jj could not read the workspace at the time.
	Known bool `json:"known"`
}

// BaselineBookmark records whether the bookmark's PR list was known.
type BaselineBookmark struct {
	PRsKnown bool `json:"prs_known"`
}

// BaselinePR records a PR's state and review requests.
type BaselinePR struct {
	BookmarkID         string   `json:"bookmark_id"`
	State              string   `json:"state"` // open, draft, merged, closed or unknown
	ReviewDecision     string   `json:"review_decision,omitempty"`
	RequestedReviewers []string `json:"requested_reviewers,omitempty"`
	ReviewersKnown     bool     `json:"reviewers_known"`
}

// baselineFrom records what snapshot s shows.
func baselineFrom(s *Snapshot, at time.Time) *Baseline {
	b := &Baseline{
		Schema:         BaselineSchema,
		Generation:     s.Generation,
		AcknowledgedAt: at,
		ChangeWindow:   ChangeWindow,
		Workspaces:     map[string]BaselineWorkspace{},
		Bookmarks:      map[string]BaselineBookmark{},
		PRs:            map[string]BaselinePR{},
	}
	for _, n := range s.Nodes {
		switch {
		case n.Workspace != nil:
			w := n.Workspace
			changes := w.Changes
			if len(changes) > ChangeWindow {
				changes = changes[:ChangeWindow]
			}
			b.Workspaces[n.ID] = BaselineWorkspace{
				Label:     n.Label,
				Changes:   append([]string{}, changes...),
				Truncated: w.ChangesTruncated,
				Known:     w.Error == "",
			}
		case n.Bookmark != nil:
			b.Bookmarks[n.ID] = BaselineBookmark{PRsKnown: n.Bookmark.PRLookup == Fresh || n.Bookmark.PRLookup == Stale}
		case n.PR != nil:
			b.PRs[n.ID] = BaselinePR{
				BookmarkID:         n.PR.BookmarkID,
				State:              prBucket(n.PR),
				ReviewDecision:     n.PR.ReviewDecision,
				RequestedReviewers: sortedCopy(n.PR.RequestedReviewers),
				ReviewersKnown:     n.PR.ReviewersKnown,
			}
		}
	}
	return b
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// loadBaseline reads last-seen.json. A missing file returns (nil, nil).
func loadBaseline(path string) (*Baseline, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var b Baseline
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, err
	}
	if b.Schema != BaselineSchema {
		return nil, fmt.Errorf("last-seen schema %d, want %d", b.Schema, BaselineSchema)
	}
	return &b, nil
}

func saveBaseline(path string, b *Baseline) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'), 0o600)
}

// Delta statuses.
const (
	DeltaNoPreviousLook = "no_previous_look"
	DeltaCompared       = "compared"

	ItemNew       = "new"
	ItemChanged   = "changed"
	ItemUnknown   = "unknown"
	ItemUnchanged = "unchanged"
)

// Delta is what changed between the acknowledged baseline and a snapshot.
// Unknown (a source that could not be read, then or now) is reported
// separately from unchanged.
type Delta struct {
	Status             string           `json:"status"`
	Message            string           `json:"message,omitempty"`
	BaselineGeneration uint64           `json:"baseline_generation,omitempty"`
	BaselineAt         time.Time        `json:"baseline_at,omitzero"`
	SnapshotGeneration uint64           `json:"snapshot_generation"`
	Summary            DeltaSummary     `json:"summary"`
	Workspaces         []WorkspaceDelta `json:"workspaces,omitempty"`
	PRs                []PRDelta        `json:"prs,omitempty"`
	ReviewRequests     []ReviewDelta    `json:"review_requests,omitempty"`
}

// DeltaSummary counts the delta.
type DeltaSummary struct {
	NewChanges        int `json:"new_changes"`
	PRStateChanges    int `json:"pr_state_changes"`
	NewReviewRequests int `json:"new_review_requests"`
	// Unknown counts workspaces and PRs that could not be compared. A known
	// PR whose reviewers are unknown counts once here too.
	Unknown   int `json:"unknown"`
	Unchanged int `json:"unchanged"`
	// Truncated counts workspaces with more changes than ChangeWindow; their
	// older changes are not compared.
	Truncated int `json:"truncated"`
}

// WorkspaceDelta reports new changes in one workspace.
type WorkspaceDelta struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Status     string `json:"status"`
	NewChanges int    `json:"new_changes"`
	// Truncated is the number of changes beyond the comparison window.
	Truncated int `json:"truncated,omitempty"`
}

// PRDelta reports a PR state or review-decision change.
type PRDelta struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Status     string `json:"status"`
	FromState  string `json:"from_state,omitempty"`
	ToState    string `json:"to_state,omitempty"`
	FromReview string `json:"from_review,omitempty"`
	ToReview   string `json:"to_review,omitempty"`
}

// ReviewDelta reports newly requested reviewers on a PR.
type ReviewDelta struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Status string   `json:"status"`
	Added  []string `json:"added,omitempty"`
}

// computeDelta compares s against b. A nil baseline is "No previous look".
func computeDelta(b *Baseline, s *Snapshot) *Delta {
	d := &Delta{SnapshotGeneration: s.Generation}
	if b == nil {
		d.Status, d.Message = DeltaNoPreviousLook, "No previous look"
		return d
	}
	d.Status = DeltaCompared
	d.BaselineGeneration, d.BaselineAt = b.Generation, b.AcknowledgedAt

	for _, n := range s.Nodes {
		w := n.Workspace
		if w == nil {
			continue
		}
		wd := WorkspaceDelta{ID: n.ID, Label: n.Label, Truncated: w.ChangesTruncated}
		base, had := b.Workspaces[n.ID]
		switch {
		case w.Error != "":
			wd.Status = ItemUnknown
		case !had:
			wd.Status, wd.NewChanges = ItemNew, len(w.Changes)
		case !base.Known:
			wd.Status = ItemUnknown
		default:
			seen := make(map[string]bool, len(base.Changes))
			for _, c := range base.Changes {
				seen[c] = true
			}
			for _, c := range w.Changes {
				if !seen[c] {
					wd.NewChanges++
				}
			}
			wd.Status = ItemUnchanged
			if wd.NewChanges > 0 {
				wd.Status = ItemChanged
			}
		}
		if wd.Truncated > 0 || (had && base.Truncated > 0) {
			d.Summary.Truncated++
			if wd.Truncated == 0 {
				wd.Truncated = base.Truncated
			}
		}
		d.Summary.NewChanges += wd.NewChanges
		d.tally(wd.Status)
		if wd.Status != ItemUnchanged {
			d.Workspaces = append(d.Workspaces, wd)
		}
	}

	current := map[string]bool{}
	for _, n := range s.Nodes {
		p := n.PR
		if p == nil {
			continue
		}
		current[n.ID] = true
		pd := PRDelta{ID: n.ID, Label: n.Label, ToState: prBucket(p), ToReview: p.ReviewDecision}
		rd := ReviewDelta{ID: n.ID, Label: n.Label}
		base, had := b.PRs[n.ID]
		switch {
		case had:
			pd.FromState, pd.FromReview = base.State, base.ReviewDecision
			pd.Status = ItemUnchanged
			if base.State != pd.ToState || base.ReviewDecision != pd.ToReview {
				pd.Status = ItemChanged
			}
			if base.ReviewersKnown && p.ReviewersKnown {
				rd.Added = added(base.RequestedReviewers, p.RequestedReviewers)
			} else {
				rd.Status = ItemUnknown
			}
		case b.Bookmarks[p.BookmarkID].PRsKnown:
			// The bookmark's PR list was known then and lacked this PR.
			pd.Status = ItemNew
			if p.ReviewersKnown {
				rd.Added = sortedCopy(p.RequestedReviewers)
			} else {
				rd.Status = ItemUnknown
			}
		default:
			pd.Status, rd.Status = ItemUnknown, ItemUnknown
		}
		if pd.Status == ItemChanged || pd.Status == ItemNew {
			d.Summary.PRStateChanges++
		}
		d.tally(pd.Status)
		if pd.Status != ItemUnchanged {
			d.PRs = append(d.PRs, pd)
		}
		if len(rd.Added) > 0 {
			rd.Status = ItemNew
			d.Summary.NewReviewRequests += len(rd.Added)
		}
		if rd.Status != "" {
			// Unknown reviewers on a PR already tallied unknown are one
			// unknown item, not two.
			if rd.Status == ItemUnknown && pd.Status != ItemUnknown {
				d.Summary.Unknown++
			}
			d.ReviewRequests = append(d.ReviewRequests, rd)
		}
	}
	// A previously seen PR whose bookmark lookup is now unknown.
	ids := make([]string, 0, len(b.PRs))
	for id := range b.PRs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if current[id] {
			continue
		}
		base := b.PRs[id]
		if bm := s.Node(base.BookmarkID); bm != nil && bm.Bookmark != nil &&
			(bm.Bookmark.PRLookup == Unknown || bm.Bookmark.PRLookup == Error) {
			d.PRs = append(d.PRs, PRDelta{ID: id, Label: id, Status: ItemUnknown, FromState: base.State, FromReview: base.ReviewDecision})
			d.Summary.Unknown++
		}
	}
	return d
}

func (d *Delta) tally(status string) {
	switch status {
	case ItemUnknown:
		d.Summary.Unknown++
	case ItemUnchanged:
		d.Summary.Unchanged++
	}
}

// added returns the elements of now that are not in before, sorted.
func added(before, now []string) []string {
	had := map[string]bool{}
	for _, r := range before {
		had[r] = true
	}
	var out []string
	for _, r := range now {
		if !had[r] {
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}
