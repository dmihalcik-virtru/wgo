package dash

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// tableVectors is testdata/table_vectors.json: a snapshot with gh-99's facts
// and check_stats (per its contract), the fields gh-99 registers, and sort
// cases. live_logic.js runs the same cases through live-table.js, so the
// table and the Markdown extract list rows in the same order.
type tableVectors struct {
	Fields   []Field         `json:"fields"`
	Snapshot json.RawMessage `json:"snapshot"`
	Sorts    []struct {
		Name  string   `json:"name"`
		Query string   `json:"query"`
		Sort  string   `json:"sort"`
		Order []string `json:"order"`
	} `json:"sorts"`
}

func loadTableVectors(t testing.TB) (tableVectors, *Registry) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "table_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v tableVectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	reg, err := Fields().With(v.Fields...)
	if err != nil {
		t.Fatalf("table vector fields: %v", err)
	}
	return v, reg
}

func TestTableSortVectors(t *testing.T) {
	v, reg := loadTableVectors(t)
	ix, err := newQueryIndex(v.Snapshot, reg)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Sorts {
		t.Run(c.Name, func(t *testing.T) {
			keys := SplitCols(c.Sort)
			if err := checkSort(reg, keys); err != nil {
				t.Fatal(err)
			}
			q, err := ParseQuery(c.Query, reg)
			if err != nil {
				t.Fatal(err)
			}
			rows := slices.Clone(ix.evaluate(q).rowIdx)
			ix.sortRows(rows, ParseSort(keys))
			got := make([]string, len(rows))
			for i, r := range rows {
				got[i] = ix.ids[r]
			}
			if !slices.Equal(got, c.Order) {
				t.Errorf("sort %q over %q:\n got  %q\n want %q", c.Sort, c.Query, got, c.Order)
			}
		})
	}
}

func TestExtractSortsLikeTheTable(t *testing.T) {
	v, reg := loadTableVectors(t)
	// A view straight from the JSON: the vectors' snapshot carries gh-99's
	// facts, which only the query index (decoding the raw JSON) reads.
	var snap Snapshot
	if err := json.Unmarshal(v.Snapshot, &snap); err != nil {
		t.Fatal(err)
	}
	view, err := newViewJSON(&snap, v.Snapshot, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	md, err := Extract(view, reg, nil, ExtractArgs{Query: "kind:pr", Cols: []string{"waiting", "move"}, Group: "none", Sort: []string{"-waiting", "move"}})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "- [") {
			order = append(order, line[3:strings.Index(line, "]")])
		}
	}
	// #5's wait is unknown; #1, #3 (author) and #4 (none) wait on no one,
	// so they come after it, in move order.
	want := []string{"acme/api#2", "acme/web#6", "acme/api#5", "acme/api#1", "acme/web#3", "acme/web#4"}
	if !slices.Equal(order, want) {
		t.Errorf("extract order = %q, want %q\n%s", order, want, md)
	}
	for _, s := range []string{"waiting: 10d · move: reviewer", "pull/5) — Bump the HSM vendor library · waiting: unknown · move: unknown",
		"pull/1) — Refresh tokens before expiry · move: author\n", // waiting does not apply
		"Sort: `-waiting,move` (unknown values last)\n"} {
		if !strings.Contains(md, s) {
			t.Errorf("extract lacks %q:\n%s", s, md)
		}
	}
	plain, err := Extract(view, reg, nil, ExtractArgs{Query: "kind:pr", Group: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "Sort:") {
		t.Errorf("an unsorted extract states no sort:\n%s", plain)
	}
}

func TestExtractSortErrors(t *testing.T) {
	v, _ := vectorFixture(t)
	// A registry where waiting is still planned, as before gh-99 landed.
	var fs []Field
	for _, f := range Fields().List() {
		if f.Name != "waiting" {
			fs = append(fs, f)
		}
	}
	reg := mustRegistry(fs, map[string]string{"waiting": "gh-99"})
	for _, tc := range []struct {
		sort []string
		want string
	}{
		{[]string{"-updatd_ago"}, `unknown sort key "updatd_ago"; did you mean updated_ago?`},
		{[]string{"waiting"}, "sort key waiting is not available yet: it arrives with gh-99"},
		{[]string{"-"}, `empty sort key "-"`},
		{[]string{"zzzzzz"}, `unknown sort key "zzzzzz". Fields:`},
	} {
		_, err := Extract(v, reg, nil, ExtractArgs{Sort: tc.sort})
		if !errors.Is(err, ErrExtractArgs) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want %q", tc.sort, err, tc.want)
		}
	}
}

func TestWaitingDoesNotApplyWithoutAWait(t *testing.T) {
	for _, tc := range []struct {
		facts map[string]any
		want  bool
	}{
		{map[string]any{"move": "none"}, true},
		{map[string]any{"move": "author", "reasons": []any{map[string]any{"code": "ci_failing"}}}, true},
		{map[string]any{"move": "author", "reasons": []any{map[string]any{"code": "changes_requested"}}}, false}, // the blocking review's time is unknown
		{map[string]any{"move": "reviewer"}, false},
		{map[string]any{"move": "nobody"}, false},
		{map[string]any{"move": "unknown"}, false},
		{nil, false},
	} {
		ix := &queryIndex{nodes: []map[string]any{{"pr": map[string]any{"facts": tc.facts}}}}
		if tc.facts == nil {
			ix.nodes[0] = map[string]any{"pr": map[string]any{}}
		}
		if got := ix.waitingNA(0); got != tc.want {
			t.Errorf("%v: waitingNA = %v, want %v", tc.facts, got, tc.want)
		}
	}
}
