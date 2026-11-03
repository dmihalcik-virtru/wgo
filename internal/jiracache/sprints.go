package jiracache

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/virtru/wgo/internal/atomicfile"
	"github.com/virtru/wgo/internal/store"
)

// SprintTTL is how long a board's cached sprints stay fresh.
const SprintTTL = 15 * time.Minute

// MaxSprintTickets bounds an active sprint's ticket search; a sprint with
// more matching tickets is marked Truncated.
const MaxSprintTickets = 200

// BoardSprints is a board's active and future sprints, and the tickets in
// each active sprint assigned to Assignees.
type BoardSprints struct {
	Board   int           `json:"board"`
	Sprints []SprintEntry `json:"sprints"`
	// Assignees are the Jira identities the ticket searches covered,
	// sorted: account IDs, emails or currentUser().
	Assignees []string `json:"assignees,omitempty"`
	// Site is the Jira host, for browse links, when acli reported one.
	Site string `json:"site,omitempty"`
	// Notes are problems with the searches that did not fail them, such as
	// a configured Jira identity that Jira rejected; wgo dash shows them as
	// diagnostics.
	Notes []string `json:"notes,omitempty"`
}

// SprintEntry is one sprint of a board.
type SprintEntry struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	// State is "active", "future" or "closed".
	State string    `json:"state"`
	Goal  string    `json:"goal,omitempty"`
	Start time.Time `json:"start,omitzero"`
	End   time.Time `json:"end,omitzero"`
	// Tickets are an active sprint's tickets, at most MaxSprintTickets,
	// with the facts the search reported. It is nil when not searched (a
	// future sprint), or when the search failed (Error) and no earlier
	// search of the sprint is cached.
	Tickets   []SprintTicket `json:"tickets,omitzero"`
	Truncated bool           `json:"truncated,omitempty"`
	Error     string         `json:"error,omitempty"`
	// TicketsFetchedAt is set when the search failed and Tickets are kept
	// from an earlier one: when that search ran.
	TicketsFetchedAt time.Time `json:"tickets_fetched_at,omitzero"`
}

// SprintTicket is a ticket a sprint search found.
type SprintTicket struct {
	Key  string `json:"key"`
	Info Info   `json:"info"`
}

// SprintFetcher reads a board's sprints and their tickets from Jira. cmd
// supplies an acli-backed one; like Fetcher it keeps jira out of this
// package.
type SprintFetcher interface {
	FetchSprints(board int, assignees []string) (BoardSprints, error)
}

type sprintsEntry struct {
	Data      BoardSprints `json:"data"`
	FetchedAt time.Time    `json:"fetched_at"`
	// Failed marks a negative entry, written when the fetch for a board
	// with nothing cached failed; Error says how.
	Failed bool   `json:"failed,omitempty"`
	Error  string `json:"error,omitempty"`
}

// SprintsResult is a sprint cache lookup.
type SprintsResult struct {
	Data      BoardSprints
	State     State
	FetchedAt time.Time
	// Failed is set for a negative entry: the last fetch of a board with
	// nothing cached failed with Err, so there is no Data.
	Failed bool
	Err    string
}

// ReadSprints returns a board's cached sprints and their freshness. It never
// makes a network call. A negative entry, or one in which a sprint's search
// failed, expires under the shorter negativeTTL, so it is retried soon.
func ReadSprints(board int, ttl time.Duration) SprintsResult {
	path, err := sprintsPath(board)
	if err != nil {
		return SprintsResult{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logf("jira cache: read %s: %v", path, err)
		}
		return SprintsResult{}
	}
	var e sprintsEntry
	if err := json.Unmarshal(data, &e); err != nil {
		logf("jira cache: corrupt entry %s: %v", path, err)
		return SprintsResult{}
	}
	r := SprintsResult{Data: e.Data, FetchedAt: e.FetchedAt, Failed: e.Failed, Err: e.Error, State: Fresh}
	effTTL := ttl
	if (e.Failed || e.Data.searchFailed()) && negativeTTL < ttl {
		effTTL = negativeTTL
	}
	if time.Since(e.FetchedAt) >= effTTL {
		r.State = Stale
	}
	return r
}

// searchFailed reports whether any sprint's ticket search failed.
func (b BoardSprints) searchFailed() bool {
	for _, s := range b.Sprints {
		if s.Error != "" {
			return true
		}
	}
	return false
}

// RefreshSprints fetches a board's sprints and writes them through the
// cache. On a failed fetch the cache keeps a usable entry, so the last
// known sprints are still shown; only a board with nothing cached gets a
// negative entry recording the error. Sprints whose ticket search failed
// are stored with their Error, and also returned as an error.
func RefreshSprints(f SprintFetcher, board int, assignees []string) error {
	if f == nil {
		return nil
	}
	data, err := f.FetchSprints(board, assignees)
	if err != nil {
		if r := ReadSprints(board, 0); r.State == Miss || r.Failed {
			e := sprintsEntry{Data: BoardSprints{Board: board}, FetchedAt: time.Now(), Failed: true, Error: err.Error()}
			if werr := writeSprints(board, e); werr != nil {
				logf("jira cache: negative write for board %d: %v", board, werr)
			}
		}
		return err
	}
	data.Board = board
	data.Assignees = SortedAssignees(assignees)
	keepTickets(&data, ReadSprints(board, 0))
	if err := writeSprints(board, sprintsEntry{Data: data, FetchedAt: time.Now()}); err != nil {
		return fmt.Errorf("jira cache write: %w", err)
	}
	var errs []error
	for _, s := range data.Sprints {
		if s.Error != "" {
			errs = append(errs, fmt.Errorf("sprint %d ticket search: %s", s.ID, s.Error))
		}
	}
	return errors.Join(errs...)
}

// keepTickets gives each sprint whose search failed the tickets of the
// previous entry's search of it, if any, stamped with when that search ran:
// one failed search must not empty a sprint until the next refresh.
func keepTickets(data *BoardSprints, prev SprintsResult) {
	if prev.State == Miss || prev.Failed {
		return
	}
	for i := range data.Sprints {
		s := &data.Sprints[i]
		if s.Error == "" || s.Tickets != nil {
			continue
		}
		for _, p := range prev.Data.Sprints {
			if p.ID == s.ID && p.Tickets != nil {
				s.Tickets, s.Truncated = p.Tickets, p.Truncated
				s.TicketsFetchedAt = p.TicketsFetchedAt
				if s.TicketsFetchedAt.IsZero() {
					s.TicketsFetchedAt = prev.FetchedAt
				}
			}
		}
	}
}

// SortedAssignees returns assignees sorted and deduplicated: the form
// BoardSprints.Assignees is stored and compared in.
func SortedAssignees(assignees []string) []string {
	out := slices.Clone(assignees)
	slices.Sort(out)
	return slices.Compact(out)
}

// LockSprintsRefresh is LockRefresh for a board's sprints.
func LockSprintsRefresh(board int, window time.Duration) bool {
	path, err := sprintsPath(board)
	if err != nil {
		return false
	}
	lock := strings.TrimSuffix(path, ".json") + ".lock"
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		return false
	}
	return acquireLease(lock, window)
}

func writeSprints(board int, e sprintsEntry) error {
	path, err := sprintsPath(board)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(&e, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o644)
}

// sprintsPath is ~/.wgo/cache/jira/sprints/board-<id>.json.
func sprintsPath(board int) (string, error) {
	s, err := store.New()
	if err != nil {
		return "", err
	}
	return filepath.Join(s.BaseDir(), "cache", "jira", "sprints", "board-"+strconv.Itoa(board)+".json"), nil
}
