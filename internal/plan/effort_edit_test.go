package plan

import (
	"strings"
	"testing"
)

func editEffort(t *testing.T, in, name string, edit func(*EffortEntry)) string {
	t.Helper()
	p := mustParse(t, in)
	id := GenerateEffortID(name)
	e, ok := p.Efforts[id]
	if !ok {
		t.Fatalf("effort %q not parsed", name)
	}
	edit(&e)
	p.Efforts[id] = e
	return p.Render()
}

func TestPatchEffortBlock(t *testing.T) {
	const withDesc = "# Plan\n\n## Efforts\n\n### Auth\nFirst line.\nSecond line.\n\n- wgo:a\n- api:b\n\n### Other\n- wgo:z\n"
	const noDesc = "# Plan\n\n## Efforts\n\n### Auth\n- wgo:a\n- api:b\n\n### Other\n- wgo:z\n"

	tests := []struct {
		name string
		in   string
		edit func(*EffortEntry)
		want string
	}{
		{
			name: "set a description where there is none",
			in:   noDesc,
			edit: func(e *EffortEntry) { e.Description = "Why it exists." },
			want: "# Plan\n\n## Efforts\n\n### Auth\nWhy it exists.\n\n- wgo:a\n- api:b\n\n### Other\n- wgo:z\n",
		},
		{
			name: "change a multi-line description",
			in:   withDesc,
			edit: func(e *EffortEntry) { e.Description = "Only one line." },
			want: "# Plan\n\n## Efforts\n\n### Auth\nOnly one line.\n\n- wgo:a\n- api:b\n\n### Other\n- wgo:z\n",
		},
		{
			name: "grow a description",
			in:   withDesc,
			edit: func(e *EffortEntry) { e.Description = "One.\nTwo.\nThree.\nFour." },
			want: "# Plan\n\n## Efforts\n\n### Auth\nOne.\nTwo.\nThree.\nFour.\n\n- wgo:a\n- api:b\n\n### Other\n- wgo:z\n",
		},
		{
			name: "clear a description and its separator",
			in:   withDesc,
			edit: func(e *EffortEntry) { e.Description = "" },
			want: noDesc,
		},
		{
			name: "change the description and link in one mutation",
			in:   withDesc,
			edit: func(e *EffortEntry) {
				e.Description = "Short."
				e.Branches = append(e.Branches, "web:c")
			},
			want: "# Plan\n\n## Efforts\n\n### Auth\nShort.\n\n- wgo:a\n- api:b\n- web:c\n\n### Other\n- wgo:z\n",
		},
		{
			name: "clear the description and unlink the first entry",
			in:   withDesc,
			edit: func(e *EffortEntry) {
				e.Description = ""
				e.Branches = e.Branches[1:]
			},
			want: "# Plan\n\n## Efforts\n\n### Auth\n- api:b\n\n### Other\n- wgo:z\n",
		},
		{
			name: "rename the heading",
			in:   withDesc,
			edit: func(e *EffortEntry) { e.Name = "Authn" },
			want: strings.Replace(withDesc, "### Auth\n", "### Authn\n", 1),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := editEffort(t, tc.in, "Auth", tc.edit); got != tc.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

func TestInsertedEntryStaysAfterNestedLines(t *testing.T) {
	const in = "# Plan\n\n## Active Branches\n\n- **wgo:a** — r\n  - detail\n\n## Notes\n"
	p := mustParse(t, in)
	p.AddBranch("wgo", "b", "r2")
	want := "# Plan\n\n## Active Branches\n\n- **wgo:a** — r\n  - detail\n- **wgo:b** — r2\n\n## Notes\n"
	if got := p.Render(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestParseBranchRef(t *testing.T) {
	good := map[string][2]string{
		"wgo:feat":                  {"wgo", "feat"},
		"owner/repo:feat":           {"owner/repo", "feat"},
		"/Users/a/My Repos/wgo:foo": {"/Users/a/My Repos/wgo", "foo"},
	}
	for ref, want := range good {
		repo, bm, ok := ParseBranchRef(ref)
		if !ok || repo != want[0] || bm != want[1] {
			t.Errorf("ParseBranchRef(%q) = %q, %q, %v", ref, repo, bm, ok)
		}
	}
	for _, ref := range []string{"", "wgo", ":feat", "wgo:", "wgo:a b", " :x"} {
		if _, _, ok := ParseBranchRef(ref); ok {
			t.Errorf("ParseBranchRef(%q) accepted", ref)
		}
	}
}

func TestEffortEntryWithSpaceInPathSurvivesReparse(t *testing.T) {
	const in = "# Plan\n\n## Efforts\n\n### Auth\n- /Users/a/My Repos/wgo:foo\n"
	p := mustParse(t, in)
	e := p.Efforts[GenerateEffortID("Auth")]
	if len(e.Branches) != 1 || e.Branches[0] != "/Users/a/My Repos/wgo:foo" {
		t.Fatalf("branches = %q, diagnostics = %q", e.Branches, p.Diagnostics)
	}
}

func TestEffortUnparsedLinesAndDuplicates(t *testing.T) {
	const in = "# Plan\n\n## Efforts\n\n### Auth\nDesc.\n- bad entry\n- wgo:a\n\nstray note\n\n### Auth\n- wgo:z\n"
	p := mustParse(t, in)
	id := GenerateEffortID("Auth")
	got := p.EffortUnparsedLines(id)
	if len(got) != 2 || got[0] != "- bad entry" || got[1] != "stray note" {
		t.Errorf("unparsed = %q", got)
	}
	if !p.EffortHasDuplicateHeading(id) {
		t.Error("duplicate heading not reported")
	}
	if p.EffortHasDuplicateHeading("nope") {
		t.Error("unexpected duplicate")
	}
	if e := p.Efforts[id]; e.Description != "Desc." {
		t.Errorf("description = %q", e.Description)
	}
}

func TestValidateEffort(t *testing.T) {
	ok := EffortEntry{Name: "Auth", Description: "Why.\nBecause.", Branches: []string{"wgo:a"}}
	if err := ValidateEffort(ok); err != nil {
		t.Fatal(err)
	}
	bad := []EffortEntry{
		{Name: ""},
		{Name: "a\nb"},
		{Name: "# x"},
		{Name: "x", Description: "## heading"},
		{Name: "x", Description: "- item"},
		{Name: "x", Branches: []string{"a:b c"}},
		{Name: "x", Branches: []string{"nocolon"}},
	}
	for _, e := range bad {
		if ValidateEffort(e) == nil {
			t.Errorf("ValidateEffort(%+v) accepted", e)
		}
	}
}
