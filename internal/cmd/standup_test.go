package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/virtru/wgo/internal/jira"
)

func TestDayDiff(t *testing.T) {
	base := time.Date(2026, 7, 1, 9, 0, 0, 0, time.Local)
	// Same calendar day, different times → 0.
	assert.Equal(t, 0, dayDiff(base, time.Date(2026, 7, 1, 23, 0, 0, 0, time.Local)))
	// Future.
	assert.Equal(t, 5, dayDiff(base, time.Date(2026, 7, 6, 1, 0, 0, 0, time.Local)))
	// Past.
	assert.Equal(t, -7, dayDiff(base, time.Date(2026, 6, 24, 20, 0, 0, 0, time.Local)))
}

func TestSprintDeadline(t *testing.T) {
	// Zero end date.
	msg, days := sprintDeadline(time.Time{}, "active")
	assert.Equal(t, "no end date set", msg)
	assert.Equal(t, 0, days)

	// Past end date on a closed sprint → overdue with closed framing, positive days.
	past := time.Now().AddDate(0, 0, -7)
	msg, days = sprintDeadline(past, "closed")
	assert.Contains(t, msg, "overdue, sprint closed")
	assert.Equal(t, 7, days)

	// Future end date → days left, negative daysOverdue.
	future := time.Now().AddDate(0, 0, 5)
	msg, days = sprintDeadline(future, "active")
	assert.Contains(t, msg, "left")
	assert.Equal(t, -5, days)
}

func TestDeriveProject(t *testing.T) {
	issues := []jira.Issue{
		{Key: "DSPX-3397"},
		{Key: "DSPX-2007"},
	}
	assert.Equal(t, "DSPX", deriveProject(issues))
	assert.Equal(t, "", deriveProject([]jira.Issue{{Key: "nodash"}}))
	assert.Equal(t, "", deriveProject(nil))
}

func TestFilterSprints(t *testing.T) {
	sprints := []jira.Sprint{
		{ID: 7677, Name: "PQ 🗝️ 26s6"},
		{ID: 7719, Name: "Web Weaver 🕷️ Sprint 8"},
	}
	// By name substring (case-insensitive).
	got := filterSprints(sprints, "pq")
	assert.Len(t, got, 1)
	assert.Equal(t, 7677, got[0].ID)
	// By exact id.
	got = filterSprints(sprints, "7719")
	assert.Len(t, got, 1)
	assert.Equal(t, 7719, got[0].ID)
	// No match.
	assert.Empty(t, filterSprints(sprints, "nope"))
}
