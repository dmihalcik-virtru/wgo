package bujo

import (
	"testing"
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
				if task != nil {
					t.Errorf("expected nil for empty line, got %v", task)
				}
				return
			}
			if task == nil {
				t.Fatal("expected task, got nil")
			}
			if task.Bullet != tc.bullet {
				t.Errorf("bullet: got %q want %q", task.Bullet, tc.bullet)
			}
			if task.Text != tc.text {
				t.Errorf("text: got %q want %q", task.Text, tc.text)
			}
		})
	}
}

func TestParseRefs(t *testing.T) {
	line := "Fix stuff #auth-service:oauth-branch #auth-service#42 !payments-api#99"
	refs := ParseRefs(line)

	if len(refs) != 3 {
		t.Fatalf("expected 3 refs, got %d: %v", len(refs), refs)
	}

	// branch ref
	if refs[0].Repo != "auth-service" || refs[0].Branch != "oauth-branch" {
		t.Errorf("unexpected branch ref: %+v", refs[0])
	}
	// PR ref
	if refs[1].Repo != "auth-service" || refs[1].PR != 42 {
		t.Errorf("unexpected PR ref: %+v", refs[1])
	}
	// issue ref
	if refs[2].Repo != "payments-api" || refs[2].Issue != 99 {
		t.Errorf("unexpected issue ref: %+v", refs[2])
	}
}

func TestTaskRender(t *testing.T) {
	task := &Task{Bullet: BulletOpen, Text: "Do something", Raw: "○ Do something"}
	if got := task.Render(); got != "○ Do something" {
		t.Errorf("got %q", got)
	}

	note := &Task{Bullet: BulletNote, Text: "raw line", Raw: "raw line"}
	if got := note.Render(); got != "raw line" {
		t.Errorf("got %q", got)
	}
}

func TestMatchesPattern(t *testing.T) {
	task := &Task{Text: "Review the Auth PR"}
	if !task.MatchesPattern("auth") {
		t.Error("expected match for 'auth'")
	}
	if task.MatchesPattern("payments") {
		t.Error("unexpected match for 'payments'")
	}
}
