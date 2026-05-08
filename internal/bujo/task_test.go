package bujo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTask(t *testing.T) {
	tests := []struct {
		line   string
		bullet BulletType
		text   string
	}{
		{"○ Write tests", BulletOpen, "Write tests"},
		{"◉ Fix the bug #repo:branch", BulletInProgress, "Fix the bug #repo:branch"},
		{"✓ Done task", BulletDone, "Done task"},
		{"✗ Cancelled task", BulletCancelled, "Cancelled task"},
		{"→ Deferred task", BulletMigrated, "Deferred task"},
		{"! Priority task", BulletPriority, "Priority task"},
		{"x Done alternate", BulletDone, "Done alternate"},
		{"~ Cancelled alternate", BulletCancelled, "Cancelled alternate"},
		{"plain note", BulletNote, "plain note"},
		{"", BulletNote, ""},
	}

	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			task := ParseTask(tc.line)
			if tc.line == "" {
				assert.Nil(t, task, "expected nil for empty line")
				return
			}
			require.NotNil(t, task, "expected task, got nil")
			assert.Equal(t, tc.bullet, task.Bullet, "bullet mismatch")
			assert.Equal(t, tc.text, task.Text, "text mismatch")
		})
	}
}

func TestParseRefs(t *testing.T) {
	line := "Fix stuff #auth-service:oauth-branch #auth-service#42 !payments-api#99"
	refs := ParseRefs(line)

	require.Len(t, refs, 3)

	// branch ref
	assert.Equal(t, "auth-service", refs[0].Repo)
	assert.Equal(t, "oauth-branch", refs[0].Branch)
	// PR ref
	assert.Equal(t, "auth-service", refs[1].Repo)
	assert.Equal(t, 42, refs[1].PR)
	// issue ref
	assert.Equal(t, "payments-api", refs[2].Repo)
	assert.Equal(t, 99, refs[2].Issue)
}

func TestTaskRender(t *testing.T) {
	task := &Task{Bullet: BulletOpen, Text: "Do something", Raw: "○ Do something"}
	assert.Equal(t, "○ Do something", task.Render())

	note := &Task{Bullet: BulletNote, Text: "raw line", Raw: "raw line"}
	assert.Equal(t, "raw line", note.Render())
}

func TestMatchesPattern(t *testing.T) {
	task := &Task{Text: "Review the Auth PR"}
	assert.True(t, task.MatchesPattern("auth"), "expected match for 'auth'")
	assert.False(t, task.MatchesPattern("payments"), "unexpected match for 'payments'")
}
