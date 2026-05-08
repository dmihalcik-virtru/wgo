package bujo

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDailyLogRenderParse(t *testing.T) {
	date := time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)
	dl := &DailyLog{Date: date}
	dl.AddCompleted("Implement auth flow", "approved")
	dl.AddCancelled("Refactor discovery", "too risky")
	dl.AddBranchEvent("created", "auth-service", "feature/oauth")

	rendered := dl.Render()

	assert.Contains(t, rendered, "# 2026-02-20")
	assert.Contains(t, rendered, "## Completed")
	assert.Contains(t, rendered, "Implement auth flow")
	assert.Contains(t, rendered, "## Cancelled")
	assert.Contains(t, rendered, "## Branch Events")
	assert.Contains(t, rendered, "Created feature/oauth in auth-service")

	// Round-trip
	parsed, err := ParseDailyLog(rendered, date)
	require.NoError(t, err)
	assert.Len(t, parsed.Completed, 1)
	assert.Len(t, parsed.Cancelled, 1)
	assert.Len(t, parsed.Events, 1)
}
