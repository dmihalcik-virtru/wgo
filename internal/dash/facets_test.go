package dash

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The linked filters' browser suite (gh-102) drives the analysis fixture as
// it is.
func init() {
	browserSuites = append(browserSuites, browserSuite{flag: "facets", options: func(t testing.TB) HandlerOptions {
		v, err := LoadFixture(analysisFixturePath)
		if err != nil {
			t.Fatal(err)
		}
		return HandlerOptions{Source: StaticSource{V: v}}
	}})
}

func TestResolveFacets(t *testing.T) {
	reg := Fields()
	got, notes := ResolveFacets(nil, reg)
	if !slices.Equal(got, DefaultFacets) || notes != nil {
		t.Fatalf("no config: %v %v, want the defaults", got, notes)
	}
	got, notes = ResolveFacets([]string{" Repo ", "moev", "text", "repo", "", "size_bucket"}, reg)
	if !slices.Equal(got, []string{"repo", "size_bucket"}) {
		t.Errorf("facets %v, want repo, size_bucket", got)
	}
	want := []string{
		`[dash] facets in ~/.wgo/config.toml names "moev", which is not a query field; did you mean move? Fields: kind, text,`,
		"[dash] facets in ~/.wgo/config.toml names text, which cannot be a facet",
		"[dash] facets in ~/.wgo/config.toml lists repo twice; the second is ignored",
		"[dash] facets in ~/.wgo/config.toml has an empty name; remove it",
	}
	if len(notes) != len(want) {
		t.Fatalf("notes %q", notes)
	}
	for i, w := range want {
		if !strings.HasPrefix(notes[i], w) {
			t.Errorf("note %d = %q, want it to start %q", i, notes[i], w)
		}
	}
	got, notes = ResolveFacets([]string{"zzzzzz"}, reg)
	if !slices.Equal(got, DefaultFacets) || len(notes) != 2 || !strings.HasSuffix(notes[0], "which is not a query field. Fields: "+strings.Join(reg.Names(), ", ")) ||
		!strings.Contains(notes[1], "shows the default facets") {
		t.Errorf("nothing usable: %v %q", got, notes)
	}
	// A default whose field is not registered is left out.
	small, err := NewRegistry([]Field{{Name: "kind", Type: FieldEnum, Kinds: queryKinds, Paths: []string{"kind"}}, {Name: "repo", Type: FieldSet, Kinds: queryKinds, Paths: []string{"pr.repo"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := ResolveFacets(nil, small); !slices.Equal(got, []string{"repo"}) {
		t.Errorf("defaults over a small registry: %v", got)
	}
}

// TestFacetsReachThePage: the bootstrap carries the facets and the notes.
func TestFacetsReachThePage(t *testing.T) {
	_, body := get(t, newServer(t, fixtureDash(t)).URL+"/")
	if got := bootOf(t, body)["facets"]; len(got.([]any)) != len(DefaultFacets) {
		t.Errorf("default boot facets %v", got)
	}
	facets, notes := ResolveFacets([]string{"repo", "moev"}, Fields())
	_, body = get(t, newServerOpts(t, HandlerOptions{Source: fixtureDash(t), Facets: facets, FacetNotes: notes}).URL+"/")
	boot := bootOf(t, body)
	if f := boot["facets"].([]any); len(f) != 1 || f[0] != "repo" {
		t.Errorf("boot facets %v", f)
	}
	if n := boot["facet_notes"].([]any); len(n) != 1 || !strings.Contains(n[0].(string), "did you mean move?") {
		t.Errorf("boot facet notes %v", n)
	}
}

// TestFacetDefaultsMatchTheAddon: live-facets.js falls back to the same
// order for a bootstrap without facets.
func TestFacetDefaultsMatchTheAddon(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "review", "web", "live-facets.js"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`DEFAULT_FACETS = \[([^\]]*)\]`).FindSubmatch(b)
	if m == nil {
		t.Fatal("live-facets.js has no DEFAULT_FACETS = [...]")
	}
	var js []string
	for _, s := range strings.Split(string(m[1]), ",") {
		js = append(js, strings.Trim(strings.TrimSpace(s), `"`))
	}
	if !slices.Equal(js, DefaultFacets) {
		t.Errorf("live-facets.js DEFAULT_FACETS %v, Go DefaultFacets %v", js, DefaultFacets)
	}
}

// TestShippedWeekOnTheFixture (gh-102, requirement 8): brushing a week
// outside the last seven days from shipped lists exactly the PRs merged that
// week, by merge date, including those updated in a later week; a when:
// range finds the same merged PRs.
func TestShippedWeekOnTheFixture(t *testing.T) {
	v, err := LoadFixture(analysisFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pr:acme/api#108", "pr:acme/web#204", "pr:acme/web#205"}
	for _, expr := range []string{"kind:pr;prstate:merged;merged:2026-10-16..2026-10-22", "kind:pr;prstate:merged;when:2026-10-16..2026-10-22"} {
		res, err := v.Query(expr)
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Sorted(slices.Values(res.Rows)); !slices.Equal(got, want) {
			t.Errorf("%s: rows %v, want %v", expr, got, want)
		}
	}
}

// TestAttentionSummaryCountsOnTheFixture (gh-102, requirement 11): the
// attention summary's three counts are its saved filters' row counts; the
// JS summary asserts the same numbers in live_logic.js, so both evaluators
// agree on them.
func TestAttentionSummaryCountsOnTheFixture(t *testing.T) {
	v, err := LoadFixture(analysisFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int{"my-move": 4, "waiting-reviewers": 4, "nobody": 2} {
		f, ok := BuiltinFilter(name)
		if !ok {
			t.Fatalf("no built-in %s", name)
		}
		res, err := v.Query(f.Expr)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != want {
			t.Errorf("%s (%s): %d rows, want %d", name, f.Expr, len(res.Rows), want)
		}
	}
}
