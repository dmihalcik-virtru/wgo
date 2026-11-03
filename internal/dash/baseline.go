package dash

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/virtru/wgo/internal/atomicfile"
	"github.com/virtru/wgo/internal/inventory"
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
	// InventoryParts names the github_inventory parts ("alice/open-authored")
	// that had data, fresh or stale, when acknowledged. A PR only the
	// inventory found is new if one of the sets it is in was known then.
	// Absent in older baselines, so such PRs compare as unknown.
	InventoryParts []string `json:"inventory_parts,omitempty"`
}

// BaselineWorkspace records a workspace's visible changes.
type BaselineWorkspace struct {
	Label     string   `json:"label"`
	Changes   []string `json:"changes"`
	Truncated int      `json:"truncated,omitempty"`
	// TruncatedUnknown records that the window was full but the overflow
	// could not be counted. Absent in older baselines, meaning false.
	TruncatedUnknown bool `json:"truncated_unknown,omitempty"`
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
	// InventoryParts, for a PR only the inventory found, names the
	// inventory sets it was in (see inventoryParts).
	InventoryParts []string `json:"inventory_parts,omitempty"`
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
				Label:            n.Label,
				Changes:          append([]string{}, changes...),
				Truncated:        w.ChangesTruncated,
				TruncatedUnknown: w.ChangesTruncatedUnknown,
				Known:            w.Error == "",
			}
		case n.Bookmark != nil:
			b.Bookmarks[n.ID] = BaselineBookmark{PRsKnown: n.Bookmark.PRLookup.hasData()}
		case n.PR != nil:
			b.PRs[n.ID] = BaselinePR{
				BookmarkID:         n.PR.BookmarkID,
				State:              prBucket(n.PR),
				ReviewDecision:     n.PR.ReviewDecision,
				RequestedReviewers: sortedCopy(n.PR.RequestedReviewers),
				ReviewersKnown:     n.PR.ReviewersKnown,
			}
			if !slices.Contains(n.PR.Sources, PRSourceBookmark) {
				bp := b.PRs[n.ID]
				bp.InventoryParts = inventoryParts(n.PR)
				b.PRs[n.ID] = bp
			}
		}
	}
	for _, p := range s.Sources[SourceGitHubInventory].Parts {
		if p.State.hasData() {
			b.InventoryParts = append(b.InventoryParts, p.Name)
		}
	}
	sort.Strings(b.InventoryParts)
	return b
}

// inventoryParts names the github_inventory parts holding PR p, by its
// state and involvement: an author's open PR is in their open-authored set
// and a merged or closed one in closed-recent; an open PR asking for a
// person's review is in their review-requested set.
func inventoryParts(p *PRInfo) []string {
	var out []string
	for _, in := range p.Involvement {
		var set inventory.Set
		switch {
		case in.Role == RoleAuthor && p.State == "open":
			set = inventory.OpenAuthored
		case in.Role == RoleAuthor:
			set = inventory.ClosedRecent
		case in.Role == RoleReviewRequested && p.State == "open":
			set = inventory.ReviewRequested
		default:
			continue
		}
		out = append(out, inventory.Key{Login: in.Person, Set: set}.String())
	}
	return out
}

// inventoryKnew reports whether baseline b had data for one of the
// inventory sets PR p is in now, so that p's absence from b means it is new.
// A PR that closed before b was acknowledged is never new: had its set held
// it then, b would too, so its absence says the window grew, not that the
// PR appeared.
func inventoryKnew(b *Baseline, p *PRInfo) bool {
	if len(b.InventoryParts) == 0 || !slices.Contains(p.Sources, PRSourceInventory) {
		return false
	}
	if closed := cmp.Or(p.ClosedAt, p.MergedAt); p.State != "open" && (closed.IsZero() || closed.Before(b.AcknowledgedAt)) {
		return false
	}
	for _, part := range inventoryParts(p) {
		if slices.Contains(b.InventoryParts, part) {
			return true
		}
	}
	return false
}

// inventoryLacks reports whether s has no data for one of parts, or, with no
// parts named, for any inventory set at all: a PR from those sets may still
// be there.
func inventoryLacks(s *Snapshot, parts []string) bool {
	has := map[string]bool{}
	some := false
	for _, p := range s.Sources[SourceGitHubInventory].Parts {
		has[p.Name] = p.State.hasData()
		some = some || has[p.Name]
	}
	if len(parts) == 0 {
		return !some
	}
	for _, p := range parts {
		if !has[p] {
			return true
		}
	}
	return false
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
	// TruncatedUnknown means the overflow could not be counted.
	TruncatedUnknown bool `json:"truncated_unknown,omitempty"`
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
		wd := WorkspaceDelta{ID: n.ID, Label: n.Label, Truncated: w.ChangesTruncated, TruncatedUnknown: w.ChangesTruncatedUnknown}
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
		if wd.Truncated > 0 || wd.TruncatedUnknown || (had && (base.Truncated > 0 || base.TruncatedUnknown)) {
			d.Summary.Truncated++
			if wd.Truncated == 0 {
				wd.Truncated = base.Truncated
			}
			if had && base.TruncatedUnknown {
				wd.TruncatedUnknown = true
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
		case b.Bookmarks[p.BookmarkID].PRsKnown || inventoryKnew(b, p):
			// The bookmark's PR list, or an inventory set holding the PR,
			// was known then and lacked it.
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
	// A previously seen PR whose bookmark lookup is now unknown, or that
	// only the inventory found while one of its sets now has no data.
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
		bm := s.Node(base.BookmarkID)
		if (bm != nil && bm.Bookmark != nil && (bm.Bookmark.PRLookup == Unknown || bm.Bookmark.PRLookup == Error)) ||
			((base.BookmarkID == "" || len(base.InventoryParts) > 0) && inventoryLacks(s, base.InventoryParts)) {
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
