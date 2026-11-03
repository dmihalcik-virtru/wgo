package jiracache

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oldEntry is a cache entry as wgo wrote it before ticket facts existed.
const oldEntry = `{
  "info": {"status": "In Review", "assignee": "Alice", "site": "acme.example"},
  "fetched_at": "%s"
}`

func writeRaw(t *testing.T, home, ticket, content string) {
	t.Helper()
	dir := filepath.Join(home, ".wgo", "cache", "jira")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ticket+".json"), []byte(content), 0o644))
}

// TestOldEntryStillLoads: an entry written before the facts existed is a
// normal hit; its new fields are unknown (zero, nil lists, story points not
// known) until the next refresh.
func TestOldEntryStillLoads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeRaw(t, home, "TEAM-1", fmt.Sprintf(oldEntry, time.Now().UTC().Format(time.RFC3339)))

	info, state := Read("TEAM-1", time.Hour)
	assert.Equal(t, Fresh, state)
	assert.Equal(t, Info{Status: "In Review", Assignee: "Alice", Site: "acme.example"}, info)
	assert.Nil(t, info.Labels, "unknown, not none")
	assert.Nil(t, info.Sprints)
	assert.False(t, info.StoryPointsKnown)
}

// TestFactsRoundTrip keeps every fact, and keeps an empty list (none) apart
// from a missing one (unknown).
func TestFactsRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	five := 5.0
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	in := Info{
		Status: "In Review", Assignee: "Alice", Site: "acme.example",
		Summary: "Refresh API tokens", StatusCategory: "in_progress", Type: "Story", Priority: "High",
		Sprints:     []SprintRef{{ID: 1230, Name: "Sprint 11", State: "closed"}, {ID: 1234, Name: "Sprint 12", State: "active"}},
		StoryPoints: &five, StoryPointsKnown: true,
		Parent: &Parent{Key: "TEAM-100", Summary: "Token lifecycle", Type: "Epic"},
		Team:   "Platform", Components: []string{}, Labels: []string{"backend"},
		Resolution: "Done", ResolvedAt: at, CreatedAt: at, UpdatedAt: at, AssigneeID: "acct-alice",
		Notes: []string{"a note"},
	}
	require.NoError(t, Write("TEAM-2", in))
	out, state := Read("TEAM-2", time.Hour)
	require.Equal(t, Fresh, state)
	assert.Equal(t, in, out)
	assert.NotNil(t, out.Components, "[] must survive the round trip as none")

	// Unestimated (known, no value) survives too.
	require.NoError(t, Write("TEAM-3", Info{StoryPointsKnown: true}))
	out, _ = Read("TEAM-3", time.Hour)
	assert.True(t, out.StoryPointsKnown)
	assert.Nil(t, out.StoryPoints)
}

func TestFieldsCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_, ok := ReadFields(FieldsTTL)
	assert.False(t, ok, "nothing discovered yet")

	require.NoError(t, WriteFields(Fields{Sprint: "customfield_10020"}))
	_, err := os.Stat(filepath.Join(home, ".wgo", "cache", "jira", "fields.json"))
	require.NoError(t, err, "fields are cached under ~/.wgo/cache/jira/fields.json")
	f, ok := ReadFields(FieldsTTL)
	assert.True(t, ok)
	assert.Equal(t, Fields{Sprint: "customfield_10020"}, f)

	_, ok = ReadFields(0)
	assert.False(t, ok, "expired discoveries are not reused")
}

// BenchmarkReadEntry measures the statusline's one cache read for an entry
// written before the ticket facts and for one carrying all of them.
func BenchmarkReadEntry(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)
	dir := filepath.Join(home, ".wgo", "cache", "jira")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "TEAM-1.json"), []byte(fmt.Sprintf(oldEntry, time.Now().UTC().Format(time.RFC3339))), 0o644); err != nil {
		b.Fatal(err)
	}
	five := 5.0
	at := time.Now()
	full := Info{Status: "In Review", Assignee: "Alice", Site: "acme.example", Summary: "Refresh API tokens before they expire",
		StatusCategory: "in_progress", Type: "Story", Priority: "High",
		Sprints:     []SprintRef{{ID: 1230, Name: "Sprint 11", State: "closed"}, {ID: 1234, Name: "Sprint 12", State: "active"}},
		StoryPoints: &five, StoryPointsKnown: true, Parent: &Parent{Key: "TEAM-100", Summary: "Token lifecycle", Type: "Epic"},
		Team: "Platform", Components: []string{"api", "web"}, Labels: []string{"backend"}, CreatedAt: at, UpdatedAt: at, AssigneeID: "acct-alice"}
	if err := Write("TEAM-2", full); err != nil {
		b.Fatal(err)
	}
	for _, tc := range []struct{ name, ticket string }{{"old", "TEAM-1"}, {"facts", "TEAM-2"}} {
		b.Run(tc.name, func(b *testing.B) {
			for b.Loop() {
				if _, st := Read(tc.ticket, time.Hour); st != Fresh {
					b.Fatalf("state %v", st)
				}
			}
		})
	}
}
