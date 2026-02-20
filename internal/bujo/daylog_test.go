package bujo

import (
	"strings"
	"testing"
	"time"
)

func TestDailyLogRenderParse(t *testing.T) {
	date := time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)
	dl := &DailyLog{Date: date}
	dl.AddCompleted("Implement auth flow", "approved")
	dl.AddCancelled("Refactor discovery", "too risky")
	dl.AddBranchEvent("created", "auth-service", "feature/oauth")

	rendered := dl.Render()

	if !strings.Contains(rendered, "# 2026-02-20") {
		t.Error("missing date header")
	}
	if !strings.Contains(rendered, "## Completed") {
		t.Error("missing Completed section")
	}
	if !strings.Contains(rendered, "Implement auth flow") {
		t.Error("missing completed task")
	}
	if !strings.Contains(rendered, "## Cancelled") {
		t.Error("missing Cancelled section")
	}
	if !strings.Contains(rendered, "## Branch Events") {
		t.Error("missing Branch Events section")
	}
	if !strings.Contains(rendered, "Created feature/oauth in auth-service") {
		t.Error("missing branch event")
	}

	// Round-trip
	parsed, err := ParseDailyLog(rendered, date)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Completed) != 1 {
		t.Errorf("expected 1 completed, got %d", len(parsed.Completed))
	}
	if len(parsed.Cancelled) != 1 {
		t.Errorf("expected 1 cancelled, got %d", len(parsed.Cancelled))
	}
	if len(parsed.Events) != 1 {
		t.Errorf("expected 1 event, got %d", len(parsed.Events))
	}
}
