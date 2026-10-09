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
// specific ledger PR (e.g. discussion-only collaborators).
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

// CardItem is the slice analyst's judgement of one work item. Its non-empty
// outcome and complexity supersede the ledger's mechanical guess for the
// authored PRs it cites. When several items cite a PR they apply in file-name
// then item order, and a later item's blank field keeps the earlier value.
type CardItem struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	URLs       []string `json:"urls"`
	Outcome    string   `json:"outcome"`
	Complexity string   `json:"complexity"`
}

// Run is a loaded run directory.
type Run struct {
	Label string
	Dir   string
	// Me is the user's login from coverage.json, or the placeholder "me" when
	// coverage.json does not name one.
	Me     string
	Rows   []Row
	People People
	Cards  []CardItem
	// Missing lists the optional inputs (coverage.json, people.json, cards/)
	// the run lacks, so callers can say what the graph is missing.
	Missing []string
}

// Load reads the run directory at dir. The ledger is required; people.json,
// coverage.json and cards/ are optional so a partially produced run (e.g.
// before the analysts finish) still loads.
func Load(dir string) (*Run, error) {
	// Abs so a relative argument such as "." still yields the run's name.
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	r := &Run{Label: filepath.Base(dir), Dir: dir}

	rows, err := readLedger(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		return nil, err
	}
	r.Rows = rows

	var cov struct {
		Me string `json:"me"`
	}
	found, err := readOptionalJSON(filepath.Join(dir, "coverage.json"), &cov)
	if err != nil {
		return nil, err
	}
	r.Me = cov.Me
	switch {
	case !found:
		r.Missing = append(r.Missing, "coverage.json")
	case r.Me == "":
		r.Missing = append(r.Missing, "the user's login in coverage.json")
	}
	if r.Me == "" {
		r.Me = "me"
	}

	found, err = readOptionalJSON(filepath.Join(dir, "people.json"), &r.People)
	if err != nil {
		return nil, err
	}
	if !found {
		r.Missing = append(r.Missing, "people.json")
	}

	cards, found, err := loadCards(filepath.Join(dir, "cards"))
	if err != nil {
		return nil, err
	}
	if !found {
		r.Missing = append(r.Missing, "cards/")
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
	line := 0
	for sc.Scan() {
		line++
		if len(sc.Bytes()) == 0 {
			continue
		}
		var row Row
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, line, err)
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s after line %d: %w", path, line, err)
	}
	return rows, nil
}

// readOptionalJSON decodes path into v, treating a missing file as empty. It
// reports whether the file existed.
func readOptionalJSON(path string, v any) (bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return true, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

// loadCards reads every *.json card file in dir in name order. It reports
// whether dir existed; any other failure to read it is an error.
func loadCards(dir string) ([]CardItem, bool, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, fmt.Errorf("list cards in %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var items []CardItem
	for _, name := range names {
		var card struct {
			Items []CardItem `json:"items"`
		}
		// The file was just listed, so a missing one is an error, not optional.
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, true, fmt.Errorf("read %s: %w", path, err)
		}
		if err := json.Unmarshal(b, &card); err != nil {
			return nil, true, fmt.Errorf("%s: %w", path, err)
		}
		items = append(items, card.Items...)
	}
	return items, true, nil
}
