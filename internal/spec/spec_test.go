package spec

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseBytes_NoFrontmatter(t *testing.T) {
	input := []byte("# Hello\n\nNo frontmatter here.\n")
	sf, err := ParseBytes(input)
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if sf.Frontmatter.Ticket != "" {
		t.Errorf("Frontmatter.Ticket = %q, want empty", sf.Frontmatter.Ticket)
	}
	if sf.Body != string(input) {
		t.Errorf("Body = %q, want full input", sf.Body)
	}
}

func TestParseBytes_WithFrontmatter(t *testing.T) {
	input := []byte("---\n" +
		"ticket: WGO-101\n" +
		"title: Spec scaffold\n" +
		"status: draft\n" +
		"authors: [dmihalcik, sujan]\n" +
		"branches: []\n" +
		"prs: []\n" +
		"created: 2026-05-06\n" +
		"updated: 2026-05-06\n" +
		"---\n\n" +
		"# WGO-101\n\nbody text\n")

	sf, err := ParseBytes(input)
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if sf.Frontmatter.Ticket != "WGO-101" {
		t.Errorf("Ticket = %q, want WGO-101", sf.Frontmatter.Ticket)
	}
	if sf.Frontmatter.Status != StatusDraft {
		t.Errorf("Status = %q, want draft", sf.Frontmatter.Status)
	}
	if got := sf.Frontmatter.Created.Format("2006-01-02"); got != "2026-05-06" {
		t.Errorf("Created = %q, want 2026-05-06", got)
	}
	if got := sf.Frontmatter.Updated.Format("2006-01-02"); got != "2026-05-06" {
		t.Errorf("Updated = %q, want 2026-05-06", got)
	}
	if len(sf.Frontmatter.Authors) != 2 || sf.Frontmatter.Authors[0] != "dmihalcik" {
		t.Errorf("Authors = %v, want [dmihalcik sujan]", sf.Frontmatter.Authors)
	}
	if !strings.Contains(sf.Body, "# WGO-101") {
		t.Errorf("Body missing header: %q", sf.Body)
	}
}

func TestUpdateFrontmatter_PreservesBody(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.md")
	body := "\n# Title\n\n## Section\n\nLine with trailing space \nFinal line\n"
	original := "---\n" +
		"ticket: WGO-200\n" +
		"status: draft\n" +
		"authors: [a]\n" +
		"branches: []\n" +
		"prs: []\n" +
		"created: 2026-05-06\n" +
		"updated: 2026-05-06\n" +
		"---" + body
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := UpdateFrontmatter(path, func(fm *Frontmatter) error {
		fm.Status = StatusInProgress
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateFrontmatter: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	gotStr := string(got)

	idx := strings.Index(gotStr[4:], "\n---")
	if idx == -1 {
		t.Fatalf("rewritten file missing closing ---: %q", gotStr)
	}
	bodyStart := 4 + idx + 4
	if got, want := gotStr[bodyStart:], body; got != want {
		t.Errorf("body changed:\n got: %q\nwant: %q", got, want)
	}

	sf, err := Parse(path)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if sf.Frontmatter.Status != StatusInProgress {
		t.Errorf("Status = %q, want in_progress", sf.Frontmatter.Status)
	}
	if sf.Frontmatter.Ticket != "WGO-200" {
		t.Errorf("Ticket = %q, want WGO-200", sf.Frontmatter.Ticket)
	}
}

func TestParseTicketFromBranch(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"WGO-101", "WGO-101"},
		{"WGO-101-spec-foo", "WGO-101"},
		{"feature-WGO-101", ""},
		{"not-a-ticket", ""},
		{"", ""},
		{"A-1", "A-1"},
		{"A-1-something", "A-1"},
		{"DSPX-2674-fix-bug", "DSPX-2674"},
		{"wgo-101", ""},
	}
	for _, tt := range tests {
		got := ParseTicketFromBranch(tt.in)
		if got != tt.want {
			t.Errorf("ParseTicketFromBranch(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRenderTemplate(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	data, err := RenderTemplate(TemplateData{
		Ticket:      "WGO-999",
		Title:       "demo",
		Description: "demo description",
		Authors:     []string{"alice", "bob"},
		Branches:    []string{"virtru/wgo:WGO-999-demo"},
		Now:         now,
	})
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "ticket: WGO-999") {
		t.Errorf("missing ticket line: %q", out)
	}
	if !strings.Contains(out, "created: 2026-05-06") {
		t.Errorf("missing created date: %q", out)
	}
	if !strings.Contains(out, "authors: [alice, bob]") {
		t.Errorf("authors not rendered as list: %q", out)
	}
	if !strings.Contains(out, "branches: [virtru/wgo:WGO-999-demo]") {
		t.Errorf("branches not rendered: %q", out)
	}

	sf, err := ParseBytes(data)
	if err != nil {
		t.Fatalf("rendered template does not re-parse: %v", err)
	}
	if sf.Frontmatter.Ticket != "WGO-999" {
		t.Errorf("re-parsed Ticket = %q, want WGO-999", sf.Frontmatter.Ticket)
	}
	if sf.Frontmatter.Status != StatusDraft {
		t.Errorf("re-parsed Status = %q, want draft", sf.Frontmatter.Status)
	}
}

func TestFindByTicket_Exists(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	specPath := filepath.Join(dir, "spec", "WGO-999.md")
	if err := os.WriteFile(specPath, []byte("---\nticket: WGO-999\nstatus: draft\nauthors: []\nbranches: []\nprs: []\ncreated: 2026-05-06\nupdated: 2026-05-06\n---\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := FindByTicket(dir, "WGO-999")
	if err != nil {
		t.Fatalf("FindByTicket: %v", err)
	}
	if got != specPath {
		t.Errorf("path = %q, want %q", got, specPath)
	}
}

func TestFindByTicket_Missing(t *testing.T) {
	dir := t.TempDir()
	_, err := FindByTicket(dir, "WGO-404")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want os.ErrNotExist", err)
	}
}

func TestFindByBranch(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	specPath := filepath.Join(dir, "spec", "WGO-101.md")
	if err := os.WriteFile(specPath, []byte("---\nticket: WGO-101\nstatus: draft\nauthors: []\nbranches: []\nprs: []\ncreated: 2026-05-06\nupdated: 2026-05-06\n---\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := FindByBranch(dir, "WGO-101-spec-foo")
	if err != nil {
		t.Fatalf("FindByBranch: %v", err)
	}
	if got != specPath {
		t.Errorf("path = %q, want %q", got, specPath)
	}

	if _, err := FindByBranch(dir, "feature-WGO-101"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("non-prefix branch should yield os.ErrNotExist, got %v", err)
	}
}
