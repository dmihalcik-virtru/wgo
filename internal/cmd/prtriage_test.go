package cmd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/virtru/wgo/internal/config"
	"github.com/virtru/wgo/internal/dash"
)

func TestTriageRulesFromConfig(t *testing.T) {
	if _, err := triageRules(&config.Config{}); err != nil {
		t.Errorf("defaults: %v", err)
	}
	cfg := &config.Config{PR: config.PRConfig{Triage: config.PRTriageConfig{Rules: []config.PRTriageRule{
		{Bucket: "mine", Match: "prstate:draft"},
		{Bucket: "broken", Match: "prstat:draft"},
		{Bucket: "rest"},
	}}}}
	_, err := triageRules(cfg)
	if err == nil || !strings.Contains(err.Error(), `rule 2 (bucket "broken")`) || !strings.Contains(err.Error(), "config.toml") {
		t.Errorf("invalid rule: %v", err)
	}
}

// saveTriageFlags restores wgo pr triage's flags after a test.
func saveTriageFlags(t *testing.T) {
	t.Helper()
	out, force, js, cached, resync := triageOut, triageForce, triageJSON, triageCached, triageResync
	t.Cleanup(func() {
		triageOut, triageForce, triageJSON, triageCached, triageResync = out, force, js, cached, resync
	})
}

// TestPRTriageRefusesToOverwrite: an existing worklist is refused before
// any collection, and left as it was.
func TestPRTriageRefusesToOverwrite(t *testing.T) {
	saveTriageFlags(t)
	goldenHome(t, "")
	path := filepath.Join(t.TempDir(), "mine.md")
	if err := os.WriteFile(path, []byte("my notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	triageOut, triageForce, triageJSON = path, false, false
	err := runPRTriage(context.Background(), io.Discard, true)
	if err == nil || !strings.Contains(err.Error(), "--resync") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("err %v, want a refusal naming --resync and --force", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "my notes\n" {
		t.Errorf("the worklist changed: %q", data)
	}
	if err := refuseOverwrite(filepath.Join(t.TempDir(), "none.md")); err != nil {
		t.Errorf("a missing file: %v", err)
	}
}

func TestNewestWorklist(t *testing.T) {
	dir := t.TempDir()
	if _, err := newestWorklist(dir); err == nil || !strings.Contains(err.Error(), "wgo pr triage --resync PATH") {
		t.Errorf("empty dir: %v", err)
	}
	for _, n := range []string{"2026-11-01-prs.md", "2026-11-14-prs.md", "notes-prs.md"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := newestWorklist(dir); err != nil || filepath.Base(got) != "2026-11-14-prs.md" {
		t.Errorf("newest %q, %v", got, err)
	}
}

// TestResyncFile: --json leaves the file alone; otherwise new PRs are
// appended with the file's own line endings; a file that is not a
// worklist is refused untouched.
func TestResyncFile(t *testing.T) {
	v, err := dash.LoadFixture(filepath.Join("..", "dash", "testdata", "analysis_fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := triageRules(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := dash.BuildTriage(v, rules, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-10-30-prs.md")
	orig := "# Open PR triage, 2026-10-29 09:00\r\n\r\nmy notes\r\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := resyncFile(path, v, tr, false)
	if err != nil || len(r.New) != tr.Total {
		t.Fatalf("dry run: new %d of %d, %v", len(r.New), tr.Total, err)
	}
	if data, _ := os.ReadFile(path); string(data) != orig {
		t.Errorf("a dry run wrote the file:\n%q", data)
	}
	if _, err := resyncFile(path, v, tr, true); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	if !strings.HasPrefix(text, orig) || !strings.Contains(text, "## New since") || strings.Count(text, "\n") != strings.Count(text, "\r\n") {
		t.Errorf("resynced file lost its notes or mixed line endings:\n%q", text)
	}

	spec := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(spec, []byte("# A spec\n\n- [ ] a task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resyncFile(spec, v, tr, true); err == nil || !strings.Contains(err.Error(), "is not a worklist") {
		t.Errorf("not a worklist: %v", err)
	}
	if data, _ := os.ReadFile(spec); string(data) != "# A spec\n\n- [ ] a task\n" {
		t.Errorf("the non-worklist changed: %q", data)
	}
}

func TestWriteResync(t *testing.T) {
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.Local)
	r := &dash.Resync{
		Done: []dash.ResyncDone{
			{URL: "https://github.com/acme/api/pull/1", Section: "land (2)", State: "merged", At: at},
			{URL: "https://github.com/acme/api/pull/6", Section: "parked (3)", State: "gone"},
		},
		Left:  []dash.SectionCount{{Section: "land", Left: 1}, {Section: "parked", Left: 2}},
		Moved: []dash.ResyncMoved{{URL: "https://github.com/acme/api/pull/7", Section: "fix ticket (1)", Bucket: "draft?"}},
		New:   []dash.ResyncNew{{Bucket: "active", Item: dash.TriageItem{Ref: "acme/api#20", URL: "https://github.com/acme/api/pull/20", Title: "New work"}}},
		Open:  4,
		Sources: []dash.TriageSource{
			{Name: "alice/open-authored", State: dash.Stale, FetchedAt: at},
			{Name: dash.SourceJira, State: dash.Stale, FetchedAt: at},
		},
		Incomplete:  []string{"alice/open-authored: cut short"},
		TickSkipped: true,
	}
	var b strings.Builder
	writeResync(&b, "/tmp/w.md", r, false)
	out := b.String()
	for _, s := range []string{
		"2 newly closed or merged, 3 unchecked, 4 open on GitHub",
		"alice/open-authored is stale, fetched 2026-10-30 09:00",
		"INCOMPLETE: alice/open-authored: cut short; nothing was ticked",
		"✓ acme/api#1  merged 2026-10-30  (land (2))",
		"✓ acme/api#6  no longer open  (parked (3))",
		"~ acme/api#7 is now draft? (listed under fix ticket (1))",
		"1 new open PR, added under New since:",
		"+ acme/api#20  active  New work",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "jira is") {
		t.Errorf("a stale Jira does not hide closes, so it is not reported:\n%s", out)
	}
}
