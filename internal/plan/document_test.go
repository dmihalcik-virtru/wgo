package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/virtru/wgo/internal/bujo"
)

func containsSubstring(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestGoldenRoundTrip parses each testdata/roundtrip/*.md file and renders it
// unchanged: the output must equal the input byte-for-byte, and every
// expected diagnostic in the matching .diag file must be reported.
func TestGoldenRoundTrip(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "roundtrip", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no golden files found")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			in, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			p, err := Parse(string(in))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := p.Render(); got != string(in) {
				t.Errorf("round trip changed the file.\n--- want\n%s\n--- got\n%s", in, got)
			}
			want, err := os.ReadFile(strings.TrimSuffix(f, ".md") + ".diag")
			if err != nil {
				t.Fatalf("missing .diag file: %v", err)
			}
			for _, d := range strings.Split(strings.TrimSpace(string(want)), "\n") {
				if d == "" {
					continue
				}
				if !containsSubstring(p.Diagnostics, d) {
					t.Errorf("missing diagnostic %q in %q", d, p.Diagnostics)
				}
			}
		})
	}
}

func readGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "roundtrip", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustParse(t *testing.T, content string) *Plan {
	t.Helper()
	p, err := Parse(content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p
}

func TestGoldenFullParsedView(t *testing.T) {
	p := mustParse(t, readGolden(t, "full.md"))

	if len(p.Tasks) != 3 {
		t.Errorf("tasks = %d, want 3", len(p.Tasks))
	}
	if len(p.ActiveBranches) != 2 {
		t.Errorf("active branches = %d, want 2", len(p.ActiveBranches))
	}
	if p.Notes != "Some notes." {
		t.Errorf("notes = %q, want only the first Notes section", p.Notes)
	}
	var headers []string
	for _, s := range p.UnmanagedSections {
		headers = append(headers, s.Header)
	}
	want := []string{"## Custom Section", "## Custom Section", "## Notes"}
	if strings.Join(headers, "|") != strings.Join(want, "|") {
		t.Errorf("unmanaged sections = %q, want %q", headers, want)
	}
	if !strings.Contains(p.UnmanagedSections[0].Content, "## not a heading, it is inside a fence") {
		t.Errorf("fenced heading split the custom section: %q", p.UnmanagedSections[0].Content)
	}

	id := GenerateEffortID("Plan efforts")
	e, ok := p.Efforts[id]
	if !ok {
		t.Fatalf("Plan efforts missing: %v", p.Efforts)
	}
	if strings.Join(e.Branches, ",") != "wgo:gh-71-plan-efforts,wgo:feat/x" {
		t.Errorf("first block wins for the duplicate heading; branches = %q", e.Branches)
	}
	if e.Description != "Make efforts a populated, human-editable grouping." {
		t.Errorf("description = %q", e.Description)
	}
	if got := p.EffortOrder; len(got) != 2 || got[0] != id || got[1] != GenerateEffortID("Dashboard") {
		t.Errorf("effort order = %q", got)
	}
}

// replaceOnce replaces exactly one occurrence of old in s, failing otherwise,
// so expected outputs are derived from the golden input by explicit edits.
func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if n := strings.Count(s, old); n != 1 {
		t.Fatalf("expected exactly one %q in input, found %d", old, n)
	}
	return strings.Replace(s, old, new, 1)
}

func TestMutationsTouchOnlyTheirLines(t *testing.T) {
	in := readGolden(t, "full.md")

	t.Run("add task", func(t *testing.T) {
		p := mustParse(t, in)
		p.AddTask(bujo.BulletOpen, "new task")
		want := replaceOnce(t, in, "- [ ] a passthrough note line\n", "- [ ] a passthrough note line\n○ new task\n")
		if got := p.Render(); got != want {
			t.Errorf("got:\n%s", got)
		}
	})

	t.Run("remove task", func(t *testing.T) {
		p := mustParse(t, in)
		if p.RemoveTask("flaky") == nil {
			t.Fatal("task not found")
		}
		want := replaceOnce(t, in, "! fix the flaky test\n", "")
		if got := p.Render(); got != want {
			t.Errorf("got:\n%s", got)
		}
	})

	t.Run("update task in place", func(t *testing.T) {
		p := mustParse(t, in)
		p.UpdateTask("ship", bujo.BulletDone)
		want := replaceOnce(t, in, "○ ship the efforts parser\n", "✓ ship the efforts parser\n")
		if got := p.Render(); got != want {
			t.Errorf("got:\n%s", got)
		}
	})

	t.Run("add branch", func(t *testing.T) {
		p := mustParse(t, in)
		p.AddBranch("api", "fix/auth", "Fix auth")
		want := replaceOnce(t, in, "↳ on gh-71-plan-efforts\n", "↳ on gh-71-plan-efforts\n- **api:fix/auth** — Fix auth\n")
		if got := p.Render(); got != want {
			t.Errorf("got:\n%s", got)
		}
	})

	t.Run("update branch reason in place", func(t *testing.T) {
		p := mustParse(t, in)
		p.AddBranch("wgo", "gh-71-plan-efforts", "new reason", "spec/gh-71.md")
		want := replaceOnce(t, in, "Parse and author plan efforts 📄", "new reason 📄")
		if got := p.Render(); got != want {
			t.Errorf("got:\n%s", got)
		}
	})

	t.Run("link to effort", func(t *testing.T) {
		p := mustParse(t, in)
		id := GenerateEffortID("Plan efforts")
		e := p.Efforts[id]
		e.Branches = append(e.Branches, "api:gh-71")
		p.Efforts[id] = e
		want := replaceOnce(t, in, "- wgo:feat/x\n\nA stray", "- wgo:feat/x\n- api:gh-71\n\nA stray")
		if got := p.Render(); got != want {
			t.Errorf("got:\n%s", got)
		}
	})

	t.Run("unlink from effort keeps stray text and duplicate block", func(t *testing.T) {
		p := mustParse(t, in)
		id := GenerateEffortID("Plan efforts")
		e := p.Efforts[id]
		e.Branches = e.Branches[1:]
		p.Efforts[id] = e
		want := replaceOnce(t, in, "- wgo:gh-71-plan-efforts\n- wgo:feat/x\n", "- wgo:feat/x\n")
		if got := p.Render(); got != want {
			t.Errorf("got:\n%s", got)
		}
	})

	t.Run("remove effort drops only its block", func(t *testing.T) {
		p := mustParse(t, in)
		delete(p.Efforts, GenerateEffortID("Dashboard"))
		want := replaceOnce(t, in, "### Dashboard\n- wgo:gh-70-dashboard\n\n", "")
		if got := p.Render(); got != want {
			t.Errorf("got:\n%s", got)
		}
	})

	t.Run("new efforts append by name", func(t *testing.T) {
		p := mustParse(t, in)
		for _, name := range []string{"Zeta", "Alpha"} {
			id := GenerateEffortID(name)
			p.Efforts[id] = EffortEntry{ID: id, Name: name, Branches: []string{"wgo:" + strings.ToLower(name)}}
		}
		want := replaceOnce(t, in, "- not:an-entry\n~~~\n\n## Notes", "- not:an-entry\n~~~\n\n### Alpha\n- wgo:alpha\n\n### Zeta\n- wgo:zeta\n\n## Notes")
		if got := p.Render(); got != want {
			t.Errorf("got:\n%s", got)
		}
	})

	t.Run("change notes", func(t *testing.T) {
		p := mustParse(t, in)
		p.Notes = "Rewritten."
		want := replaceOnce(t, in, "## Notes\n\nSome notes.\n", "## Notes\n\nRewritten.\n")
		if got := p.Render(); got != want {
			t.Errorf("got:\n%s", got)
		}
	})
}

func TestMissingSectionsAreInserted(t *testing.T) {
	in := "# Plan\n\nIntro.\n\n## Custom\n\nkeep\n"
	p := mustParse(t, in)
	p.AddTask(bujo.BulletOpen, "task")
	id := GenerateEffortID("E")
	p.Efforts[id] = EffortEntry{ID: id, Name: "E", Description: "Desc.", Branches: []string{"r:b"}}
	want := in + "\n## Tasks\n\n○ task\n\n## Efforts\n\n### E\nDesc.\n\n- r:b\n"
	if got := p.Render(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}

	// Inserted before the next owned section that exists.
	in2 := "# Plan\n\n## Active Branches\n\n## Notes\n"
	p2 := mustParse(t, in2)
	p2.AddTask(bujo.BulletOpen, "task")
	p2.Efforts[id] = EffortEntry{ID: id, Name: "E", Branches: []string{"r:b"}}
	want2 := "# Plan\n\n## Tasks\n\n○ task\n\n## Active Branches\n\n## Efforts\n\n### E\n- r:b\n\n## Notes\n"
	if got := p2.Render(); got != want2 {
		t.Errorf("got:\n%q\nwant:\n%q", got, want2)
	}
}

func TestEffortWithoutEntriesGetsLinked(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"heading only":       {"## Efforts\n\n### E\n", "## Efforts\n\n### E\n\n- r:b\n"},
		"heading then blank": {"## Efforts\n\n### E\n\n## Notes\n", "## Efforts\n\n### E\n- r:b\n\n## Notes\n"},
		"with description":   {"## Efforts\n\n### E\nDesc.\n\n## Notes\n", "## Efforts\n\n### E\nDesc.\n\n- r:b\n\n## Notes\n"},
		"two paragraphs":     {"## Efforts\n\n### E\nDesc.\n\nmore\n", "## Efforts\n\n### E\nDesc.\n\nmore\n\n- r:b\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := mustParse(t, tc.in)
			id := GenerateEffortID("E")
			e := p.Efforts[id]
			e.Branches = append(e.Branches, "r:b")
			p.Efforts[id] = e
			if got := p.Render(); got != tc.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

func TestCRLFPreserved(t *testing.T) {
	lf := readGolden(t, "full.md")
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")

	p := mustParse(t, crlf)
	if got := p.Render(); got != crlf {
		t.Fatalf("CRLF round trip changed the file")
	}
	if p.Notes != "Some notes." {
		t.Errorf("CRLF left a carriage return in parsed text: %q", p.Notes)
	}

	p.AddTask(bujo.BulletOpen, "new task")
	got := p.Render()
	if strings.Count(got, "\n") != strings.Count(got, "\r\n") {
		t.Errorf("mixed line endings after an edit:\n%q", got)
	}
	want := replaceOnce(t, crlf, "- [ ] a passthrough note line\r\n", "- [ ] a passthrough note line\r\n○ new task\r\n")
	if got != want {
		t.Errorf("got:\n%q", got)
	}
}

func TestMixedLineEndingsNormalized(t *testing.T) {
	in := "# Plan\r\n\r\n## Notes\r\n\r\nx\n"
	p := mustParse(t, in)
	if got := p.Render(); got != strings.ReplaceAll(in, "x\n", "x\r\n") {
		t.Errorf("got %q", got)
	}
	if !containsSubstring(p.Diagnostics, "mixed line endings") {
		t.Errorf("expected a mixed-line-endings diagnostic, got %q", p.Diagnostics)
	}
}

func TestNoTrailingNewlinePreserved(t *testing.T) {
	in := "# Plan\n\n## Custom\n\nno newline at end"
	p := mustParse(t, in)
	if got := p.Render(); got != in {
		t.Errorf("got %q", got)
	}
}

// TestRenderDeterministic renders the same edits many times, and feeds each
// render back through Parse, expecting identical output every time.
func TestRenderDeterministic(t *testing.T) {
	base := readGolden(t, "full.md")
	edit := func() string {
		p := mustParse(t, base)
		for _, b := range []string{"zeta", "alpha", "mid", "beta"} {
			p.AddBranch("repo", b, "reason "+b)
		}
		for _, name := range []string{"Zed", "Ann", "Mo", "Bea"} {
			id := GenerateEffortID(name)
			p.Efforts[id] = EffortEntry{ID: id, Name: name, Branches: []string{"r:" + name}}
		}
		return p.Render()
	}
	first := edit()
	for i := 0; i < 20; i++ {
		if got := edit(); got != first {
			t.Fatalf("render %d differs:\n%s\n--- first\n%s", i, got, first)
		}
	}
	cur := first
	for i := 0; i < 5; i++ {
		next := mustParse(t, cur).Render()
		if next != cur {
			t.Fatalf("parse/render cycle %d changed output:\n%s", i, next)
		}
		cur = next
	}
	if !strings.Contains(first, "- **repo:alpha** — reason alpha\n- **repo:beta** — reason beta\n- **repo:mid** — reason mid\n- **repo:zeta** — reason zeta\n") {
		t.Errorf("new active branches not sorted:\n%s", first)
	}
	if !(strings.Index(first, "### Ann") < strings.Index(first, "### Bea") &&
		strings.Index(first, "### Bea") < strings.Index(first, "### Mo") &&
		strings.Index(first, "### Mo") < strings.Index(first, "### Zed")) {
		t.Errorf("new efforts not sorted by name:\n%s", first)
	}
}

func TestDuplicateHeadingBlocksSurviveEdits(t *testing.T) {
	in := readGolden(t, "duplicate_headings.md")
	p := mustParse(t, in)
	id := GenerateEffortID("Same")
	e := p.Efforts[id]
	if strings.Join(e.Branches, ",") != "a:one" {
		t.Fatalf("first block should own the ID, got %q", e.Branches)
	}
	e.Branches = append(e.Branches, "a:new")
	p.Efforts[id] = e
	want := replaceOnce(t, in, "- a:one\n", "- a:one\n- a:new\n")
	if got := p.Render(); got != want {
		t.Errorf("got:\n%s", got)
	}

	// Removing the effort drops only the owning block; the duplicates stay.
	p = mustParse(t, in)
	delete(p.Efforts, id)
	want = replaceOnce(t, in, "### Same\nFirst block.\n\n- a:one\n\n", "")
	if got := p.Render(); got != want {
		t.Errorf("got:\n%s", got)
	}
}

func TestPlanBuiltInCode(t *testing.T) {
	p := &Plan{ActiveBranches: map[string]BranchEntry{}, Efforts: map[string]EffortEntry{}}
	p.AddBranch("r", "b", "why")
	want := "# Plan\n\n## Active Branches\n\n- **r:b** — why\n\n## Notes\n"
	if got := p.Render(); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestRenderWithPairInsertsAfterActiveBranches(t *testing.T) {
	in := "# Plan\n\n## Active Branches\n\n- **r:b** — why\n\n## Custom\n\nkeep\n"
	p := mustParse(t, in)
	got := p.RenderWithPair("Pat", map[string]BranchEntry{"r:b": {Repo: "r", Branch: "b", Reason: "why"}})
	want := "# Plan\n\n## Active Branches\n\n- **r:b** — why\n\n## Active With Pat\n\n- **r:b** — why\n\n## Custom\n\nkeep\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestDiagnosticsSortedByLine(t *testing.T) {
	for _, name := range []string{"full.md", "malformed_efforts.md"} {
		p := mustParse(t, readGolden(t, name))
		prev := 0
		for _, d := range p.Diagnostics {
			var n int
			if _, err := fmt.Sscanf(d, "plan line %d:", &n); err != nil {
				continue // file-level diagnostics carry no line
			}
			if n < prev {
				t.Errorf("%s: diagnostics out of line order: %q", name, p.Diagnostics)
				break
			}
			prev = n
		}
	}
}
