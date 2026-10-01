// Package review turns a year-in-review run directory (written by the
// year-in-review skill under ~/.wgo/cache/review/runs/<label>/) into an
// influence graph of people, PRs, tickets and repos. The run-dir layout is the
// only contract with the skill; everything here is read-only.
package review

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Row is one ledger.jsonl record. The ledger is heterogeneous: PR rows carry a
// kind (pr_authored, pr_reviewed, ...) and Jira rows carry kind "jira" plus
// key. Fields a kind does not use stay zero.
type Row struct {
	Kind    string   `json:"kind"`
	URL     string   `json:"url"`
	Repo    string   `json:"repo"`
	Number  int      `json:"number"`
	Title   string   `json:"title"`
	Author  string   `json:"author"`
	State   string   `json:"state"`
	Created string   `json:"created"`
	Tickets []string `json:"tickets"`

	// pr_authored
	Additions        int                       `json:"additions"`
	Deletions        int                       `json:"deletions"`
	Outcome          string                    `json:"outcome"`
	Complexity       string                    `json:"complexity"`
	CycleHours       *float64                  `json:"cycle_hours"`
	Reviewers        map[string]map[string]int `json:"reviewers"`
	BotReviewers     []string                  `json:"bot_reviewers"`
	DownstreamOthers []string                  `json:"downstream_others"`
	CrossRefs        []CrossRef                `json:"cross_refs"`

	// pr_reviewed
	MyReviewStates map[string]int `json:"my_review_states"`

	// jira
	Key         string   `json:"key"`
	Summary     string   `json:"summary"`
	Parent      string   `json:"parent"`
	Status      string   `json:"status"`
	Components  []string `json:"components"`
	StoryPoints *float64 `json:"story_points"`
}

// CrossRef is a PR or issue that references one of the user's PRs.
type CrossRef struct {
	URL    string `json:"url"`
	Title  string `json:"title"`
	Author string `json:"author"`
	Type   string `json:"type"`
}

// Counterpart is the aggregate relationship between the user and one person,
// from people.json. It is the only source for people who never appear on a
// specific authored PR (e.g. discussion-only collaborators).
type Counterpart struct {
	ReviewedMyPRs int `json:"reviewed_my_prs"`
	BuiltOnMyPRs  int `json:"built_on_my_prs"`
	IReviewed     int `json:"i_reviewed"`
	Discussed     int `json:"discussed"`
}

// People mirrors people.json.
type People struct {
	Counterparts map[string]Counterpart `json:"counterparts"`
	Teams        map[string][]string    `json:"teams"`
	MyTeams      []string               `json:"my_teams"`
}

// CardItem is the slice analyst's judgement of one work item. Its outcome and
// complexity supersede the ledger's mechanical guess for the PRs it cites.
type CardItem struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	URLs       []string `json:"urls"`
	Outcome    string   `json:"outcome"`
	Complexity string   `json:"complexity"`
}

// Run is a loaded run directory.
type Run struct {
	Label  string
	Dir    string
	Me     string
	Rows   []Row
	People People
	Cards  []CardItem
}

// Load reads the run directory at dir. The ledger is required; people.json,
// coverage.json and cards/ are optional so a partially produced run (e.g.
// before the analysts finish) still loads.
func Load(dir string) (*Run, error) {
	r := &Run{Label: filepath.Base(dir), Dir: dir}

	rows, err := readLedger(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		return nil, err
	}
	r.Rows = rows

	var cov struct {
		Me string `json:"me"`
	}
	if err := readOptionalJSON(filepath.Join(dir, "coverage.json"), &cov); err != nil {
		return nil, err
	}
	r.Me = cov.Me
	if r.Me == "" {
		r.Me = "me"
	}

	if err := readOptionalJSON(filepath.Join(dir, "people.json"), &r.People); err != nil {
		return nil, err
	}

	cards, err := loadCards(filepath.Join(dir, "cards"))
	if err != nil {
		return nil, err
	}
	r.Cards = cards
	return r, nil
}

func readLedger(path string) ([]Row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w (is this a year-in-review run directory?)", err)
	}
	defer func() { _ = f.Close() }()

	var rows []Row
	sc := bufio.NewScanner(f)
	// Ledger lines embed PR bodies and cross-ref lists, so they can be large.
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for line := 1; sc.Scan(); line++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var row Row
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("ledger.jsonl line %d: %w", line, err)
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read ledger: %w", err)
	}
	return rows, nil
}

// readOptionalJSON decodes path into v, treating a missing file as empty.
func readOptionalJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

func loadCards(dir string) ([]CardItem, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var items []CardItem
	for _, p := range paths {
		var card struct {
			Items []CardItem `json:"items"`
		}
		if err := readOptionalJSON(p, &card); err != nil {
			return nil, err
		}
		items = append(items, card.Items...)
	}
	return items, nil
}
